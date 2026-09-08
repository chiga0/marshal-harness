import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {setImmediate as turn} from 'node:timers/promises';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {Store} from '../task-store/store.mjs';
import {TaskApplication} from '../task-application/application.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {startTaskService} from './composition.mjs';

const context = {principal: 'local-operator'};
const defer = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
async function until(predicate, milliseconds = 5000) {
  const deadline = Date.now() + milliseconds;
  while (!await predicate()) { assert.ok(Date.now() < deadline, 'bounded observation timeout'); await turn(); }
}
const plan = {summary: '两个独立作者', nodes: ['one', 'two'].map(id => ({id, role: 'author', goal: 'deliver ' + id, scope: [id], providerId: null})),
  edges: [], deliverables: ['two files'], acceptance: ['independent verification'], assumptions: []};

// Controlled completion facts only; these tests do NOT claim OS/model cleanup.
class Provider {
  id = 'fixture'; workers = []; autoStop = true;
  start(input) {
    const completion = defer(), identity = JSON.parse(input.prompt);
    const started = {executionId: 'execution-' + identity.workerId, startedAt: new Date().toISOString()};
    const worker = {identity, input, completion, stopCount: 0, finish: extra => completion.resolve({providerId: this.id,
      status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true, scope: 'fixture', reason: 'fixture'}, ...extra})};
    this.workers.push(worker);
    return {started: Promise.resolve(started), completion: completion.promise, stop: () => {
      worker.stopCount++; if (this.autoStop) worker.finish({status: 'cancelled', stopReason: 'cancelled'});
      return completion.promise;
    }};
  }
}
function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-service-test-')));
  const root = path.join(parent, 'data'), provider = new Provider(), services = [];
  const config = {root, mode: 'create', providers: new Map([[provider.id, provider]]),
    prepare: (ticket, {executionParent}) => ({cwd: executionParent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role})}),
    collect: ticket => ticket.role === 'planner' ? {plan} : {result: {candidate: ticket.nodeId}},
    supervisorOptions: {intervalMs: 5}, leaseMs: 10000, renewIntervalMs: 1000};
  t.after(async () => {
    provider.autoStop = true;
    for (const worker of provider.workers) worker.finish({status: 'cancelled', stopReason: 'cancelled'});
    for (const service of services) await service.shutdown();
    fs.rmSync(parent, {recursive: true, force: true});
  });
  return {parent, root, provider, config,
    async start(options = {}) {const service = await startTaskService({...config, ...options}); services.push(service); return service;},
    client(service) {const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return new TaskClient({baseURL: connection.url, token: connection.token});},
  };
}

test('missing business composition fails before creating files or accepting work', async t => {
  const f = fixture(t);
  for (const options of [{prepare: undefined}, {collect: undefined}, {providers: new Map()}, {requestTimeoutMs: 0}]) {
    await assert.rejects(startTaskService({...f.config, ...options}), {code: 'service_invalid_configuration'});
    assert.equal(fs.existsSync(f.root), false);
  }
});

test('real loopback health/readiness, private token, origin/auth and no default model start', async t => {
  const f = fixture(t), service = await f.start(), client = f.client(service);
  assert.deepEqual({...await client.request('health.get')}, {status: 'ok', profile: 'node-task-service/v1'});
  assert.equal((await client.request('ready.get')).ready, true);
  assert.equal((await client.request('provider.list')).items[0].availability, 'unknown');
  assert.equal((await client.request('supervisor.get')).activeWorkers, 0);
  assert.equal(f.provider.workers.length, 0);
  const connection = JSON.parse(fs.readFileSync(service.connectionFile));
  assert.equal(fs.statSync(service.connectionFile).mode & 0o777, 0o600);
  assert.equal(fs.statSync(f.root).mode & 0o777, 0o700);
  assert.equal(JSON.stringify(service.snapshot()).includes(connection.token), false);
  const unauthenticated = await fetch(service.address + '/v1/tasks');
  assert.equal(unauthenticated.status, 401); await unauthenticated.arrayBuffer();
  const origin = await fetch(service.address + '/health', {headers: {Origin: 'https://untrusted.invalid'}});
  assert.equal(origin.status, 403); await origin.arrayBuffer();
  const closed = await service.shutdown(); assert.equal(closed.shutdownClean, true);
  assert.equal(fs.existsSync(service.connectionFile), true); // Evidence remains, token no longer served.
});

