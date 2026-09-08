import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Supervisor } from './supervisor.mjs';
import { Store, artifactHash, sameSecret, nativeEnvironment, atomicPrivate } from './store.mjs';

const fixture = fileURLToPath(new URL('./runtime-fixture.test.mjs', import.meta.url));
const tempParent = process.platform === 'darwin' ? '/private/tmp' : '/tmp';
async function root(t) {
  const directory = await fs.mkdtemp(path.join(tempParent, 'node-team-unit-'));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  return directory;
}
function ports(mode = 'normal', verifyFiles = async files => ({ passed: true, checks: 3, files: files.map(file => ({ ...file, sha256: artifactHash(file.content) })) })) {
  return {
    plan(intent) { return { version: 'test-only/v1', intent, timeoutMs: 300000, nodes: ['normalize', 'report'].map(id => ({ id, role: 'author', file: id + '.mjs', prompt: JSON.stringify({ mode, name: id + '.mjs' }) })) }; },
    makeCommand() { return { command: process.execPath, args: [fixture], env: { NODE_TEAM_RUNTIME_FIXTURE: '1' } }; },
    parseCandidate(_config, _node, stdout) { return JSON.parse(stdout); }, verifyFiles,
  };
}
async function setup(t, mode, verifier) {
  const directory = await root(t);
  const owner = await Supervisor.open(directory, {}, ports(mode, verifier));
  // Registered later than root cleanup: explicit shutdown below each test's
  // finally also proves no detached owned process is abandoned on failure.
  return { directory, owner };
}
async function create(owner, name = 'create') { return owner.command('create', undefined, { intent: 'test business' }, name); }
async function approve(owner, task) { return owner.command('approve', task.id, { expectedRevision: task.revision, previewDigest: task.previewDigest }, 'approve'); }
async function wait(owner, task, predicate, timeout = 8000) {
  const until = Date.now() + timeout;
  for (;;) {
    const current = await owner.command('get', task.id);
    if (predicate(current)) return current;
    assert.ok(Date.now() < until, 'bounded runtime test timed out: ' + current.status + '/' + current.reason);
    await new Promise(resolve => setTimeout(resolve, 15));
  }
}

test('two guarded processes overlap, deliver bare hashes, replay and cold-read original outcome', async t => {
  const { owner, directory } = await setup(t);
  try {
    const task = await create(owner, 'constructor');
    assert.equal((await create(owner, 'constructor')).id, task.id);
    await assert.rejects(owner.command('create', undefined, { intent: 'different' }, 'constructor'), /idempotency-conflict/);
    const accepted = await approve(owner, task);
    assert.equal((await approve(owner, task)).deadline, accepted.deadline);
    const active = await wait(owner, task, current => current.workers.every(w => w.pid));
    assert.equal(new Set(active.workers.map(w => w.pid)).size, 2);
    assert.equal(active.revision, 2);
    const completed = await wait(owner, task, current => current.status === 'completed');
    assert.equal(completed.attempts, 1);
    assert.ok(completed.workers.every(w => w.cleaned));
    for (const worker of completed.workers) {
      assert.ok(Date.parse(worker.startedAt) <= Date.parse(worker.agentExitedAt));
      assert.ok(Date.parse(worker.agentExitedAt) < Date.parse(worker.finishedAt));
    }
    assert.ok(Math.max(...completed.workers.map(w => Date.parse(w.startedAt))) < Math.min(...completed.workers.map(w => Date.parse(w.agentExitedAt))));
    const delivered = await owner.command('delivery', task.id);
    assert.ok(delivered.files.every(f => /^[0-9a-f]{64}$/.test(f.sha256)));
    await assert.rejects(owner.command('cancel', task.id, { expectedRevision: completed.revision }, 'late'), /task-already-terminal/);
    await owner.shutdown();
    const reloaded = await Supervisor.open(directory, {}, ports());
    assert.deepEqual(await reloaded.command('delivery', task.id), delivered);
    assert.equal((await approve(reloaded, task)).attempts, 1);
    await reloaded.shutdown();
  } finally { await owner.shutdown(); }
});

