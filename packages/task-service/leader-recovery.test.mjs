import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {custodyDigest, verifyObservation} from '../agent-runtime/custody-contract.mjs';
const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./leader-recovery.fixture.mjs', import.meta.url));
async function until(read, label, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {const value = await read(); if (value) return value; assert.ok(Date.now() < end, 'bounded observation: ' + label); await pause(20);}
}
function lines(file) {
  try {const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);}
  catch (error) {if (error.code === 'ENOENT') return []; throw error;}
}
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const records = (state, kind) => state.projections.filter(row => row.kind === kind).map(decode);
const taskRecord = (state, id) => records(state, 'task').find(row => row.task.id === id);
const attempts = (state, id) => records(state, 'attempt').filter(row => row.worker?.taskId === id);
const capacity = state => records(state, 'budget').flatMap(row => row.active);
// Original service processes must all be closed. This reader never claims an
// owner or writes/repairs SQLite; it only captures the existing four ledgers.
function durable(f) {
  assert.ok(f.services.every(service => service.closed));
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.equal(db.prepare('PRAGMA foreign_key_check').all().length, 0);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {generation: db.prepare('SELECT generation FROM metadata').get().generation,
      events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally {db.close();}
}
async function fixture(t, publishing = false) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-recovery-')));
  const f = {root: path.join(parent, 'data'), services: [], passed: false, reportRoot: path.join(parent, 'reports')};
  let reader, readBaseURL;
  if (publishing) {
    fs.mkdirSync(f.reportRoot, {mode: 0o700});
    reader = http.createServer((request, response) => {
      if (!/^\/[A-Za-z0-9_-]+\.json$/.test(request.url)) {response.writeHead(404); response.end(); return;}
      try {const content = fs.readFileSync(path.join(f.reportRoot, request.url.slice(1))); response.writeHead(200, {'Content-Type': 'application/json'}); response.end(content);}
      catch {response.writeHead(404); response.end();}
    });
    await new Promise((resolve, reject) => {reader.once('error', reject); reader.listen(0, '127.0.0.1', resolve);});
    readBaseURL = `http://127.0.0.1:${reader.address().port}/`;
  }
  f.observations = () => lines(path.join(parent, 'leader-recovery-observations.jsonl'));
  f.gone = async () => {
    const pids = new Set(f.observations().filter(row => row.type === 'started' && row.started).flatMap(row => [row.started.guardPid, row.started.agentPid]));
    for (const pid of pids) {
      assert.ok(Number.isSafeInteger(pid) && pid > 1);
      // Signal 0 only observes identity liveness; never kill a persisted PID.
      await until(() => {try {process.kill(pid, 0); return false;} catch (error) {assert.equal(error.code, 'ESRCH'); return true;}}, 'original guard/agent exit');
    }
  };
  t.after(async () => {
    if (!f.passed) t.diagnostic('Preserved original Leader recovery evidence: ' + parent);
    for (const service of f.services) await service.stop('SIGKILL');
    if (reader) await new Promise(resolve => reader.close(resolve));
    await f.gone(); if (f.passed) fs.rmSync(parent, {recursive: true, force: true});
  });
  f.launch = async mode => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {
      cwd: parent, env: {MARSHAL_LEADER_RECOVERY_FIXTURE: '1', ...(publishing ?
        {MARSHAL_LEADER_RECOVERY_PUBLICATION: '1', MARSHAL_LEADER_RECOVERY_READ_URL: readBaseURL} : {})}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, closed = false, token, spawnCode = null;
    child.once('error', error => {spawnCode = error.code;}); child.once('exit', () => {exited = true;});
    const done = new Promise(resolve => child.once('close', (code, signal) => {closed = true; resolve({code, signal});}));
    child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384 && !exited) child.kill('SIGKILL');});
    child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384 && !exited) child.kill('SIGKILL');});
    const service = {get closed() {return closed;}, async stop(signal) {
      if (!exited && !closed) child.kill(signal);
      const timer = setTimeout(() => {if (!exited && !closed) child.kill('SIGKILL');}, 10000);
      try {await until(() => closed, 'original CLI close', 15000); const result = await done;
        if (token) assert.equal((stdout + stderr).includes(token), false);
        if (signal === 'SIGTERM') {assert.deepEqual(result, {code: 0, signal: null});
          const last = stdout.trim().split('\n').at(-1); assert.equal(JSON.parse(last).clean, true);}
        return result;
      } finally {clearTimeout(timer);}
    }};
    f.services.push(service);
    // One original invocation only: open's production preclaim custody wait is
    // 15s. This observation budget includes startup, not a retry/lease extension.
    await until(() => {assert.equal(closed, false, 'original CLI ' + mode + ' failed: ' + JSON.stringify({spawnCode, stderr}));
      return stdout.includes('\n');}, 'original CLI ' + mode, 20000);
    const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(first.connectionFile));
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token}); return service;
  };
  return f;
}
async function approve(client, intent, key, values = {east: 10, west: 20}) {
  const body = {intent, context: {text: JSON.stringify(values)}, requirements: {
    deliverables: ['east报告', 'west报告'], acceptance: ['保留east原业务值', '保留west原业务值']}};
  // Original ten-attempt budget fits first-pass delivery, but cannot fund an
  // interrupted fifth execution plus the entire remaining recovery path.
  if (intent === 'leader recovery interrupted') body.limits = {timeoutMs: 120000, maxAttempts: 10, maxWorkers: 3};
  const created = await client.createTask(body, key + '-create');
  let current = await until(async () => {const value = await client.getTask(created.id);
    assert.ok(!['failed', 'intervention'].includes(value.status), JSON.stringify(value)); return value.status === 'awaiting-answer' && value;}, 'Leader business request');
  const view = await client.request('task.leader', {path: {taskId: created.id}});
  const reply = {path: {taskId: created.id, requestId: view.pendingRequest.id}, idempotencyKey: key + '-reply',
    body: {expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest, answer: 'north'}};
  const receipt = await client.request('task.leader.reply', reply);
  current = await until(async () => {const value = await client.getTask(created.id);
    assert.ok(!['failed', 'intervention'].includes(value.status), JSON.stringify(value)); return value.status === 'awaiting-approval' && value;}, 'original plan');
  const plan = await client.request('task.plan', {path: {taskId: created.id}});
  const approval = {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest};
  const operation = await client.approveTask(created.id, approval, key + '-approve');
  return {created, body, key, reply, receipt, plan, approval, operation};
}
async function replay(client, original) {
  assert.deepEqual(encode(await client.createTask(original.body, original.key + '-create')), encode(original.created));
  assert.deepEqual(encode(await client.approveTask(original.created.id, original.approval, original.key + '-approve')), encode(original.operation));
  assert.deepEqual(encode(await client.request('task.leader.reply', original.reply)), encode({...original.receipt, replayed: true}));
}

