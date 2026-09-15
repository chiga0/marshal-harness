import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {TaskClient} from './index.mjs';
import {contract} from '../task-api/contract.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';

const example = name => structuredClone(contract.components.schemas[name].examples[0]);
const token = 'runtime-client-fixture-local-token-000001';
const path = {taskId: 'task-example', questionId: 'question-example'};
const body = example('RuntimeAnswerQuestion');
// parseJson intentionally returns null-prototype data; compare cloned JSON data
// to ordinary fixture objects without weakening any field or value assertion.
const request = (client, value = body) => client.request('task.answer', {path, body: value, idempotencyKey: 'original-key'}).then(structuredClone);

test('runtime client rejects cross-family or cross-bound responses even without the trusted HTTP handler', async () => {
  for (const mutate of [value => {value.questionId = 'foreign';}, value => {value.questionDigest = 'sha256:' + 'f'.repeat(64);},
    value => {value.operation.taskId = 'foreign';}, value => {value.operation.kind = 'task.approve';},
    value => {value.currentTask.revision--;}, value => {value.task.revision++;}, value => {value.deliveryNonce = 'private';},
    value => {value.currentTask.plan.digest = 'sha256:' + 'f'.repeat(64);}, value => {value.acceptedRevision++;},
    value => {Object.keys(value).forEach(key => delete value[key]); Object.assign(value, example('AnswerReceipt'));}]) {
    let calls = 0; const value = example('RuntimeAnswerReceipt'); mutate(value);
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async () => {
      calls++; return new Response(JSON.stringify(value), {status: 202, headers: {'Content-Type': 'application/json'}});
    }});
    await assert.rejects(request(client), {code: 'client_invalid_response'}); assert.equal(calls, 1);
  }
});

test('runtime client rejects malformed/mixed answers locally and does not invent keys, revisions or messages', async () => {
  let calls = 0; const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async () => {calls++;}});
  const {questionDigest: _omitted, ...missing} = body;
  for (const input of [missing, {...body, previewDigest: body.questionDigest}, {...body, answer: {optionId: 'allow-once'}},
    {...body, answer: '中'.repeat(1366)}, {...body, answer: '\0'}, {...body, questionRevision: 2}, {...body, expectedRevision: 0}])
    await assert.rejects(request(client, input), {code: 'client_invalid_request'});
  await assert.rejects(client.request('task.answer', {path, body}), {code: 'client_invalid_idempotency_key'});
  assert.equal(calls, 0);
});

test('lost runtime answer HTTP response permits only explicit same-key replay and never consumes/resumes in the client', async t => {
  // DI transport fixture only: receipt creation here is not a Core ACK/Store test.
  let handler, commits = 0; const seen = [], original = example('RuntimeAnswerReceipt');
  const server = createServer((req, res) => handler(req, res));
  t.after(() => {server.closeAllConnections(); server.close();});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const baseURL = 'http://127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({token, expectedHost: new URL(baseURL).host, application: async command => {
    seen.push(structuredClone(command));
    assert.equal(command.operation, 'task.answer'); assert.deepEqual(structuredClone(command.body), body); assert.equal(command.key, 'original-key');
    if (commits === 0) {commits++; return original;}
    return {...original, replayed: true, currentTask: {...original.currentTask, revision: 4, status: 'running'}};
  }});
  let calls = 0;
  const lossy = new TaskClient({baseURL, token, fetch: async (...args) => {calls++; const response = await fetch(...args);
    await response.arrayBuffer(); throw Error('fixture lost response');}});
  await assert.rejects(request(lossy), {code: 'client_transport_error'});
  assert.equal(calls, 1); assert.equal(commits, 1); assert.equal(seen.length, 1);
  const replayed = await request(new TaskClient({baseURL, token}));
  assert.equal(replayed.replayed, true); assert.equal(replayed.currentTask.revision, 4);
  assert.deepEqual(replayed.task, original.task); assert.deepEqual(replayed.operation, original.operation);
  assert.equal(replayed.deliveryStatus, 'pending'); assert.equal(seen.length, 2); assert.equal(commits, 1);
  assert.deepEqual(seen[0], seen[1]);
});

test('runtime question list from an untrusted transport cannot bind a foreign subject or duplicate choices', async () => {
  for (const mutate of [question => {question.subject = 'sha256:' + 'f'.repeat(64);}, question => {question.taskId = 'foreign';},
    question => {question.options = [{value: 'x', label: 'X'}, {value: 'x', label: 'Y'}];}, question => {question.deliveryStatus = 'pending';}]) {
    const value = example('Questions'); value.items = [example('RunningQuestion')]; mutate(value.items[0]); let calls = 0;
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async () => {
      calls++; return new Response(JSON.stringify(value), {status: 200, headers: {'Content-Type': 'application/json'}});
    }});
    await assert.rejects(client.request('task.questions', {path: {taskId: path.taskId}}), {code: 'client_invalid_response'});
    assert.equal(calls, 1);
  }
});
