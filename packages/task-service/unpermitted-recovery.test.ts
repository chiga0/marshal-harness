import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {spawn} from 'node:child_process';
import {setTimeout as pause} from 'node:timers/promises';
import {fileURLToPath} from 'node:url';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {encode} from '../task-store/store.mjs';
import {verifyObservation, custodyDigest} from '../agent-runtime/custody-contract.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url)), config = fileURLToPath(new URL('./unpermitted-recovery.fixture.mjs', import.meta.url));
const data = {rows: [{region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100}]};
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
async function until(fn, ms = 15000) {const end = Date.now() + ms;
  for (;;) {const value = await fn(); if (value) return value; assert.ok(Date.now() < end, 'bounded v5 observation'); await pause(10);}}
function lines(file) {try {const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).trim().split('\n').filter(Boolean).map(JSON.parse);}
  catch (error) {if (error.code === 'ENOENT') return []; throw error;}}
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const projections = (state, kind) => state.projections.filter(row => row.kind === kind).map(decode);
function state(f) {
  assert.ok(f.children.every(child => child.exited), 'read-only SQL only after original services exit');
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, allowExtension: false});
  try {db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    const rows = table => db.prepare('SELECT * FROM ' + table).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {events: rows('events'), projections: rows('projections'), receipts: rows('receipts'), outbox: rows('outbox'), heads: rows('heads')};
  } finally {db.close();}
}
async function fixture(t, point, extension = null) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-v5-http-'))), f = {root: path.join(parent, 'data'), children: [], complete: false};
  f.observations = () => lines(path.join(parent, extension === 'repair' ? 'repair-observations.jsonl' : extension === 'questions' ? 'question-observations.jsonl' : 'custody-observations.jsonl'));
  f.marker = () => extension ? f.observations().find(row => row.type === 'v5-barrier') : lines(path.join(parent, 'control-commit.jsonl'))[0];
  t.after(async () => {for (const child of f.children) await child.stop('SIGKILL');
    if (f.complete) fs.rmSync(parent, {recursive: true, force: true}); else t.diagnostic('Preserved v5 failure: ' + parent);});
  f.launch = async (mode, fault = 'none') => {
    const file = extension ? fileURLToPath(new URL('./unpermitted-extensions.fixture.mjs', import.meta.url)) : config;
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', file], {cwd: parent,
      env: extension ? {MARSHAL_V5_EXTENSION: extension, MARSHAL_REPAIR_V5_CUT: fault === 'cut' ? '1' : '0'} :
        {MARSHAL_CONTROL_COMMIT_FIXTURE: '1', MARSHAL_CONTROL_COMMIT_POINT: fault, MARSHAL_CONTROL_COMMIT_CAPACITY: point.startsWith('cancel-') ? '2' : '1'}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {child.once('error', () => {exited = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('close', (code, signal) => {exited = true; resolve({code, signal});});});
    child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL');});
    child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL');});
    const result = {get exited() {return exited;}, async stop(signal) {
      if (!exited) child.kill(signal); const timer = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 10000);
      try {await until(() => exited); const exit = await done; if (token) assert.equal((stdout + stderr).includes(token), false);
        if (signal === 'SIGTERM') assert.equal(JSON.parse(stdout.trim().split('\n').at(-1)).clean, true); return exit;
      } finally {clearTimeout(timer);}
    }}; f.children.push(result);
    await until(() => {assert.equal(exited, false, 'CLI startup: ' + stderr); return stdout.includes('\n');}, 20000);
    const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(first.connectionFile)); token = connection.token;
    result.client = new TaskClient({baseURL: connection.url, token}); return result;
  }; return f;
}
async function approve(client, inputId, intent, key, maxAttempts = 4) {
  const body = {intent, ...(inputId ? {context: {inputRefs: [inputId]}} : {}), limits: {timeoutMs: 45000, maxAttempts, maxWorkers: 2}};
  const created = await client.createTask(body, key + '-create');
  const plan = await until(async () => {const value = await client.getTask(created.id); return value.status === 'awaiting-approval' && value;});
  const approval = {expectedRevision: plan.revision, planRevision: plan.plan.revision, planDigest: plan.plan.digest};
  const operation = await client.approveTask(created.id, approval, key + '-approve'); return {created, body, approval, operation, key};
}
async function replay(client, task) {
  assert.deepEqual(await client.createTask(task.body, task.key + '-create'), task.created);
  assert.deepEqual(await client.approveTask(task.created.id, task.approval, task.key + '-approve'), task.operation);
}
// All SIX original cuts also execute on v5. The v2 file remains unchanged;
// neither v2 recovery nor v5 ready alone substitutes the next actual team.
for (const point of ['reservation-before', 'reservation-after', 'binding-before', 'binding-after', 'cancel-before', 'cancel-after'])
  test(`v5 ${point}: SIGKILL, original settlement, new HTTP team/download and exact cold replay`, {timeout: 60000}, async t => {
    const f = await fixture(t, point), first = await f.launch('create', point);
    const uploaded = await first.client.request('input.create', {idempotencyKey: 'input', body: {
      name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const old = await approve(first.client, uploaded.id, 'custody interrupted authors', 'old');
    const cancel = point.startsWith('cancel-'), rolledBack = point === 'reservation-before';
    let cancelRequest, pending, replied = false;
    if (cancel) {
      await until(async () => (await first.client.request('task.workers', {path: {taskId: old.created.id}})).items
        .filter(value => value.role === 'author' && value.status === 'running' && value.startedAt).length === 2);
      cancelRequest = {path: {taskId: old.created.id}, idempotencyKey: 'lost-cancel', body: {expectedRevision: (await first.client.getTask(old.created.id)).revision}};
      pending = first.client.request('task.cancel', cancelRequest).then(value => {replied = true; return value;}, error => error);
    }
    const marker = await until(f.marker);
    assert.equal(marker.point, point); assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
    if (pending) {assert.equal((await pending).code, 'client_transport_error'); assert.equal(replied, false);}
    const before = state(f), prior = projections(before, 'task').find(record => record.task.id === old.created.id);
    const active = projections(before, 'attempt').filter(record => record.worker?.taskId === old.created.id && record.worker.role === 'author');
    assert.equal(prior.attempts, rolledBack ? 1 : cancel ? 3 : 2); assert.equal(active.length, rolledBack ? 0 : cancel ? 2 : 1);
    for (const worker of active) {
      assert.equal(worker.worker.status, cancel ? 'running' : 'queued'); assert.equal(worker.cleanup, null);
      assert.deepEqual(worker.ticket.startProtocol, {profile: 'node-unpermitted-reservation/v1', preparation: 'file-staging-only/v1'});
      assert.equal(before.receipts.filter(row => row.scope === worker.worker.id && row.operation === 'execution.custody-binding').length, cancel || point === 'binding-after' ? 1 : 0);
    }
    assert.equal(!!prior.cancelIntent, point === 'cancel-after');
    assert.equal(before.receipts.filter(row => row.operation === 'task.cancel').length, point === 'cancel-after' ? 1 : 0);
    const second = await f.launch('open'); await replay(second.client, old);
    if (rolledBack) {
      // No reservation is not an automatic Task retry/failure authority. The
      // existing explicit cancel may fence never-reserved old commands.
      await assert.rejects(second.client.request('ready.get'), {code: 'not_ready'});
      const current = await second.client.getTask(old.created.id), op = await second.client.request('task.cancel', {
        path: {taskId: current.id}, idempotencyKey: 'explicit-no-worker-cancel', body: {expectedRevision: current.revision}});
      await until(async () => (await second.client.getTask(current.id)).status === 'cancelled');
      assert.equal((await second.client.request('operation.get', {path: {operationId: op.id}})).status, 'succeeded');
    }
    const failed = await second.client.getTask(old.created.id), cancelled = rolledBack || point === 'cancel-after';
    assert.equal(failed.status, cancelled ? 'cancelled' : 'failed'); if (!cancelled) assert.equal(failed.code, 'service_interrupted');
    assert.equal((await second.client.request('ready.get')).ready, true); assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
    if (point === 'cancel-after') {
      assert.deepEqual(encode(await second.client.request('task.cancel', cancelRequest)), encode(marker.value));
      assert.equal((await second.client.request('operation.get', {path: {operationId: marker.value.id}})).status, 'succeeded');
    } else if (point === 'cancel-before') await assert.rejects(second.client.request('task.cancel', cancelRequest), {code: 'revision_conflict'});
    const audit = await second.client.request('task.audit', {path: {taskId: old.created.id}});
    assert.equal(audit.attempts, prior.attempts); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
    assert.equal(audit.acceptance.status, 'pending'); assert.deepEqual(failed.artifactIds, []); assert.equal(failed.deadlineAt, old.created.deadlineAt);
    const next = await approve(second.client, uploaded.id, 'fresh healthy team', 'next');
    const completed = await until(async () => {const value = await second.client.getTask(next.created.id); return value.status === 'completed' && value;});
    const downloads = await Promise.all(completed.artifactIds.map(id => second.client.downloadArtifact(id))), delivery = downloads.find(value => value.artifact.kind === 'delivery');
    assert.ok(delivery); assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
    assert.equal((await second.client.request('task.audit', {path: {taskId: next.created.id}})).attempts, 4);
    assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
    const settled = state(f);
    const changes = settled.events.filter(row => row.stream === old.created.id).map(decode).map(event => event.payload);
    for (const worker of active) {
      const final = projections(settled, 'attempt').find(record => record.worker?.id === worker.worker.id);
      assert.deepEqual(final.ticket, worker.ticket); assert.equal(final.executionId, worker.executionId);
      if (worker.custody) {
        assert.equal(final.unpermittedSettlement, undefined);
        const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', worker.custody.descriptor.custodyId + '.observation.json')));
        assert.equal(verifyObservation(worker.custody.descriptor, observation), true);
        assert.equal(final.cleanup.cleaned, true); assert.equal(final.custody.settledDigest, custodyDigest(observation));
        assert.deepEqual(final.cleanup, observation.payload.cleanup);
        if (!cancel) {assert.equal(observation.payload.permitReceived, false); assert.equal(final.cleanup.scope, 'none-start');}
      } else {assert.equal(final.cleanup, null); assert.equal(final.unpermittedSettlement.disposition, 'never-permitted');}
    }
    assert.equal(changes.filter(event => event.type === 'worker.unpermitted-settled').length, ['reservation-after', 'binding-before'].includes(point) ? 1 : 0);
    assert.equal(projections(settled, 'budget').flatMap(value => value.active).length, 0);
    for (const receipt of before.receipts) assert.deepEqual(settled.receipts.find(row => row.scope === receipt.scope && row.operation === receipt.operation && row.key_digest === receipt.key_digest), receipt);
    const starts = f.observations().filter(row => row.type === 'started');
    assert.equal(starts.filter(row => row.taskId === old.created.id).length, cancel ? 3 : 1);
    assert.equal(starts.filter(row => row.taskId === next.created.id).length, 4);
    const third = await f.launch('open'); await replay(third.client, old); await replay(third.client, next);
    assert.deepEqual(await third.client.getTask(old.created.id), failed); assert.deepEqual(await third.client.getTask(next.created.id), completed);
    if (point === 'cancel-after') assert.deepEqual(encode(await third.client.request('task.cancel', cancelRequest)), encode(marker.value));
    assert.deepEqual((await third.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
    assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null});
    assert.deepEqual(state(f), settled); assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.complete = true;
  });

test('v5 repair reservation crash retains original negative Decision/receipt/selected sibling and cold settles without rework refund', {timeout: 60000}, async t => {
  const f = await fixture(t, 'repair', 'repair'), first = await f.launch('create', 'cut');
  const input = await first.client.request('input.create', {idempotencyKey: 'input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const old = await approve(first.client, input.id, 'v5 repair interruption', 'old', 6);
  const failed = await until(async () => {const value = await first.client.getTask(old.created.id); return value.status === 'failed' && value;});
  assert.deepEqual(failed.allowedActions, ['repair']);
  const audit = await first.client.request('task.audit', {path: {taskId: failed.id}}), workers = await first.client.request('task.workers', {path: {taskId: failed.id}});
  assert.equal(audit.attempts, 4); assert.equal(audit.decision.status, 'rejected'); assert.deepEqual(audit.decision.contentRejection.failedAssertions, ['west-content']);
  const request = {path: {taskId: failed.id}, idempotencyKey: 'repair', body: {expectedRevision: failed.revision, planDigest: old.approval.planDigest,
    decisionDigest: audit.decision.digest, nodeIds: ['west'], feedback: '修正 west 金额，不改变 east 或原规则'}};
  const receipt = await first.client.request('task.repair', request), marker = await until(f.marker);
  assert.equal(receipt.repairId, marker.repairId); assert.equal(receipt.operation.status, 'accepted');
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = state(f), reserved = projections(before, 'attempt').find(value => value.worker?.id === marker.workerId);
  assert.equal(reserved.ticket.repairId, receipt.repairId); assert.equal(reserved.cleanup, null);
  assert.equal(projections(before, 'task').find(value => value.task.id === failed.id).attempts, 5);
  const second = await f.launch('open'), finalTask = await second.client.getTask(failed.id);
  assert.equal(finalTask.status, 'failed'); assert.equal(finalTask.code, 'service_interrupted');
  const finalAudit = await second.client.request('task.audit', {path: {taskId: failed.id}});
  assert.equal(finalAudit.attempts, 5); assert.equal(finalAudit.reworkCount, 1); assert.equal(finalAudit.retryCount, 0);
  assert.equal(finalAudit.acceptance.status, 'pending'); assert.deepEqual(finalAudit.decision, audit.decision);
  assert.deepEqual((await second.client.request('task.workers', {path: {taskId: failed.id}})).items.filter(value => workers.items.some(old => old.id === value.id)), workers.items);
  assert.equal((await second.client.request('operation.get', {path: {operationId: receipt.operation.id}})).status, 'failed');
  const replayed = await second.client.request('task.repair', request);
  assert.equal(replayed.replayed, true); assert.deepEqual(replayed.operation, receipt.operation); assert.equal(replayed.currentTask.status, 'failed');
  assert.equal((await second.client.request('ready.get')).ready, true);
  const next = await approve(second.client, input.id, 'fresh healthy team', 'next', 6);
  const complete = await until(async () => {const value = await second.client.getTask(next.created.id); return value.status === 'completed' && value;});
  const artifacts = await Promise.all(complete.artifactIds.map(id => second.client.downloadArtifact(id))), delivery = artifacts.find(value => value.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null}); const settled = state(f), starts = f.observations().filter(row => row.type === 'started');
  assert.equal(starts.filter(row => row.taskId === failed.id).length, 4);
  assert.equal(projections(settled, 'attempt').find(value => value.worker?.id === marker.workerId).cleanup, null);
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
  const third = await f.launch('open'); assert.deepEqual(await third.client.getTask(failed.id), finalTask);
  assert.deepEqual(await third.client.request('task.repair', request), replayed);
  assert.deepEqual((await third.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null}); assert.deepEqual(state(f), settled);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.complete = true;
});

test('v5 retains original native question/answer/ACK binding and independent delivery across cold open', {timeout: 60000}, async t => {
  const f = await fixture(t, 'questions', 'questions'), first = await f.launch('create');
  const old = await approve(first.client, null, '原业务问答，在同一个 Worker 继续', 'question');
  const question = await until(async () => (await first.client.request('task.questions', {path: {taskId: old.created.id}})).items[0]);
  assert.equal(question.status, 'open'); assert.equal(question.deliveryStatus, null);
  await until(async () => (await first.client.request('task.workers', {path: {taskId: old.created.id}})).items.some(value => value.nodeId === 'west' && value.status === 'completed'));
  const request = {path: {taskId: old.created.id, questionId: question.id}, idempotencyKey: 'answer', body: {
    expectedRevision: (await first.client.getTask(old.created.id)).revision, questionRevision: 1, questionDigest: question.questionDigest, answer: 'south'}};
  const receipt = await first.client.request('task.answer', request);
  const complete = await until(async () => {const value = await first.client.getTask(old.created.id); return value.status === 'completed' && value;});
  const questions = await first.client.request('task.questions', {path: {taskId: old.created.id}});
  assert.equal(questions.items[0].workerId, question.workerId); assert.equal(questions.items[0].deliveryStatus, 'acknowledged');
  const artifacts = await Promise.all(complete.artifactIds.map(id => first.client.downloadArtifact(id))), delivery = artifacts.find(value => value.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content), {region: 'south', west: 'independent native candidate'});
  assert.equal((await first.client.request('task.audit', {path: {taskId: old.created.id}})).attempts, 4);
  assert.deepEqual(await first.stop('SIGTERM'), {code: 0, signal: null}); const before = state(f);
  assert.equal(projections(before, 'attempt').filter(value => value.unpermittedSettlement).length, 0);
  const second = await f.launch('open'); assert.deepEqual(await second.client.getTask(old.created.id), complete);
  assert.deepEqual(await second.client.request('task.questions', {path: {taskId: old.created.id}}), questions);
  const replayed = await second.client.request('task.answer', request); assert.equal(replayed.replayed, true);
  assert.deepEqual(replayed.operation, receipt.operation);
  assert.deepEqual((await second.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null}); assert.deepEqual(state(f), before); f.complete = true;
});
