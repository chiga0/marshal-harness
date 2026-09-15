import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {TaskApplication} from './application.ts';
import {Store} from '../task-store/store.ts';
import {validate} from '../task-api/contract.ts';

const context = {principal: 'local-operator'}, now = 1800000000000;
async function fixture(t, maxWorkers = 2) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-execution-test-'))), root = path.join(parent, 'state');
  let instant = now; const clock = () => instant;
  let store = Store.create(root, {clock}), owner = await store.claimOwner(0, 'server', now + 3600000);
  const execution = {maxWorkers, providerIds: ['fixture'], defaultProvider: 'fixture'};
  let app = new TaskApplication({store, owner, clock, execution});
  t.after(() => { store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  return {get app() {return app;}, get execution() {return app.execution;},
    create(key = 'create', limit = 8) {return app.dispatch({operation: 'task.create', key, body: {intent: '并行交付文档及数据处理程序',
      limits: {timeoutMs: 60000, maxAttempts: limit, maxWorkers: 2}}}, context);},
    commands() {return store.read(owner, tx => tx.commands());},
    async get(id) {return app.dispatch({operation: 'task.get', taskId: id}, context);},
    capacity() {return store.read(owner, tx => app.execution.capacity(tx).value.active);},
    advance(ms) {instant += ms;},
    async reopen() {store.close(); store = Store.openExisting(root, {clock}); owner = await store.claimOwner(owner.generation, 'next', now + 3600000);
      app = new TaskApplication({store, owner, clock, execution});}};
}
const plan = () => ({summary: '并行执行再独立检查', nodes: ['first', 'second', 'review'].map(id => ({id,
  role: id === 'review' ? 'reviewer' : 'author', goal: '完成' + id, scope: [id], providerId: null})),
  edges: [{from: 'first', to: 'review'}, {from: 'second', to: 'review'}],
  deliverables: ['程序与说明'], acceptance: ['独立运行验证'], assumptions: []});
function started(ticket) {return {executionId: 'execution-' + ticket.workerId, startedAt: new Date(now).toISOString()};}
function result(ticket, extra = {}) {return {status: 'completed', stopReason: 'end_turn',
  result: {digest: 'sha256:' + 'a'.repeat(64)},
  cleanup: {started: started(ticket), cleaned: true}, ...extra};}
async function planned(f) {
  const task = await f.create();
  const command = (await f.commands())[0], ticket = (await f.execution.nextWork(command.id, command.revision));
  assert.ok(ticket); (await f.execution.started(ticket, started(ticket)));
  (await f.execution.finish(ticket, result(ticket, {plan: plan()})));
  const current = await f.get(task.id), approved = await f.app.dispatch({operation: 'task.approve', key: 'approve', taskId: task.id,
    body: {expectedRevision: current.revision, planRevision: current.plan.revision, planDigest: current.plan.digest}}, context);
  const dispatch = (await f.commands()).find(c => JSON.parse(c.payload).action === 'dispatch');
  assert.equal((await f.execution.expandDispatch(dispatch.id, dispatch.revision)), true);
  return {task: await f.get(task.id), approved};
}

test('planner reserves original budget and one launch only before confirmed execution', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, command.revision));
  assert.equal(ticket.role, 'planner'); assert.equal(ticket.deadline, now + 60000);
  assert.deepEqual(ticket.input.inputArtifacts, []); // The exact manifest is part of the frozen/hash-bound input.
  assert.equal(ticket.generation, '1'); assert.equal((await f.capacity()).length, 1);
  assert.equal((await f.execution.nextWork(command.id, command.revision)), null);
  assert.equal((await f.commands())[0].status, 'unknown');
  assert.equal((await f.execution.mayStart(ticket)), true);
  (await f.execution.started(ticket, started(ticket)));
  (await f.execution.finish(ticket, result(ticket, {plan: plan()})));
  const current = await f.get(task.id);
  assert.equal(current.status, 'awaiting-approval'); assert.equal((await f.capacity()).length, 0);
  assert.equal((await f.commands())[0].status, 'observed');
  const audit = await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context);
  assert.equal(audit.attempts, 1); assert.equal(audit.workers[0].role, 'planner');
});

