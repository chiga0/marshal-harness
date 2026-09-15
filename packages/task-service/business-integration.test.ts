import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {setImmediate as turn} from 'node:timers/promises';
import {createVerificationPort} from '../task-application/application.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {startTaskService} from './composition.mjs';

const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
async function until(predicate) {
  const deadline = Date.now() + 5000;
  while (!await predicate()) {assert.ok(Date.now() < deadline, 'bounded integration observation'); await turn();}
}
const proposal = {summary: '两个文件分支与独立验收', nodes: ['left', 'right', 'verify'].map(id => ({id,
  role: id === 'verify' ? 'verifier' : 'author', goal: '交付 ' + id, scope: [id], providerId: null})),
  edges: [{from: 'left', to: 'verify'}, {from: 'right', to: 'verify'}],
  deliverables: ['完整双文件快照'], acceptance: ['完整性和精确内容'], assumptions: []};
const binding = () => ({nodeId: 'verify', description: '独立固定夹具检查两个文件，不接受作者报告。',
  layouts: ['left', 'right'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify',
    inputs: ['left', 'right'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}})), allowedPaths: []}),
  deliveries: ['left', 'right'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});

// Real HTTP/Application/Store/FileBusiness/depot. Agent completion and checker
// execution/cleanup are controlled fixtures, NOT model/OS evidence.
async function fixture(t, {holdVerifier = false, failPrepare = false} = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-service-business-')));
  const root = path.join(parent, 'data'), prepared = new Map(), authors = [], released = [], observed = [], verifiers = [];
  let businessCloses = 0, service;
  const fact = ticket => ({executionId: 'fixture-' + ticket.workerId, startedAt: new Date().toISOString()});
  const provider = {id: 'fixture', start(input) {
    const ticket = prepared.get(input.cwd); assert.ok(ticket); const started = fact(ticket), completion = deferred();
    const end = () => completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
      outputText: ticket.role === 'planner' ? JSON.stringify(proposal) : 'author report does not accept',
      cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    if (ticket.role === 'planner') end();
    else {fs.writeFileSync(path.join(input.cwd, ticket.nodeId + '.txt'), ticket.nodeId, {mode: 0o644}); authors.push({ticket, end});}
    return {started: Promise.resolve(started), completion: completion.promise, stop() {
      completion.resolve({providerId: provider.id, status: 'cancelled', stopReason: 'cancelled', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
      return completion.promise;
    }};
  }};
  const verification = createVerificationPort({id: 'independent-fixture', policy: {id: 'both-files', version: '1', description: '固定夹具精确核对两个分支；不代表实机 checker。'},
    bindPlan: binding, start({ticket, prepared: input}) {
      const started = fact(ticket), completion = deferred(), item = {ticket, stopped: 0}; verifiers.push(item);
      const files = ['left', 'right'].map(name => ({path: name + '.txt', content: fs.readFileSync(path.join(input.cwd, name + '.txt'), 'utf8')}));
      assert.deepEqual(files.map(file => file.content), ['left', 'right']);
      const end = () => completion.resolve({type: 'verification', status: 'passed', cleanup: {started, cleaned: true, scope: 'controlled-fixture'},
        evidence: {name: 'checks.json', mediaType: 'application/json', content: Buffer.from('{"controlledChecks":2}')},
        delivery: {name: 'files.json', mediaType: 'application/json', content: Buffer.from(JSON.stringify(files))}});
      if (!holdVerifier) end();
      return {started: Promise.resolve(started), completion: completion.promise, stop() {item.stopped++; end(); return completion.promise;}};
    }});
  t.after(async () => {await service?.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  service = await startTaskService({root, mode: 'create', providers: new Map([[provider.id, provider]]), verification,
    supervisorOptions: {intervalMs: 5}, businessFactory: ports => {
      const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        approvedLayout: ticket => ports.approvedLayout(ticket), observeExecution: ticket => {
          const source = ports.observeExecution(ticket); observed.push({workerId: ticket.workerId, ...source}); return source;
        }});
      return {async prepare(ticket, context) {
        const input = await business.prepare(ticket, context); prepared.set(input.cwd, ticket);
        if (failPrepare) throw new Error('controlled prepare failure after actual FD allocation');
        return input;
      }, collect: (ticket, result, context) => business.collect(ticket, result, context),
      release(ticket) {released.push(ticket.workerId); business.release(ticket);},
      close() {businessCloses++; business.close();}};
    }});
  const connection = JSON.parse(fs.readFileSync(service.connectionFile));
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  return {service, client, authors, released, observed, verifiers, get businessCloses() {return businessCloses;},
    async approve() {
      const created = await client.createTask({intent: '交付双文件，独立完整验收', limits: {timeoutMs: 30000, maxAttempts: 6, maxWorkers: 2}}, 'business-create');
      let task;
      await until(async () => {task = await client.getTask(created.id); return task.status === 'awaiting-approval';});
      const plan = await client.request('task.plan', {path: {taskId: task.id}});
      assert.ok(plan.acceptance.some(value => value.includes('both-files')));
      await client.approveTask(task.id, {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}, 'business-approve');
      await until(() => authors.length === 2); // Both are outstanding before either is completed.
      return task.id;
    }};
}

test('one verification capability and actual business factory close HTTP plan-to-Decision/download path', async t => {
  const f = await fixture(t), taskId = await f.approve();
  f.authors.forEach(author => author.end());
  let task;
  await until(async () => {task = await f.client.getTask(taskId); return task.status === 'completed';});
  assert.equal(f.verifiers.length, 1); assert.equal(task.artifactIds.length, 2);
  const artifacts = await Promise.all(task.artifactIds.map(id => f.client.downloadArtifact(id)));
  const delivery = artifacts.find(item => item.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content.toString()), [{path: 'left.txt', content: 'left'}, {path: 'right.txt', content: 'right'}]);
  assert.equal((await f.client.request('task.audit', {path: {taskId}})).acceptance.status, 'passed');
  await until(() => f.released.length === 4);
  assert.equal(new Set(f.released).size, 4); assert.equal(f.observed.length, 3); // planner + authors, original App observations.
  assert.equal((await f.service.shutdown()).shutdownClean, true); assert.equal(f.businessCloses, 1);
});

test('verifier cancellation retains original receipt fence and releases every business handle once', async t => {
  const f = await fixture(t, {holdVerifier: true}), taskId = await f.approve(); f.authors.forEach(author => author.end());
  await until(() => f.verifiers.length === 1);
  const task = await f.client.getTask(taskId);
  await f.client.request('task.cancel', {path: {taskId}, body: {expectedRevision: task.revision}, idempotencyKey: 'cancel-verifier'});
  await until(async () => (await f.client.getTask(taskId)).status === 'cancelled');
  assert.equal((await f.client.getTask(taskId)).artifactIds.length, 0); assert.equal(f.verifiers[0].stopped, 1);
  await until(() => f.released.length === 4); assert.equal(new Set(f.released).size, 4);
  await f.service.shutdown(); assert.equal(f.businessCloses, 1);
});

test('prepare failure after real directory allocation releases without waiting for service shutdown', async t => {
  const f = await fixture(t, {failPrepare: true});
  const task = await f.client.createTask({intent: 'controlled preparation failure'}, 'prepare-failure');
  await until(() => f.released.length === 1);
  await until(async () => (await f.client.getTask(task.id)).status === 'failed');
  assert.equal(f.authors.length, 0); assert.equal(f.verifiers.length, 0);
  assert.equal(f.businessCloses, 0); // Per-worker release precedes global disposal.
  await f.service.shutdown(); assert.equal(f.businessCloses, 1);
});