test('v7 original in-flight Leader service death: original custody, same-root open, then another complete HTTP team', {timeout: 100000}, async t => {
  const f = await fixture(t), first = await f.launch('create'), old = await approve(first.client, 'leader recovery interrupted', 'old');
  const held = await until(() => f.observations().find(row => row.type === 'prompt-held' && row.taskId === old.created.id), 'original Leader ACP prompt');
  const publicWorkers = (await first.client.request('task.workers', {path: {taskId: old.created.id}})).items;
  assert.equal(publicWorkers.filter(row => row.role === 'author' && row.status === 'completed').length, 2);
  assert.equal(publicWorkers.find(row => row.id === held.workerId).status, 'running');
  const viewBefore = await first.client.request('task.leader', {path: {taskId: old.created.id}});
  assert.ok(viewBefore.lastDecision); assert.equal(viewBefore.review, null); assert.equal(viewBefore.publication, null);
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = durable(f), record = taskRecord(before, old.created.id), worker = attempts(before, old.created.id).find(row => row.worker.id === held.workerId);
  assert.equal(record.attempts, 5); assert.equal(capacity(before).length, 1); assert.equal(worker.ticket.executionType, 'leader');
  assert.equal(worker.worker.status, 'running'); assert.equal(worker.cleanup, null); assert.equal(worker.ticket.planDigest, old.plan.digest);
  assert.equal(record.task.deadlineAt, old.created.deadlineAt); assert.equal(record.leader.history.length, 2);
  assert.equal(before.outbox.find(row => row.id === worker.ticket.commandId).status, 'unknown');
  const descriptor = worker.custody.descriptor;
  // Await the real custodian's sealed output; no fixture generates/signs it.
  const observation = await until(() => {
    try {return JSON.parse(fs.readFileSync(path.join(f.root, 'custody', descriptor.custodyId + '.observation.json')));}
    catch (error) {if (error.code === 'ENOENT') return false; throw error;}
  }, 'original signed sealed observation', 20000);
  assert.equal(verifyObservation(descriptor, observation), true); assert.equal(observation.payload.cleanup.cleaned, true);
  await f.gone(); t.diagnostic('Original Leader signature/cleanup verified before the single same-root open; attempts=5, active=1, no Review/publication.');
  // A failure here remains a real recovery gap, not an accepted successful
  // outcome. Never reopen a replacement root or hand-settle the managed Worker.
  const second = await f.launch('open');
  const oldTask = await until(async () => {const value = await second.client.getTask(old.created.id);
    return ['failed', 'intervention'].includes(value.status) && value;}, 'honest interrupted Task outcome');
  assert.deepEqual(oldTask.artifactIds, []); assert.equal(oldTask.deadlineAt, old.created.deadlineAt);
  assert.equal((await second.client.request('ready.get')).ready, true);
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
  assert.equal((await second.client.request('task.audit', {path: {taskId: old.created.id}})).attempts, record.attempts);
  await replay(second.client, old);
  const next = await approve(second.client, 'leader recovery healthy successor Task', 'next', {east: 30, west: 50});
  const completed = await until(async () => {const value = await second.client.getTask(next.created.id);
    assert.ok(!['failed', 'intervention'].includes(value.status), JSON.stringify(value)); return value.status === 'completed' && value;}, 'new Task real complete', 45000);
  const finalView = await second.client.request('task.leader', {path: {taskId: next.created.id}});
  assert.equal(finalView.review.verdict, 'accept'); assert.ok(finalView.summaryArtifactId); assert.equal(finalView.publication, null);
  assert.equal((await second.client.request('task.audit', {path: {taskId: next.created.id}})).acceptance.status, 'passed');
  const downloads = await Promise.all(completed.artifactIds.map(id => second.client.downloadArtifact(id)));
  const delivery = downloads.find(item => item.artifact.kind === 'delivery'); assert.ok(delivery);
  assert.equal(digest(delivery.content), delivery.artifact.digest);
  assert.deepEqual(JSON.parse(delivery.content), [{nodeId: 'east', region: 'north', value: 30}, {nodeId: 'west', region: 'north', value: 50}]);
  await second.stop('SIGTERM'); await f.gone();
  const settled = durable(f), oldSettled = taskRecord(settled, old.created.id), closed = attempts(settled, old.created.id).find(row => row.worker.id === held.workerId);
  assert.equal(settled.generation, before.generation + 1); assert.equal(capacity(settled).length, 0);
  assert.equal(closed.custody.settledDigest, custodyDigest(observation)); assert.deepEqual(closed.cleanup, observation.payload.cleanup);
  assert.deepEqual(oldSettled.plan, record.plan); assert.deepEqual(oldSettled.limits, record.limits);
  assert.deepEqual(oldSettled.selectedResults, record.selectedResults); assert.deepEqual(oldSettled.leader.history, record.leader.history);
  assert.equal(oldSettled.attempts, record.attempts); assert.equal(attempts(settled, old.created.id).length, attempts(before, old.created.id).length);
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(item => item.scope === row.scope && item.operation === row.operation && item.key_digest === row.key_digest), row);
  for (const row of before.projections.filter(row => row.kind === 'attempt' && decode(row).worker?.status === 'completed'))
    assert.deepEqual(settled.projections.find(item => item.kind === row.kind && item.id === row.id), row);
  const starts = f.observations().filter(row => row.type === 'started'), third = await f.launch('open');
  assert.deepEqual(encode(await third.client.getTask(old.created.id)), encode(oldTask));
  assert.deepEqual(encode(await third.client.getTask(next.created.id)), encode(completed));
  await replay(third.client, old); await replay(third.client, next);
  assert.deepEqual(Buffer.from((await third.client.downloadArtifact(delivery.artifact.id)).content), Buffer.from(delivery.content));
  assert.equal((await third.client.request('ready.get')).ready, true); assert.equal((await third.client.request('supervisor.get')).activeWorkers, 0);
  await third.stop('SIGTERM'); const repeated = durable(f);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(repeated[key], settled[key], 'cold replay without additional facts: ' + key);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.passed = true;
});

