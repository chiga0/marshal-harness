import test from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, writeFile, readFile, chmod} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {once} from 'node:events';

const here = dirname(fileURLToPath(import.meta.url));
const main = join(here, 'main.mjs');
const fixture = join(here, 'fixtures/pi.mjs');

async function start(dataDir, config) {
  const child = spawn(process.execPath, [main, '--data-dir', dataDir, '--config', config], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '', stderr = '';
  child.stderr.on('data', b => { stderr += b.toString(); });
  const ready = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`server startup timed out: ${stderr.slice(-300)}`)), 10000);
    child.once('error', e => { clearTimeout(timer); reject(e); });
    child.once('exit', code => { clearTimeout(timer); reject(new Error(`server exit ${code}: ${stderr.slice(-300)}`)); });
    child.stdout.on('data', b => {
      stdout += b.toString();
      for (const line of stdout.split('\n')) {
        try {
          const value = JSON.parse(line);
          if (value.url && value.connectionFile) { clearTimeout(timer); resolve(value); }
        } catch {}
      }
    });
  });
  const connection = JSON.parse(await readFile(ready.connectionFile, 'utf8'));
  assert.equal(typeof connection.token, 'string');
  assert.ok(!stdout.includes(connection.token), 'connection token must not appear in stdout');
  return {child, ...ready, token: connection.token};
}

async function request(server, path, method = 'GET', body, key) {
  const response = await fetch(server.url + path, {
    method, signal: AbortSignal.timeout(5000),
    headers: {Authorization: `Bearer ${server.token}`, 'Content-Type': 'application/json',
      ...(key ? {'Idempotency-Key': key} : {})},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return {status: response.status, body: await response.json()};
}

async function until(server, id, predicate, timeout = 20000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const result = await request(server, `/v1/tasks/${id}`);
    assert.equal(result.status, 200);
    if (predicate(result.body)) return result.body;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('task observation timeout');
}

async function stopFront(server) {
  if (server.child.exitCode !== null || server.child.signalCode !== null) return;
  const closed = once(server.child, 'exit');
  server.child.kill('SIGTERM');
  await closed;
}

test('pure HTTP team delivery, idempotency, active frontend restart and owned cancel', {timeout: 60000}, async t => {
  const root = await mkdtemp(join(process.platform === 'darwin' ? '/private/tmp' : tmpdir(), 'mnt-'));
  const dataDir = join(root, 'state');
  const config = join(root, 'config.json');
  await chmod(root, 0o700);
  await chmod(fixture, 0o755);
  await writeFile(config, JSON.stringify({provider: 'pi', executable: fixture}), {mode: 0o600});
  let server;
  t.after(async () => {
    if (server) await stopFront(server);
    const stop = spawn(process.execPath, [main, 'stop', '--data-dir', dataDir], {stdio: 'ignore'});
    await once(stop, 'exit');
    // Evidence and failed outputs are intentionally retained, never broad rm.
  });
  server = await start(dataDir, config);
  const denied = await fetch(server.url + '/v1/tasks');
  assert.equal(denied.status, 401);
  const origin = await fetch(server.url + '/v1/tasks', {headers: {
    Authorization: `Bearer ${server.token}`, Origin: 'https://untrusted.invalid',
  }});
  assert.equal(origin.status, 403);

  const created = await request(server, '/v1/tasks', 'POST', {intent: 'Build a reliable order report'}, 'create-first');
  assert.equal(created.status, 201);
  const draft = created.body;
  assert.equal(draft.status, 'awaiting-approval');
  const replay = await request(server, '/v1/tasks', 'POST', {intent: 'Build a reliable order report'}, 'create-first');
  assert.equal(replay.body.id, draft.id);
  const conflict = await request(server, '/v1/tasks', 'POST', {intent: 'different'}, 'create-first');
  assert.equal(conflict.status, 409);
  const approval = {expectedRevision: draft.revision, previewDigest: draft.previewDigest};
  const stale = await request(server, `/v1/tasks/${draft.id}/approve`, 'POST', {
    ...approval, expectedRevision: draft.revision + 1,
  }, 'stale-approve');
  assert.equal(stale.status, 409);
  const approved = await request(server, `/v1/tasks/${draft.id}/approve`, 'POST', approval, 'approve-first');
  assert.ok([200, 202].includes(approved.status));
  const approvedReplay = await request(server, `/v1/tasks/${draft.id}/approve`, 'POST', approval, 'approve-first');
  assert.equal(approvedReplay.body.attempts, 1);
  const finished = await until(server, draft.id, value => ['completed', 'failed'].includes(value.status));
  assert.equal(finished.status, 'completed', JSON.stringify(finished));
  const delivery = await request(server, `/v1/tasks/${draft.id}/delivery`);
  assert.equal(delivery.status, 200);
  assert.deepEqual(delivery.body.files.map(f => f.name).sort(), ['normalize.mjs', 'report.mjs']);
  assert.ok(delivery.body.checks > 0);
  assert.ok(delivery.body.files.every(f => /^[a-f0-9]{64}$/.test(f.sha256)));
  const tooLate = await request(server, `/v1/tasks/${draft.id}/cancel`, 'POST', {expectedRevision: finished.revision}, 'late-cancel');
  assert.equal(tooLate.status, 409);

  const slow = (await request(server, '/v1/tasks', 'POST', {intent: 'fixture-slow order report'}, 'create-slow')).body;
  await request(server, `/v1/tasks/${slow.id}/approve`, 'POST', {
    expectedRevision: slow.revision, previewDigest: slow.previewDigest,
  }, 'approve-slow');
  const running = await until(server, slow.id, value => value.status === 'running' &&
    value.workers.length === 2 && value.workers.every(w => w.pid && w.startedAt));
  const before = await request(server, `/v1/tasks/${slow.id}/workers`);
  await stopFront(server);
  server = await start(dataDir, config);
  const recovered = await request(server, `/v1/tasks/${slow.id}`);
  assert.equal(recovered.body.id, slow.id);
  assert.equal(recovered.body.status, 'running');
  const after = await request(server, `/v1/tasks/${slow.id}/workers`);
  const identities = value => value.workers.map(w => ({id: w.id, nodeId: w.nodeId, pid: w.pid, startedAt: w.startedAt}));
  assert.deepEqual(identities(after.body), identities(before.body));
  assert.equal(recovered.body.deadline, running.deadline, 'restart must not refresh the task deadline');
  const cancel = await request(server, `/v1/tasks/${slow.id}/cancel`, 'POST', {
    expectedRevision: running.revision,
  }, 'cancel-slow');
  assert.equal(cancel.status, 202);
  const cancelled = await until(server, slow.id, value => value.status === 'cancelled');
  assert.equal(cancelled.status, 'cancelled');
  const stoppedWorkers = (await request(server, `/v1/tasks/${slow.id}/workers`)).body.workers;
  assert.ok(stoppedWorkers.every(w => w.cleaned === true), 'cancelled requires confirmed owned cleanup');
  const unavailable = await request(server, `/v1/tasks/${slow.id}/delivery`);
  assert.notEqual(unavailable.status, 200);
  await stopFront(server);
  server = await start(dataDir, config);
  assert.equal((await request(server, `/v1/tasks/${slow.id}`)).body.status, 'cancelled');
});
