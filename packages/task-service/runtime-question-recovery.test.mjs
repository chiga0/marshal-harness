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
import {custodyDigest, verifyObservation} from '../agent-runtime/custody-contract.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./runtime-question-recovery.fixture.mjs', import.meta.url));
async function until(observe, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {const value = await observe(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded runtime question observation timed out'); await pause(10);}
}
function lines(file) {
  try {const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);}
  catch (error) {if (error.code === 'ENOENT') return []; throw error;}
}
const data = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const projections = (state, kind) => state.projections.filter(row => row.kind === kind).map(data);
const taskRecord = (state, taskId) => projections(state, 'task').find(record => record.task.id === taskId);
const workers = (state, taskId) => projections(state, 'attempt').filter(record => record.worker?.taskId === taskId);
const capacity = state => projections(state, 'budget').flatMap(record => record.active);
// Read-only snapshots ONLY after the test's original service processes exited.
// No owner claim, SQL repair, manufactured receipt or cleanup substitution.
function durable(f) {
  assert.ok(f.services.every(service => service.exited));
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {metadata: rows('SELECT * FROM metadata'), events: rows('SELECT * FROM events ORDER BY stream,sequence'),
      heads: rows('SELECT * FROM heads ORDER BY stream'), projections: rows('SELECT * FROM projections ORDER BY kind,id'),
      receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'), outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally {db.close();}
}
async function fixture(t, scenario) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-runtime-question-')));
  const f = {root: path.join(parent, 'data'), services: [], complete: false, observations: () => lines(path.join(parent, 'question-observations.jsonl'))};
  f.gone = async () => {
    for (const row of f.observations().filter(value => value.type === 'started' && value.started)) {
      for (const pid of [row.started.agentPid, row.started.guardPid]) {
        assert.ok(Number.isSafeInteger(pid) && pid > 1);
        await until(() => {try {process.kill(pid, 0); return false;} catch (error) {assert.equal(error.code, 'ESRCH'); return true;}});
      }
    }
  };
  t.after(async () => {
    if (!f.complete) t.diagnostic('Preserved runtime question evidence: ' + parent);
    for (const service of f.services) {await service.stop('SIGKILL'); service.checkOutput();}
    // Original ChildProcess handles alone may be signalled. PIDs above are only
    // absence observations; preserve private directories if cleanup is unknown.
    await f.gone(); if (f.complete) fs.rmSync(parent, {recursive: true, force: true});
  });
  f.launch = async mode => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {cwd: parent,
      env: {MARSHAL_QUESTION_FIXTURE: '1', MARSHAL_QUESTION_SCENARIO: scenario}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => {exited = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('close', (code, signal) => {exited = true; resolve({code, signal});});
    });
    child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL');});
    child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL');});
    const service = {child, get exited() {return exited;}, checkOutput() {if (token) assert.equal((stdout + stderr).includes(token), false);}, async stop(signal) {
      if (!exited) child.kill(signal);
      const watchdog = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 10000);
      try {await until(() => exited); return await done;} finally {clearTimeout(watchdog);}
    }};
    f.services.push(service);
    // Observe one open through its real 15-second preclaim custody bound; never
    // turn a failed/unknown startup into a blind reopen loop.
    await until(() => {assert.equal(exited, false, 'original CLI startup failed: ' + stderr); return stdout.includes('\n');}, 20000);
    const output = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(output.connectionFile));
    assert.equal(JSON.parse(fs.readFileSync(path.join(f.root, 'profile.json'))).layout, 3);
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token});
    assert.equal((await service.client.request('ready.get')).ready, true); return service;
  };
  return f;
}
// Defensive HTTP decoding uses null-prototype objects. Normalize only that
// representation for comparison with literal expectations, retaining all data.
const get = (client, operation, taskId) => client.request(operation, {path: {taskId}}).then(structuredClone);
async function approved(client, intent, key) {
  const body = {intent, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, key + '-create');
  const ready = await until(async () => {const value = await client.getTask(created.id); return value.status === 'awaiting-approval' && value;});
  const plan = await get(client, 'task.plan', created.id);
  assert.equal(plan.nodes.length, 3); assert.equal(plan.interaction.profile, 'task-runtime-question/v1');
  assert.equal(plan.interaction.maxQuestions, 1); assert.deepEqual(plan.budget, body.limits);
  const approval = {expectedRevision: ready.revision, planRevision: plan.revision, planDigest: plan.digest};
  const operation = await client.approveTask(created.id, approval, key + '-approve');
  const page = await until(async () => {const value = await get(client, 'task.questions', created.id); return value.items.length === 1 && value;});
  const question = page.items[0]; assert.equal(question.kind, 'business'); assert.equal(question.nodeId, 'east');
  assert.equal(question.subject, question.questionDigest); assert.equal(question.status, 'open'); assert.equal(question.deliveryStatus, null);
  assert.deepEqual(question.options, [{value: 'north', label: 'north'}, {value: 'south', label: 'south'}]);
  assert.ok(Date.parse(question.deadlineAt) <= Date.parse(created.deadlineAt));
  // A waiting original east author cannot block the independent west branch.
  const waitingWorkers = await until(async () => {
    const items = (await get(client, 'task.workers', created.id)).items;
    return items.some(worker => worker.nodeId === 'west' && worker.status === 'completed') && items;
  });
  const east = waitingWorkers.find(worker => worker.id === question.workerId);
  assert.equal(east.status, 'awaiting-answer'); assert.ok(east.startedAt);
  // Attempt is the Task-wide reservation ordinal (planner already consumed 1),
  // not a per-node retry counter. Capture it and require exact identity later.
  assert.ok([2, 3].includes(east.attempt));
  return {body, created, approval, operation, plan, question, key, east};
}
async function answer(client, original, value) {
  const body = {expectedRevision: (await client.getTask(original.created.id)).revision,
    questionRevision: original.question.revision, questionDigest: original.question.questionDigest, answer: value};
  const request = {path: {taskId: original.created.id, questionId: original.question.id}, body, idempotencyKey: original.key + '-answer'};
  const receipt = await client.request('task.answer', request);
  assert.equal(receipt.deliveryStatus, 'pending'); assert.equal(receipt.operation.status, 'accepted');
  assert.equal(receipt.acceptedRevision, body.expectedRevision + 1); return {request, receipt, value};
}
async function replay(client, original, accepted) {
  assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
  assert.deepEqual(await client.approveTask(original.created.id, original.approval, original.key + '-approve'), original.operation);
  const value = await client.request('task.answer', accepted.request);
  assert.deepEqual({...value, currentTask: null, replayed: false}, {...accepted.receipt, currentTask: null});
  assert.equal(value.replayed, true); assert.equal(value.deliveryStatus, 'pending');
  assert.deepEqual(value.currentTask, await client.getTask(original.created.id));
  await assert.rejects(client.request('task.answer', {...accepted.request, body: {...accepted.request.body, answer: accepted.value === 'north' ? 'south' : 'north'}}), {code: 'idempotency_conflict'});
}
async function complete(client, original, accepted, f) {
  const completed = await until(async () => {const task = await client.getTask(original.created.id); return task.status === 'completed' && task;});
  const question = (await get(client, 'task.questions', completed.id)).items[0];
  assert.equal(question.id, original.question.id); assert.equal(question.workerId, original.question.workerId);
  assert.equal(question.status, 'answered'); assert.equal(question.deliveryStatus, 'acknowledged'); assert.equal(question.answer, accepted.value);
  assert.equal((await client.request('operation.get', {path: {operationId: accepted.receipt.operation.id}})).status, 'succeeded');
  const audit = await get(client, 'task.audit', completed.id); assert.equal(audit.attempts, 4); assert.equal(audit.acceptance.status, 'passed');
  assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
  const downloads = await Promise.all(completed.artifactIds.map(id => client.downloadArtifact(id)));
  assert.equal(downloads.length, 2); const delivery = downloads.find(value => value.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content), {region: accepted.value, west: 'independent native candidate'});
  const evidence = JSON.parse(downloads.find(value => value.artifact.kind === 'evidence').content);
  const actual = evidence.assertions.find(item => item.name === 'answer-bound-files').actual;
  assert.equal(actual.matches, true); assert.equal(actual.region, accepted.value); assert.equal(actual.questionDigest, original.question.questionDigest);
  assert.equal(actual.workerId, original.question.workerId);
  const finalWorker = await client.request('worker.get', {path: {workerId: original.question.workerId}});
  assert.equal(finalWorker.attempt, original.east.attempt); assert.equal(finalWorker.startedAt, original.east.startedAt);
  const starts = f.observations().filter(row => row.type === 'started' && row.taskId === completed.id);
  assert.equal(starts.length, 4); assert.equal(starts.filter(row => row.workerId === original.question.workerId).length, 1);
  assert.equal(actual.executionId, starts.find(row => row.workerId === original.question.workerId).started.executionId);
  assert.equal(f.observations().filter(row => row.type === 'completion' && row.taskId === completed.id && row.cleanup?.cleaned).length, 4);
  await replay(client, original, accepted); return {completed, delivery, question};
}

