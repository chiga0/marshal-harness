import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./recovery-crash.fixture.mjs', import.meta.url));
async function until(observe, timeout = 10000) {
  const deadline = Date.now() + timeout;
  for (;;) { const value = await observe(); if (value) return value;
    assert.ok(Date.now() < deadline, 'bounded crash-test observation timed out'); await pause(10); }
}
function lines(file) { try { return fs.readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; } }
async function gone(pid) {
  assert.ok(Number.isSafeInteger(pid) && pid > 1);
  await until(() => { try { process.kill(pid, 0); return false; } catch (error) { assert.equal(error.code, 'ESRCH'); return true; } });
}
// Read-only SQLite snapshots only AFTER the service owner has exited. No owner
// claim, insert, refund, result import, raw-PID cleanup or second authority.
function durable(root) {
  const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON');
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {generation: db.prepare('SELECT generation FROM metadata').get().generation,
      attempts: rows("SELECT * FROM projections WHERE kind='attempt' ORDER BY id"),
      capacity: rows("SELECT * FROM projections WHERE kind='budget' ORDER BY id"),
      receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      starts: rows("SELECT * FROM outbox WHERE kind IN ('start','verify') ORDER BY id")};
  } finally { db.close(); }
}
async function launch(root, mode, barrier) {
  const child = spawn(process.execPath, [cli, '--root', root, '--mode', mode, '--config', config], {
    cwd: path.dirname(root), env: {MARSHAL_SERVICE_CRASH_FIXTURE: '1', MARSHAL_SERVICE_CRASH_STOP_BARRIER: barrier ? '1' : '0'},
    stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '', exited = false;
  const done = new Promise(resolve => {
    child.once('error', () => { exited = true; resolve({code: null, signal: 'spawn-error'}); });
    child.once('exit', (code, signal) => { exited = true; resolve({code, signal}); });
  });
  child.stdout.on('data', bytes => { stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL'); });
  child.stderr.on('data', bytes => { stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL'); });
  const stop = async signal => { if (!exited) child.kill(signal); return done; };
  try {
    await until(() => { assert.equal(exited, false, 'service exited before publishing connection'); return stdout.includes('\n'); });
    const ready = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(ready.connectionFile));
    const client = new TaskClient({baseURL: connection.url, token: connection.token});
    return {child, done, stop, client, checkOutput() { assert.equal((stdout + stderr).includes(connection.token), false); }};
  } catch (error) { await stop('SIGKILL'); throw error; }
}

for (const barrier of [false, true]) test(barrier ?
  'real service SIGKILL after durable cancel fence and before cleanup preserves unresolved obligation on open' :
  'real service SIGKILL after dispatch started cleans owned group but open never retries or forges recovery', {timeout: 30000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-service-crash-'))), root = path.join(parent, 'data');
  const journal = path.join(parent, 'fixture-executions.jsonl'), services = []; let owned, descendant;
  t.after(async () => {
    for (const service of services) { await service.stop('SIGKILL'); service.checkOutput(); }
    if (owned) { await gone(owned.started.guardPid); await gone(owned.started.agentPid); }
    if (descendant) await gone(descendant);
    fs.rmSync(parent, {recursive: true, force: true});
  });
  const first = await launch(root, 'create', barrier); services.push(first);
  const body = {intent: '无模型服务崩溃测试，保留未决执行', limits: {timeoutMs: 20000, maxAttempts: 2, maxWorkers: 1}};
  const created = await first.client.createTask(body, 'crash-create');
  owned = await until(() => lines(journal).find(value => value.type === 'started' && value.started));
  const processes = await until(() => { try { return JSON.parse(fs.readFileSync(path.join(owned.cwd, 'fixture-processes.json'))); }
    catch (error) { if (error.code === 'ENOENT') return null; throw error; } });
  descendant = processes.descendantPid; assert.equal(processes.agentPid, owned.started.agentPid);
  let workers;
  await until(async () => { workers = await first.client.request('task.workers', {path: {taskId: created.id}});
    return workers.items.length === 1 && workers.items[0].status === 'running'; });
  assert.equal(workers.items[0].id, owned.workerId); assert.equal(workers.items[0].startedAt, owned.started.startedAt);
  assert.equal((await first.client.request('task.audit', {path: {taskId: created.id}})).attempts, 1);
  let cancelBody, cancellation;
  if (barrier) {
    const current = await first.client.getTask(created.id); cancelBody = {expectedRevision: current.revision};
    cancellation = await first.client.request('task.cancel', {path: {taskId: created.id}, body: cancelBody, idempotencyKey: 'crash-cancel'});
    await until(() => lines(journal).some(value => value.type === 'stop-entered'));
    assert.equal((await first.client.getTask(created.id)).status, 'cancelling');
    // The barrier has not stopped the original handle yet: this is an actual
    // durable-fence-before-cleanup crash, not a fake terminal/cleanup fixture.
    process.kill(owned.started.agentPid, 0); process.kill(descendant, 0);
  }
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  await gone(owned.started.guardPid); await gone(owned.started.agentPid); await gone(descendant);
  const before = durable(root), originalJournal = lines(journal), executionNames = fs.readdirSync(path.join(root, 'executions'));
  assert.equal(before.generation, 1); assert.equal(before.starts.length, 1); assert.equal(before.starts[0].status, 'unknown');
  assert.ok(fs.existsSync(owned.cwd), 'crash must not silently delete unresolved execution directory');
  const second = await launch(root, 'open', false); services.push(second);
  const current = await second.client.getTask(created.id);
  assert.equal(current.status, 'intervention'); assert.equal(current.code, 'previous_execution_unresolved');
  assert.equal(current.deadlineAt, created.deadlineAt);
  assert.equal((await second.client.request('health.get')).status, 'ok');
  await assert.rejects(second.client.request('ready.get'), {code: 'not_ready'});
  await assert.rejects(second.client.createTask(body, 'new-after-crash'), {code: 'not_ready'});
  assert.deepEqual(await second.client.createTask(body, 'crash-create'), created);
  await assert.rejects(second.client.createTask({...body, intent: 'different'}, 'crash-create'), {code: 'idempotency_conflict'});
  const supervisor = await second.client.request('supervisor.get');
  assert.equal(supervisor.status, 'intervention'); assert.equal(supervisor.activeWorkers, 1);
  const audit = await second.client.request('task.audit', {path: {taskId: created.id}});
  assert.equal(audit.attempts, 1); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
  assert.equal(audit.acceptance.status, 'pending'); assert.equal(audit.workers.length, 1);
  assert.equal(audit.workers[0].finishedAt, null);
  if (barrier) {
    assert.deepEqual(await second.client.request('task.cancel', {path: {taskId: created.id}, body: cancelBody, idempotencyKey: 'crash-cancel'}), cancellation);
    const observed = await second.client.request('operation.get', {path: {operationId: cancellation.id}});
    assert.equal(observed.status, 'unknown', 'no current-generation cleanup cannot settle cancellation as success');
  }
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
  const after = durable(root); assert.equal(after.generation, before.generation + 1);
  for (const key of ['attempts', 'capacity', 'receipts', 'starts']) assert.deepEqual(after[key], before[key], key + ' must not be retried/refunded/imported');
  assert.deepEqual(lines(journal), originalJournal); assert.deepEqual(fs.readdirSync(path.join(root, 'executions')), executionNames);
  assert.ok(fs.existsSync(owned.cwd));
});