test('two independent nodes reserve concurrently, fan-in waits, progress does not stale sibling tickets', async t => {
  const f = await fixture(t), {task} = await planned(f);
  const commands = (await f.commands()).filter(c => JSON.parse(c.payload).action === 'execute');
  const find = id => commands.find(c => JSON.parse(c.payload).nodeId === id);
  const first = (await f.execution.nextWork(find('first').id, 1)), second = (await f.execution.nextWork(find('second').id, 1));
  assert.ok(first && second); assert.equal((await f.capacity()).length, 2);
  assert.equal((await f.execution.nextWork(find('review').id, 1)), null);
  (await f.execution.started(first, started(first))); (await f.execution.started(second, started(second)));
  assert.equal((await f.execution.progress(first, 1, {summary: '正在读取输入', tool: 'read_file', source: 'agent'})), true);
  assert.equal((await f.execution.progress(first, 1, {summary: '重发', tool: null, source: 'agent'})), false);
  (await f.execution.finish(second, result(second))); assert.equal((await f.capacity()).length, 1);
  assert.equal((await f.execution.nextWork(find('review').id, 1)), null);
  (await f.execution.finish(first, result(first)));
  const review = (await f.execution.nextWork(find('review').id, 1));
  assert.ok(review); assert.deepEqual(review.input.upstream.map(item => item.nodeId).sort(), ['first', 'second']);
  const workers = await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context);
  assert.equal(validate(workers, 'Workers'), true);
  assert.equal((await f.get(task.id)).status, 'running'); // no business acceptance implied
});

test('global capacity counts planners from other Tasks and cannot be raised by Task limits', async t => {
  const f = await fixture(t, 1);
  await f.create('one'); await f.create('two');
  const commands = (await f.commands());
  assert.ok((await f.execution.nextWork(commands[0].id, 1)));
  assert.equal((await f.execution.nextWork(commands[1].id, 1)), null);
  assert.equal((await f.capacity()).length, 1);
});

test('cancel before physical start fences mayStart, retains late cleanup but rejects candidate', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)), current = await f.get(task.id);
  await f.app.dispatch({operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: current.revision}}, context);
  assert.equal((await f.execution.mayStart(ticket)), false);
  assert.equal((await f.execution.started(ticket, started(ticket))).stop, true);
  const worker = (await f.execution.finish(ticket, result(ticket, {plan: plan()})));
  assert.equal(worker.status, 'cancelled'); assert.equal((await f.capacity()).length, 0);
  assert.equal((await f.get(task.id)).plan, null);
});

test('unknown cleanup keeps capacity and intervention; reopen never dispatches old generation', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  (await f.execution.finish(ticket, result(ticket, {cleanup: {started: started(ticket), cleaned: false}})));
  assert.equal((await f.get(task.id)).status, 'intervention'); assert.equal((await f.capacity()).length, 1);
  await f.reopen(); assert.equal((await f.execution.nextWork(command.id, 1)), null);
  await assert.rejects(f.execution.finish(ticket, result(ticket)), error => error.code === 'recovery_required');
  assert.equal((await f.capacity()).length, 1);
});

test('remaining plan attempts include prior planner debit; forged execution identity is rejected', async t => {
  const f = await fixture(t), task = await f.create('create', 3), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  await assert.rejects(f.execution.finish(ticket, result(ticket, {cleanup: {started: {executionId: 'forged'}, cleaned: true}})), error => error.code === 'recovery_required');
  (await f.execution.finish(ticket, result(ticket, {plan: plan()})));
  assert.equal((await f.get(task.id)).status, 'failed');
  assert.equal((await f.get(task.id)).code, 'invalid_plan'); assert.equal((await f.capacity()).length, 0);
});

test('cancel Operation waits for cleanup, then settles without changing its original receipt', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  const current = await f.get(task.id), request = {operation: 'task.cancel', taskId: task.id, key: 'cancel',
    body: {expectedRevision: current.revision}};
  const receipt = await f.app.dispatch(request, context);
  const stop = (await f.commands()).find(c => JSON.parse(c.payload).action === 'cancel');
  assert.equal((await f.execution.settleControl(stop.id, stop.revision)), false);
  assert.deepEqual((await f.execution.reconcile(task.id)).stopWorkerIds, [ticket.workerId]);
  assert.equal((await f.get(task.id)).status, 'cancelling');
  (await f.execution.finish(ticket, result(ticket)));
  assert.equal((await f.execution.reconcile(task.id)).status, 'cancelled');
  assert.equal((await f.execution.settleControl(stop.id, stop.revision)), true);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'succeeded');
  assert.deepEqual(await f.app.dispatch(request, context), receipt);
});

