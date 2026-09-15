// Read-only observations plus ONE explicit HTTP command. No Task reducer,
// automatic Task cancellation, PID control, provider delay or retry lives here.
import fs from 'node:fs';
import path from 'node:path';
import {DatabaseSync} from 'node:sqlite';
import {setTimeout as pause} from 'node:timers/promises';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {executionFact, cancelledExecutionFact, assertTeam, DriverError} from '../task-qwen-live/driver.fixture.mjs';
import {equal} from './scenario.fixture.mjs';

const check = (value, code) => {if (!value) throw new DriverError(code);};
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const workerStatuses = new Set(['running', 'awaiting-answer']);
export const workerCancellation = Object.freeze({profile: 'task-worker-cancellation/v1'});

export async function cancelPiWorker({client, taskId, observations, getVerifierStarts, end}) {
  const authors = () => observations.filter(entry => entry.identity.role === 'author');
  const live = () => check(observations.length <= 3 && getVerifierStarts() === 0 && authors().every(entry =>
    !entry.failed && !entry.settled && !['stopping', 'terminal'].includes(entry.handle.snapshot().phase)), 'worker_cancel_window_missed');
  let task, workers;
  for (;;) {
    check(Date.now() < end, 'worker_cancel_window_timeout'); live();
    workers = await client.request('task.workers', {path: {taskId}});
    task = await client.getTask(taskId); live();
    check(task.id === taskId && !['failed', 'completed', 'cancelled', 'cancelling', 'intervention', 'paused'].includes(task.status), 'worker_cancel_window_missed');
    if (['running', 'awaiting-answer'].includes(task.status) && authors().length === 2 && authors().every(entry => entry.started)) {
      check(observations.length === 3 && workers.nextCursor === null && workers.items.length <= 3 &&
        equal(authors().map(entry => entry.identity.nodeId).sort(), ['east', 'west']) &&
        new Set(observations.map(entry => entry.identity.workerId)).size === 3 && observations.every(entry => entry.identity.taskId === taskId), 'worker_cancel_identity');
      if (workers.items.length === 3 && authors().every(entry => workers.items.some(worker => worker.id === entry.identity.workerId &&
        worker.taskId === taskId && worker.nodeId === entry.identity.nodeId && worker.role === 'author' && workerStatuses.has(worker.status) &&
        worker.startedAt === entry.started.startedAt))) break;
    }
    await pause(20); // Only observe the original processes; never hold their work.
  }
  live(); check(Date.now() < end, 'worker_cancel_window_timeout');
  const target = authors().find(entry => entry.identity.nodeId === 'east'), sibling = authors().find(entry => entry.identity.nodeId === 'west');
  const started = structuredClone(target.started), requestedAt = new Date().toISOString();
  const request = {path: {workerId: target.identity.workerId}, body: {expectedRevision: task.revision}, idempotencyKey: 'pi-runtime-cancel-east'};
  let operation;
  try {operation = await client.request('worker.cancel', request);}
  catch (error) {if (error.status === 409) throw new DriverError('worker_cancel_window_missed'); throw error;}
  const bound = value => value?.taskId === taskId && value.workerId === target.identity.workerId && value.kind === 'worker.cancel' && value.id === operation.id;
  check(id(operation?.id) && bound(operation) && operation.status === 'accepted', 'worker_cancel_operation_mismatch');
  let done, terminalOperation;
  for (;;) {
    check(Date.now() < end, 'worker_cancel_observation_timeout');
    check(observations.length === 3 && getVerifierStarts() === 0, 'worker_cancel_unexpected_execution');
    done = await client.getTask(taskId);
    terminalOperation = await client.request('operation.get', {path: {operationId: operation.id}});
    check(bound(terminalOperation) && !['unknown', 'failed', 'cancelled'].includes(terminalOperation.status), 'worker_cancel_not_reconciled');
    check(!['cancelled', 'completed', 'intervention'].includes(done.status), 'worker_cancel_wrong_task_outcome');
    if (done.status === 'failed' && terminalOperation.status === 'succeeded' && observations.every(entry => entry.settled)) break;
    await pause(20);
  }
  check(done.id === taskId && done.code === 'worker_cancelled' && done.deadlineAt === task.deadlineAt &&
    equal(done.artifactIds, []) && observations.every(entry => !entry.failed), 'worker_cancel_wrong_task_outcome');
  const executions = observations.map(entry => entry === target ?
    cancelledExecutionFact(entry.identity, entry.result, requestedAt, started, 'pi_provider_stopped') : executionFact(entry.identity, entry.result));
  const west = executions.find(entry => entry.workerId === sibling.identity.workerId);
  check(Date.parse(west.agentExitedAt) > Date.parse(requestedAt), 'worker_cancel_sibling_did_not_continue');
  const after = await client.request('task.workers', {path: {taskId}});
  check(after.nextCursor === null && after.items.length === 3 && executions.every(entry => after.items.some(worker =>
    worker.id === entry.workerId && worker.taskId === taskId && worker.nodeId === entry.nodeId && worker.role === entry.role &&
    worker.status === entry.status && worker.startedAt === entry.startedAt && worker.attempt === workers.items.find(before => before.id === worker.id)?.attempt)), 'worker_cancel_result_identity');
  const graph = await client.request('task.graph', {path: {taskId}}), audit = await client.getAudit(taskId);
  check(graph.taskId === taskId && graph.nodes.length === 3 && graph.nodes.some(node => node.id === 'east' && node.status === 'cancelled') &&
    graph.nodes.some(node => node.id === 'west' && node.status === 'completed') &&
    graph.nodes.some(node => node.id === 'verify' && node.status === 'cancelled' && node.workerIds.length === 0), 'worker_cancel_dependencies_unsettled');
  check(audit.attempts === 3 && audit.retryCount === 0 && audit.reworkCount === 0 && audit.acceptance.status === 'pending' &&
    audit.acceptance.digest === null && equal(audit.acceptance.evidenceIds, []) &&
    (await client.request('supervisor.get')).activeWorkers === 0, 'worker_cancel_budget_or_acceptance');
  return {done, request, operation, terminalOperation, requestedAt, executions, workers: after.items, graph,
    overlapMs: assertTeam(executions), siblingWorkerId: sibling.identity.workerId};
}

