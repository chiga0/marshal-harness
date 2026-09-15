import test from 'node:test';
import assert from 'node:assert/strict';
import {cliShutdown} from './cli-shutdown.mjs';
import {safeServiceDiagnostic, terminalServiceDiagnostic} from './service-diagnostic.mjs';
import {TaskExecutionCoordinator} from '../task-execution/controller.mjs';

test('startup failure waits for acquired edge; signal and failure share one obligation', async () => {
  let resources = {}, edges = 0, services = 0, emitted = 0;
  const shutdown = cliShutdown(() => resources, (result, code) => {
    emitted++; assert.equal(code, 1); assert.equal(result.code, 'service_supervisor_failed');
  });
  const first = shutdown.stop('service_supervisor_failed');
  assert.equal(shutdown.stop(), first);
  await Promise.resolve(); assert.equal(emitted, 0);
  resources = {edge: {async close() {edges++;}}, service: {async shutdown() {
    services++; return {state: 'closed', shutdownClean: true, failure: 'service_supervisor_failed'};
  }}};
  shutdown.started(); await first;
  assert.equal(edges, 1); assert.equal(services, 1); assert.equal(emitted, 1);
});
test('edge cleanup failure does not skip service; failure arriving during signal cleanup retains exit 1', async () => {
  let services = 0, release;
  const barrier = new Promise(resolve => {release = resolve;});
  const shutdown = cliShutdown(() => ({edge: {async close() {await barrier; throw new Error('secret');}},
    service: {async shutdown() {services++; return {state: 'closed', shutdownClean: true, failure: null};}}}),
  (result, code) => {assert.equal(code, 1); assert.equal(result.clean, false); assert.equal(result.code, 'service_owner_unavailable');});
  shutdown.started(); const first = shutdown.stop(); await Promise.resolve();
  assert.equal(shutdown.stop('service_owner_unavailable'), first); release(); await first;
  assert.equal(services, 1);
});
test('service shutdown rejection is not reported clean and original failure is retained', async () => {
  const shutdown = cliShutdown(() => ({service: {async shutdown() {throw new Error('secret');}}}),
    (result, code) => {assert.deepEqual(result, {state: 'failed', clean: false, code: 'service_supervisor_failed'}); assert.equal(code, 1);});
  shutdown.started(); await shutdown.stop('service_supervisor_failed');
});
test('service diagnostics retain only closed metadata and never raw exception fields', () => {
  assert.deepEqual(safeServiceDiagnostic({code: 'supervisor_failed', stage: 'reconcile-or-dispatch', port: 'scan',
    error: 'secret', cause: 'token', taskId: 'private'}), {code: 'supervisor_failed', stage: 'reconcile-or-dispatch', port: 'scan'});
  assert.deepEqual(safeServiceDiagnostic({code: 'supervisor_failed', stage: 'secret', port: 'secret'}), {code: 'supervisor_failed'});
  assert.equal(safeServiceDiagnostic({code: 'secret'}), null);
  assert.equal(terminalServiceDiagnostic({code: 'service_custody_unresolved'}), false);
  assert.equal(terminalServiceDiagnostic({code: 'service_supervisor_failed'}), true);
});
test('coordinator scan failure retains port and stage, never the original exception', async () => {
  const reports = [], execution = Object.fromEntries(['scan', 'reconcile', 'poll', 'settleControl', 'expandDispatch',
    'nextWork', 'mayStart', 'started', 'progress', 'fail', 'finish'].map(name => [name, () => {throw new Error('secret-token-and-path');}]));
  const coordinator = new TaskExecutionCoordinator({execution, providers: new Map(), prepare() {}, collect() {}, onError: value => reports.push(value)});
  const result = await coordinator.tick();
  assert.deepEqual(result.failure, {code: 'supervisor_failed', stage: 'reconcile-or-dispatch', port: 'scan'});
  assert.deepEqual(reports, [result.failure]); assert.equal(JSON.stringify(result).includes('secret'), false);
  assert.equal((await coordinator.close()).clean, true);
});
