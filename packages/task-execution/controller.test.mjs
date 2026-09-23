import test from 'node:test';
import assert from 'node:assert/strict';
import {TaskExecutionCoordinator} from './controller.mjs';

const ALL_PORTS = ['scan', 'reconcile', 'poll', 'settleControl', 'expandDispatch',
  'nextWork', 'mayStart', 'started', 'progress', 'fail', 'finish'];

test('global fault records the raw error via recordFault exactly once; recorder failure is swallowed', async () => {
  const recorded = [];
  const execution = Object.fromEntries(ALL_PORTS.map(name => [name, () => {throw new Error('secret-token-and-path');}]));
  const coordinator = new TaskExecutionCoordinator({execution, providers: new Map(), prepare() {}, collect() {},
    recordFault: fault => { recorded.push(fault); if (recorded.length === 1) throw new Error('recorder broken'); }});
  const result = await coordinator.tick();
  assert.deepEqual(result.failure, {code: 'supervisor_failed', stage: 'reconcile-or-dispatch', port: 'scan'});
  assert.equal(recorded.length, 1);
  assert.equal(recorded[0].code, 'supervisor_failed');
  assert.equal(recorded[0].stage, 'reconcile-or-dispatch');
  assert.equal(recorded[0].port, 'scan');
  assert.equal(typeof recorded[0].at, 'number');
  assert.equal(recorded[0].error.name, 'Error');
  assert.equal(recorded[0].error.message, 'supervisor_execution_unavailable');
  assert.equal(recorded[0].error.cause.message, 'secret-token-and-path'); // #call 附带 cause：崩溃锚点拿得到原始异常
  assert.match(recorded[0].error.cause.stack, /secret-token-and-path/);
  assert.equal(JSON.stringify(result).includes('secret'), false); // 通知面仍脱敏
  await coordinator.tick(); // 故障已定：二次 tick 不再触发 recordFault
  assert.equal(recorded.length, 1);
  assert.equal((await coordinator.close()).clean, true);
});

test('recordFault defaults to null: fault path unchanged for existing callers', async () => {
  const reports = [];
  const execution = Object.fromEntries(ALL_PORTS.map(name => [name, () => {throw new Error('boom');}]));
  const coordinator = new TaskExecutionCoordinator({execution, providers: new Map(), prepare() {}, collect() {}, onError: value => reports.push(value)});
  const result = await coordinator.tick();
  assert.deepEqual(result.failure, {code: 'supervisor_failed', stage: 'reconcile-or-dispatch', port: 'scan'});
  assert.deepEqual(reports, [result.failure]);
  assert.equal((await coordinator.close()).clean, true);
});