test('pause settles the dispatch fence without stopping live workers or duplicating resume work', async t => {
  const f = await fixture(t), {task, approved} = await planned(f);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: approved.id}, context)).status, 'succeeded');
  const command = (await f.commands()).find(c => JSON.parse(c.payload).nodeId === 'first');
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  const current = await f.get(task.id);
  const pause = await f.app.dispatch({operation: 'task.pause', taskId: task.id, key: 'pause', body: {expectedRevision: current.revision}}, context);
  assert.deepEqual((await f.execution.reconcile(task.id)).stopWorkerIds, []);
  const obligation = (await f.commands()).find(c => JSON.parse(c.payload).action === 'pause');
  assert.equal((await f.execution.settleControl(obligation.id, obligation.revision)), true);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: pause.id}, context)).status, 'succeeded');
  const paused = await f.get(task.id);
  await f.app.dispatch({operation: 'task.resume', taskId: task.id, key: 'resume', body: {expectedRevision: paused.revision}}, context);
  const resume = (await f.commands()).find(c => JSON.parse(c.payload).action === 'resume');
  assert.equal((await f.execution.settleControl(resume.id, resume.revision)), true);
  assert.equal((await f.commands()).filter(c => JSON.parse(c.payload).action === 'execute').length, 3);
});

test('cold recovery retains old worker capacity and never issues a PID-based stop', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  await f.reopen();
  assert.deepEqual((await f.execution.reconcile(task.id)), {taskId: task.id, status: 'intervention', stopWorkerIds: []});
  assert.equal((await f.get(task.id)).code, 'previous_execution_unresolved');
  assert.equal((await f.capacity()).length, 1);
  assert.equal((await f.execution.nextWork(command.id, 1)), null);
});

test('ticket-bound failure fences before cleanup, preserves capacity and replay adds no facts', async t => {
  const f = await fixture(t), {task} = await planned(f);
  const commands = (await f.commands()).filter(command => JSON.parse(command.payload).action === 'execute');
  const find = id => commands.find(command => JSON.parse(command.payload).nodeId === id);
  const first = (await f.execution.nextWork(find('first').id, 1)), second = (await f.execution.nextWork(find('second').id, 1));
  (await f.execution.started(first, started(first))); assert.equal((await f.execution.mayStart(second)), true);
  const revision = (await f.get(task.id)).revision;
  assert.deepEqual((await f.execution.fail(first, 'worker_failed')), {taskId: task.id, status: 'cancelling'});
  assert.equal((await f.get(task.id)).revision, revision + 1);
  assert.equal((await f.execution.mayStart(second)), false); assert.equal((await f.execution.nextWork(find('review').id, 1)), null);
  assert.equal((await f.capacity()).length, 2);
  const before = await f.app.dispatch({operation: 'task.events', taskId: task.id}, context);
  (await f.execution.fail(first, 'worker_failed'));
  assert.deepEqual(await f.app.dispatch({operation: 'task.events', taskId: task.id}, context), before);
  assert.equal((await f.get(task.id)).revision, revision + 1);
  const workers = await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context);
  assert.equal(workers.items.find(worker => worker.id === first.workerId).status, 'running');
  assert.equal(workers.items.find(worker => worker.id === second.workerId).status, 'queued');
  assert.equal((await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context)).attempts, 3);
});

test('failure fence rejects another identity, stale owner and arbitrary reason without partial writes', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)), before = await f.get(task.id);
  await assert.rejects(f.execution.fail({...ticket, taskId: 'other-task'}, 'worker_failed'), error => error.code === 'recovery_required');
  await assert.rejects(f.execution.fail(ticket, 'arbitrary private error text'), error => error.code === 'invalid_request');
  assert.deepEqual(await f.get(task.id), before); assert.equal((await f.capacity()).length, 1);
  await f.reopen();
  await assert.rejects(f.execution.fail(ticket, 'worker_failed'), error => error.code === 'recovery_required');
  assert.deepEqual(await f.get(task.id), before); assert.equal((await f.capacity()).length, 1);
});

test('earlier user cancel and finished Worker cannot be overwritten by a late failure fence', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  const current = await f.get(task.id);
  await f.app.dispatch({operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: current.revision}}, context);
  const cancelled = await f.get(task.id);
  (await f.execution.fail(ticket, 'worker_failed')); assert.deepEqual(await f.get(task.id), cancelled);
  (await f.execution.finish(ticket, result(ticket)));
  assert.equal((await f.execution.reconcile(task.id)).status, 'cancelled');
  const final = await f.get(task.id);
  (await f.execution.fail(ticket, 'worker_failed')); assert.deepEqual(await f.get(task.id), final);
});

