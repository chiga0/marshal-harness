import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {Store} from '../task-store/store.mjs';
import {TaskApplication} from './application.mjs';
import {validate} from '../task-api/contract.mjs';

const NOW = 1800000000000;
const context = {principal: 'local-operator'};
const failure = (code, status) => error => error.code === code && error.status === status;
const body = () => ({intent: '实现一个可运行的业务报表交付',
  context: {text: '根据提供的业务需求产出程序和使用说明，不发布外部系统。'},
  requirements: {deliverables: ['可运行代码', '使用说明'], acceptance: ['交叉校验结果']},
  limits: {timeoutMs: 60000, maxAttempts: 8, maxWorkers: 2}});
const proposal = () => ({summary: '并行实现计算与消费端，再独立校验交付',
  nodes: [{id: 'compute', role: 'author', goal: '实现计算', scope: ['compute'], providerId: null},
    {id: 'consumer', role: 'author', goal: '实现消费者', scope: ['consumer'], providerId: null},
    {id: 'check', role: 'verifier', goal: '独立验收', scope: ['verification'], providerId: null}],
  edges: [{from: 'compute', to: 'check'}, {from: 'consumer', to: 'check'}],
  deliverables: ['可运行代码与说明'], acceptance: ['独立消费实际成果并核对'], assumptions: []});
