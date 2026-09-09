import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {TaskApplication} from './application.mjs';
import {Store, FORMAT, UNPERMITTED_FORMAT, WORKER_CANCELLATION_FORMAT, encode} from '../task-store/store.mjs';
import {validate} from '../task-api/contract.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
const context = {principal: 'local-operator'}, protocol = {profile: 'node-unpermitted-reservation/v1', preparation: 'file-staging-only/v1'};
const plan = {summary: '两分支保留原依赖', nodes: ['a', 'b', 'check'].map(id => ({id, role: id === 'check' ? 'reviewer' : 'author', goal: id, scope: [], providerId: null})),
  edges: [{from: 'a', to: 'check'}, {from: 'b', to: 'check'}], deliverables: ['原共同成果'], acceptance: ['独立检查'], assumptions: []};
function fixture(t, qualified = false, format = WORKER_CANCELLATION_FORMAT) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'worker-cancel-unit-'))), root = path.join(parent, 'store');
  let now = Date.now(), store = Store.create(root, {format, clock: () => now}), owner = store.claimOwner(0, 'owner', now + 60000);
  const execution = {providerIds: ['fixture'], defaultProvider: 'fixture', ...(qualified ? {startProtocol: protocol} : {})};
  let app = new TaskApplication({store, owner, execution, clock: () => now});
  t.after(() => {store.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const f = {get app() {return app;}, get store() {return store;}, root,
    query(operation, extra = {}) {return app.dispatch({operation, ...extra}, context);},
    commands() {return app.transaction(false, tx => tx.commands());},
    snapshot(taskId) {return app.transaction(false, tx => ({task: app.get(tx, taskId), workers: app.execution.workers(tx, app.get(tx, taskId)).map(x => x.record),
      capacity: app.execution.capacity(tx).value, events: tx.events(taskId), commands: tx.commands()}));},
    reopen(qualification = qualified) {store.close(); store = Store.openExisting(root, {format, clock: () => now}); owner = store.claimOwner(owner.generation, 'next', now + 60000);
      app = new TaskApplication({store, owner, clock: () => now, execution: {...execution, startProtocol: qualification ? protocol : null}});},
    advance(ms) {now += ms;},
    async create(key = 'new') {return f.query('task.create', {key, body: {intent: '只取消一个 Worker', limits: {timeoutMs: 45000, maxAttempts: 6, maxWorkers: 2}}});},
    async reserve(key = 'new') {const task = await f.create(key), command = f.commands().find(c => c.taskId === task.id); return {task, ticket: app.execution.nextWork(command.id, command.revision)};},
    async pair() {const task = await f.create(); const frozen = app.proposePlan(task.id, task.revision, plan);
      await f.query('task.approve', {taskId: task.id, key: 'approve', body: {expectedRevision: frozen.revision + 1, planRevision: frozen.revision, planDigest: frozen.digest}});
      const dispatch = f.commands().find(c => JSON.parse(c.payload).action === 'dispatch'); app.execution.expandDispatch(dispatch.id, dispatch.revision);
      const commands = f.commands().filter(c => JSON.parse(c.payload).action === 'execute');
      return {task, a: app.execution.nextWork(commands.find(c => JSON.parse(c.payload).nodeId === 'a').id, 1),
        b: app.execution.nextWork(commands.find(c => JSON.parse(c.payload).nodeId === 'b').id, 1)};},
    async cancel(ticket, key = 'stop') {const current = await f.query('task.get', {taskId: ticket.taskId});
      const request = {operation: 'worker.cancel', workerId: ticket.workerId, key, body: {expectedRevision: current.revision}};
      return {request, receipt: await app.dispatch(request, context)};},
  }; return f;
}
// Component-level deterministic Execution ports, not Runtime cleanup proof.
const started = ticket => ({executionId: 'run-' + ticket.workerId, startedAt: new Date().toISOString()});
const result = fact => ({status: 'completed', stopReason: 'end_turn', cleanup: {started: fact, cleaned: true}, result: {value: 'original'}});
test('target receipt/CAS, late result, retained sibling and dependency close use one original reducer', async t => {
  const f = fixture(t), {task, a, b} = await f.pair(), sa = started(a), sb = started(b);
  f.app.execution.started(a, sa); f.app.execution.started(b, sb);
  const before = f.snapshot(task.id), {request, receipt} = await f.cancel(a);
  assert.equal(validate(receipt, 'Operation'), true); assert.equal(receipt.workerId, a.workerId);
  assert.equal(f.app.execution.mayStart(a), false); assert.deepEqual(f.app.execution.reconcile(task.id).stopWorkerIds, [a.workerId]);
  assert.deepEqual(await f.app.dispatch(request, context), receipt);
  await assert.rejects(f.app.dispatch({...request, body: {expectedRevision: request.body.expectedRevision + 1}}, context), {code: 'idempotency_conflict'});
  await assert.rejects(f.app.dispatch({...request, key: 'another'}, context), {code: 'revision_conflict'});
  assert.equal(f.app.execution.finish(a, result(sa)).status, 'cancelled');
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'succeeded');
  assert.equal((await f.query('task.get', {taskId: task.id})).status, 'running');
  assert.equal(f.snapshot(task.id).capacity.active.length, 1); assert.equal(f.snapshot(task.id).workers.find(w => w.worker.id === a.workerId).resultRef, null);
  f.app.execution.finish(b, result(sb));
  const final = f.snapshot(task.id); assert.equal(final.task.task.status, 'failed'); assert.equal(final.task.task.code, 'worker_cancelled');
  assert.equal(final.workers.find(w => w.worker.id === b.workerId).worker.status, 'completed');
  assert.equal(final.task.nodes.find(n => n.id === 'check').status, 'cancelled'); assert.deepEqual(final.task.nodes.find(n => n.id === 'check').workerIds, []);
  assert.equal(final.task.attempts, before.task.attempts); assert.equal(final.task.task.deadlineAt, before.task.task.deadlineAt); assert.equal(final.capacity.active.length, 0);
  await assert.rejects(f.cancel(b), {code: 'state_conflict'});
  f.reopen(); assert.deepEqual(await f.app.dispatch(request, context), receipt); assert.deepEqual(f.snapshot(task.id), final);
});
test('cancelled planner cannot publish a plan', async t => {
  const f = fixture(t), {task, ticket} = await f.reserve(), {receipt} = await f.cancel(ticket);
  assert.equal(f.app.execution.started(ticket, started(ticket)).stop, true);
  const fact = f.snapshot(task.id).workers[0];
  f.app.execution.finish(ticket, {...result({executionId: fact.executionId, startedAt: fact.worker.startedAt}), plan});
  assert.equal((await f.query('task.get', {taskId: task.id})).plan, null);
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'succeeded');
});
test('global cancel can win remaining siblings, while deadline/new-key/cross-Task requests cannot retarget original receipt', async t => {
  const f = fixture(t), {task, a, b} = await f.pair(), sa = started(a), sb = started(b);
  f.app.execution.started(a, sa); f.app.execution.started(b, sb); const target = await f.cancel(a);
  await assert.rejects(f.cancel(a, 'new-key'), {code: 'state_conflict'});
  f.app.execution.finish(a, result(sa)); // Free only A before reserving another Task.
  const other = await f.create('other-task'), plannerCommand = f.commands().find(c => c.taskId === other.id);
  const otherTicket = f.app.execution.nextWork(plannerCommand.id, plannerCommand.revision);
  const otherStop = await f.cancel(otherTicket, target.request.key); assert.notEqual(otherStop.receipt.id, target.receipt.id);
  const body = {expectedRevision: (await f.query('task.get', {taskId: task.id})).revision};
  const global = await f.query('task.cancel', {taskId: task.id, key: 'global-stop', body});
  await assert.rejects(f.cancel(b), {code: 'state_conflict'});
  f.app.execution.finish(b, result(sb)); f.app.execution.reconcile(task.id);
  const command = f.commands().find(c => JSON.parse(c.payload).operationId === global.id); f.app.execution.settleControl(command.id, command.revision);
  assert.equal((await f.query('task.get', {taskId: task.id})).status, 'cancelled');
  assert.equal((await f.query('operation.get', {operationId: target.receipt.id})).status, 'succeeded');
  assert.equal((await f.query('operation.get', {operationId: global.id})).status, 'succeeded');
  assert.deepEqual(await f.app.dispatch(target.request, context), target.receipt);
  const g = fixture(t), reserved = await g.reserve(); g.advance(45001);
  await assert.rejects(g.cancel(reserved.ticket), {code: 'state_conflict'});
});
test('target cleanup mismatch never releases unknown capacity or forges a successful Operation', async t => {
  const f = fixture(t), {ticket, task} = await f.reserve(), fact = started(ticket); f.app.execution.started(ticket, fact);
  const {receipt} = await f.cancel(ticket), before = f.snapshot(task.id);
  assert.throws(() => f.app.execution.finish(ticket, result({...fact, executionId: 'foreign'})));
  assert.deepEqual(f.snapshot(task.id), before); assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'accepted');
});
test('named never-permitted target settles independently from a bound sibling, with exact original signed cleanup and cold replay', {timeout: 15000}, async t => {
  const f = fixture(t, true), {task, a, b} = await f.pair(), directory = path.join(path.dirname(f.root), 'custody'); fs.mkdirSync(directory, {mode: 0o700});
  const manager = createExecutionCustody({root: directory}); t.after(() => manager.close());
  const profile = {id: 'original-observer', scope: 'inherited-process-group', eligible: true};
  const handle = await manager.prepare(f.app.execution.custodyBinding(b, profile)); f.app.execution.bindCustody(b, handle.descriptor, profile);
  await handle.stop(); const observation = manager.read(handle.descriptor); assert.equal(observation.payload.permitReceived, false);
  const {request, receipt} = await f.cancel(a); f.reopen(false); f.app.execution.reconcile(task.id);
  const stop = f.commands().find(c => JSON.parse(c.payload).operationId === receipt.id); f.app.execution.settleControl(stop.id, stop.revision);
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'unknown');
  f.app.execution.settleUnpermitted(a.workerId); const interim = f.snapshot(task.id);
  assert.equal(interim.capacity.active.length, 1); assert.equal(interim.task.task.status, 'intervention');
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'succeeded');
  assert.throws(() => f.app.execution.settleUnpermitted(b.workerId), {code: 'recovery_required'});
  f.app.execution.reconcileCleanup(b.workerId, observation); assert.equal(f.snapshot(task.id).capacity.active.length, 0);
  const final = f.snapshot(task.id); f.reopen(false); f.app.execution.settleUnpermitted(a.workerId); f.app.execution.reconcileCleanup(b.workerId, observation);
  assert.deepEqual(f.snapshot(task.id), final); assert.deepEqual(await f.app.dispatch(request, context), receipt);
});
test('target stop proof corruption and original extra scope fail closed without releasing capacity', {timeout: 15000}, async t => {
  const f = fixture(t), {ticket, task} = await f.reserve(), directory = path.join(path.dirname(f.root), 'custody'); fs.mkdirSync(directory, {mode: 0o700});
  const manager = createExecutionCustody({root: directory}); t.after(() => manager.close());
  const profile = {id: 'recorded-scope', scope: 'inherited-process-group', eligible: true};
  const handle = await manager.prepare(f.app.execution.custodyBinding(ticket, profile)); f.app.execution.bindCustody(ticket, handle.descriptor, profile);
  f.app.execution.recordExtraScope(ticket, 'untracked-effect'); const {receipt} = await f.cancel(ticket); await handle.stop();
  const observation = manager.read(handle.descriptor); f.reopen(); f.app.execution.reconcile(task.id);
  const before = f.snapshot(task.id); assert.throws(() => f.app.execution.reconcileCleanup(ticket.workerId, observation), {code: 'recovery_required'});
  assert.deepEqual(f.snapshot(task.id), before);
  const stop = f.commands().find(c => JSON.parse(c.payload).operationId === receipt.id); f.app.execution.settleControl(stop.id, stop.revision);
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'unknown');
  const g = fixture(t, true), reserved = await g.reserve(); await g.cancel(reserved.ticket);
  // Explicit corrupt projection negative, not a receipt or execution producer.
  g.app.transaction(true, tx => {const {row, record} = g.app.execution.worker(tx, reserved.ticket.workerId), task = g.app.get(tx, reserved.task.id);
    record.stopIntent.reservationDigest = 'sha256:' + '0'.repeat(64); task.task.revision++;
    const source = g.app.save(tx, task, 'fixture.corrupted-stop');
    g.app.execution.putWorker(tx, row, record, source);});
  g.reopen(); const corrupt = g.snapshot(reserved.task.id); assert.throws(() => g.app.execution.settleUnpermitted(reserved.ticket.workerId), {code: 'recovery_required'});
  assert.deepEqual(g.snapshot(reserved.task.id), corrupt);
});
test('target unknown retains capacity/operation; cross-worker route never replays another receipt', async t => {
  const f = fixture(t), {task, a, b} = await f.pair(), sa = started(a), sb = started(b);
  f.app.execution.started(a, sa); f.app.execution.started(b, sb); const {request, receipt} = await f.cancel(a);
  await assert.rejects(f.app.dispatch({...request, workerId: b.workerId}, context), {code: 'revision_conflict'});
  f.app.execution.finish(a, {...result(sa), cleanup: {started: sa, cleaned: false}});
  assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'unknown');
  assert.equal(f.snapshot(task.id).capacity.active.length, 2); assert.equal((await f.query('task.get', {taskId: task.id})).status, 'intervention');
  f.reopen(); assert.deepEqual(await f.app.dispatch(request, context), receipt); await assert.rejects(f.cancel(b), {code: 'state_conflict'});
});
test('v6 original protocol survives configuration removal; unqualified reservation cannot gain it on reopen', async t => {
  for (const qualified of [false, true]) await t.test(String(qualified), async t => {
    const f = fixture(t, qualified), {task, ticket} = await f.reserve(), {request, receipt} = await f.cancel(ticket), before = f.snapshot(task.id);
    f.reopen(!qualified); f.app.execution.reconcile(task.id);
    const stop = f.commands().find(c => JSON.parse(c.payload).action === 'worker-cancel'); f.app.execution.settleControl(stop.id, stop.revision);
    assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'unknown');
    if (qualified) {
      f.app.execution.settleUnpermitted(ticket.workerId); const final = f.snapshot(task.id);
      assert.equal(final.workers[0].cleanup, null); assert.equal(final.workers[0].worker.status, 'cancelled');
      assert.equal(final.capacity.active.length, 0); assert.equal(final.task.attempts, before.task.attempts);
      assert.equal((await f.query('operation.get', {operationId: receipt.id})).status, 'succeeded');
      f.reopen(false); f.app.execution.settleUnpermitted(ticket.workerId); assert.deepEqual(f.snapshot(task.id), final);
    } else {assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'recovery_required'}); assert.equal(f.snapshot(task.id).capacity.active.length, 1);}
    assert.deepEqual(await f.app.dispatch(request, context), receipt);
    const next = await f.reserve('new-after-config-change'); assert.ok(next.ticket);
    assert.deepEqual(next.ticket.startProtocol, qualified ? undefined : protocol);
  });
});
test('new cancellation rolls back completely; old format remains 501 and old Operation bytes stay closed', async t => {
  const f = fixture(t), {ticket, task} = await f.reserve(), current = await f.query('task.get', {taskId: task.id}), before = f.snapshot(task.id);
  const write = f.store.write.bind(f.store); f.store.write = (owner, fn) => write(owner, tx => {fn(tx); throw Error('before COMMIT');});
  await assert.rejects(f.query('worker.cancel', {workerId: ticket.workerId, key: 'rollback', body: {expectedRevision: current.revision}}));
  f.store.write = write; assert.deepEqual(f.snapshot(task.id), before); await f.cancel(ticket);
  const old = fixture(t, false, FORMAT), original = await old.reserve();
  await assert.rejects(old.cancel(original.ticket), {code: 'unsupported_operation'});
  const op = {id: 'op', taskId: 'task', kind: 'task.cancel', status: 'accepted', taskRevision: 2, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString()};
  assert.equal(validate(op, 'Operation'), true); assert.equal(validate({...op, workerId: 'worker'}, 'Operation'), false);
  assert.equal(validate({...op, kind: 'worker.cancel'}, 'Operation'), false);
});