test('cancel persists once, rejects fresh repeated keys and waits for owned group cleanup', async t => {
  const { owner } = await setup(t, 'hang');
  try {
    const task = await create(owner); await approve(owner, task);
    const active = await wait(owner, task, current => current.workers.every(w => w.pid));
    const body = { expectedRevision: active.revision };
    const pending = await owner.command('cancel', task.id, body, 'cancel');
    assert.equal(pending.status, 'cancelling');
    assert.equal((await owner.command('cancel', task.id, body, 'cancel')).status, 'cancelling');
    await assert.rejects(owner.command('cancel', task.id, { expectedRevision: pending.revision }, 'new-cancel'), /cancel-already-requested/);
    const cancelled = await wait(owner, task, current => current.status === 'cancelled');
    assert.ok(cancelled.workers.every(w => w.cleaned));
    assert.equal(owner.task(task.id).controls.length, 2);
    assert.equal(owner.guards.size, 0);
    for (const worker of cancelled.workers) assert.throws(() => process.kill(worker.pid, 0), { code: 'ESRCH' });
    assert.equal((await owner.command('cancel', task.id, body, 'cancel')).status, 'cancelled');
    assert.equal(owner.faulted, false);
  } finally { await owner.shutdown(); }
});

test('cancel wins over late independent verification and passes AbortSignal', async t => {
  let entered, release;
  const started = new Promise(resolve => { entered = resolve; });
  const { owner } = await setup(t, 'normal', async (files, { signal }) => {
    entered(signal);
    await new Promise(resolve => { release = resolve; });
    return { passed: true, checks: 3, files: files.map(f => ({ ...f, sha256: artifactHash(f.content) })) };
  });
  try {
    const task = await create(owner); await approve(owner, task);
    const signal = await started;
    const verifying = await owner.command('get', task.id);
    assert.equal(verifying.status, 'verifying');
    const pending = await owner.command('cancel', task.id, { expectedRevision: verifying.revision }, 'cancel');
    assert.equal(signal.aborted, true);
    assert.equal(pending.status, 'cancelling');
    await assert.rejects(owner.command('delivery', task.id), /delivery-not-ready/);
    release();
    await wait(owner, task, current => current.status === 'cancelled');
    assert.equal(owner.task(task.id).delivery, undefined);
  } finally { release?.(); await owner.shutdown(); }
});

for (const mode of ['overflow', 'fail']) test('failed ' + mode + ' keeps reason and never exposes delivery', async t => {
  const { owner } = await setup(t, mode);
  try {
    const task = await create(owner); await approve(owner, task);
    const failed = await wait(owner, task, current => current.status === 'failed');
    assert.equal(failed.reason, mode === 'overflow' ? 'output-limit' : 'agent-failed');
    assert.equal(failed.attempts, 1);
    assert.ok(failed.workers.every(w => w.cleaned));
    await assert.rejects(owner.command('delivery', task.id), /delivery-not-ready/);
  } finally { await owner.shutdown(); }
});

test('unknown unclean obligation blocks admission and cold owner replacement', async t => {
  const { owner, directory } = await setup(t);
  const task = await create(owner);
  // Negative durable fixture, not a claimed successful cleanup producer.
  await owner.change(state => { const current = state.tasks[0]; current.status = 'intervention'; current.reason = 'guard-cleanup-unconfirmed'; current.workers[0].cleaned = false; });
  const next = await create(owner, 'next');
  await assert.rejects(approve(owner, next), /capacity-busy/);
  await assert.rejects(Supervisor.open(directory, {}, ports()), /supervisor-recovery-needs-intervention/);
  assert.equal(owner.task(task.id).workers[0].cleaned, false);
});

test('private store refuses corrupt, symlink, and changed durable state', async t => {
  const directory = await root(t), store = await Store.open(directory);
  await store.save(structuredClone(store.state));
  await atomicPrivate(store.file, { ...store.state, unexpected: true });
  await assert.rejects(store.save(structuredClone(store.state)), /state-drift/);
  await assert.rejects(Store.open(directory), /invalid-state/);
  const link = path.join(directory, 'linked'); await fs.symlink(store.file, link);
  await assert.rejects(Store.open(link), /unsafe-data-dir/);
});

test('non-secret environment preserves native HOME and current Node on PATH', () => {
  const env = nativeEnvironment({ HOME: '/private/example', PATH: '/usr/bin:/bin', LANG: 'C', GITHUB_TOKEN: 'not-inherited', NODE_OPTIONS: '--inspect' });
  assert.equal(env.HOME, '/private/example');
  assert.equal(env.PATH.split(path.delimiter)[0], path.dirname(process.execPath));
  assert.equal(env.GITHUB_TOKEN, undefined); assert.equal(env.NODE_OPTIONS, undefined);
  assert.equal(sameSecret('abc', 'abc'), true); assert.equal(sameSecret('abc', 'abé'), false);
});
