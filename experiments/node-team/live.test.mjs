import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, chmod, writeFile, readFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, dirname, isAbsolute} from 'node:path';
import {fileURLToPath} from 'node:url';
import {once} from 'node:events';
import {createHash} from 'node:crypto';

const executable = process.env.MARSHAL_NODE_LIVE_PI;
const main = join(dirname(fileURLToPath(import.meta.url)), 'main.mjs');

test('real local Pi pair: collection, frontend restart and cancellation without native Marshal', {
  skip: !executable, timeout: 390000,
}, async t => {
  assert.ok(isAbsolute(executable), 'explicit absolute Pi entry point required');
  const root = await mkdtemp(join(process.platform === 'darwin' ? '/private/tmp' : tmpdir(), 'mnt-live-'));
  await chmod(root, 0o700);
  const dataDir = join(root, 'state'), config = join(root, 'config.json');
  await writeFile(config, JSON.stringify({provider: 'pi', executable}), {mode: 0o600});
  let server;
  const fronts = [];
  const summary = {profile: 'node-local-experiment', nativeMarshalInvoked: false,
    startedAt: new Date().toISOString(), result: 'incomplete'};

  async function start() {
    const child = spawn(process.execPath, [main, '--data-dir', dataDir, '--config', config], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    fronts.push(child);
    // Never render provider output, tokens, prompts or configuration in test diagnostics.
    child.stderr.resume();
    let stdout = '';
    const ready = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('live_frontend_start_timeout')), 10000);
      child.once('error', () => {clearTimeout(timer); reject(new Error('live_frontend_spawn_failed'));});
      child.once('exit', () => {clearTimeout(timer); reject(new Error('live_frontend_exit_before_ready'));});
      child.stdout.on('data', b => {
        stdout = (stdout + b.toString()).slice(-16384);
        for (const line of stdout.split('\n')) {
          try {
            const value = JSON.parse(line);
            if (value.url && value.connectionFile) {clearTimeout(timer); resolve(value);}
          } catch {}
        }
      });
    });
    const connection = JSON.parse(await readFile(ready.connectionFile, 'utf8'));
    assert.ok(!stdout.includes(connection.token), 'stdout contains connection token');
    server = {child, url: ready.url, token: connection.token};
  }
  async function stopFront(child) {
    if (child.exitCode !== null || child.signalCode !== null) return;
    const stopped = once(child, 'exit');
    child.kill('SIGTERM');
    await stopped;
  }
  async function api(path, method = 'GET', body, key) {
    const response = await fetch(server.url + path, {
      method, signal: AbortSignal.timeout(5000), headers: {
        Authorization: `Bearer ${server.token}`, 'Content-Type': 'application/json',
        ...(key ? {'Idempotency-Key': key} : {}),
      }, body: body === undefined ? undefined : JSON.stringify(body),
    });
    assert.ok(response.ok, `live_http_status_${response.status}`);
    return response.json();
  }
  async function observe(id, predicate, timeout = 310000) {
    const end = Date.now() + timeout;
    while (Date.now() < end) {
      const task = await api(`/v1/tasks/${id}`);
      if (predicate(task)) return task;
      if (['failed', 'cancelled'].includes(task.status)) throw new Error(`live_task_${task.status}`);
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    throw new Error('live_task_observation_timeout');
  }
  async function launch(key) {
    const draft = await api('/v1/tasks', 'POST', {
      intent: 'Generate a correct order normalization module and a separate SKU aggregation module, with validation and safe-integer overflow handling.',
    }, key);
    await api(`/v1/tasks/${draft.id}/approve`, 'POST', {
      expectedRevision: draft.revision, previewDigest: draft.previewDigest,
    }, key + '-approve');
    await observe(draft.id, value => value.status === 'running', 10000);
    const end = Date.now() + 10000;
    while (Date.now() < end) {
      const result = await api(`/v1/tasks/${draft.id}/workers`);
      if (result.workers.length === 2 && result.workers.every(w => w.pid && w.startedAt)) return {draft, workers: result.workers};
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error('two_real_workers_not_observed');
  }
  t.after(async () => {
    for (const child of fronts) await stopFront(child);
    const stop = spawn(process.execPath, [main, 'stop', '--data-dir', dataDir], {stdio: 'ignore'});
    await once(stop, 'exit');
    summary.finishedAt = new Date().toISOString();
    await writeFile(join(root, 'acceptance-summary.json'), JSON.stringify(summary, null, 2) + '\n', {mode: 0o600});
    t.diagnostic(`Private evidence: ${join(root, 'acceptance-summary.json')}`);
  });

  await start();
  const first = await launch('live-delivery');
  const before = await api(`/v1/tasks/${first.draft.id}`);
  const identity = workers => workers.map(w => ({id: w.id, nodeId: w.nodeId, pid: w.pid, startedAt: w.startedAt}));
  await stopFront(server.child);
  await start();
  const recovered = await api(`/v1/tasks/${first.draft.id}`);
  assert.equal(recovered.deadline, before.deadline);
  assert.deepEqual(identity((await api(`/v1/tasks/${first.draft.id}/workers`)).workers), identity(first.workers));
  const terminal = await observe(first.draft.id, task => ['completed', 'failed'].includes(task.status));
  assert.equal(terminal.status, 'completed', 'real pair did not pass independent verification');
  const completedWorkers = (await api(`/v1/tasks/${first.draft.id}/workers`)).workers;
  const overlapMs = Math.min(...completedWorkers.map(w => Date.parse(w.finishedAt))) -
    Math.max(...completedWorkers.map(w => Date.parse(w.startedAt)));
  assert.ok(overlapMs > 0, 'real worker execution intervals must overlap');
  const delivery = await api(`/v1/tasks/${first.draft.id}/delivery`);
  assert.deepEqual(delivery.files.map(f => f.name).sort(), ['normalize.mjs', 'report.mjs']);
  assert.ok(delivery.checks > 0);
  for (const file of delivery.files) assert.equal(createHash('sha256').update(file.content).digest('hex'), file.sha256);
  summary.delivery = {taskId: first.draft.id, workers: identity(first.workers), checks: delivery.checks,
    files: delivery.files.map(({name, sha256}) => ({name, sha256})), overlapMs, frontendRestartRecovered: true};

  const second = await launch('live-cancel');
  const current = await api(`/v1/tasks/${second.draft.id}`);
  await api(`/v1/tasks/${second.draft.id}/cancel`, 'POST', {expectedRevision: current.revision}, 'live-cancel-stop');
  await observe(second.draft.id, task => task.status === 'cancelled', 15000);
  assert.ok((await api(`/v1/tasks/${second.draft.id}/workers`)).workers.every(w => w.cleaned === true));
  await stopFront(server.child);
  await start();
  assert.equal((await api(`/v1/tasks/${second.draft.id}`)).status, 'cancelled');
  summary.cancellation = {taskId: second.draft.id, workers: identity(second.workers), persisted: true};
  summary.result = 'passed';
});