test('a reduced plan timeout is frozen from original creation, not refreshed per worker', async t => {
  const f = await fixture(t), task = await f.create();
  await f.app.proposePlan(task.id, task.revision, {...plan(), budget: {timeoutMs: 1000, maxAttempts: 8, maxWorkers: 2}});
  const current = await f.get(task.id);
  await f.app.dispatch({operation: 'task.approve', taskId: task.id, key: 'approve', body: {
    expectedRevision: current.revision, planRevision: current.plan.revision, planDigest: current.plan.digest}}, context);
  const dispatch = (await f.commands()).find(c => JSON.parse(c.payload).action === 'dispatch');
  (await f.execution.expandDispatch(dispatch.id, 1));
  const commands = (await f.commands()).filter(c => JSON.parse(c.payload).action === 'execute');
  const ticket = (await f.execution.nextWork(commands.find(c => JSON.parse(c.payload).nodeId === 'first').id, 1));
  assert.equal(ticket.deadline, now + 1000); (await f.execution.started(ticket, started(ticket)));
  f.advance(1000);
  assert.equal((await f.execution.mayStart(ticket)), false);
  assert.equal((await f.execution.nextWork(commands.find(c => JSON.parse(c.payload).nodeId === 'second').id, 1)), null);
  assert.deepEqual((await f.execution.reconcile(task.id)).stopWorkerIds, [ticket.workerId]);
  assert.equal((await f.execution.finish(ticket, result(ticket))).status, 'cancelled');
  assert.equal((await f.execution.reconcile(task.id)).status, 'failed');
});

test('cold cancellation with no Worker settles old Operation and keeps original receipt', async t => {
  const f = await fixture(t), task = await f.create();
  const request = {operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: task.revision}};
  const receipt = await f.app.dispatch(request, context);
  const command = (await f.commands()).find(c => JSON.parse(c.payload).action === 'cancel');
  await f.reopen(); assert.equal((await f.execution.reconcile(task.id)).status, 'cancelled');
  assert.equal((await f.execution.settleControl(command.id, command.revision)), true);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'succeeded');
  assert.deepEqual(await f.app.dispatch(request, context), receipt);
  assert.equal((await f.capacity()).length, 0);
});

test('frequent Worker observations do not invalidate user control CAS', async t => {
  const f = await fixture(t), task = await f.create(), command = (await f.commands())[0];
  const ticket = (await f.execution.nextWork(command.id, 1)); (await f.execution.started(ticket, started(ticket)));
  const current = await f.get(task.id);
  for (let sequence = 1; sequence <= 20; sequence++)
    assert.equal((await f.execution.progress(ticket, sequence, {summary: '读取进度', tool: 'read', source: 'agent'})), true);
  assert.equal((await f.get(task.id)).revision, current.revision);
  await f.app.dispatch({operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: current.revision}}, context);
  await assert.rejects(f.app.dispatch({operation: 'task.cancel', taskId: task.id, key: 'other-cancel', body: {
    expectedRevision: current.revision}}, context), error => error.code === 'revision_conflict');
});

test('64 legal large-goal nodes finish without historical ticket amplification of transaction bytes', async t => {
  const f = await fixture(t), task = await f.create('large', 100);
  const proposal = {summary: '大计划事务边界', nodes: Array.from({length: 64}, (_, i) => ({id: 'node-' + i,
    role: 'author', goal: 'a'.repeat(8000), scope: ['scope-' + i], providerId: null})), edges: [],
    deliverables: ['数据及文档'], acceptance: ['独立验证'], assumptions: []};
  await f.app.proposePlan(task.id, 1, proposal);
  const current = await f.get(task.id);
  await f.app.dispatch({operation: 'task.approve', taskId: task.id, key: 'approve', body: {
    expectedRevision: current.revision, planRevision: current.plan.revision, planDigest: current.plan.digest}}, context);
  const command = (await f.commands()).find(c => JSON.parse(c.payload).action === 'dispatch');
  (await f.execution.expandDispatch(command.id, 1));
  for (const command of (await f.commands()).filter(c => JSON.parse(c.payload).action === 'execute')) {
    const ticket = (await f.execution.nextWork(command.id, 1)); assert.ok(ticket);
    (await f.execution.started(ticket, started(ticket)));
    assert.equal((await f.execution.finish(ticket, result(ticket))).status, 'completed');
  }
  assert.equal((await f.capacity()).length, 0);
  const workers = await f.app.dispatch({operation: 'task.workers', taskId: task.id, page: {limit: 100}}, context);
  assert.equal(workers.items.length, 64); assert.equal(validate(workers, 'Workers'), true);
});
