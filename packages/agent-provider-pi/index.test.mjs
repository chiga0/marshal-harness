import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createPiProvider} from './index.mjs';

const agent = fileURLToPath(new URL('./agent.fixture.mjs', import.meta.url));
function input(t, extra = {}) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'pi-provider-test-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true})); return {cwd, deadline: Date.now() + 10000, prompt: 'approved fixture work', ...extra};
}
const provider = mode => createPiProvider({id: 'pi-fixture', executable: process.execPath, args: [agent, mode]});
function cleaned(result) {
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.cleanup.scope, 'inherited-process-group');
  assert.equal(result.cleanup.guardExit.signal, 'SIGKILL');
  if (result.cleanup.started) assert.throws(() => process.kill(result.cleanup.started.guardPid, 0), {code: 'ESRCH'});
}
test('native RPC fixture traverses original guard/provider and returns only bounded safe progress', {timeout: 15000}, async t => {
  const events = [], p = provider('normal'), handle = p.start(input(t, {onProgress: value => events.push(value)}));
  t.after(() => handle.stop()); assert.equal(handle.snapshot().phase, 'starting'); assert.equal(p.maturity, 'COMPONENT');
  const started = await handle.started, result = await handle.completion; cleaned(result);
  assert.equal(result.cleanup.started, started); assert.equal(result.status, 'completed'); assert.equal(result.stopReason, 'end_turn');
  assert.equal(result.outputText, 'public output'); assert.equal(result.sessionId, 'pi-session'); assert.equal(result.usage.source, 'unavailable');
  assert.ok(events.some(value => value.tool?.kind === 'read' && value.tool.status === 'completed'));
  assert.doesNotMatch(JSON.stringify({events, result}), /PRIVATE|args|sessionFile|errorMessage/);
  assert.equal(handle.snapshot().phase, 'terminal'); assert.equal(await handle.stop(), result);
});
test('retry settles before completion; error/length/absent settled remain non-success', {timeout: 20000}, async t => {
  for (const mode of ['retry', 'error', 'length', 'no-settled', 'overflow']) {
    const handle = provider(mode).start(input(t, mode === 'no-settled' ? {deadline: Date.now() + 1300} : {}));
    t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
    assert.equal(result.status, mode === 'retry' ? 'completed' : 'failed');
    if (mode === 'retry') assert.equal(result.outputText, 'public output');
    else assert.equal(result.outputText, '');
  }
});
test('same handle stops before bootstrap and during state lookup without sending a prompt', {timeout: 15000}, async t => {
  for (const immediate of [true, false]) {
    let enter; const entered = new Promise(resolve => { enter = resolve; });
    const handle = provider('hang-state').start(input(t, {onProgress: value => { if (value.phase === 'initializing') enter(); }}));
    t.after(() => handle.stop()); if (!immediate) await entered;
    const stopping = handle.stop(); assert.equal(handle.stop(), stopping); const result = await stopping;
    cleaned(result); assert.equal(result.status, 'cancelled'); assert.equal(result.sessionId, null);
  }
});
test('running cancellation clears queue then aborts and still waits for original real cleanup', {timeout: 15000}, async t => {
  let enter; const entered = new Promise(resolve => { enter = resolve; });
  const options = input(t, {onProgress: value => { if (value.phase === 'running') enter(); }});
  const handle = provider('cancel-order').start(options); t.after(() => handle.stop());
  await entered;
  // This observed file is written by our checked-in peer only after clear_queue
  // and abort. Waiting one event-loop turn lets the promised prompt enter RPC.
  await new Promise(resolve => setTimeout(resolve, 50));
  const result = await handle.stop(); cleaned(result); assert.equal(result.status, 'cancelled');
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(options.cwd, 'cancel-order.json'))), {cleared: true});
});
test('known detached shell/custom tool cannot release a directory on inherited-group cleanup', {timeout: 15000}, async t => {
  for (const mode of ['shell', 'custom-tool']) {
    const handle = provider(mode).start(input(t)); t.after(() => handle.stop()); const result = await handle.completion;
    assert.equal(result.status, 'unknown'); assert.equal(result.reason, 'pi_execution_scope_unproven');
    assert.equal(result.cleanup, null); assert.equal(result.runtimeCleanup.cleaned, true); assert.equal(result.outputText, '');
    assert.equal(result.runtimeCleanup.scope, 'inherited-process-group'); assert.equal(await handle.stop(), result);
  }
});
test('native question, reused session and unsupported permission policy are explicit, never silently accepted', {timeout: 15000}, async t => {
  assert.throws(() => provider('normal').start(input(t, {onPermission: () => ({outcome: {outcome: 'cancelled'}})})), {code: 'pi_permission_bridge_unavailable'});
  for (const mode of ['interaction', 'existing-session']) {
    const handle = provider(mode).start(input(t)); t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
    assert.equal(result.status, 'failed'); assert.equal(result.reason, mode === 'interaction' ? 'pi_interaction_required' : 'pi_session_not_fresh');
  }
});
test('bad input fails before launch, missing program retains original failure cleanup and stuck progress is bounded', {timeout: 15000}, async t => {
  assert.throws(() => createPiProvider({id: 'pi', executable: 'relative'}), {code: 'pi_invalid_configuration'});
  assert.throws(() => provider('normal').start(input(t, {deadline: 0})), {code: 'pi_invalid_input'});
  const options = input(t), missing = createPiProvider({id: 'missing', executable: path.join(options.cwd, 'missing')}).start(options);
  t.after(() => missing.stop()); const failed = await missing.completion; cleaned(failed); assert.equal(await missing.started, null);
  const stuck = provider('normal').start(input(t, {onProgress: () => new Promise(() => {})})); t.after(() => stuck.stop());
  const result = await stuck.completion; cleaned(result); assert.equal(result.reason, 'pi_progress_timeout');
});
