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
import {encode, digest} from '../task-store/store.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./soak.fixture.mjs', import.meta.url));
const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
// 下载消费者独立从原上传 bytes 复算，不导入作者或验收器的答案。
const expected = ['east', 'west'].map(region => {
  const rows = data.rows.filter(row => row.region === region && row.status === 'paid');
  return {region, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)};
});
function observations(file) {
  try { const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
}
async function until(observe, label, ms = 45000) {
  const end = Date.now() + ms;
  for (;;) { const value = await observe(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded soak observation timed out: ' + label); await pause(25); }
}
async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-soak-')));
  const root = path.join(parent, 'data'), journal = path.join(parent, 'soak-observations.jsonl');
  const child = spawn(process.execPath, [cli, '--root', root, '--mode', 'create', '--config', config],
    {cwd: parent, env: {MARSHAL_SOAK_FIXTURE: '1'}, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '', exited = false, closed = false, spawnError = false, finished = false, token;
  const done = new Promise(resolve => {
    child.once('error', () => {exited = true; spawnError = true;});
    child.once('exit', () => {exited = true;});
    // close 还等待原 stdout/stderr 排空，exit 不保证最后 clean 帧已经抵达。
    child.once('close', (code, signal) => {exited = true; closed = true; resolve(spawnError ? {code: null, signal: 'spawn-error'} : {code, signal});});
  });
  child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL');});
  child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL');});
  async function stop() {
    if (!exited) child.kill('SIGTERM');
    const timer = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 15000);
    try {await until(() => closed, 'original CLI shutdown and pipe drain', 18000); return await done;} finally {clearTimeout(timer);}
  }
  t.after(async () => {
    await stop();
    if (token) assert.equal((stdout + stderr).includes(token), false);
    if (finished) fs.rmSync(parent, {recursive: true, force: true});
    else t.diagnostic('Preserved failed bounded soak evidence: ' + parent);
  });
  await until(() => {assert.equal(exited, false, 'original CLI startup failed: ' + stderr); return stdout.includes('\n');}, 'CLI startup', 15000);
  const output = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(output.connectionFile));
  assert.equal(JSON.parse(fs.readFileSync(path.join(root, 'profile.json'))).layout, 2); token = connection.token;
  return {root, child, client: new TaskClient({baseURL: connection.url, token}), rows: () => observations(journal),
    async close() {
      assert.deepEqual(await stop(), {code: 0, signal: null});
      assert.equal(JSON.parse(stdout.trim().split('\n').at(-1)).clean, true);
      assert.equal((stdout + stderr).includes(token), false);
    }, durable() {
      assert.equal(exited, true, 'SQL inspection occurs only after the ORIGINAL service exits');
      const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
      try {
        db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
        assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
        return db.prepare('SELECT kind,id,bytes FROM projections ORDER BY kind,id').all().map(row => ({kind: row.kind, id: row.id, value: JSON.parse(Buffer.from(row.bytes))}));
      } finally {db.close();}
    }, complete() {finished = true;}};
}

// 三波而非两 Task 延迟测试；有限、无模型的回归，不是生产 SLO 或恶意代码隔离证明。
test('one CLI continuously delivers 12 HTTP teams across waves, isolates real rejected work and cancellation without capacity leaks', {timeout: 180000}, async t => {
  const startedAt = Date.now(), f = await fixture(t), client = f.client, all = [], healthy = [], samples = [];
  const input = await client.request('input.create', {idempotencyKey: 'soak-sales', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  async function sample() {
    const state = await client.request('supervisor.get');
    assert.equal(state.maxWorkers, 4); assert.ok(Number.isInteger(state.activeWorkers) && state.activeWorkers >= 0 && state.activeWorkers <= 4);
    assert.ok(['busy', 'ready'].includes(state.status), 'a bad Task cannot put the service in intervention');
    samples.push(state.activeWorkers); assert.equal((await client.request('health.get')).status, 'ok');
    assert.equal((await client.request('ready.get')).ready, true);
  }
  async function approved(intent, key) {
    const body = {intent, context: {inputRefs: [input.id]}, limits: {timeoutMs: 60000, maxAttempts: 4, maxWorkers: 2}};
    const created = await client.createTask(body, key + '-create');
    const preview = await until(async () => {await sample(); const value = await client.getTask(created.id);
      assert.ok(!['failed', 'cancelled', 'intervention'].includes(value.status), 'planner must succeed'); return value.status === 'awaiting-approval' && value;}, key + ' preview');
    const plan = await client.request('task.plan', {path: {taskId: created.id}});
    assert.deepEqual(plan.nodes.map(node => node.id), ['east', 'west', 'verify']);
    const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
    const operation = await client.approveTask(created.id, approval, key + '-approve');
    const original = {key, body, created, approval, operation}; all.push(original); return original;
  }
  async function terminal(original, status) {
    return until(async () => {await sample(); const task = await client.getTask(original.created.id);
      if (['completed', 'failed', 'cancelled', 'intervention'].includes(task.status)) {assert.equal(task.status, status, original.key); return task;}
      return false;}, original.key + ' ' + status);
  }
  async function consume(original) {
    const task = await terminal(original, 'completed');
    const audit = await client.getAudit(task.id);
    assert.equal(audit.attempts, 4); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
    assert.equal(audit.acceptance.status, 'passed'); assert.equal(task.deadlineAt, original.created.deadlineAt);
    const downloaded = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
    const deliveries = downloaded.filter(value => value.artifact.kind === 'delivery'); assert.equal(deliveries.length, 1);
    const delivery = deliveries[0]; assert.equal(delivery.artifact.taskId, task.id); assert.equal(delivery.artifact.digest, digest(delivery.content));
    assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
    healthy.push({original, task, audit, delivery}); return task;
  }
  async function wave(number) {
    const tasks = await Promise.all(Array.from({length: 4}, (_, index) => approved('soak healthy ' + number + '-' + index, 'wave' + number + '-' + index)));
    await Promise.all(tasks.map(consume));
  }
  // 真正受管 ACP 进程写错误 west 候选；原独立 checker 拒收。
  // 这是明确内容故障注入，不声称自然模型错误，也不手改 Worker 目录。
  const bad = await approved('soak content failure', 'content-failure');
  await wave(1); const rejected = await terminal(bad, 'failed');
  const badAudit = await client.getAudit(bad.created.id);
  assert.equal(badAudit.acceptance.status, 'failed'); assert.equal(badAudit.attempts, 4); assert.match(badAudit.acceptance.digest, /^sha256:[a-f0-9]{64}$/);
  // 旧非 repair checker 的 false 只有原负面事实/cleanup，不合成不存在的报告 artifact。
  assert.deepEqual(badAudit.acceptance.evidenceIds, []);
  assert.deepEqual(rejected.artifactIds, []);
  const held = await approved('soak held authors', 'held');
  await until(async () => {
    await sample(); const workers = (await client.request('task.workers', {path: {taskId: held.created.id}})).items;
    return workers.filter(worker => worker.role === 'author' && worker.startedAt && worker.status === 'running').length === 2;
  }, 'both held authors actually started');
  await wave(2);
  assert.equal((await client.getTask(held.created.id)).status, 'running', 'healthy work must finish while the original stuck Task is still active');
  const cancelBody = {expectedRevision: (await client.getTask(held.created.id)).revision};
  const cancellation = await client.request('task.cancel', {path: {taskId: held.created.id}, body: cancelBody, idempotencyKey: 'held-cancel'});
  const cancelled = await terminal(held, 'cancelled');
  await until(async () => (await client.request('operation.get', {path: {operationId: cancellation.id}})).status === 'succeeded', 'original cancel operation');
  assert.deepEqual(cancelled.artifactIds, []); assert.equal((await client.getAudit(held.created.id)).attempts, 3);
  await wave(3);
  assert.equal(healthy.length, 12); assert.equal(all.length, 14);
  await until(async () => {await sample(); return (await client.request('supervisor.get')).activeWorkers === 0;}, 'final capacity zero');
  const starts = f.rows().filter(row => row.type === 'started');
  for (const original of all) {
    assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
    assert.deepEqual(await client.approveTask(original.created.id, original.approval, original.key + '-approve'), original.operation);
    const workers = (await client.request('task.workers', {path: {taskId: original.created.id}})).items;
    assert.equal(workers.length, original === held ? 3 : 4); assert.equal(new Set(workers.map(worker => worker.id)).size, workers.length);
    assert.ok(workers.every(worker => ['completed', 'failed', 'cancelled'].includes(worker.status)));
    assert.deepEqual(new Set(workers.map(worker => worker.id)), new Set(starts.filter(row => row.taskId === original.created.id).map(row => row.workerId)));
  }
  assert.deepEqual(await client.request('task.cancel', {path: {taskId: held.created.id}, body: cancelBody, idempotencyKey: 'held-cancel'}), cancellation);
  assert.deepEqual(await client.getTask(bad.created.id), rejected); assert.deepEqual(await client.getAudit(bad.created.id), badAudit);
  await sample(); assert.deepEqual(f.rows().filter(row => row.type === 'started'), starts);
  await f.close();
  // 完整原进程时间线补充轮询采样，不能只用最终 active=0 掩盖中途超发。
  const active = new Map(), unique = new Set(); let peak = 0;
  const rows = f.rows(), failedVerification = rows.filter(row => row.type === 'completion' && row.taskId === bad.created.id && row.role === 'verifier');
  assert.equal(failedVerification.length, 1); assert.equal(failedVerification[0].status, 'failed');
  assert.equal(failedVerification[0].reason, 'verification_assertion_failed');
  // 原 Service 的通知投影只暴露 code；绑定另由原 completion 与持久 Decision 验证。
  // 一次失败通知不能静默丢失，也不能包含全局 supervisor/service failure。
  assert.deepEqual(rows.filter(row => row.type === 'diagnostic'), [{type: 'diagnostic', code: 'worker_failed'}]);
  const heldCompletions = rows.filter(row => row.type === 'completion' && row.taskId === held.created.id && row.role === 'author');
  assert.equal(heldCompletions.length, 2);
  for (const row of heldCompletions) {assert.equal(row.status, 'cancelled'); assert.equal(row.reason, 'provider_stopped');}
  assert.equal(rows.some(row => row.taskId === held.created.id && row.role === 'verifier'), false);
  for (const row of rows) {
    if (row.type === 'started') {
      assert.ok(row.started?.executionId); assert.equal(unique.has(row.started.executionId), false); unique.add(row.started.executionId);
      assert.equal(active.has(row.workerId), false); active.set(row.workerId, row);
      assert.ok(active.size <= 4); peak = Math.max(peak, active.size);
      assert.ok([...active.values()].filter(value => value.taskId === row.taskId).length <= 2);
    } else if (row.type === 'completion') {
      const original = active.get(row.workerId); assert.ok(original, 'no completion without its original start');
      assert.equal(row.cleanup?.cleaned, true); assert.deepEqual(row.cleanup.started, original.started); active.delete(row.workerId);
    }
  }
  assert.equal(unique.size, 55); assert.equal(active.size, 0); assert.ok(peak >= 3, 'actual cross-Task overlap'); assert.ok(samples.includes(4));
  const durable = f.durable(), tasks = durable.filter(row => row.kind === 'task').map(row => row.value);
  assert.equal(tasks.length, 14); assert.equal(tasks.reduce((sum, row) => sum + row.attempts, 0), 55, 'no refund or duplicated admission');
  const budgets = durable.filter(row => row.kind === 'budget'); assert.equal(budgets.length, 1);
  assert.equal(budgets[0].id, 'service-capacity'); assert.deepEqual(budgets[0].value.active, []);
  const decisions = durable.filter(row => row.kind === 'attempt' && row.value.type === 'independent-verification').map(row => row.value);
  assert.equal(decisions.length, 13); assert.equal(decisions.filter(value => value.status === 'accepted').length, 12);
  const negative = decisions.filter(value => value.taskId === bad.created.id); assert.equal(negative.length, 1);
  assert.equal(negative[0].status, 'rejected'); assert.equal(negative[0].reasonCode, 'verification_assertion_failed');
  assert.equal(negative[0].workerId, failedVerification[0].workerId); assert.equal(negative[0].planDigest, bad.approval.planDigest);
  assert.equal(digest(encode(negative[0])), badAudit.acceptance.digest); assert.deepEqual(negative[0].artifacts, []);
  assert.equal(decisions.some(value => value.taskId === held.created.id), false, 'cancel is not an independent rejection');
  for (const original of all) {
    const row = tasks.find(value => value.task.id === original.created.id); assert.ok(row);
    assert.deepEqual(row.limits, original.body.limits); assert.equal(row.task.deadlineAt, original.created.deadlineAt);
    assert.equal(row.attempts, original === held ? 3 : 4);
  }
  t.diagnostic(JSON.stringify({successfulTasks: healthy.length, rejectedTasks: 1, cancelledTasks: 1, waves: 3,
    originalProcessStarts: unique.size, peakProcesses: peak, finalCapacity: 0, elapsedMs: Date.now() - startedAt, scope: 'bounded-no-model-regression-not-production-SLO'}));
  f.complete();
});