test('runtime question: original HTTP answer/ACK continues the same native Worker and binds independent delivery through cold reopen', {timeout: 60000}, async t => {
  const f = await fixture(t, 'positive'), first = await f.launch('create');
  const original = await approved(first.client, 'question healthy team', 'healthy');
  const accepted = await answer(first.client, original, 'south'), result = await complete(first.client, original, accepted, f);
  assert.deepEqual(await first.stop('SIGTERM'), {code: 0, signal: null}); await f.gone();
  const before = durable(f); assert.equal(capacity(before).length, 0);
  const second = await f.launch('open'); await replay(second.client, original, accepted);
  assert.deepEqual(await second.client.getTask(original.created.id), result.completed);
  assert.deepEqual((await second.client.downloadArtifact(result.delivery.artifact.id)).content, result.delivery.content);
  assert.deepEqual((await get(second.client, 'task.questions', original.created.id)).items, [result.question]);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
  const reopened = durable(f);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(reopened[key], before[key], key);
  f.complete = true;
});

for (const scenario of ['cancel', 'dispatch-crash', 'ack-crash']) test(`runtime question ${scenario}: old delivery is never resent or re-executed; a new HTTP team still completes`, {timeout: 80000}, async t => {
  const f = await fixture(t, scenario), first = await f.launch('create');
  const old = await approved(first.client, 'question interrupted ' + scenario, 'old');
  const accepted = await answer(first.client, old, 'north');
  await until(async () => {const question = (await get(first.client, 'task.questions', old.created.id)).items[0];
    return question.deliveryStatus === (scenario === 'ack-crash' ? 'acknowledged' : 'dispatched');});
  if (scenario === 'ack-crash') await until(() => f.observations().some(row => row.type === 'ack-committed' && row.taskId === old.created.id));
  const oldStarts = f.observations().filter(row => row.type === 'started' && row.taskId === old.created.id); assert.equal(oldStarts.length, 3);
  let cancellation, cancelRequest;
  if (scenario === 'cancel') {
    cancelRequest = {path: {taskId: old.created.id}, body: {expectedRevision: (await first.client.getTask(old.created.id)).revision}, idempotencyKey: 'old-cancel'};
    cancellation = await first.client.request('task.cancel', cancelRequest);
    await until(async () => (await first.client.getTask(old.created.id)).status === 'cancelled');
    assert.deepEqual(await first.stop('SIGTERM'), {code: 0, signal: null});
  } else assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
  const before = durable(f), prior = taskRecord(before, old.created.id);
  assert.equal(prior.attempts, 3); assert.deepEqual(prior.limits, old.body.limits); assert.deepEqual(prior.task.artifactIds, []);
  const oldWorker = workers(before, old.created.id).find(record => record.worker.id === old.question.workerId);
  const second = await f.launch('open');
  const terminal = scenario === 'cancel' ? 'cancelled' : 'failed';
  const stopped = await until(async () => {const task = await second.client.getTask(old.created.id); return task.status === terminal && task;});
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0); await f.gone();
  assert.deepEqual(stopped.artifactIds, []); assert.equal(stopped.deadlineAt, old.created.deadlineAt);
  if (scenario !== 'cancel') assert.equal(stopped.code, 'service_interrupted');
  const closedQuestion = (await get(second.client, 'task.questions', old.created.id)).items[0];
  assert.equal(closedQuestion.status, 'answered'); assert.equal(closedQuestion.deliveryStatus, scenario === 'ack-crash' ? 'acknowledged' : 'unknown');
  assert.equal(closedQuestion.answer, 'north');
  const operation = await second.client.request('operation.get', {path: {operationId: accepted.receipt.operation.id}});
  assert.equal(operation.status, scenario === 'ack-crash' ? 'succeeded' : 'unknown');
  await replay(second.client, old, accepted);
  if (cancellation) {
    assert.deepEqual(await second.client.request('task.cancel', cancelRequest), cancellation);
    assert.equal((await second.client.request('operation.get', {path: {operationId: cancellation.id}})).status, 'succeeded');
  }
  // A readable old record is insufficient: the SAME reopened service admits and
  // fully verifies another actual planner/two-native-author Task with a new answer.
  const next = await approved(second.client, 'question next healthy team', 'next');
  const nextAnswer = await answer(second.client, next, 'south'), delivered = await complete(second.client, next, nextAnswer, f);
  assert.deepEqual(f.observations().filter(row => row.type === 'started' && row.taskId === old.created.id), oldStarts);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null}); await f.gone();
  const settled = durable(f), current = workers(settled, old.created.id).find(record => record.worker.id === oldWorker.worker.id);
  assert.equal(capacity(settled).length, 0); assert.equal(taskRecord(settled, old.created.id).attempts, prior.attempts);
  assert.equal(current.worker.status, terminal); assert.equal(current.cleanup.cleaned, true);
  assert.equal(current.worker.attempt, oldWorker.worker.attempt);
  assert.equal(current.executionId, oldWorker.executionId); assert.deepEqual(current.ticket, oldWorker.ticket);
  const events = settled.events.filter(event => event.stream === old.created.id).map(event => data(event).payload);
  assert.equal(events.filter(event => event.type === 'worker.answer-accepted').length, 1);
  assert.equal(events.filter(event => event.type === 'worker.answer-dispatched').length, 1);
  assert.equal(events.filter(event => event.type === 'worker.answer-acknowledged').length, scenario === 'ack-crash' ? 1 : 0);
  if (scenario !== 'cancel') {
    const descriptor = oldWorker.custody.descriptor;
    const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', descriptor.custodyId + '.observation.json')));
    assert.equal(verifyObservation(descriptor, observation), true);
    assert.equal(current.custody.settledDigest, custodyDigest(observation)); assert.deepEqual(current.cleanup, observation.payload.cleanup);
    assert.equal(events.filter(event => event.type === 'worker.cleanup-reconciled' && event.workerId === old.question.workerId).length, 1);
  }
  for (const row of before.receipts) assert.deepEqual(settled.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
  const starts = f.observations().filter(row => row.type === 'started'), third = await f.launch('open');
  await replay(third.client, old, accepted); await replay(third.client, next, nextAnswer);
  assert.deepEqual(await third.client.getTask(old.created.id), stopped);
  assert.deepEqual(await third.client.getTask(next.created.id), delivered.completed);
  assert.deepEqual((await third.client.downloadArtifact(delivered.delivery.artifact.id)).content, delivered.delivery.content);
  assert.equal((await third.client.request('supervisor.get')).activeWorkers, 0);
  assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null});
  const reopened = durable(f);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(reopened[key], settled[key], key);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.complete = true;
});
