import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {TaskClient} from '../task-client/index.mjs';
import {data, expected, durable} from './backup-restore.fixture.mjs';
import {encode} from '../task-store/store.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
async function until(fn, label, ms = 30000) {
  const end = Date.now() + ms;
  for (;;) {const result = await fn(); if (result) return result;
    assert.ok(Date.now() < end, label); await pause(20);}
}
test('real ACP update/output/frame floods fail boundedly without leaking raw content or blocking subsequent HTTP team delivery', {timeout: 90000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-event-flood-'))), root = path.join(parent, 'data');
  const child = spawn(process.execPath, [here('./main.mjs'), '--root', root, '--mode', 'create', '--config', here('./event-flood.fixture.mjs')],
    {cwd: parent, env: {MARSHAL_EVENT_FLOOD_FIXTURE: '1', MARSHAL_SOAK_FIXTURE: '1'}, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '', closed = false, overflow = false, complete = false, token;
  const done = new Promise(resolve => {child.once('error', () => {closed = true; resolve({code: null, signal: 'spawn-error'});});
    child.once('close', (code, signal) => {closed = true; resolve({code, signal});});});
  const receive = (name, bytes) => {if (name === 'stdout') stdout += bytes; else stderr += bytes;
    if (Buffer.byteLength(stdout + stderr) > 16384) {overflow = true; if (!closed) child.kill('SIGKILL');}};
  child.stdout.on('data', bytes => receive('stdout', bytes)); child.stderr.on('data', bytes => receive('stderr', bytes));
  async function stop() {
    if (!closed) child.kill('SIGTERM');
    const timer = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 12000);
    try {await until(() => closed, 'original CLI shutdown', 15000); return await done;} finally {clearTimeout(timer);}
  }
  t.after(async () => {
    await stop(); assert.equal(overflow, false); if (token) assert.equal((stdout + stderr).includes(token), false);
    assert.equal((stdout + stderr).includes('private-flood-canary'), false);
    if (complete) fs.rmSync(parent, {recursive: true, force: true}); else t.diagnostic('Preserved event flood evidence: ' + parent);
  });
  await until(() => {assert.equal(closed, false, 'CLI startup'); return stdout.includes('\n');}, 'ready', 15000);
  const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(first.connectionFile));
  token = connection.token; const client = new TaskClient({baseURL: connection.url, token});
  const input = await client.request('input.create', {idempotencyKey: 'flood-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const latency = [], originals = [];
  async function sample(taskId) {
    const start = performance.now();
    const [ready, state, task] = await Promise.all([client.request('ready.get'), client.request('supervisor.get'), client.getTask(taskId)]);
    latency.push(performance.now() - start); assert.ok(latency.at(-1) < 3000, 'bounded public observation');
    assert.equal(ready.ready, true); assert.ok(state.activeWorkers >= 0 && state.activeWorkers <= 4); return task;
  }
  async function run(intent, key, final) {
    const body = {intent, context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
    const created = await client.createTask(body, key + '-create');
    const preview = await until(async () => {const value = await sample(created.id); assert.ok(!['failed', 'intervention'].includes(value.status));
      return value.status === 'awaiting-approval' && value;}, 'original planner');
    const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
    const receipt = await client.approveTask(created.id, approval, key + '-approve');
    const terminal = await until(async () => {const value = await sample(created.id);
      if (['completed', 'failed', 'cancelled', 'intervention'].includes(value.status)) {assert.equal(value.status, final); return value;} return false;}, 'original terminal');
    const audit = await client.getAudit(created.id), workers = (await client.request('task.workers', {path: {taskId: created.id}})).items;
    assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0); assert.equal(terminal.deadlineAt, created.deadlineAt);
    assert.ok(workers.every(worker => ['completed', 'failed', 'cancelled'].includes(worker.status)));
    const original = {created, body, approval, receipt, terminal, workers, audit, key}; originals.push(original); return original;
  }
  for (const mode of ['updates', 'output', 'frame']) {
    const bad = await run('flood ' + mode, mode, 'failed');
    assert.deepEqual(bad.terminal.artifactIds, []); assert.equal(bad.workers.some(worker => worker.role === 'verifier'), false);
    assert.ok(bad.audit.attempts <= 3); assert.deepEqual({...bad.audit.acceptance}, {status: 'pending', evidenceIds: [], digest: null});
    const response = await client.request('task.events', {path: {taskId: bad.created.id}, query: {limit: 100}});
    assert.equal(JSON.stringify(response).includes('private-flood-canary'), false);
    assert.ok(response.items.length < 100, 'discarded flood must not amplify authority ledger');
  }
  const healthy = await run('healthy after three protocol failures', 'healthy', 'completed');
  assert.equal(healthy.audit.attempts, 4); assert.equal(healthy.audit.acceptance.status, 'passed');
  const downloads = await Promise.all(healthy.terminal.artifactIds.map(id => client.downloadArtifact(id)));
  const delivery = downloads.find(value => value.artifact.kind === 'delivery'); assert.ok(delivery);
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  await until(async () => (await client.request('supervisor.get')).activeWorkers === 0, 'no capacity leak');
  for (const original of originals) {
    assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
    assert.deepEqual(await client.approveTask(original.created.id, original.approval, original.key + '-approve'), original.receipt);
    assert.deepEqual(await client.getTask(original.created.id), original.terminal);
  }
  assert.deepEqual(await stop(), {code: 0, signal: null});
  assert.deepEqual(JSON.parse(stdout.trim().split('\n').at(-1)), {state: 'closed', clean: true, code: null});
  const observations = fs.readFileSync(path.join(parent, 'flood-observations.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
  for (const mode of ['updates', 'output', 'frame']) {
    const start = observations.find(row => row.mode === mode && row.type === 'started');
    const end = observations.find(row => row.mode === mode && row.type === 'completion');
    assert.ok(start?.started?.executionId); assert.equal(end.status, 'failed');
    assert.equal(end.reason, {updates: 'provider_progress_limit', output: 'provider_output_limit', frame: 'acp_frame_limit'}[mode]);
    assert.equal(end.cleanup.cleaned, true); assert.deepEqual(end.cleanup.started, start.started);
    assert.equal(end.outputBytes, 0); assert.equal(end.usage.source, 'unavailable');
  }
  const snapshot = durable(root, () => assert.equal(closed, true));
  assert.equal(snapshot.projections.filter(row => row.kind === 'task').length, 4);
  t.diagnostic(JSON.stringify({badTasks: 3, successfulTasks: 1, maxObservationMs: Math.ceil(Math.max(...latency)), samples: latency.length,
    scope: 'bounded real protocol flood and subsequent delivery; no concurrent-good-task survival, model, production SLO or disk-full proof'}));
  complete = true;
});