test('v7 successor HTTP: original Leader dies, one current-generation call completes the SAME Task and cold replay', {timeout: 100000}, async t => {
  const f = await fixture(t), first = await f.launch('create'), old = await approve(first.client, 'leader recovery resumable', 'resume');
  const held = await until(() => f.observations().find(row => row.type === 'prompt-held' && row.taskId === old.created.id), 'original held Leader');
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = durable(f), record = taskRecord(before, old.created.id), oldWorker = attempts(before, old.created.id).find(row => row.worker.id === held.workerId);
  assert.equal(record.attempts, 5); assert.equal(capacity(before).length, 1);
  const second = await f.launch('open');
  const completed = await until(async () => {const value = await second.client.getTask(old.created.id);
    assert.ok(!['failed', 'cancelled', 'intervention'].includes(value.status), JSON.stringify(value)); return value.status === 'completed' && value;}, 'same Task successor delivery', 45000);
  await replay(second.client, old);
  const audit = await second.client.request('task.audit', {path: {taskId: old.created.id}});
  assert.equal(audit.attempts, 11); assert.equal(audit.acceptance.status, 'passed');
  const view = await second.client.request('task.leader', {path: {taskId: old.created.id}}); assert.equal(view.review.verdict, 'accept'); assert.ok(view.summaryArtifactId);
  const downloads = await Promise.all(completed.artifactIds.map(id => second.client.downloadArtifact(id))), delivery = downloads.find(item => item.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content), [{nodeId: 'east', region: 'north', value: 10}, {nodeId: 'west', region: 'north', value: 20}]);
  await second.stop('SIGTERM'); await f.gone(); const settled = durable(f), closed = attempts(settled, old.created.id).find(row => row.worker.id === held.workerId);
  assert.equal(closed.worker.status, 'failed'); assert.equal(closed.cleanup.cleaned, true); assert.equal(closed.recovery.status, 'resumed');
  const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', oldWorker.custody.descriptor.custodyId + '.observation.json')));
  assert.equal(verifyObservation(oldWorker.custody.descriptor, observation), true); assert.equal(closed.custody.settledDigest, custodyDigest(observation));
  assert.deepEqual(taskRecord(settled, old.created.id).selectedResults, record.selectedResults); assert.equal(capacity(settled).length, 0);
  assert.equal(f.observations().filter(row => row.type === 'started' && row.workerId === held.workerId).length, 1);
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(item => item.scope === row.scope && item.operation === row.operation && item.key_digest === row.key_digest), row);
  const starts = f.observations().filter(row => row.type === 'started'), third = await f.launch('open');
  assert.deepEqual(encode(await third.client.getTask(old.created.id)), encode(completed)); await replay(third.client, old);
  assert.deepEqual(Buffer.from((await third.client.downloadArtifact(delivery.artifact.id)).content), Buffer.from(delivery.content));
  await third.stop('SIGTERM'); const repeated = durable(f);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(repeated[key], settled[key]);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.passed = true;
});