test('exact renewal keeps SQLite owner and Application aligned beyond initial expiry', async t => {
  const f = fixture(t), service = await f.start({leaseMs: 250, renewIntervalMs: 40}), client = f.client(service);
  const originalGeneration = service.snapshot().generation;
  const deadline = Date.now() + 700;
  while (Date.now() < deadline) { await client.request('ready.get'); await turn(); }
  assert.equal((await client.request('task.list')).items.length, 0);
  assert.equal(service.snapshot().generation, originalGeneration); // Renew, not re-claim.
  assert.equal(service.snapshot().failure, null);
});

test('HTTP input upload/download uses actual Application/depot and persists across reopen', async t => {
  const f = fixture(t), service = await f.start(), client = f.client(service);
  const body = {name: 'business-input.txt', mediaType: 'text/plain', contentBase64: Buffer.from('business bytes').toString('base64')};
  const uploaded = await client.request('input.create', {body, idempotencyKey: 'input-key'});
  assert.equal(uploaded.kind, 'input'); assert.equal(uploaded.taskId, null);
  const downloaded = await client.downloadArtifact(uploaded.id);
  assert.equal(downloaded.content.toString('utf8'), 'business bytes');
  await service.shutdown();
  const reopened = await f.start({mode: 'open'}), next = f.client(reopened);
  assert.deepEqual(await next.request('input.create', {body, idempotencyKey: 'input-key'}), uploaded);
  assert.equal((await next.downloadArtifact(uploaded.id)).content.toString('utf8'), 'business bytes');
  assert.equal(f.provider.workers.length, 0);
});

test('HTTP create-plan-approve drives two workers; shutdown waits cleanup and cold replay is exact', async t => {
  const f = fixture(t), service = await f.start(), client = f.client(service);
  const body = {intent: 'deliver two files', limits: {timeoutMs: 30000, maxAttempts: 5, maxWorkers: 2}};
  const original = await client.createTask(body, 'original-create');
  await until(() => f.provider.workers.length === 1); f.provider.workers[0].finish();
  let task;
  await until(async () => {task = await client.getTask(original.id); return task.status === 'awaiting-approval';});
  const approved = {expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest};
  await client.approveTask(task.id, approved, 'original-approve');
  await until(() => f.provider.workers.length === 3);
  await until(async () => (await client.request('supervisor.get')).activeWorkers === 2);
  f.provider.autoStop = false;
  const closing = service.shutdown();
  await until(() => f.provider.workers.slice(1).every(worker => worker.stopCount === 1));
  assert.notEqual(service.snapshot().state, 'closed');
  f.provider.workers.slice(1).forEach(worker => worker.finish({status: 'cancelled', stopReason: 'cancelled'}));
  assert.equal((await closing).shutdownClean, true);
  const reopened = await f.start({mode: 'open'}), next = f.client(reopened);
  assert.notEqual(reopened.connectionFile, service.connectionFile);
  assert.notEqual(reopened.snapshot().generation, service.snapshot().generation);
  assert.deepEqual(await next.createTask(body, 'original-create'), original);
  const terminal = await next.getTask(original.id);
  assert.equal(terminal.status, 'failed'); // Graceful shutdown is NOT transparent resume.
  assert.equal(f.provider.workers.length, 3);
});

test('HTTP cancel finishes only after controlled cleanup, original key does not restart', async t => {
  const f = fixture(t), service = await f.start(), client = f.client(service);
  const task = await client.createTask({intent: 'cancel planned work'}, 'cancel-create');
  await until(() => f.provider.workers.length === 1);
  const latest = await client.getTask(task.id), options = {path: {taskId: task.id}, body: {expectedRevision: latest.revision}, idempotencyKey: 'cancel-key'};
  const operation = await client.request('task.cancel', options);
  await until(async () => (await client.getTask(task.id)).status === 'cancelled');
  assert.deepEqual(await client.request('task.cancel', options), operation);
  assert.equal(f.provider.workers.length, 1);
  assert.equal((await client.request('operation.get', {path: {operationId: operation.id}})).status, 'succeeded');
});

test('same root refuses concurrent owner and unknown/partial layouts without repairs', async t => {
  const f = fixture(t), service = await f.start();
  await assert.rejects(f.start({mode: 'open'}), {code: 'service_start_unavailable'});
  assert.equal((await f.client(service).request('ready.get')).ready, true);
  await service.shutdown();
  fs.writeFileSync(path.join(f.root, 'unknown'), 'keep');
  await assert.rejects(f.start({mode: 'open'}), {code: 'service_root_unavailable'});
  assert.equal(fs.readFileSync(path.join(f.root, 'unknown'), 'utf8'), 'keep');
  const partial = path.join(f.parent, 'partial'); fs.mkdirSync(partial, {mode: 0o700});
  await assert.rejects(f.start({root: partial, mode: 'open'}));
  assert.deepEqual(fs.readdirSync(partial), []);
});

