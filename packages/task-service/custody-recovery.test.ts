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
import {encode} from '../task-store/store.mjs';
import {custodyDigest, verifyObservation} from '../agent-runtime/custody-contract.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./custody-recovery.fixture.mjs', import.meta.url));
const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
async function until(observe, ms = 12000) {
  const end = Date.now() + ms;
  for (;;) { const value = await observe(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded custody observation timed out'); await pause(10); }
}
function lines(file) {
  try { const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
}
async function gone(pid) {
  assert.ok(Number.isSafeInteger(pid) && pid > 1);
  await until(() => { try { process.kill(pid, 0); return false; } catch (error) { assert.equal(error.code, 'ESRCH'); return true; } });
}
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex').toString('utf8'));
const projected = (state, kind) => state.projections.filter(row => row.kind === kind).map(decode);
const taskRecord = (state, id) => projected(state, 'task').find(row => row.task.id === id);
const workers = (state, id) => projected(state, 'attempt').filter(row => row.worker?.taskId === id);
const capacity = state => projected(state, 'budget').flatMap(row => row.active);
const decisions = (state, id) => projected(state, 'attempt').filter(row => row.type === 'independent-verification' && row.taskId === id);
// Only query after every original service ChildProcess has exited. This never
// claims an owner, mutates SQLite, settles custody or treats a PID as authority.
function durable(f) {
  assert.ok(f.services.every(service => service.exited));
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {generation: db.prepare('SELECT generation FROM metadata').get().generation,
      events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally { db.close(); }
}
async function fixture(t, scenario) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-custody-http-')));
  const f = {root: path.join(parent, 'data'), journal: path.join(parent, 'custody-observations.jsonl'), services: [], completed: false};
  f.observations = () => lines(f.journal);
  f.processesGone = async () => {
    for (const value of f.observations().filter(row => row.type === 'started' && row.started)) {
      await gone(value.started.guardPid); await gone(value.started.agentPid);
    }
  };
  t.after(async () => {
    if (!f.completed) t.diagnostic('Preserved failed custody fixture: ' + parent);
    for (const service of f.services) { await service.stop('SIGKILL'); service.checkOutput(); }
    // If real cleanup fails, preserve the private directory/evidence. Never
    // delete a live execution directory or signal persisted process IDs.
    await f.processesGone(); if (f.completed) fs.rmSync(parent, {recursive: true, force: true});
  });
  f.launch = async mode => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {cwd: parent,
      env: {MARSHAL_CUSTODY_FIXTURE: '1', MARSHAL_CUSTODY_SCENARIO: scenario}, stdio: ['ignore', 'pipe', 'pipe']});
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
      if (!exited) child.kill(signal);
      const watchdog = setTimeout(() => { if (!exited) child.kill('SIGKILL'); }, 10000);
      try { await until(() => exited); return await done; } finally { clearTimeout(watchdog); }
    }};
    f.services.push(service);
    // v2 open itself waits at most 15s for the ORIGINAL sealed observations
    // before any owner claim. Observe that one invocation; never retry open.
    await until(() => { assert.equal(exited, false, 'original CLI startup failed: ' + stderr); return stdout.includes('\n'); }, 20000);
    const output = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(output.connectionFile));
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token}); return service;
  };
  return f;
}
async function approved(client, inputId, intent, key) {
  const body = {intent, context: {inputRefs: [inputId]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, key + '-create');
  const preview = await until(async () => { const task = await client.getTask(created.id); return task.status === 'awaiting-approval' && task; });
  const plan = await client.request('task.plan', {path: {taskId: created.id}});
  assert.equal(plan.nodes.length, 3); assert.equal(plan.nodes.filter(node => node.role === 'verifier').length, 1);
  const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
  const operation = await client.approveTask(created.id, approval, key + '-approve');
  return {body, created, approval, operation, key};
}
async function replay(client, original) {
  assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
  assert.deepEqual(await client.approveTask(original.created.id, original.approval, original.key + '-approve'), original.operation);
  await assert.rejects(client.createTask({...original.body, intent: 'changed'}, original.key + '-create'), {code: 'idempotency_conflict'});
  await assert.rejects(client.approveTask(original.created.id, {...original.approval, expectedRevision: 1}, original.key + '-approve'), {code: 'idempotency_conflict'});
}

for (const scenario of ['authors', 'cancel', 'verifier']) test(`custody ${scenario}: original service death settles cleanup only, then a new HTTP team delivers`, {timeout: 60000}, async t => {
  const f = await fixture(t, scenario), first = await f.launch('create');
  const input = await first.client.request('input.create', {idempotencyKey: 'sales-input', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const old = await approved(first.client, input.id, 'custody interrupted ' + scenario, 'interrupted');
  let liveWorkers;
  await until(async () => {
    liveWorkers = (await first.client.request('task.workers', {path: {taskId: old.created.id}})).items;
    return scenario === 'verifier' ? liveWorkers.some(worker => worker.role === 'verifier' && worker.startedAt && worker.status === 'running') :
      liveWorkers.filter(worker => worker.role === 'author' && worker.startedAt && worker.status === 'running').length === 2;
  });
  const oldStarts = f.observations().filter(row => row.type === 'started' && row.taskId === old.created.id);
  assert.equal(oldStarts.length, scenario === 'verifier' ? 4 : 3);
  let cancelBody, cancellation;
  if (scenario === 'cancel') {
    cancelBody = {expectedRevision: (await first.client.getTask(old.created.id)).revision};
    cancellation = await first.client.request('task.cancel', {path: {taskId: old.created.id}, body: cancelBody, idempotencyKey: 'interrupted-cancel'});
    await until(() => f.observations().some(row => row.type === 'stop-barrier' && row.taskId === old.created.id));
    assert.equal((await first.client.getTask(old.created.id)).status, 'cancelling');
  }
  // Send no signals to custodians/guards/Agents read from disk or observations.
  // Only the test's original service handle dies; production custody must act.
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = durable(f), original = taskRecord(before, old.created.id);
  assert.equal(original.attempts, scenario === 'verifier' ? 4 : 3);
  assert.equal(capacity(before).length, scenario === 'verifier' ? 1 : 2);
  assert.equal(decisions(before, old.created.id).length, 0); assert.deepEqual(original.task.artifactIds, []);
  assert.deepEqual(original.limits, old.body.limits); assert.equal(original.task.deadlineAt, old.created.deadlineAt);
  const interruptedWorkers = workers(before, old.created.id).filter(row => row.worker.status === 'running');
  assert.equal(interruptedWorkers.length, capacity(before).length);
  for (const row of interruptedWorkers) { assert.equal(row.cleanup, null); assert.equal(before.outbox.find(command => command.id === row.ticket.commandId).status, 'unknown'); }
  const second = await f.launch('open');
  const terminal = scenario === 'cancel' ? 'cancelled' : 'failed';
  const recovered = await until(async () => { const task = await second.client.getTask(old.created.id); return task.status === terminal && task; });
  await until(async () => { try { return (await second.client.request('ready.get')).ready; } catch (error) { if (error.code === 'not_ready') return false; throw error; } });
  await f.processesGone();
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
  assert.equal(recovered.deadlineAt, old.created.deadlineAt); assert.deepEqual(recovered.artifactIds, []);
  if (scenario !== 'cancel') assert.equal(recovered.code, 'service_interrupted');
  const oldAudit = await second.client.request('task.audit', {path: {taskId: old.created.id}});
  assert.equal(oldAudit.attempts, original.attempts); assert.equal(oldAudit.retryCount, 0); assert.equal(oldAudit.reworkCount, 0);
  assert.equal(oldAudit.acceptance.status, 'pending');
  await replay(second.client, old);
  if (scenario === 'cancel') {
    assert.deepEqual(await second.client.request('task.cancel', {path: {taskId: old.created.id}, body: cancelBody, idempotencyKey: 'interrupted-cancel'}), cancellation);
    assert.equal((await second.client.request('operation.get', {path: {operationId: cancellation.id}})).status, 'succeeded');
  }
  // /ready alone is insufficient: use the same live instance and real budget
  // admission for another complete planner -> authors -> verifier -> download.
  const next = await approved(second.client, input.id, 'custody next healthy team', 'next');
  const completed = await until(async () => { const task = await second.client.getTask(next.created.id); return task.status === 'completed' && task; });
  const downloads = await Promise.all(completed.artifactIds.map(id => second.client.downloadArtifact(id)));
  assert.equal(downloads.length, 2);
  const delivery = downloads.find(item => item.artifact.kind === 'delivery'); assert.ok(delivery);
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  const nextAudit = await second.client.request('task.audit', {path: {taskId: next.created.id}});
  assert.equal(nextAudit.attempts, 4); assert.equal(nextAudit.acceptance.status, 'passed');
  const nextCompletions = f.observations().filter(row => row.type === 'completion' && row.taskId === next.created.id);
  assert.equal(nextCompletions.length, 4); assert.ok(nextCompletions.every(row => row.cleanup.cleaned === true));
  assert.deepEqual(f.observations().filter(row => row.type === 'started' && row.taskId === old.created.id), oldStarts);
  const finalOld = await second.client.getTask(old.created.id);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
  const settled = durable(f); assert.equal(settled.generation, before.generation + 1); assert.equal(capacity(settled).length, 0);
  assert.equal(taskRecord(settled, old.created.id).attempts, original.attempts);
  assert.deepEqual(taskRecord(settled, old.created.id).limits, original.limits);
  assert.equal(decisions(settled, old.created.id).length, 0); assert.equal(decisions(settled, next.created.id).length, 1);
  for (const row of interruptedWorkers) {
    const current = workers(settled, old.created.id).find(value => value.worker.id === row.worker.id);
    assert.equal(current.worker.status, terminal);
    // Read the ORIGINAL custodian's signed output only after service shutdown.
    // No synthetic signature, hand-written cleanup or PID-based authority.
    const descriptor = row.custody.descriptor;
    const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', descriptor.custodyId + '.observation.json')));
    assert.equal(verifyObservation(descriptor, observation), true);
    assert.deepEqual(current.custody.descriptor, descriptor);
    assert.equal(current.custody.settledDigest, custodyDigest(observation));
    assert.deepEqual(current.cleanup, observation.payload.cleanup);
    assert.equal(current.cleanup.cleaned, true);
    const settlements = settled.events.filter(event => event.stream === old.created.id).map(event => decode(event).payload)
      .filter(event => event.type === 'worker.cleanup-reconciled' && event.workerId === row.worker.id);
    assert.equal(settlements.length, 1);
    assert.equal(settlements[0].observationDigest, current.custody.settledDigest);
    assert.equal(settled.outbox.find(command => command.id === row.ticket.commandId).status, 'observed');
  }
  // Already accepted upstream facts stay byte-identical when only the final
  // verifier dies; cleanup recovery must not rewrite business acceptance.
  for (const row of before.projections.filter(row => row.kind === 'attempt' && decode(row).worker?.status === 'completed'))
    assert.deepEqual(settled.projections.find(value => value.kind === row.kind && value.id === row.id), row);
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
  const starts = f.observations().filter(row => row.type === 'started');
  const third = await f.launch('open');
  assert.deepEqual(await third.client.getTask(old.created.id), finalOld);
  assert.deepEqual(await third.client.getTask(next.created.id), completed);
  await replay(third.client, old); await replay(third.client, next);
  assert.deepEqual((await third.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.equal((await third.client.request('ready.get')).ready, true);
  assert.equal((await third.client.request('supervisor.get')).activeWorkers, 0);
  assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null});
  const repeated = durable(f); assert.equal(repeated.generation, settled.generation + 1);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(repeated[key], settled[key], 'cleanup replay must be append/refund/start-free: ' + key);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts);
  f.completed = true;
});
