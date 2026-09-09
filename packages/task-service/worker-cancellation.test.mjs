import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {encode} from '../task-store/store.mjs';
import {data, expected} from './worker-cancellation.fixture.mjs';
const cli = fileURLToPath(new URL('./main.mjs', import.meta.url)), config = fileURLToPath(new URL('./worker-cancellation.fixture.mjs', import.meta.url));
const equal = (a, b) => assert.deepEqual(encode(a), encode(b));
async function until(fn, ms = 16000) {const deadline = Date.now() + ms; for (;;) {const value = await fn(); if (value) return value;
  assert.ok(Date.now() < deadline, 'bounded worker cancellation observation'); await pause(10);}}
function journal(parent) {try {const text = fs.readFileSync(path.join(parent, 'worker-observations.jsonl'), 'utf8');
  return text.slice(0, text.lastIndexOf('\n') + 1).trim().split('\n').filter(Boolean).map(JSON.parse);} catch (e) {if (e.code === 'ENOENT') return []; throw e;}}
async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-worker-cancel-'))), root = path.join(parent, 'data'), children = [];
  let complete = false;
  t.after(async () => {for (const service of children) await service.stop('SIGKILL');
    if (complete) fs.rmSync(parent, {recursive: true, force: true}); else t.diagnostic('Preserved worker cancellation evidence: ' + parent);});
  const f = {parent, root, journal: () => journal(parent), complete() {complete = true;},
    release(workerId) {fs.writeFileSync(path.join(parent, 'release', workerId), 'release\n', {flag: 'wx', mode: 0o600});},
    snapshot() {assert.ok(children.every(child => child.exited)); const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true, allowExtension: false});
      try {db.exec('PRAGMA query_only=ON'); const rows = table => db.prepare('SELECT * FROM ' + table).all().map(row => Object.fromEntries(Object.entries(row)
        .map(([key, value]) => [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
        return {events: rows('events'), projections: rows('projections'), receipts: rows('receipts'), outbox: rows('outbox'), heads: rows('heads')};} finally {db.close();}},
    async launch(mode, cut = 'none', extra = {}) {
      const child = spawn(process.execPath, [cli, '--root', root, '--mode', mode, '--config', extra.config ?? config], {cwd: parent,
        env: {WORKER_CANCEL_CUT: cut, ...extra.env}, stdio: ['ignore', 'pipe', 'pipe']});
      let output = '', errors = '', exited = false;
      const done = new Promise(resolve => {child.once('error', error => {exited = true; resolve({code: null, signal: error.code});});
        child.once('close', (code, signal) => {exited = true; resolve({code, signal});});});
      child.stdout.on('data', bytes => {output += bytes; if (output.length > 16384) child.kill('SIGKILL');});
      child.stderr.on('data', bytes => {errors += bytes; if (errors.length > 16384) child.kill('SIGKILL');});
      const service = {get exited() {return exited;}, async stop(signal) {if (!exited) child.kill(signal);
        const timer = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 10000);
        try {await until(() => exited); const result = await done;
          if (signal === 'SIGTERM') {assert.deepEqual(result, {code: 0, signal: null}); assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true);} return result;
        } finally {clearTimeout(timer);}}}; children.push(service);
      await until(() => {assert.equal(exited, false, 'CLI: ' + errors); return output.includes('\n');}, 20000);
      const line = JSON.parse(output.slice(0, output.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(line.connectionFile));
      assert.equal((output + errors).includes(connection.token), false);
      service.client = new TaskClient({baseURL: connection.url, token: connection.token}); return service;
    }}; return f;
}
async function create(client, intent, key = 'task') {
  const input = await client.request('input.create', {idempotencyKey: key + '-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const body = {intent, context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const original = await client.createTask(body, key); return {original, body, key};
}
async function approve(client, task) {
  const current = await until(async () => {const value = await client.getTask(task.original.id); return value.status === 'awaiting-approval' && value;});
  const body = {expectedRevision: current.revision, planRevision: current.plan.revision, planDigest: current.plan.digest};
  await client.approveTask(current.id, body, task.key + '-approve'); return current.id;
}
const workers = (client, taskId) => client.request('task.workers', {path: {taskId}});
async function cancel(client, target, key = 'target-stop') {
  const body = {expectedRevision: (await client.getTask(target.taskId)).revision};
  return {body, key, operation: await client.cancelWorker(target.id, body, key)};
}
async function healthy(client, key) {
  const task = await create(client, 'healthy', key), taskId = await approve(client, task);
  const completed = await until(async () => {const value = await client.getTask(taskId); return value.status === 'completed' && value;});
  const files = await Promise.all(completed.artifactIds.map(id => client.downloadArtifact(id))), delivery = files.find(file => file.artifact.kind === 'delivery');
  assert.ok(delivery); assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected); return delivery;
}
test('real CLI target A stops; B original handle continues, downstream never starts, next team downloads and cold receipts stay exact', {timeout: 50000}, async t => {
  const f = await fixture(t), service = await f.launch('create'), c = service.client, task = await create(c, 'hold-authors'), taskId = await approve(c, task);
  const pair = await until(async () => {const values = (await workers(c, taskId)).items.filter(w => w.role === 'author' && w.startedAt); return values.length === 2 && values;});
  const a = pair.find(w => w.nodeId === 'east'), b = pair.find(w => w.nodeId === 'west'), cancelled = await cancel(c, a);
  assert.equal(cancelled.operation.workerId, a.id); assert.equal(cancelled.operation.status, 'accepted');
  await until(async () => (await c.request('operation.get', {path: {operationId: cancelled.operation.id}})).status === 'succeeded');
  const interim = (await workers(c, taskId)).items; assert.equal(interim.find(w => w.id === a.id).status, 'cancelled');
  assert.equal(interim.find(w => w.id === b.id).status, 'running'); assert.equal(f.journal().some(row => row.type === 'stop' && row.workerId === b.id), false);
  assert.equal(f.journal().some(row => row.type === 'completion' && row.workerId === b.id), false);
  f.release(b.id); const final = await until(async () => {const value = await c.getTask(taskId); return value.status === 'failed' && value;});
  assert.equal(final.code, 'worker_cancelled'); equal(final.artifactIds, []);
  const graph = await c.request('task.graph', {path: {taskId}}), verifier = graph.nodes.find(node => node.role === 'verifier');
  assert.equal(verifier.status, 'cancelled'); equal(verifier.workerIds, []);
  const audit = await c.getAudit(taskId); assert.equal(audit.attempts, 3); assert.equal(audit.acceptance.status, 'pending'); assert.equal(audit.acceptance.digest, null);
  equal(await c.cancelWorker(a.id, cancelled.body, cancelled.key), cancelled.operation);
  const delivery = await healthy(c, 'fresh'); assert.equal((await c.request('supervisor.get')).activeWorkers, 0);
  await service.stop('SIGTERM'); const state = f.snapshot(), starts = f.journal().filter(row => row.type === 'started');
  const retained = state.projections.map(row => {try {return JSON.parse(Buffer.from(row.bytes, 'hex'));} catch {return {};}}).find(value => value.worker?.id === b.id);
  assert.equal(retained.worker.status, 'completed'); assert.ok(retained.resultRef); assert.equal(retained.cleanup.cleaned, true);
  const second = await f.launch('open'); equal(await second.client.cancelWorker(a.id, cancelled.body, cancelled.key), cancelled.operation);
  equal(await second.client.getTask(taskId), final); assert.deepEqual((await second.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  await second.stop('SIGTERM'); assert.deepEqual(f.snapshot(), state); assert.deepEqual(f.journal().filter(row => row.type === 'started'), starts); f.complete();
});
for (const role of ['planner', 'verifier']) test('real target ' + role + ' cannot submit a plan or independent Decision after stop', {timeout: 40000}, async t => {
  const f = await fixture(t), service = await f.launch('create'), c = service.client, task = await create(c, 'hold-' + role);
  if (role !== 'planner') await approve(c, task);
  const target = await until(async () => (await workers(c, task.original.id)).items.find(w => w.role === role && w.startedAt));
  const stop = await cancel(c, target); await until(async () => (await c.getTask(task.original.id)).status === 'failed');
  assert.equal((await c.request('operation.get', {path: {operationId: stop.operation.id}})).status, 'succeeded');
  const value = await c.getTask(task.original.id); assert.equal(value.code, 'worker_cancelled'); equal(value.artifactIds, []);
  if (role === 'planner') assert.equal(value.plan, null);
  assert.equal((await c.getAudit(value.id)).acceptance.status, 'pending'); assert.equal((await c.request('supervisor.get')).activeWorkers, 0);
  await service.stop('SIGTERM'); f.complete();
});
test('v6 Git prepared but unbound crash keeps target UNKNOWN even after switching to staging-only configuration', {timeout: 60000}, async t => {
  const f = await fixture(t), options = {config: fileURLToPath(new URL('./worker-cancellation-git.fixture.mjs', import.meta.url)), env: {WORKER_GIT_FIXTURE: '1'}};
  const first = await f.launch('create', 'none', options), client = first.client;
  const description = fs.readFileSync(path.join(f.parent, 'git-input.json'), 'utf8');
  const task = await client.createTask({intent: '原 Git 两库候选，不授权任何无许可准备', context: {text: description},
    limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 1}}, 'unbound-git');
  const ready = await until(async () => {const value = await client.getTask(task.id); return value.status === 'awaiting-approval' && value;});
  await client.approveTask(task.id, {expectedRevision: ready.revision, planRevision: ready.plan.revision, planDigest: ready.plan.digest}, 'git-plan');
  const prepared = await until(() => f.journal().find(row => row.type === 'git-prepared'));
  assert.equal(fs.statSync(path.join(prepared.cwd, '.git')).isFile(), true);
  const body = {expectedRevision: (await client.getTask(task.id)).revision};
  const pending = client.cancelWorker(prepared.workerId, body, 'git-unbound-stop').catch(error => error);
  await until(() => f.journal().some(row => row.type === 'barrier'));
  await first.stop('SIGKILL'); assert.equal((await pending).code, 'client_transport_error');
  const before = f.snapshot(), decode = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
  const prior = before.projections.map(decode).find(row => row.worker?.id === prepared.workerId);
  assert.equal(prior.ticket.startProtocol, undefined); assert.equal(prior.custody, undefined); assert.equal(prior.executionId, null); assert.equal(prior.cleanup, null);
  assert.equal(before.receipts.filter(row => row.operation === 'worker.cancel').length, 1);
  assert.equal(before.receipts.filter(row => row.scope === prepared.workerId && row.operation === 'execution.custody-binding').length, 0);
  assert.equal(before.events.map(decode).filter(row => row.payload?.workerId === prepared.workerId && row.payload.type === 'worker.custody-permitted').length, 0);
  let originalReceipt;
  for (const safe of [false, true]) {
    const current = await f.launch('open', 'none', {...options, env: {...options.env, WORKER_GIT_SWITCH_SAFE: safe ? '1' : '0'}});
    const receipt = await current.client.cancelWorker(prepared.workerId, body, 'git-unbound-stop');
    if (originalReceipt) equal(receipt, originalReceipt); else originalReceipt = receipt;
    assert.equal((await current.client.request('operation.get', {path: {operationId: receipt.id}})).status, 'unknown');
    const taskNow = await current.client.getTask(task.id); assert.equal(taskNow.status, 'intervention'); equal(taskNow.artifactIds, []);
    assert.equal((await current.client.getAudit(task.id)).attempts, 2); assert.equal((await current.client.request('supervisor.get')).activeWorkers, 1);
    await assert.rejects(current.client.request('ready.get')); await current.stop('SIGKILL');
    const state = f.snapshot(), worker = state.projections.map(decode).find(row => row.worker?.id === prepared.workerId);
    equal(worker.ticket, prior.ticket); assert.equal(worker.cleanup, null); assert.equal(worker.unpermittedSettlement, undefined);
    equal(worker.worker, prior.worker); assert.equal(fs.statSync(path.join(prepared.cwd, '.git')).isFile(), true);
    assert.deepEqual(state.receipts, before.receipts); assert.equal(f.journal().filter(row => row.type === 'git-prepared').length, 1);
  }
  f.complete();
});

for (const cut of ['stop-before', 'stop-after', 'settle-before', 'settle-after']) test('worker cancellation ' + cut + ' COMMIT: original SIGKILL, exact receipt and next HTTP team', {timeout: 60000}, async t => {
  const f = await fixture(t), first = await f.launch('create', cut), c = first.client, task = await create(c, 'hold-authors'), taskId = await approve(c, task);
  const pair = await until(async () => {const values = (await workers(c, taskId)).items.filter(w => w.role === 'author' && w.startedAt); return values.length === 2 && values;});
  const a = pair.find(w => w.nodeId === 'east'), body = {expectedRevision: (await c.getTask(taskId)).revision};
  const response = c.cancelWorker(a.id, body, 'lost-stop').then(value => ({value}), error => ({error: error.code}));
  const marker = await until(() => f.journal().find(row => row.type === 'barrier'));
  assert.equal(marker.cut, cut); assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const received = await response, before = f.snapshot(), accepted = cut !== 'stop-before';
  assert.equal(before.receipts.filter(row => row.operation === 'worker.cancel').length, accepted ? 1 : 0);
  if (cut.startsWith('stop-')) assert.equal(received.error, 'client_transport_error');
  const second = await f.launch('open'), client = second.client, final = await client.getTask(taskId);
  assert.equal(final.status, 'failed'); assert.equal((await client.getAudit(taskId)).attempts, 3);
  assert.equal((await client.request('supervisor.get')).activeWorkers, 0); assert.equal((await client.request('ready.get')).ready, true);
  if (accepted) {
    const receipt = await client.cancelWorker(a.id, body, 'lost-stop'); assert.equal(receipt.workerId, a.id);
    assert.equal((await client.request('operation.get', {path: {operationId: receipt.id}})).status, 'succeeded');
    if (received.value) equal(receipt, received.value);
  } else await assert.rejects(client.cancelWorker(a.id, body, 'lost-stop'), {code: 'revision_conflict'});
  const delivery = await healthy(client, 'new-after-crash'); await second.stop('SIGTERM');
  const settled = f.snapshot(), starts = f.journal().filter(row => row.type === 'started');
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
  const third = await f.launch('open'); equal(await third.client.getTask(taskId), final);
  assert.deepEqual((await third.client.downloadArtifact(delivery.artifact.id)).content, delivery.content); await third.stop('SIGTERM');
  assert.deepEqual(f.snapshot(), settled); assert.deepEqual(f.journal().filter(row => row.type === 'started'), starts); f.complete();
});
