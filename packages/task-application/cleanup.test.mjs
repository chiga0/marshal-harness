import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath} from 'node:url';
import {Store, CUSTODY_FORMAT, encode} from '../task-store/store.mjs';
import {TaskApplication} from './application.mjs';
import {TaskCleanup} from './cleanup.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
import {launchCommand} from '../agent-runtime/index.mjs';
import {validate} from '../task-api/contract.mjs';

const context = {principal: 'local-operator'};
const profile = {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true};
async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-cleanup-chain-')));
  const root = path.join(parent, 'store'), custodyRoot = path.join(parent, 'custody'); fs.mkdirSync(custodyRoot, {mode: 0o700});
  let store = Store.create(root, {format: CUSTODY_FORMAT});
  const manager = createExecutionCustody({root: custodyRoot});
  const execution = {providerIds: ['fixture'], defaultProvider: 'fixture'};
  let app = new TaskApplication({store, owner: store.claimOwner(0, 'first', Date.now() + 60000), execution});
  t.after(async () => { await manager.close(); store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  const task = await app.dispatch({operation: 'task.create', key: 'create', body: {intent: '真实清理组件测试'}}, context);
  const command = app.execution.poll().items[0], ticket = app.execution.nextWork(command.id, command.revision);
  const handle = await manager.prepare(app.execution.custodyBinding(ticket, profile));
  app.execution.bindCustody(ticket, handle.descriptor, profile); handle.permit();
  const runtime = await launchCommand({executable: process.execPath,
    args: [fileURLToPath(new URL('../agent-runtime/command.fixture.mjs', import.meta.url)), 'sum'], cwd: parent,
    env: {}, deadline: ticket.deadline, input: Buffer.from('{"nonce":"original","values":[5]}\n'), executionContext: {launch: handle.launch}});
  app.execution.started(ticket, runtime.started);
  const result = await runtime.completion; assert.equal(result.cleanup.cleaned, true);
  return {get app() {return app;}, get store() {return store;}, root, task, ticket, descriptor: handle.descriptor, manager,
    observation: manager.read(handle.descriptor),
    reopen() {
      const generation = store.info().generation; store.close();
      assert.throws(() => Store.openExisting(root), error => error.code === 'unavailable');
      store = Store.openExisting(root, {format: CUSTODY_FORMAT});
      assert.equal(TaskCleanup.inspectBeforeClaim(store).items.length, 1);
      app = new TaskApplication({store, owner: store.claimOwner(generation, 'second', Date.now() + 60000), execution});
    },
  };
}
test('same Store original public key settles old execution once, preserves budget/deadline and does not accept result', {timeout: 15000}, async t => {
  const f = await fixture(t), deadline = f.task.deadlineAt; f.reopen();
  assert.throws(() => f.app.execution.finish(f.ticket, {cleanup: f.observation.payload.cleanup}), error => error.code === 'recovery_required');
  const result = f.app.execution.reconcileCleanup(f.ticket.workerId, f.observation);
  assert.equal(result.status, 'failed');
  const head = f.app.transaction(false, tx => tx.head(f.task.id));
  assert.deepEqual(f.app.execution.reconcileCleanup(f.ticket.workerId, f.observation), result);
  assert.deepEqual(f.app.transaction(false, tx => tx.head(f.task.id)), head);
  const task = await f.app.dispatch({operation: 'task.get', taskId: f.task.id}, context);
  assert.equal(task.status, 'failed'); assert.equal(task.code, 'service_interrupted'); assert.equal(task.deadlineAt, deadline);
  assert.equal(validate(task, 'Task'), true);
  assert.equal(f.app.transaction(false, tx => f.app.execution.capacity(tx).value.active.length), 0);
  const audit = await f.app.dispatch({operation: 'task.audit', taskId: f.task.id}, context);
  assert.equal(audit.attempts, 1); assert.equal(audit.acceptance.status, 'pending');
  f.manager.acknowledge(f.descriptor, result.observationDigest);
});
test('cancel intent survives intervention and cleanup settles original Operation without changing its receipt', {timeout: 15000}, async t => {
  const f = await fixture(t), task = await f.app.dispatch({operation: 'task.get', taskId: f.task.id}, context);
  const request = {operation: 'task.cancel', taskId: f.task.id, key: 'cancel', body: {expectedRevision: task.revision}};
  const receipt = await f.app.dispatch(request, context); f.reopen();
  f.app.execution.reconcile(f.task.id);
  assert.equal(f.app.execution.reconcileCleanup(f.ticket.workerId, f.observation).status, 'cancelled');
  assert.equal(f.app.execution.poll().items.some(command => JSON.parse(command.payload).action === 'cancel'), false);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: receipt.id}, context)).status, 'succeeded');
  assert.deepEqual(await f.app.dispatch(request, context), receipt);
});
test('replacement/foreign observations reject an otherwise valid binding, then original proof settles', {timeout: 15000}, async t => {
  const f = await fixture(t); f.reopen();
  const head = f.app.transaction(false, tx => tx.head(f.task.id));
  for (const observation of [{...f.observation, signature: 'A'.repeat(86) + '=='},
    {...f.observation, payload: {...f.observation.payload, custodyId: 'foreign'}},
    {...f.observation, payload: {...f.observation.payload, cleanup: {...f.observation.payload.cleanup, reason: 'forged'}}}]) {
    assert.throws(() => f.app.execution.reconcileCleanup(f.ticket.workerId, observation), error => error.code === 'recovery_required');
    assert.deepEqual(f.app.transaction(false, tx => tx.head(f.task.id)), head);
  }
  assert.equal(f.app.transaction(false, tx => f.app.execution.capacity(tx).value.active.length), 1);
  assert.equal(f.app.execution.reconcileCleanup(f.ticket.workerId, f.observation).status, 'failed');
  assert.equal(f.app.transaction(false, tx => f.app.execution.capacity(tx).value.active.length), 0);
});
test('independently valid original observation cannot settle a durable unknown extra scope', {timeout: 15000}, async t => {
  const f = await fixture(t);
  f.app.execution.recordExtraScope(f.ticket, 'remote_effect_unproven'); f.reopen();
  const head = f.app.transaction(false, tx => tx.head(f.task.id));
  assert.throws(() => f.app.execution.reconcileCleanup(f.ticket.workerId, f.observation), error => error.code === 'recovery_required');
  assert.deepEqual(f.app.transaction(false, tx => tx.head(f.task.id)), head);
  assert.equal(f.app.transaction(false, tx => f.app.execution.capacity(tx).value.active.length), 1);
  assert.ok(encode(f.observation).length > 0);
});