function fixture(t, options = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-application-test-')));
  const root = path.join(parent, 'state');
  let now = NOW;
  let store = Store.create(root, {clock: () => now});
  let owner = store.claimOwner(0, 'server-1', NOW + 3600000);
  let app = new TaskApplication({store, owner, clock: () => now, ...options});
  t.after(() => { store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  return {get app() { return app; }, get store() { return store; }, get owner() { return owner; },
    advance(ms) { now += ms; },
    call(request) { return app.dispatch(request, context); },
    counts() { return store.read(owner, tx => ({commands: tx.commands(), tasks: tx.projections('task')})); },
    reopen() {
      store.close(); store = Store.openExisting(root, {clock: () => now});
      owner = store.claimOwner(owner.generation, 'server-next', now + 3600000);
      app = new TaskApplication({store, owner, clock: () => now, ...options});
    }};
}
async function planned(f) {
  const created = await f.call({operation: 'task.create', key: 'create-1', body: body()});
  const plan = f.app.proposePlan(created.id, created.revision, proposal());
  const task = await f.call({operation: 'task.get', taskId: created.id});
  const approval = {operation: 'task.approve', key: 'approve-1', taskId: task.id,
    body: {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}};
  return {created, task, plan, approval};
}

test('real SQLite: create, plan, exact confirmation, original receipt and outbox survive reopen', async t => {
  const f = fixture(t), {created, task, plan, approval} = await planned(f);
  assert.equal(created.status, 'draft'); assert.equal(task.status, 'awaiting-approval');
  assert.match(plan.digest, /^sha256:[a-f0-9]{64}$/);
  assert.equal(f.counts().commands.length, 1);
  const op = await f.call(approval);
  assert.equal(op.status, 'accepted'); assert.equal(op.taskRevision, 3);
  assert.equal(f.counts().commands.length, 2);
  assert.deepEqual(await f.call(approval), op);
  const oldApp = f.app; f.reopen();
  assert.deepEqual(await f.call(approval), op);
  assert.deepEqual(await f.call({operation: 'operation.get', operationId: op.id}), op);
  assert.equal((await f.call({operation: 'task.get', taskId: task.id})).status, 'queued');
  assert.equal(f.counts().commands.length, 2);
  await assert.rejects(oldApp.dispatch(approval, context), failure('application_unavailable', 503));
  // Reopening storage does not relabel an unobserved launch as completed or
  // implicitly retry it under the new owner's generation.
  assert.ok(f.counts().commands.every(command => command.generation === 1n && command.status === 'pending'));
});

test('exact-key conflict, stale revision and forged plan each reject without partial events or dispatch', async t => {
  const f = fixture(t), {task, approval} = await planned(f);
  await f.call(approval);
  const before = await f.call({operation: 'task.events', taskId: task.id});
  await assert.rejects(f.call({...approval, body: {...approval.body, expectedRevision: 3}}), failure('idempotency_conflict', 409));
  await assert.rejects(f.call({...approval, key: 'new-key'}), failure('revision_conflict', 409));
  assert.deepEqual(await f.call({operation: 'task.events', taskId: task.id}), before);
  assert.equal(f.counts().commands.length, 2);
  const other = fixture(t), p = await planned(other);
  await assert.rejects(other.call({...p.approval, body: {...p.approval.body, planDigest: 'sha256:' + '0'.repeat(64)}}), failure('plan_conflict', 409));
  assert.equal(other.counts().commands.length, 1);
  assert.equal((await other.call({operation: 'task.get', taskId: p.task.id})).revision, 2);
});

test('cancel is a durable fence and stop obligation, never manufactured terminal cleanup', async t => {
  const f = fixture(t), {task, approval} = await planned(f);
  const approved = await f.call(approval);
  const command = {operation: 'task.cancel', taskId: task.id, key: 'cancel-1', body: {expectedRevision: approved.taskRevision}};
  const op = await f.call(command);
  assert.equal(op.status, 'accepted');
  const cancelled = await f.call({operation: 'task.get', taskId: task.id});
  assert.equal(cancelled.status, 'cancelling'); assert.deepEqual(cancelled.allowedActions, []);
  assert.equal(f.counts().commands.filter(c => c.kind === 'stop').length, 1);
  f.reopen(); assert.deepEqual(await f.call(command), op);
  assert.equal((await f.call({operation: 'task.get', taskId: task.id})).status, 'cancelling');
  await assert.rejects(f.call({...approval, key: 'later', body: {...approval.body, expectedRevision: 4}}), failure('state_conflict', 409));
});

test('pause/resume keep absolute deadline, exact plan and attempt budget, not completion', async t => {
  const f = fixture(t), {task, approval} = await planned(f);
  await f.call(approval);
  await f.call({operation: 'task.pause', taskId: task.id, key: 'pause-1', body: {expectedRevision: 3}});
  f.advance(1000);
  await f.call({operation: 'task.resume', taskId: task.id, key: 'resume-1', body: {expectedRevision: 4}});
  const current = await f.call({operation: 'task.get', taskId: task.id});
  assert.equal(current.deadlineAt, task.deadlineAt); assert.deepEqual(current.plan, task.plan);
  assert.equal(current.status, 'queued');
  assert.equal((await f.call({operation: 'task.audit', taskId: task.id})).attempts, 0);
});

test('expired confirmation, invalid plan and graph without a plan have no authority', async t => {
  const f = fixture(t), created = await f.call({operation: 'task.create', key: 'c', body: body()});
  await assert.rejects(f.call({operation: 'task.graph', taskId: created.id}), failure('plan_conflict', 409));
  assert.throws(() => f.app.proposePlan(created.id, 1, {...proposal(), edges: [{from: 'check', to: 'check'}]}), failure('invalid_plan_graph', 400));
  assert.equal((await f.call({operation: 'task.get', taskId: created.id})).revision, 1);
  const plan = f.app.proposePlan(created.id, 1, proposal()); f.advance(60000);
  await assert.rejects(f.call({operation: 'task.approve', taskId: created.id, key: 'a',
    body: {expectedRevision: 2, planRevision: plan.revision, planDigest: plan.digest}}), failure('state_conflict', 409));
  assert.equal(f.counts().commands.length, 1);
});

test('read projection/event pagination and unknown usage reflect durable facts', async t => {
  const f = fixture(t), {task, plan, approval} = await planned(f); await f.call(approval);
  const graph = await f.call({operation: 'task.graph', taskId: task.id});
  assert.equal(graph.planRevision, plan.revision); assert.equal(graph.nodes.length, 3);
  assert.ok(graph.nodes.every(n => n.status === 'pending' && n.workerIds.length === 0));
  const first = await f.call({operation: 'task.events', taskId: task.id, page: {limit: 2}});
  const next = await f.call({operation: 'task.events', taskId: task.id, page: {limit: 2, cursor: first.nextCursor}});
  assert.deepEqual([...first.items, ...next.items].map(event => event.sequence), [1, 2, 3]);
  const audit = await f.call({operation: 'task.audit', taskId: task.id});
  assert.equal(audit.usage.tokens, null); assert.equal(audit.usage.source, 'unavailable');
  assert.equal(audit.acceptance.status, 'pending');
  await assert.rejects(f.call({operation: 'task.events', taskId: task.id, page: {cursor: 'garbage'}}), failure('invalid_request', 400));
  await assert.rejects(f.call({operation: 'task.get', taskId: 'missing'}), failure('not_found', 404));
  await assert.rejects(f.call({operation: 'artifact.content', artifactId: 'missing'}), failure('unsupported_operation', 501));
});

test('caller authentication is checked before any durable create or read', async t => {
  const f = fixture(t);
  await assert.rejects(f.app.dispatch({operation: 'task.create', key: 'c', body: body()}, {}), failure('forbidden', 403));
  const aborted = AbortSignal.abort();
  await assert.rejects(f.app.dispatch({operation: 'task.create', key: 'c', body: body()}, {...context, signal: aborted}), failure('application_unavailable', 503));
  assert.deepEqual(f.counts(), {commands: [], tasks: []});
});

test('maximum accepted plan scope is HTTP-consumable; excess scope preserves original Task and events', async t => {
  const f = fixture(t), created = await f.call({operation: 'task.create', key: 'create', body: body()});
  const proposed = proposal(); proposed.nodes[0].scope = Array.from({length: 32}, (_, n) => 'scope-' + n);
  const plan = f.app.proposePlan(created.id, 1, proposed);
  assert.equal(validate(plan, 'Plan'), true);
  const before = await f.call({operation: 'task.events', taskId: created.id});
  proposed.nodes[0].scope.push('excess');
  assert.throws(() => f.app.proposePlan(created.id, 2, proposed), failure('invalid_plan_node', 400));
  assert.equal((await f.call({operation: 'task.get', taskId: created.id})).revision, 2);
  assert.deepEqual(await f.call({operation: 'task.events', taskId: created.id}), before);
});
