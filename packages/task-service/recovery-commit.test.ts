import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./recovery-commit.fixture.mjs', import.meta.url));
const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
async function until(observe, timeout = 10000) {
  const deadline = Date.now() + timeout;
  for (;;) { const value = await observe(); if (value) return value;
    assert.ok(Date.now() < deadline, 'bounded commit-crash observation timed out'); await pause(10); }
}
function lines(file) {
  try { return fs.readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
}
async function gone(pid) {
  assert.ok(Number.isSafeInteger(pid) && pid > 1);
  await until(() => { try { process.kill(pid, 0); return false; } catch (error) { assert.equal(error.code, 'ESRCH'); return true; } });
}
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex').toString('utf8'));
const projections = (snapshot, kind) => snapshot.projections.filter(row => row.kind === kind).map(decode);
const workers = snapshot => projections(snapshot, 'attempt').filter(row => row.worker);
const decisions = snapshot => projections(snapshot, 'attempt').filter(row => row.type === 'independent-verification');
const manifests = (snapshot, taskId) => projections(snapshot, 'artifact').filter(row => row.type === 'manifest' && row.artifact.taskId === taskId);
const taskRecord = (snapshot, taskId) => projections(snapshot, 'task').find(row => row.task.id === taskId);
const capacity = snapshot => projections(snapshot, 'budget').flatMap(row => row.active);
const createReceipts = snapshot => snapshot.receipts.filter(row => row.operation === 'task.create');
// No SQLite inspection while ANY original service process is alive. This is a
// query-only observation, never owner acquisition, raw SQL mutation or recovery.
function durable(f) {
  assert.ok(f.services.every(service => service.exited));
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON');
    assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {generation: db.prepare('SELECT generation FROM metadata').get().generation,
      events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'),
      receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally { db.close(); }
}
function depotBytes(f, ref) {
  assert.match(ref.digest, /^sha256:[a-f0-9]{64}$/);
  const target = path.join(f.root, 'artifacts', ref.digest.slice(7));
  const fd = fs.openSync(target, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const stat = fs.fstatSync(fd); assert.ok(stat.isFile()); assert.equal(stat.mode & 0o777, 0o600);
    assert.equal(stat.size, ref.bytes); const bytes = fs.readFileSync(fd);
    assert.equal(digest(bytes), ref.digest); return bytes;
  } finally { fs.closeSync(fd); }
}
async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-service-commit-')));
  const f = {root: path.join(parent, 'data'), journal: path.join(parent, 'commit-observations.jsonl'), services: []};
  f.observations = () => lines(f.journal);
  f.processesGone = async () => {
    for (const item of f.observations().filter(row => row.type === 'started' && row.started)) {
      await gone(item.started.guardPid); await gone(item.started.agentPid);
    }
  };
  t.after(async () => {
    for (const service of f.services) { await service.stop('SIGKILL'); service.checkOutput(); }
    await f.processesGone(); fs.rmSync(parent, {recursive: true, force: true});
  });
  f.launch = async (mode, point = 'none') => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {
      cwd: parent, env: {MARSHAL_SERVICE_COMMIT_FIXTURE: '1', MARSHAL_SERVICE_COMMIT_POINT: point}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => { exited = true; resolve({code: null, signal: 'spawn-error'}); });
      child.once('exit', (code, signal) => { exited = true; resolve({code, signal}); });
    });
    child.stdout.on('data', bytes => { stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL'); });
    child.stderr.on('data', bytes => { stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL'); });
    const service = {child, done, get exited() { return exited; }, checkOutput() {
      if (token) assert.equal((stdout + stderr).includes(token), false);
    }, async stop(signal) {
      if (!exited) child.kill(signal); // Only this test's original owned handle.
      const watchdog = setTimeout(() => { if (!exited) child.kill('SIGKILL'); }, 10000);
      try { await until(() => exited, 12000); return await done; } finally { clearTimeout(watchdog); }
    }};
    f.services.push(service);
    await until(() => { assert.equal(exited, false, 'CLI exited before connection: ' + stderr); return stdout.includes('\n'); });
    const ready = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(ready.connectionFile));
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token}); return service;
  };
  return f;
}
async function inputBody(client) {
  const input = await client.request('input.create', {idempotencyKey: 'sales-input', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  return {intent: '事务提交恢复夹具：两个地区独立统计净销售额并验收完整报告',
    context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
}
async function crash(f, service, point) {
  const marker = await until(() => f.observations().find(item => item.type === 'barrier' && item.point === point));
  assert.deepEqual(await service.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  await f.processesGone();
  assert.equal(f.observations().filter(item => item.type === 'barrier').length, 1);
  return marker;
}
function unchanged(before, after, keys) { for (const key of keys) assert.deepEqual(after[key], before[key], 'unchanged ' + key); }

for (const side of ['before', 'after']) test(`real HTTP create ${side} COMMIT crash preserves all-or-none authority and exact lost-reply semantics`, {timeout: 30000}, async t => {
  const f = await fixture(t), first = await f.launch('create', 'create-' + side), body = await inputBody(first.client);
  let answered = false;
  const pending = first.client.createTask(body, 'commit-create').then(value => { answered = true; return value; }, error => error);
  const marker = await crash(f, first, 'create-' + side), lost = await pending;
  assert.equal(answered, false); assert.equal(lost.code, 'client_transport_error');
  const before = durable(f), tasks = projections(before, 'task');
  assert.equal(before.generation, 1); assert.equal(workers(before).length, 0); assert.equal(capacity(before).length, 0);
  assert.equal(tasks.length, side === 'before' ? 0 : 1); assert.equal(createReceipts(before).length, tasks.length);
  assert.equal(before.outbox.length, tasks.length);
  const original = taskRecord(before, marker.taskId);
  if (side === 'before') {
    assert.equal(before.events.some(row => row.stream === marker.taskId), false);
    assert.equal(before.heads.some(row => row.stream === marker.taskId), false);
  } else {
    assert.equal(digest(Buffer.from(before.projections.find(row => row.id === marker.taskId).bytes, 'hex')), marker.taskDigest);
    assert.deepEqual(decode(createReceipts(before)[0]), marker.result);
    assert.deepEqual(original.limits, body.limits); assert.equal(original.attempts, 0);
    assert.equal(original.task.deadlineAt, marker.deadlineAt); assert.equal(before.outbox[0].status, 'pending');
    assert.equal(before.outbox[0].generation, 1); assert.equal(before.outbox[0].attempt_id, '');
    const event = before.events.filter(row => row.stream === marker.taskId);
    assert.equal(event.length, 1); assert.equal(event[0].sequence, 1); assert.equal(event[0].digest, marker.eventDigest);
    for (const row of [createReceipts(before)[0], before.outbox[0], before.projections.find(row => row.id === marker.taskId)]) {
      assert.equal(row.source_stream, marker.taskId); assert.equal(row.source_sequence, 1); assert.equal(row.source_digest, marker.eventDigest);
    }
  }
  const second = await f.launch('open');
  const replay = await second.client.createTask(body, 'commit-create');
  assert.deepEqual(await second.client.createTask(body, 'commit-create'), replay);
  await assert.rejects(second.client.createTask({...body, intent: 'changed'}, 'commit-create'), {code: 'idempotency_conflict'});
  if (side === 'after') {
    assert.deepEqual(encode(replay), encode(marker.result));
    assert.equal((await second.client.getTask(replay.id)).deadlineAt, marker.deadlineAt);
    await assert.rejects(second.client.request('ready.get'), {code: 'not_ready'});
    await assert.rejects(second.client.createTask(body, 'fresh-key'), {code: 'not_ready'});
    assert.equal((await second.client.request('task.audit', {path: {taskId: replay.id}})).attempts, 0);
  } else {
    // The rolled-back ID/receipt never existed. This is a new legitimate create,
    // not a retry of old external work; exactly one original planner may run.
    assert.notEqual(replay.id, marker.taskId);
    await until(async () => (await second.client.getTask(replay.id)).status === 'awaiting-approval');
    const audit = await second.client.request('task.audit', {path: {taskId: replay.id}});
    assert.equal(audit.attempts, 1); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
    assert.equal(audit.acceptance.status, 'pending');
  }
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
  const after = durable(f); assert.equal(after.generation, 2);
  if (side === 'after') {
    unchanged(before, after, ['events', 'heads', 'projections', 'receipts', 'outbox']);
    assert.equal(f.observations().some(item => item.type === 'started'), false);
  } else {
    assert.equal(projections(after, 'task').length, 1); assert.equal(createReceipts(after).length, 1);
    assert.equal(workers(after).length, 1); assert.equal(capacity(after).length, 0);
    assert.equal(after.outbox.length, 1); assert.equal(after.outbox[0].status, 'observed');
    assert.equal(after.events.some(row => row.stream === marker.taskId), false);
    assert.equal(f.observations().filter(item => item.type === 'started').length, 1);
    assert.deepEqual(taskRecord(after, replay.id).limits, body.limits);
    assert.deepEqual(taskRecord(after, replay.id).input, body);
    assert.deepEqual(encode(decode(createReceipts(after)[0])), encode(replay));
    assert.deepEqual(after.receipts.filter(row => row.operation === 'input.create'), before.receipts);
  }
});

for (const side of ['before', 'after']) test(`real team result depot precedes ${side} COMMIT crash; cold owner never invents cleanup or loses committed delivery`, {timeout: 30000}, async t => {
  const f = await fixture(t), first = await f.launch('create', 'result-' + side), body = await inputBody(first.client);
  const created = await first.client.createTask(body, 'commit-create');
  const preview = await until(async () => { const task = await first.client.getTask(created.id); return task.status === 'awaiting-approval' && task; });
  const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
  const operation = await first.client.approveTask(created.id, approval, 'commit-approve');
  const marker = await crash(f, first, 'result-' + side), before = durable(f);
  assert.equal(marker.taskId, created.id); assert.equal(marker.attempts, 4);
  const completions = f.observations().filter(item => item.type === 'completion');
  assert.equal(completions.length, 4); assert.ok(completions.every(item => item.cleanup.cleaned === true));
  const verifier = completions.find(item => item.kind === 'verification');
  assert.equal(verifier.status, 'passed'); assert.equal(verifier.workerId, marker.result.id);
  assert.deepEqual(verifier.outputs.map(item => item.kind), ['evidence', 'delivery']);
  const bytes = verifier.outputs.map(ref => depotBytes(f, ref)); // Independently re-read exact durable objects.
  assert.deepEqual(JSON.parse(bytes[1]).files.map(file => JSON.parse(file.content)), expected);
  const task = taskRecord(before, created.id), worker = workers(before).find(row => row.worker.id === verifier.workerId);
  const command = before.outbox.find(row => row.id === worker.ticket.commandId);
  assert.deepEqual(task.limits, body.limits); assert.equal(task.task.deadlineAt, created.deadlineAt);
  assert.equal(task.attempts, 4); assert.equal(task.retryCount, 0); assert.equal(task.reworkCount, 0);
  assert.equal(workers(before).length, 4);
  assert.equal(before.outbox.filter(row => {
    const payload = JSON.parse(Buffer.from(row.payload, 'hex').toString('utf8'));
    return payload.action === 'execute' && payload.nodeId === 'verify';
  }).length, 1);
  assert.equal(command.kind, 'start'); assert.equal(worker.ticket.executionType, 'verification');
  assert.deepEqual(encode(decode(createReceipts(before)[0])), encode(created));
  const finalEvents = before.events.filter(row => row.stream === created.id).map(decode)
    .filter(event => event.payload.type === 'worker.finished' && event.payload.workerId === verifier.workerId);
  if (side === 'before') {
    assert.equal(task.task.status, 'running'); assert.equal(task.acceptance, undefined);
    assert.equal(worker.worker.status, 'running'); assert.equal(worker.cleanup, null);
    assert.equal(worker.worker.finishedAt, null); assert.equal(command.status, 'unknown');
    assert.equal(capacity(before).length, 1); assert.equal(capacity(before)[0].workerId, verifier.workerId);
    assert.equal(decisions(before).length, 0); assert.equal(manifests(before, created.id).length, 0);
    assert.equal(finalEvents.length, 0); assert.deepEqual(task.task.artifactIds, []);
    for (const ref of verifier.outputs) assert.equal(projections(before, 'artifact').some(row => row.digest === ref.digest), false);
  } else {
    assert.equal(task.task.status, 'completed'); assert.equal(task.acceptance.status, 'passed');
    assert.deepEqual(worker.cleanup, verifier.cleanup); assert.equal(worker.worker.status, 'completed');
    assert.equal(command.status, 'observed'); assert.equal(capacity(before).length, 0);
    assert.equal(decisions(before).length, 1); assert.equal(decisions(before)[0].status, 'accepted');
    assert.equal(manifests(before, created.id).length, 2); assert.equal(finalEvents.length, 1);
    assert.equal(finalEvents[0].payload.decisionDigest, task.decision.digest);
    assert.equal(digest(encode(decisions(before)[0])), task.decision.digest);
    assert.equal(digest(Buffer.from(before.projections.find(row => row.id === created.id).bytes, 'hex')), marker.taskDigest);
  }
  const recordedStarts = f.observations().filter(item => item.type === 'started');
  assert.equal(recordedStarts.length, 4);
  const second = await f.launch('open'), current = await second.client.getTask(created.id);
  assert.deepEqual(await second.client.createTask(body, 'commit-create'), created);
  assert.deepEqual(await second.client.approveTask(created.id, approval, 'commit-approve'), operation);
  await assert.rejects(second.client.approveTask(created.id, {...approval, expectedRevision: 1}, 'commit-approve'), {code: 'idempotency_conflict'});
  assert.equal(current.deadlineAt, created.deadlineAt);
  const audit = await second.client.request('task.audit', {path: {taskId: created.id}});
  assert.equal(audit.attempts, 4); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
  if (side === 'before') {
    assert.equal(current.status, 'intervention'); assert.equal(current.code, 'previous_execution_unresolved');
    assert.equal(audit.acceptance.status, 'pending'); assert.deepEqual(current.artifactIds, []);
    assert.equal((await second.client.request('supervisor.get')).activeWorkers, 1);
    await assert.rejects(second.client.request('ready.get'), {code: 'not_ready'});
    await assert.rejects(second.client.createTask(body, 'fresh-key'), {code: 'not_ready'});
  } else {
    assert.equal(current.status, 'completed'); assert.equal(audit.acceptance.status, 'passed');
    assert.equal((await second.client.request('ready.get')).ready, true);
    assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
    for (const id of current.artifactIds) {
      const download = await second.client.downloadArtifact(id), n = verifier.outputs.findIndex(ref => ref.kind === download.artifact.kind);
      assert.notEqual(n, -1); assert.deepEqual(download.content, bytes[n]); assert.equal(download.artifact.digest, verifier.outputs[n].digest);
    }
    assert.equal((await second.client.request('operation.get', {path: {operationId: operation.id}})).status, 'succeeded');
  }
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
  const after = durable(f); assert.equal(after.generation, before.generation + 1);
  unchanged(before, after, ['receipts', 'outbox']);
  for (const kind of ['attempt', 'budget', 'artifact']) assert.deepEqual(after.projections.filter(row => row.kind === kind), before.projections.filter(row => row.kind === kind));
  if (side === 'after') unchanged(before, after, ['events', 'heads', 'projections']);
  assert.deepEqual(f.observations().filter(item => item.type === 'started'), recordedStarts);
  verifier.outputs.forEach((ref, n) => assert.deepEqual(depotBytes(f, ref), bytes[n]));
});
