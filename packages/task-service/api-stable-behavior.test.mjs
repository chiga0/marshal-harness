import test from 'node:test';
import assert from 'node:assert/strict';
import {performance} from 'node:perf_hooks';
import {fixture, until, expected} from './api-stable-behavior.fixture.mjs';

// Deliberately seconds, not a sub-millisecond microbenchmark: allow ordinary
// CI scheduling and SQLite fsync, yet assert successful FULL responses below
// 2s, independently of the 10s HTTP timeout and 45s original business deadline.
// This is a deterministic regression threshold, not a production latency SLO.
const RESPONSE_BOUND_MS = 2000;
async function measured(f, taskId, status, operation, call) {
  const before = f.transports.length, started = performance.now(), result = await call(), elapsedMs = performance.now() - started;
  const entry = f.transports.slice(before).find(value => value.url === '/v1/tasks/' + taskId + (operation === 'cancel' ? '/cancel' : ''));
  assert.ok(entry, 'actual HTTP response observed'); assert.equal(entry.status, status, '504 is not latency success');
  assert.ok(elapsedMs < RESPONSE_BOUND_MS, operation + ' completed response exceeded 2s: ' + elapsedMs.toFixed(1));
  return {result, elapsedMs};
}
async function cancelled(f, taskId, receipt) {
  await until(async () => (await f.client.getTask(taskId)).status === 'cancelled');
  await until(async () => (await f.client.request('operation.get', {path: {operationId: receipt.id}})).status === 'succeeded');
  const workers = (await f.client.request('task.workers', {path: {taskId}})).items;
  assert.ok(workers.every(worker => ['completed', 'cancelled'].includes(worker.status)));
  assert.ok(f.executions.filter(entry => entry.ticket.taskId === taskId).every(entry => entry.completion?.cleanup?.cleaned === true));
  assert.equal((await f.client.getTask(taskId)).artifactIds.length, 0);
}

test('successful GET and 202 cancel are bounded while two real authors and an original long verifier remain live', {timeout: 40000}, async t => {
  const f = await fixture(t), verifying = await f.approve('api verifier held', 'verify-held');
  await until(() => f.executions.some(entry => entry.ticket.taskId === verifying.created.id && entry.kind === 'verification' && entry.started));
  const working = await f.approve('api authors held', 'authors-held');
  await until(() => f.executions.filter(entry => entry.ticket.taskId === working.created.id && entry.ticket.role === 'author' && entry.started).length === 2);
  const active = f.executions.filter(entry => entry.started && !entry.completion);
  assert.equal(active.length, 3); assert.equal(new Set(active.map(entry => entry.started.executionId)).size, 3);
  assert.equal((await f.client.request('supervisor.get')).activeWorkers, 3);
  const samples = [];
  for (const original of [verifying, working]) {
    const taskId = original.created.id;
    for (let index = 0; index < 3; index++) {
      const sample = await measured(f, taskId, 200, 'GET', () => f.client.getTask(taskId));
      assert.equal(sample.result.status, 'running'); samples.push({operation: 'GET', elapsedMs: sample.elapsedMs});
    }
    assert.ok(f.executions.filter(entry => entry.ticket.taskId === taskId && entry.started && !entry.completion).length > 0);
  }
  // The verifier remains live during author cancellation; cancellation does
  // not wait for any final verification or for the HTTP timeout to expire.
  for (const original of [working, verifying]) {
    const taskId = original.created.id, current = await f.client.getTask(taskId), body = {expectedRevision: current.revision}, key = original.key + '-cancel';
    const sample = await measured(f, taskId, 202, 'cancel', () => f.client.request('task.cancel', {path: {taskId}, body, idempotencyKey: key}));
    samples.push({operation: 'cancel', elapsedMs: sample.elapsedMs}); assert.equal(sample.result.kind, 'task.cancel');
    await cancelled(f, taskId, sample.result);
    assert.deepEqual(await f.client.request('task.cancel', {path: {taskId}, body, idempotencyKey: key}), sample.result);
  }
  assert.equal((await f.client.request('supervisor.get')).activeWorkers, 0);
  assert.equal(f.executions.filter(entry => entry.kind === 'verification').length, 1, 'cancelled authors cannot spawn another verifier');
  t.diagnostic(JSON.stringify({responseBoundMs: RESPONSE_BOUND_MS, samples})); f.complete();
});

async function page(client, taskId, cursor, limit = 3) {
  return client.request('task.events', {path: {taskId}, query: {limit, ...(cursor ? {cursor} : {})}});
}
async function remaining(client, taskId, cursor = '') {
  const items = []; let pages = 0;
  for (;;) {
    assert.ok(pages++ < 100); const current = await page(client, taskId, cursor); items.push(...current.items);
    if (current.nextCursor === null) return items;
    assert.notEqual(current.nextCursor, cursor); cursor = current.nextCursor;
  }
}

test('event polling original cursor survives new HTTP connections and normal reopen without gaps, duplicate IDs or changed receipts', {timeout: 25000}, async t => {
  const f = await fixture(t), original = await f.approve('api complete event history', 'history'), taskId = original.created.id;
  const completed = await until(async () => {const task = await f.client.getTask(taskId); return task.status === 'completed' && task;});
  await until(async () => (await f.client.request('operation.get', {path: {operationId: original.receipt.id}})).status === 'succeeded');
  const delivery = (await Promise.all(completed.artifactIds.map(id => f.client.downloadArtifact(id)))).find(item => item.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  const all = await remaining(f.client, taskId); assert.ok(all.length > 6);
  const first = await page(f.client, taskId); assert.equal(first.items.length, 3); assert.ok(first.nextCursor);
  const secondClient = f.newClient(), second = await page(secondClient, taskId, first.nextCursor); assert.equal(second.items.length, 3); assert.ok(second.nextCursor);
  assert.ok(f.transports.every(entry => entry.connection === 'close'), 'each response forces a subsequent new TCP connection; no SSE is implied');
  const starts = f.executions.length; await f.reopen();
  const tail = await remaining(f.client, taskId, second.nextCursor), resumed = [...first.items, ...second.items, ...tail];
  assert.deepEqual(resumed, all);
  assert.equal(new Set(resumed.map(event => event.id)).size, resumed.length);
  assert.deepEqual(resumed.map(event => event.sequence), Array.from({length: resumed.length}, (_, index) => index + 1));
  assert.ok(resumed.every(event => event.taskId === taskId));
  assert.deepEqual(await f.client.getTask(taskId), completed); assert.equal(f.executions.length, starts);
  assert.deepEqual(await f.client.createTask(original.body, original.key + '-create'), original.created);
  assert.deepEqual(await f.client.approveTask(taskId, original.approval, original.key + '-approve'), original.receipt);
  assert.deepEqual((await f.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.deepEqual(await page(f.client, taskId, first.nextCursor), second, 'explicit repeated cursor is the same immutable page');
  await assert.rejects(page(f.client, taskId, 'not-a-sequence'), {code: 'invalid_request'});
  const empty = await page(f.client, taskId, String(all.at(-1).sequence)); assert.deepEqual(empty.items, []); assert.equal(empty.nextCursor, null);
  f.complete();
});
