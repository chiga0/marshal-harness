import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {generateKeyPairSync} from 'node:crypto';
import {Store, FORMAT, CUSTODY_FORMAT, INTERACTION_FORMAT, REPAIR_FORMAT, UNPERMITTED_FORMAT, encode, digest, makeEvent} from '../task-store/store.mjs';
import {TaskApplication, createAuditDisclosure} from './application.mjs';
import {TaskCleanup} from './cleanup.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
import {launchCommand} from '../agent-runtime/index.mjs';
import {fileURLToPath} from 'node:url';

const context = {principal: 'local-operator'}, protocol = {profile: 'node-unpermitted-reservation/v1', preparation: 'file-staging-only/v1'};
const hash = value => digest(encode(value));
function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-unpermitted-'))), root = path.join(parent, 'store');
  let now = Date.now(), store = Store.create(root, {format: UNPERMITTED_FORMAT, clock: () => now});
  const execution = {providerIds: ['fixture'], defaultProvider: 'fixture', startProtocol: protocol};
  let app = new TaskApplication({store, clock: () => now, owner: store.claimOwner(0, 'first', now + 60000), execution});
  t.after(() => { store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  return {get app() {return app;}, get store() {return store;}, root, execution,
    async reserve() {
      const request = {operation: 'task.create', key: 'create', body: {intent: '原生未许可结算测试', limits: {timeoutMs: 10000, maxAttempts: 4, maxWorkers: 2}}};
      const task = await app.dispatch(request, context), command = app.execution.poll().items[0];
      return {task, request, ticket: app.execution.nextWork(command.id, command.revision)};
    },
    reopen(expire = false, beforeClaim = () => {}) {
      const oldApp = app, generation = store.info().generation; store.close(); if (expire) now += 20000;
      for (const format of [FORMAT, CUSTODY_FORMAT, INTERACTION_FORMAT, REPAIR_FORMAT])
        assert.throws(() => Store.openExisting(root, {format}), {code: 'unavailable'});
      store = Store.openExisting(root, {format: UNPERMITTED_FORMAT, clock: () => now});
      beforeClaim(store);
      app = new TaskApplication({store, clock: () => now, owner: store.claimOwner(generation, 'next', now + 60000), execution});
      return oldApp;
    },
    edit(ticket, change) { app.transaction(true, tx => {
      const {row, record} = app.execution.worker(tx, ticket.workerId), task = app.get(tx, ticket.taskId);
      change(record, task, tx); task.task.revision++;
      const source = app.save(tx, task, 'fixture.corrupted-projection'); app.execution.putWorker(tx, row, record, source);
    }); },
    snapshot(taskId) { return app.transaction(false, tx => ({head: tx.head(taskId), task: app.get(tx, taskId),
      workers: app.execution.workers(tx, app.get(tx, taskId)).map(value => value.record), capacity: app.execution.capacity(tx).value})); },
  };
}
test('v5 original reservation and prepared metadata settle once after deadline; old owner/result stay fenced', async t => {
  const f = fixture(t), {task, ticket, request} = await f.reserve();
  assert.deepEqual(ticket.startProtocol, protocol);
  f.app.execution.observeInput(ticket, 'prepared', '原输入，不是已提交模型的证明');
  const before = f.snapshot(task.id), old = f.reopen(true);
  assert.throws(() => old.execution.started(ticket, {executionId: 'late'}), {code: 'application_unavailable'});
  assert.throws(() => f.app.execution.started(ticket, {executionId: 'late'}), {code: 'recovery_required'});
  assert.throws(() => f.app.execution.finish(ticket, {}), {code: 'recovery_required'});
  const result = f.app.execution.settleUnpermitted(ticket.workerId), after = f.snapshot(task.id);
  assert.equal(result.disposition, 'never-permitted'); assert.equal(after.task.task.status, 'failed');
  assert.equal(after.task.task.code, 'service_interrupted'); assert.equal(after.workers[0].cleanup, null);
  assert.equal(after.workers[0].executionId, null); assert.equal(after.workers[0].worker.usage.source, 'unavailable');
  assert.deepEqual(after.capacity, {active: []});
  for (const field of ['attempts', 'retryCount', 'reworkCount', 'input', 'limits']) assert.deepEqual(after.task[field], before.task[field]);
  assert.equal(after.task.task.deadlineAt, task.deadlineAt); assert.deepEqual(after.task.task.artifactIds, []);
  assert.deepEqual(await f.app.dispatch(request, context), task);
  assert.deepEqual(f.app.execution.settleUnpermitted(ticket.workerId), result); assert.deepEqual(f.snapshot(task.id), after);
  f.reopen(); assert.deepEqual(f.app.execution.settleUnpermitted(ticket.workerId), result); assert.deepEqual(f.snapshot(task.id), after);
});
test('cancel wins; original unknown Operation settles only with the obligation, receipt stays exact', async t => {
  const f = fixture(t), {task, ticket} = await f.reserve(), current = await f.app.dispatch({operation: 'task.get', taskId: task.id}, context);
  const request = {operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: current.revision}};
  const receipt = await f.app.dispatch(request, context); f.reopen();
  f.app.execution.reconcile(task.id);
  const stop = f.app.execution.poll().items.find(command => command.kind === 'stop');
  f.app.execution.settleControl(stop.id, stop.revision);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'unknown');
  f.app.execution.settleUnpermitted(ticket.workerId);
  assert.equal((await f.app.dispatch({operation: 'task.get', taskId: task.id}, context)).status, 'cancelled');
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'succeeded');
  assert.deepEqual(await f.app.dispatch(request, context), receipt);
});
test('settlement SQL callback failure rolls back capacity, event, outbox and operation; retry then exact replay', async t => {
  const f = fixture(t), {task, ticket} = await f.reserve(); f.reopen();
  const before = f.snapshot(task.id), write = f.store.write.bind(f.store);
  f.store.write = (owner, fn) => write(owner, tx => {fn(tx); throw Error('test-before-commit');});
  assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'application_unavailable'});
  assert.deepEqual(f.snapshot(task.id), before); f.store.write = write;
  f.app.execution.settleUnpermitted(ticket.workerId); const after = f.snapshot(task.id);
  f.app.execution.settleUnpermitted(ticket.workerId); assert.deepEqual(f.snapshot(task.id), after);
});
test('any original permit fact, wrong tuple, handoff or conflicting metadata denies negative proof without writes', async t => {
  const cases = {
    'protocol': record => {record.ticket.startProtocol.preparation = 'custom';},
    'input digest': record => {record.ticket.inputDigest = hash('foreign');},
    'command': record => {record.ticket.commandId = 'foreign';},
    'start': record => {record.executionId = 'foreign';},
    'result': record => {record.resultRef = 'foreign';},
    'capacity': (record, task, tx, f) => {const c = f.app.execution.capacity(tx); c.value.active[0].generation = '999'; f.app.execution.putCapacity(tx, c.row, c.value);},
    'binding field': record => {record.custody = {descriptor: {}, extraScopes: [], settledDigest: null};},
    'permit event': (record, task, tx) => {const head = tx.head(task.task.id); tx.append(task.task.id, head,
      [makeEvent(task.task.id, head.sequence + 1n, {type: 'worker.custody-permitted', workerId: record.worker.id})]);},
    'permit receipt': (record, task, tx) => {const source = {stream: task.task.id, ...tx.head(task.task.id)};
      tx.putReceipt({scope: record.worker.id, operation: 'execution.custody-binding', keyDigest: hash('binding')}, hash('foreign'), source, encode({foreign: true}));},
  };
  for (const [name, corrupt] of Object.entries(cases)) await t.test(name, async t => {
    const f = fixture(t), {task, ticket} = await f.reserve(); f.edit(ticket, (record, task, tx) => corrupt(record, task, tx, f)); f.reopen();
    const before = f.snapshot(task.id); assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId)); assert.deepEqual(f.snapshot(task.id), before);
  });
  await t.test('actual metadata handoff producer conflicts', async t => {
    const f = fixture(t), {task, ticket} = await f.reserve();
    f.app.execution.observeInput(ticket, 'prepared', '原输入'); f.app.execution.observeInput(ticket, 'handed-off'); f.reopen();
    const before = f.snapshot(task.id); assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'recovery_required'});
    assert.deepEqual(f.snapshot(task.id), before);
  });
});
test('actual bind transaction prevents unpermitted settlement and preclaim requires all three same-source facts', async t => {
  const f = fixture(t), {task, ticket} = await f.reserve(), profile = {id: 'test', scope: 'inherited-process-group', eligible: true};
  const binding = f.app.execution.custodyBinding(ticket, profile), {publicKey} = generateKeyPairSync('ed25519');
  const descriptor = {profile: 'node-execution-custody/v1', custodyId: 'custodian', executionId: 'execution',
    publicKey: publicKey.export({format: 'der', type: 'spki'}).toString('base64'), binding, bindingDigest: hash(binding)};
  f.app.execution.bindCustody(ticket, descriptor, profile); f.reopen();
  const before = f.snapshot(task.id); assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'recovery_required'});
  assert.deepEqual(f.snapshot(task.id), before);
  // Synthetic corruption negative, NOT a signed cleanup or real process test.
  f.edit(ticket, record => {delete record.custody;});
  const generation = f.store.info().generation; f.store.close();
  const reader = Store.openExisting(f.root, {format: UNPERMITTED_FORMAT});
  try {assert.throws(() => TaskCleanup.inspectBeforeClaim(reader)); assert.equal(reader.info().generation, generation);} finally {reader.close();}
});
test('v5 cannot construct an App with disclosure callback or missing protocol; old formats reject protocol', t => {
  const f = fixture(t), disclosure = createAuditDisclosure({id: 'test', version: '1', redact() {throw Error('must never run');}});
  assert.throws(() => new TaskApplication({store: f.store, owner: f.app.owner, auditDisclosure: disclosure, execution: f.execution}), {code: 'unsupported_task'});
  assert.throws(() => new TaskApplication({store: f.store, owner: f.app.owner}), {code: 'invalid_execution_config'});
});
test('mixed original bound/unbound siblings: cancel stays unknown until real signed last cleanup; all replays exact', {timeout: 15000}, async t => {
  const f = fixture(t);
  const task = await f.app.dispatch({operation: 'task.create', key: 'mixed', body: {intent: '两个独立分支'}} , context);
  // The original trusted proposal port is used before any Planner reservation;
  // no fake started/result/cleanup is inserted to manufacture these two nodes.
  const plan = f.app.proposePlan(task.id, task.revision, {summary: '两个分支', nodes: ['a', 'b'].map(id =>
    ({id, role: 'author', goal: id, scope: [], providerId: null})), edges: [], deliverables: ['两个结果'], acceptance: ['独立检查'], assumptions: []});
  const current = await f.app.dispatch({operation: 'task.get', taskId: task.id}, context);
  await f.app.dispatch({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}}, context);
  const dispatch = f.app.execution.poll().items.find(command => JSON.parse(command.payload).action === 'dispatch');
  f.app.execution.expandDispatch(dispatch.id, dispatch.revision);
  const commands = f.app.execution.poll().items.filter(command => JSON.parse(command.payload).action === 'execute');
  const a = f.app.execution.nextWork(commands[0].id, commands[0].revision), b = f.app.execution.nextWork(commands[1].id, commands[1].revision);
  const directory = path.join(path.dirname(f.root), 'custody'); fs.mkdirSync(directory, {mode: 0o700});
  const manager = createExecutionCustody({root: directory}); t.after(() => manager.close());
  const profile = {id: 'real-none-start', scope: 'inherited-process-group', eligible: true};
  const handle = await manager.prepare(f.app.execution.custodyBinding(b, profile)); f.app.execution.bindCustody(b, handle.descriptor, profile);
  // No permit call; the ORIGINAL custodian closes and signs its none-start.
  await handle.stop(); const observation = manager.read(handle.descriptor); assert.ok(observation);
  assert.equal(observation.payload.permitReceived, false); assert.equal(observation.payload.cleanup.cleaned, true);
  const beforeCancel = await f.app.dispatch({operation: 'task.get', taskId: task.id}, context), request = {
    operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: beforeCancel.revision}};
  const receipt = await f.app.dispatch(request, context); f.reopen(); f.app.execution.reconcile(task.id);
  const stop = f.app.execution.poll().items.find(command => command.kind === 'stop'); f.app.execution.settleControl(stop.id, stop.revision);
  const settled = f.app.execution.settleUnpermitted(a.workerId), interim = f.snapshot(task.id);
  assert.equal(interim.task.task.status, 'intervention'); assert.equal(interim.capacity.active.length, 1);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'unknown');
  assert.equal(interim.workers.find(record => record.worker.id === b.workerId).cleanup, null);
  f.app.execution.reconcileCleanup(b.workerId, observation);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'succeeded');
  assert.equal(f.snapshot(task.id).task.task.status, 'cancelled'); assert.equal(f.snapshot(task.id).capacity.active.length, 0);
  assert.deepEqual(await f.app.dispatch(request, context), receipt); const final = f.snapshot(task.id);
  f.reopen(); assert.deepEqual(f.app.execution.settleUnpermitted(a.workerId), settled);
  f.app.execution.reconcileCleanup(b.workerId, observation); assert.deepEqual(f.snapshot(task.id), final);
});
test('more than 100 legitimate progress events cannot hide original bound permit or block signed recovery', {timeout: 15000}, async t => {
  const f = fixture(t), {ticket, task} = await f.reserve(), directory = path.join(path.dirname(f.root), 'custody');
  fs.mkdirSync(directory, {mode: 0o700}); const manager = createExecutionCustody({root: directory}); t.after(() => manager.close());
  const profile = {id: 'actual-command', scope: 'inherited-process-group', eligible: true};
  const handle = await manager.prepare(f.app.execution.custodyBinding(ticket, profile));
  f.app.execution.bindCustody(ticket, handle.descriptor, profile); handle.permit();
  const runtime = await launchCommand({executable: process.execPath,
    args: [fileURLToPath(new URL('../agent-runtime/command.fixture.mjs', import.meta.url)), 'sum'], cwd: path.dirname(f.root),
    env: {}, deadline: ticket.deadline, input: Buffer.from('{"nonce":"progress","values":[5]}\n'), executionContext: {launch: handle.launch}});
  f.app.execution.started(ticket, runtime.started);
  for (let sequence = 1; sequence <= 105; sequence++) assert.equal(f.app.execution.progress(ticket, sequence, {summary: '原协议进度', tool: null, source: 'execution'}), true);
  const result = await runtime.completion; assert.equal(result.cleanup.cleaned, true);
  const observation = manager.read(handle.descriptor); assert.ok(observation);
  f.reopen(false, store => assert.deepEqual(TaskCleanup.inspectBeforeClaim(store).items, [handle.descriptor]));
  assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'recovery_required'});
  f.app.execution.reconcileCleanup(ticket.workerId, observation);
  assert.equal(f.snapshot(task.id).capacity.active.length, 0); const final = f.snapshot(task.id);
  f.reopen(false, store => assert.deepEqual(TaskCleanup.inspectBeforeClaim(store).items, []));
  f.app.execution.reconcileCleanup(ticket.workerId, observation); assert.deepEqual(f.snapshot(task.id), final);
});
