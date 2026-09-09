import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, ChildProcess } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchAcp } from './index.mjs';
import { CLEANUP_WAIT_MS } from './protocol.mjs';

const AGENT = fileURLToPath(new URL('./fake-agent.fixture.mjs', import.meta.url));
const OWNER = fileURLToPath(new URL('./owner.fixture.mjs', import.meta.url));
const posix = ['darwin', 'linux'].includes(process.platform);
const blocks = text => [{ type: 'text', text }];
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function temporary(t) {
  const directory = await mkdtemp(path.join(process.platform === 'darwin' ? '/private/tmp' : '/tmp', 'acp-runtime-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}
async function launch(t, extra = {}) {
  const cwd = await temporary(t);
  const runtime = await launchAcp({ executable: process.execPath, args: [AGENT], cwd, deadline: Date.now() + 10000, ...extra });
  t.after(() => runtime.stop());
  return { runtime, cwd };
}
async function session(runtime, cwd) {
  await runtime.client.initialize();
  return (await runtime.client.newSession({ cwd })).sessionId;
}
async function gone(pid, deadline = Date.now() + 5000) {
  while (Date.now() < deadline) {
    try { process.kill(pid, 0); }
    catch (error) { if (error.code === 'ESRCH') return; throw error; }
    await pause(25);
  }
  assert.fail('owned fixture process still exists');
}
function cleanFact(fact) {
  assert.equal(fact.cleaned, true, JSON.stringify({cleanup: fact}));
  assert.equal(fact.scope, 'inherited-process-group');
  assert.equal(fact.guardExit.signal, 'SIGKILL');
  assert.equal(fact.guardExit.observed, true);
  assert.ok(fact.started.agentPid > 0 && fact.started.guardPid > 0);
}

test('real checked-in Node Agent supports persistent bidirectional ACP and does not inherit env', { skip: !posix, timeout: 15000 }, async t => {
  const updates = [];
  process.env.AGENT_RUNTIME_PARENT_ONLY = 'PRIVATE_PARENT_FIXTURE';
  t.after(() => { delete process.env.AGENT_RUNTIME_PARENT_ONLY; });
  const { runtime, cwd } = await launch(t, { onUpdate: event => updates.push(JSON.parse(event.update.content.text)) });
  const id = await session(runtime, cwd);
  assert.equal((await runtime.client.prompt(id, blocks('first'))).stopReason, 'end_turn');
  assert.equal((await runtime.client.prompt(id, blocks('second'))).stopReason, 'end_turn');
  assert.deepEqual(updates, [{ echo: 'first', envLeaked: false }, { echo: 'second', envLeaked: false }]);
  assert.notEqual(runtime.started.agentPid, runtime.started.guardPid);
  const fact = await runtime.stop(); cleanFact(fact);
  assert.equal(await runtime.completion, fact); assert.equal(await runtime.stop(), fact);
  await gone(runtime.started.agentPid); await gone(runtime.started.guardPid);
});

test('permission defaults to rejection, explicit scoped callback can respond through the live channel', { skip: !posix, timeout: 15000 }, async t => {
  for (const permitted of [false, true]) {
    const updates = [];
    const { runtime, cwd } = await launch(t, { onUpdate: event => updates.push(JSON.parse(event.update.content.text)),
      ...(permitted ? { onPermission: () => ({ outcome: { outcome: 'selected', optionId: 'once' } }) } : {}) });
    const id = await session(runtime, cwd);
    await runtime.client.prompt(id, blocks('permission'));
    assert.equal(updates[0].permission, permitted ? 'selected' : 'cancelled');
    assert.equal(updates[0].optionId, permitted ? 'once' : null);
    cleanFact(await runtime.stop());
  }
});

test('protocol cancel finishes the turn but does not stop the owned execution', { skip: !posix, timeout: 15000 }, async t => {
  const { runtime, cwd } = await launch(t);
  const id = await session(runtime, cwd);
  let ended = false;
  const prompt = runtime.client.prompt(id, blocks('hang')).then(value => { ended = true; return value; });
  await runtime.client.cancel(id);
  assert.equal(ended, false);
  assert.equal((await prompt).stopReason, 'cancelled');
  process.kill(runtime.started.guardPid, 0); process.kill(runtime.started.agentPid, 0);
  assert.equal(runtime.client.closed, false);
  cleanFact(await runtime.stop());
});

test('owner stop revokes permission, rejects pending prompt, and waits for group cleanup', { skip: !posix, timeout: 15000 }, async t => {
  let entered, release, signal;
  const ready = new Promise(resolve => { entered = resolve; });
  const { runtime, cwd } = await launch(t, { onPermission: (_params, context) => {
    signal = context.signal; entered(); return new Promise(resolve => { release = resolve; });
  } });
  const id = await session(runtime, cwd);
  const prompt = runtime.client.prompt(id, blocks('permission'));
  const rejected = assert.rejects(prompt);
  await ready; const fact = await runtime.stop(); await rejected;
  assert.equal(signal.aborted, true); release({ outcome: { outcome: 'selected', optionId: 'once' } });
  cleanFact(fact); assert.equal(runtime.client.closed, true);
});

test('direct Agent exit is observed separately and inherited descendants are cleaned', { skip: !posix, timeout: 15000 }, async t => {
  let descendant;
  const { runtime, cwd } = await launch(t, { onUpdate: event => { descendant = JSON.parse(event.update.content.text).descendantPid; } });
  const id = await session(runtime, cwd);
  await runtime.client.prompt(id, blocks('descendant-and-exit'));
  const exit = await runtime.exited;
  assert.equal(exit.observed, true); assert.equal(exit.code, 7);
  const fact = await runtime.completion; cleanFact(fact);
  assert.ok(descendant > 0); await gone(descendant); await gone(runtime.started.guardPid);
});

test('deadline stops an Agent that ignores SIGTERM without inventing direct-exit evidence', { skip: !posix, timeout: 15000 }, async t => {
  const { runtime } = await launch(t, { env: { AGENT_RUNTIME_FIXTURE_MODE: 'ignore-term' }, deadline: Date.now() + 1800 });
  const fact = await runtime.completion; cleanFact(fact);
  assert.ok(['deadline', 'owner_stop'].includes(fact.reason));
  assert.equal(fact.agentExit.observed, false);
  await gone(runtime.started.agentPid);
});

test('byte limits bound stdin/stdout/stderr; no raw stderr enters observations', { skip: !posix, timeout: 20000 }, async t => {
  for (const [mode, limits, expected] of [
    ['stdout-overflow', { outputBytes: 1024 }, 'output_limit'],
    ['stderr-overflow', { stderrBytes: 1024 }, 'stderr_limit'],
    ['normal', { inputBytes: 768 }, 'input_limit'],
  ]) {
    const { runtime, cwd } = await launch(t, { env: { AGENT_RUNTIME_FIXTURE_MODE: mode }, limits });
    if (mode === 'normal') {
      const id = await session(runtime, cwd);
      await assert.rejects(runtime.client.prompt(id, blocks('x'.repeat(2048))));
    }
    const fact = await runtime.completion; cleanFact(fact);
    assert.equal(fact.reason, expected);
    assert.ok(!JSON.stringify(fact).includes('PRIVATE_STDERR_FIXTURE'));
  }
});

test('invalid launch never starts; missing executable retains a sanitized failed-launch cleanup fact', { skip: !posix, timeout: 15000 }, async t => {
  const cwd = await temporary(t);
  await assert.rejects(launchAcp({ executable: process.execPath, cwd, deadline: Date.now() - 1 }), { code: 'runtime_invalid_options' });
  await assert.rejects(launchAcp({ executable: 'relative', cwd, deadline: Date.now() + 10000 }), { code: 'runtime_invalid_options' });
  await assert.rejects(launchAcp({ executable: path.join(cwd, 'does-not-exist'), cwd, deadline: Date.now() + 10000 }), error => {
    assert.equal(error.code, 'runtime_launch_failed'); assert.equal(error.completion.cleaned, true);
    assert.equal(error.completion.started, null); assert.equal(error.completion.agentExit.observed, false);
    return true;
  });
});

test('actual owner process death disconnects inherited IPC and cleans Agent plus descendants', { skip: !posix, timeout: 20000 }, async t => {
  const cwd = await temporary(t);
  const owner = spawn(process.execPath, [OWNER], { cwd, env: { AGENT_RUNTIME_OWNER_FIXTURE: '1' }, stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
  t.after(() => { if (owner.exitCode === null && owner.signalCode === null) owner.kill('SIGTERM'); });
  let descendant;
  const started = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('fixture_owner_start_timeout')), 10000);
    owner.on('message', message => {
      if (message.type === 'descendant') descendant = message.pid;
      if (message.type === 'started') { clearTimeout(timer); resolve(message.started); }
    });
    owner.once('error', () => { clearTimeout(timer); reject(new Error('fixture_owner_failed')); });
    owner.once('exit', () => { clearTimeout(timer); reject(new Error('fixture_owner_early_exit')); });
  });
  const closed = new Promise(resolve => owner.once('close', resolve));
  owner.kill('SIGKILL'); await closed;
  assert.ok(descendant > 0);
  await gone(started.guardPid); await gone(started.agentPid); await gone(descendant);
});

test('original leader killed after cleaning cannot certify a surviving inherited descendant', {skip: !posix, timeout: 18000}, async t => {
  let descendant;
  const {runtime, cwd} = await launch(t, {deadline: Date.now() + 15000,
    onUpdate: event => { descendant = JSON.parse(event.update.content.text).descendantPid; }});
  const id = await session(runtime, cwd);
  await runtime.client.prompt(id, blocks('descendant-bounded'));
  assert.ok(descendant > 0); process.kill(descendant, 0);
  // Inject the fault using the ORIGINAL spawned guard handle, after its real
  // cleaning message reached Runtime and before its scheduled group SIGKILL.
  // The descendant has its own bounded fixture exit; no persisted PID is killed.
  const emit = ChildProcess.prototype.emit; let interrupted = false;
  t.mock.method(ChildProcess.prototype, 'emit', function (event, ...args) {
    const result = Reflect.apply(emit, this, [event, ...args]);
    if (event === 'message' && args[0]?.type === 'cleaning' && args[0]?.executionId === runtime.started.executionId &&
        this.pid === runtime.started.guardPid && !interrupted) {
      interrupted = this.kill('SIGKILL');
    }
    return result;
  });
  const began = performance.now(), stopping = runtime.stop();
  const repeated = setTimeout(() => { void runtime.stop(); }, Math.floor(CLEANUP_WAIT_MS / 2));
  t.after(() => clearTimeout(repeated));
  const fact = await stopping;
  const elapsed = performance.now() - began;
  let descendantStillAlive = false;
  try { process.kill(descendant, 0); descendantStillAlive = true; } catch {}
  // Await the fixture's OWN bounded exit before assertions/temporary cleanup,
  // including when a regression would otherwise fail the test early.
  await gone(descendant); await gone(runtime.started.guardPid); await gone(runtime.started.agentPid);
  assert.equal(interrupted, true); assert.equal(fact.guardExit.observed, true); assert.equal(fact.guardExit.signal, 'SIGKILL');
  assert.equal(fact.cleaned, false); assert.equal(fact.reason, 'cleanup_unconfirmed');
  assert.ok(elapsed >= CLEANUP_WAIT_MS - 100);
  assert.ok(elapsed < CLEANUP_WAIT_MS + 1500, 'repeat stop must not reset cleanup budget');
  assert.equal(descendantStillAlive, true, 'descendant must be alive when the false-positive is rejected');
  assert.equal(await runtime.stop(), fact); assert.equal(await runtime.completion, fact);
});

test('read-only group probe tolerates brief exit observation lag within the existing cleanup budget', {skip: !posix, timeout: 15000}, async t => {
  const {runtime} = await launch(t);
  const kill = process.kill; let probes = 0;
  t.mock.method(process, 'kill', function (pid, signal) {
    if (pid === -runtime.started.guardPid) {
      assert.equal(signal, 0, 'an exited group is only observed, never signalled');
      if (++probes < 3) return true;
    }
    return Reflect.apply(kill, process, [pid, signal]);
  });
  const fact = await runtime.stop(); cleanFact(fact); assert.ok(probes >= 3);
  assert.equal(await runtime.stop(), fact);
});

test('Darwin transient group EPERM stays unresolved until original group actually disappears', {skip: process.platform !== 'darwin', timeout: 10000}, async t => {
  const {runtime} = await launch(t), kill = process.kill; let probes = 0, absent = false;
  t.mock.method(process, 'kill', function (pid, signal) {
    if (pid === -runtime.started.guardPid) {
      assert.equal(signal, 0, 'an exited group is only observed, never signalled');
      if (++probes <= 2) throw Object.assign(new Error('fixture transient group state'), {code: 'EPERM'});
      try { return Reflect.apply(kill, process, [pid, signal]); }
      catch (error) { absent = error.code === 'ESRCH'; throw error; }
    }
    return Reflect.apply(kill, process, [pid, signal]);
  });
  const fact = await runtime.stop(); cleanFact(fact);
  assert.ok(probes >= 3); assert.equal(absent, true);
  assert.equal(await runtime.stop(), fact);
});

test('group probe permission and unexpected errors fail closed instead of proving absence', {skip: !posix, timeout: 15000}, async t => {
  for (const code of ['EPERM', 'EINVAL']) {
    const {runtime} = await launch(t), kill = process.kill; let probes = 0;
    const mock = t.mock.method(process, 'kill', function (pid, signal) {
      if (pid === -runtime.started.guardPid) {
        assert.equal(signal, 0); probes++;
        throw Object.assign(new Error('PRIVATE_PROBE_FAILURE'), {code});
      }
      return Reflect.apply(kill, process, [pid, signal]);
    });
    const began = performance.now(), fact = await runtime.stop(); mock.mock.restore();
    if (process.platform === 'darwin' && code === 'EPERM') {
      assert.ok(probes > 1);
      assert.ok(performance.now() - began >= CLEANUP_WAIT_MS - 100);
      assert.ok(performance.now() - began < CLEANUP_WAIT_MS + 1500);
    } else assert.equal(probes, 1);
    assert.equal(fact.cleaned, false); assert.equal(fact.reason, 'cleanup_unconfirmed');
    assert.equal(fact.guardExit.observed, true); assert.equal(fact.guardExit.signal, 'SIGKILL');
    assert.equal(JSON.stringify(fact).includes('PRIVATE_PROBE_FAILURE'), false);
    await gone(runtime.started.guardPid); await gone(runtime.started.agentPid);
  }
});