test('root identity drift drops readiness and closes without deleting evidence', async t => {
  const f = fixture(t), service = await f.start({leaseMs: 500, renewIntervalMs: 40});
  fs.renameSync(f.root, f.root + '-old'); fs.mkdirSync(f.root, {mode: 0o700});
  await until(() => service.snapshot().state === 'closed');
  assert.ok(['service_owner_unavailable', 'service_supervisor_failed'].includes(service.snapshot().failure));
  assert.equal(fs.existsSync(f.root + '-old/store/authority.sqlite'), true);
});

test('cold prior execution remains intervention, never starts a replacement or claims ready', async t => {
  const f = fixture(t), initial = await f.start(); await initial.shutdown();
  const store = Store.openExisting(path.join(f.root, 'store'));
  const owner = store.claimOwner(store.info().generation, 'fixture-prior-owner', Date.now() + 10000);
  const app = new TaskApplication({store, owner, execution: {maxWorkers: 2, providerIds: ['fixture'], defaultProvider: 'fixture'}});
  const task = await app.dispatch({operation: 'task.create', key: 'crashed-create', body: {intent: 'uncertain previous start'}}, context);
  const command = app.execution.poll().items[0], ticket = app.execution.nextWork(command.id, command.revision);
  assert.ok(ticket); store.close(); // Controlled crash-shaped reservation, not a real process crash.
  const reopened = await f.start({mode: 'open'}), client = f.client(reopened);
  assert.equal((await client.getTask(task.id)).status, 'intervention');
  assert.equal((await client.request('supervisor.get')).activeWorkers, 1);
  await assert.rejects(client.request('ready.get'), {code: 'not_ready'});
  await assert.rejects(client.createTask({intent: 'not admitted'}, 'new-task'), {code: 'not_ready'});
  assert.equal(f.provider.workers.length, 0);
});

test('old unreserved pending task can be cancelled after reopen without permanently blocking readiness', async t => {
  const f = fixture(t), initial = await f.start(); await initial.shutdown();
  const store = Store.openExisting(path.join(f.root, 'store'));
  const owner = store.claimOwner(store.info().generation, 'fixture-before-reservation', Date.now() + 10000);
  const app = new TaskApplication({store, owner, execution: {maxWorkers: 2, providerIds: ['fixture'], defaultProvider: 'fixture'}});
  const task = await app.dispatch({operation: 'task.create', key: 'pending-create', body: {intent: 'not reserved'}}, context);
  const command = app.execution.poll().items[0];
  assert.equal(command.status, 'pending');
  assert.equal((await app.dispatch({operation: 'task.workers', taskId: task.id}, context)).items.length, 0);
  store.close();
  const reopened = await f.start({mode: 'open'}), client = f.client(reopened);
  await assert.rejects(client.request('ready.get'), {code: 'not_ready'});
  const latest = await client.getTask(task.id);
  await client.request('task.cancel', {path: {taskId: task.id}, body: {expectedRevision: latest.revision}, idempotencyKey: 'pending-cancel'});
  await until(async () => (await client.getTask(task.id)).status === 'cancelled');
  assert.equal((await client.request('ready.get')).ready, true);
  assert.equal(f.provider.workers.length, 0);
  const next = await client.createTask({intent: 'next valid task'}, 'after-cancel-create');
  assert.equal(next.status, 'draft');
  await until(() => f.provider.workers.length === 1);
  assert.equal((await client.request('task.audit', {path: {taskId: task.id}})).attempts, 0);
});