test('v7 original CLI publication created before receipt COMMIT: crash, exact lookup, real postverify and cold download', {timeout: 100000}, async t => {
  const f = await fixture(t, true), first = await f.launch('create'), original = await approve(first.client, 'leader publication recovery', 'publish');
  const taskId = original.created.id;
  const confirmation = await until(async () => {const task = await first.client.getTask(taskId);
    assert.ok(!['failed', 'intervention'].includes(task.status), JSON.stringify(task)); return task.status === 'awaiting-confirmation' && task;}, 'precise publication consent', 45000);
  const view = await first.client.request('task.leader', {path: {taskId}}); assert.equal(view.pendingRequest.kind, 'publication');
  assert.deepEqual(fs.readdirSync(f.reportRoot), []);
  const permission = {path: {taskId, requestId: view.pendingRequest.id}, idempotencyKey: 'publication-allow',
    body: {expectedRevision: confirmation.revision, requestDigest: view.pendingRequest.requestDigest, decision: 'allow'}};
  const receipt = await first.client.request('task.leader.reply', permission);
  const held = await until(() => f.observations().find(value => value.type === 'publication-created-held' && value.taskId === taskId), 'actual publication result before SQL receipt');
  const target = path.join(f.reportRoot, held.binding.name), bytes = fs.readFileSync(target), identity = fs.statSync(target);
  assert.equal(digest(bytes), held.binding.artifactDigest); assert.equal(bytes.length, held.binding.bytes);
  assert.deepEqual(JSON.parse(bytes), [{nodeId: 'east', region: 'north', value: 10}, {nodeId: 'west', region: 'north', value: 20}]);
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = durable(f), taskBefore = taskRecord(before, taskId), worker = attempts(before, taskId).find(value => value.worker.id === held.workerId);
  assert.equal(taskBefore.attempts, 10); assert.equal(taskBefore.leader.publication.receiptArtifactId, null);
  assert.equal(taskBefore.leader.postverify, null); assert.equal(worker.worker.status, 'running'); assert.equal(worker.cleanup, null);
  const second = await f.launch('open');
  const complete = await until(async () => {const task = await second.client.getTask(taskId);
    assert.ok(!['failed', 'cancelled', 'intervention'].includes(task.status), JSON.stringify(task)); return task.status === 'completed' && task;}, 'lookup, independent HTTP postverify, original conclude', 45000);
  const finalView = await second.client.request('task.leader', {path: {taskId}});
  assert.equal(finalView.publication.status, 'succeeded'); assert.equal(finalView.postverify.status, 'succeeded'); assert.ok(finalView.summaryArtifactId);
  const audit = await second.client.request('task.audit', {path: {taskId}}); assert.equal(audit.attempts, 12); assert.equal(audit.acceptance.status, 'passed');
  await replay(second.client, original); assert.deepEqual(encode(await second.client.request('task.leader.reply', permission)), encode({...receipt, replayed: true}));
  const downloads = await Promise.all(complete.artifactIds.map(id => second.client.downloadArtifact(id))), delivery = downloads.find(value => value.artifact.kind === 'delivery');
  assert.deepEqual(Buffer.from(delivery.content), bytes); assert.equal(fs.statSync(target).ino, identity.ino); assert.deepEqual(fs.readFileSync(target), bytes);
  await second.stop('SIGTERM'); await f.gone(); const settled = durable(f), record = taskRecord(settled, taskId);
  assert.equal(record.leader.publication.status, 'matched'); assert.equal(capacity(settled).length, 0);
  const lookup = records(settled, 'artifact').map(value => value.artifact).find(value => value?.id === record.leader.publication.receiptArtifactId);
  assert.ok(lookup); const old = attempts(settled, taskId).find(value => value.worker.id === held.workerId);
  const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', worker.custody.descriptor.custodyId + '.observation.json')));
  assert.equal(verifyObservation(worker.custody.descriptor, observation), true); assert.equal(old.custody.settledDigest, custodyDigest(observation));
  assert.deepEqual(old.cleanup, observation.payload.cleanup); assert.equal(old.worker.status, 'failed'); assert.equal(old.recovery.status, 'resumed');
  assert.deepEqual(record.selectedResults, taskBefore.selectedResults);
  assert.equal(f.observations().filter(value => value.type === 'publication-start').length, 1);
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(item => item.scope === row.scope && item.operation === row.operation && item.key_digest === row.key_digest), row);
  const third = await f.launch('open'); assert.deepEqual(encode(await third.client.getTask(taskId)), encode(complete));
  assert.deepEqual(Buffer.from((await third.client.downloadArtifact(delivery.artifact.id)).content), bytes);
  const receiptBytes = await third.client.downloadArtifact(lookup.id), evidence = JSON.parse(receiptBytes.content);
  assert.equal(evidence.status, 'matched'); assert.equal(evidence.createdByThisExecution, false);
  await third.stop('SIGTERM'); const repeated = durable(f);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(repeated[key], settled[key]);
  assert.equal(f.observations().filter(value => value.type === 'publication-start').length, 1);
  assert.equal(fs.statSync(target).ino, identity.ino); f.passed = true;
});