function heldRegular(file, maximum, read) {
  check(fs.realpathSync(path.dirname(file)) === path.dirname(file), 'worker_cancel_evidence_path');
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const before = fs.fstatSync(fd), named = fs.lstatSync(file);
    check(before.isFile() && before.uid === process.getuid() && (before.mode & 0o7777) === 0o600 && before.nlink === 1 &&
      before.size <= maximum && before.dev === named.dev && before.ino === named.ino, 'worker_cancel_evidence_file');
    const bytes = read ? Buffer.alloc(before.size) : null; let at = 0;
    while (bytes && at < bytes.length) {const count = fs.readSync(fd, bytes, at, bytes.length - at, at); check(count > 0, 'worker_cancel_evidence_file'); at += count;}
    const after = fs.fstatSync(fd), current = fs.lstatSync(file);
    check(before.size === after.size && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs &&
      after.dev === current.dev && after.ino === current.ino, 'worker_cancel_evidence_file'); return bytes;
  } finally {fs.closeSync(fd);}
}
const regularRead = (file, maximum) => heldRegular(file, maximum, true);

export function retainedResult(record, result, task, expected) {
  const candidate = record?.candidate, refs = record?.interactionRefs;
  check(record?.worker?.id === expected.workerId && record.worker.taskId === expected.taskId && record.worker.nodeId === 'west' &&
    record.worker.status === 'completed' && record.cleanup?.cleaned === true && record.executionId === expected.executionId &&
    record.ticket?.taskId === expected.taskId && record.ticket.workerId === expected.workerId && record.ticket.nodeId === 'west' &&
    record.ticket.planDigest === expected.planDigest && record.ticket.startProtocol === undefined &&
    record.cleanup.started?.executionId === expected.executionId && task.task?.id === expected.taskId && task.task.status === 'failed' &&
    task.task.code === 'worker_cancelled' && task.plan?.digest === expected.planDigest && task.workerIds?.includes(expected.workerId) &&
    !task.decision && equal(refs, []) && id(record.resultRef) &&
    record.resultDigest === digest(encode({candidate: result, interactionRefs: refs})), 'worker_cancel_retained_result');
  check(candidate?.profile === 'task-file-business/v1' && candidate.taskId === expected.taskId && candidate.workerId === expected.workerId &&
    candidate.nodeId === 'west' && candidate.planDigest === expected.planDigest && candidate.reservationDigest === record.ticket.reservationDigest &&
    ['profile', 'taskId', 'workerId', 'nodeId', 'planDigest', 'reservationDigest', 'layoutDigest', 'inputDigest', 'manifestDigest'].every(key => candidate[key] === result?.[key]) &&
    equal(candidate.files, result.files) &&
    candidate.files.length === 1 && candidate.files[0].path === 'west.json' && sha(candidate.files[0].digest) &&
    Number.isSafeInteger(candidate.files[0].bytes) && candidate.files[0].bytes > 0 && candidate.files[0].bytes <= 4096 &&
    candidate.manifestDigest === digest(Buffer.from(JSON.stringify(candidate.files.map(({path, digest, bytes}) => ({path, digest, bytes}))))), 'worker_cancel_retained_manifest');
  return {workerId: record.worker.id, attempt: record.worker.attempt, executionId: record.executionId,
    resultRef: record.resultRef, resultDigest: record.resultDigest, manifestDigest: candidate.manifestDigest, file: candidate.files[0]};
}