test('recovery preserves create/approve/input receipts and conflicts before new-key readiness gate', async t => {
  const f = fixture(t), initial = await f.start(), first = f.client(initial);
  const inputBody = {name: 'source.txt', mediaType: 'text/plain', contentBase64: Buffer.from('original').toString('base64')};
  const input = await first.request('input.create', {body: inputBody, idempotencyKey: 'preserved-input'});
  await initial.shutdown();
  let store = Store.openExisting(path.join(f.root, 'store'));
  let owner = store.claimOwner(store.info().generation, 'fixture-receipt-owner', Date.now() + 10000);
  const app = new TaskApplication({store, owner, execution: {maxWorkers: 2, providerIds: ['fixture'], defaultProvider: 'fixture'}});
  const body = {intent: 'approved task before restart'};
  const task = await app.dispatch({operation: 'task.create', key: 'preserved-create', body}, context);
  const proposed = app.proposePlan(task.id, task.revision, plan);
  const latest = await app.dispatch({operation: 'task.get', taskId: task.id}, context);
  const approveBody = {expectedRevision: latest.revision, planRevision: proposed.revision, planDigest: proposed.digest};
  const approved = await app.dispatch({operation: 'task.approve', taskId: task.id, key: 'preserved-approve', body: approveBody}, context);
  const unknown = await app.dispatch({operation: 'task.create', key: 'unknown-start', body: {intent: 'unknown execution'}}, context);
  const command = app.execution.poll().items.find(command => command.taskId === unknown.id);
  const ticket = app.execution.nextWork(command.id, command.revision); assert.ok(ticket);
  const authority = () => store.read(owner, tx => ({
    commands: tx.commands('', 100).map(({id, revision, status}) => ({id, revision, status})),
    capacity: JSON.parse(tx.projection('budget', 'service-capacity').bytes.toString('utf8')),
  }));
  const before = authority(); store.close();
  const reopened = await f.start({mode: 'open'}), client = f.client(reopened);
  await assert.rejects(client.request('ready.get'), {code: 'not_ready'});
  assert.deepEqual({...await client.createTask(body, 'preserved-create')}, task);
  assert.deepEqual({...await client.approveTask(task.id, approveBody, 'preserved-approve')}, approved);
  assert.deepEqual(await client.request('input.create', {body: inputBody, idempotencyKey: 'preserved-input'}), input);
  await assert.rejects(client.createTask({intent: 'changed'}, 'preserved-create'), {code: 'idempotency_conflict'});
  await assert.rejects(client.approveTask(task.id, {...approveBody, expectedRevision: approveBody.expectedRevision + 1}, 'preserved-approve'), {code: 'idempotency_conflict'});
  await assert.rejects(client.request('input.create', {body: {...inputBody, name: 'changed.txt'}, idempotencyKey: 'preserved-input'}), {code: 'idempotency_conflict'});
  await assert.rejects(client.createTask(body, 'new-create'), {code: 'not_ready'});
  await assert.rejects(client.approveTask(task.id, approveBody, 'new-approve'), {code: 'not_ready'});
  await assert.rejects(client.request('input.create', {body: inputBody, idempotencyKey: 'new-input'}), {code: 'not_ready'});
  assert.equal(f.provider.workers.length, 0);
  assert.equal((await client.request('task.audit', {path: {taskId: unknown.id}})).attempts, 1);
  await reopened.shutdown();
  store = Store.openExisting(path.join(f.root, 'store'));
  owner = store.claimOwner(store.info().generation, 'fixture-inspection', Date.now() + 10000);
  try {assert.deepEqual(authority(), before);} finally {store.close();}
});

test('missing cleanup is retained as intervention and shutdown cannot report clean', async t => {
  const f = fixture(t), service = await f.start(), client = f.client(service);
  const task = await client.createTask({intent: 'unknown cleanup'}, 'unknown-create');
  await until(() => f.provider.workers.length === 1);
  f.provider.workers[0].finish({cleanup: {started: null, cleaned: false, scope: 'unconfirmed', reason: 'fixture-missing'}});
  await until(async () => (await client.getTask(task.id)).status === 'intervention');
  const result = await service.shutdown();
  assert.equal(result.shutdownClean, false); assert.equal(result.failure, 'service_cleanup_unconfirmed');
  const reopened = await f.start({mode: 'open'});
  assert.equal((await f.client(reopened).getTask(task.id)).status, 'intervention');
});

test('checked-in Node CLI starts one HTTP service, prints no token and closes on its SIGTERM', async t => {
  const f = fixture(t), entry = fileURLToPath(new URL('./main.mjs', import.meta.url));
  const config = fileURLToPath(new URL('./service.fixture.mjs', import.meta.url));
  const child = spawn(process.execPath, [entry, '--root', f.root, '--mode', 'create', '--config', config], {stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '';
  child.stdout.on('data', chunk => {stdout += chunk; if (stdout.length > 8192) child.kill('SIGTERM');});
  child.stderr.on('data', chunk => {stderr += chunk; if (stderr.length > 8192) child.kill('SIGTERM');});
  const done = new Promise(resolve => child.once('exit', (code, signal) => resolve({code, signal})));
  t.after(async () => {if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await done;});
  await until(() => stdout.includes('\n'));
  const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(first.connectionFile));
  const client = new TaskClient({baseURL: first.address, token: connection.token});
  assert.equal((await client.request('ready.get')).ready, true);
  child.kill('SIGTERM');
  assert.deepEqual(await done, {code: 0, signal: null});
  assert.equal((stdout + stderr).includes(connection.token), false);
  assert.equal(JSON.parse(stdout.trim().split('\n').at(-1)).clean, true);
});