// Call ONLY after this driver's original service.shutdown() confirmed clean.
// HTTP lacks a candidate-result download route; inspect only this self-created
// Task/Worker/result and its original immutable blob, never a directory listing,
// credentials, all-session export, owner claim or SQL repair.
export function readRetainedResult(root, expected) {
  check(id(expected.taskId) && id(expected.workerId) && sha(expected.planDigest) && id(expected.executionId), 'worker_cancel_evidence_identity');
  const database = path.join(root, 'store/authority.sqlite'); heldRegular(database, 64 * 1024 * 1024, false);
  const db = new DatabaseSync(database, {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); db.exec('BEGIN');
    check(db.prepare('SELECT format FROM metadata WHERE singleton=1').get()?.format === 'marshal-node-task-sqlite/v6-worker-cancellation', 'worker_cancel_format');
    const projection = (kind, objectId) => {
      const row = db.prepare('SELECT bytes,source_stream,source_sequence,source_digest FROM projections WHERE kind=? AND id=? AND length(bytes)<=1048576').get(kind, objectId);
      check(row && row.source_stream === expected.taskId, 'worker_cancel_evidence_reference');
      const source = db.prepare('SELECT bytes FROM events WHERE stream=? AND sequence=? AND digest=? AND length(bytes)<=1048576')
        .get(row.source_stream, row.source_sequence, row.source_digest);
      check(source && digest(source.bytes) === row.source_digest, 'worker_cancel_evidence_reference'); return parseJson(row.bytes);
    };
    const task = projection('task', expected.taskId), record = projection('attempt', expected.workerId);
    check(id(record.resultRef), 'worker_cancel_retained_result');
    const summary = retainedResult(record, projection('attempt', record.resultRef), task, expected);
    const bytes = regularRead(path.join(root, 'artifacts', summary.file.digest.slice(7)), 4096);
    check(bytes.length === summary.file.bytes && digest(bytes) === summary.file.digest &&
      equal(parseJson(bytes), {region: 'west', status: 'paid', count: 2, netCents: 550}), 'worker_cancel_retained_business');
    return {...summary, report: parseJson(bytes)};
  } finally {db.close();}
}
