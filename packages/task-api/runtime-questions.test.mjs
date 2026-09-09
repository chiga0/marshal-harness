import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {contract, validate, validAnswerResponse, TaskApiError} from './contract.mjs';
import {createTaskApiHandler} from './http-handler.mjs';
import {TaskClient} from '../task-client/index.mjs';

// Contract-only DI fixtures, not question registration/dispatch/ACK authority.
const token = 'runtime-question-contract-fixture-token-00001';
const example = name => structuredClone(contract.components.schemas[name].examples[0]);
const route = '/v1/tasks/task-example/questions/question-example/answers';
const request = {taskId: 'task-example', questionId: 'question-example', body: example('RuntimeAnswerQuestion')};
const receipt = () => example('RuntimeAnswerReceipt');
const page = () => ({...example('Questions'), items: [example('RunningQuestion')]});
async function loopback(t, application) {
  let handler; const server = createServer((req, res) => handler(req, res));
  t.after(() => {server.closeAllConnections(); server.close();});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const baseURL = 'http://127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({application, token, expectedHost: new URL(baseURL).host});
  return {baseURL, client: new TaskClient({baseURL, token}), raw(body) {
    return fetch(baseURL + route, {method: 'POST', headers: {Authorization: 'Bearer ' + token,
      'Content-Type': 'application/json', 'Idempotency-Key': 'original-answer'}, body: JSON.stringify(body)});
  }};
}
const answer = (client, body = request.body, key = 'original-answer') => client.request('task.answer', {
  path: {taskId: request.taskId, questionId: request.questionId}, body, idempotencyKey: key}).then(structuredClone);

test('runtime question schemas use exclusive closed branches while original examples remain valid', () => {
  for (const [name, schema] of Object.entries(contract.components.schemas))
    for (const value of schema.examples ?? []) assert.equal(validate(value, schema), true, name);
  assert.equal(validate('both', {oneOf: [{type: 'string'}, {type: 'string'}]}), false);
  assert.equal(validate(1, {oneOf: [{type: 'string'}, {type: 'boolean'}]}), false);
  assert.equal(validate('one', {oneOf: [{type: 'string'}, {type: 'boolean'}]}), true);
  const old = example('PreapprovalAnswerQuestion'), current = example('RuntimeAnswerQuestion');
  for (const value of [old, current]) assert.equal(validate(value, 'AnswerQuestion'), true);
  for (const value of [{...old, questionDigest: current.questionDigest}, {...current, previewDigest: old.previewDigest},
    {expectedRevision: 2, questionRevision: 1, answer: 'x'}, {...current, answer: {optionId: 'allow-once'}},
    {...current, questionRevision: 2}, {...current, expectedRevision: 0}, {...current, questionRevision: '1'},
    {...current, answer: '\0'}, {...current, answer: '\ud800'}, {...current, answer: '中'.repeat(1366)},
    {...current, answer: 'a'.repeat(4097)}, {...current, answer: ''}, {...current, answer: '   '}])
    assert.equal(validate(value, 'AnswerQuestion'), false);
  for (const value of ['a'.repeat(4096), '中'.repeat(1365) + 'a']) assert.equal(validate({...current, answer: value}, 'AnswerQuestion'), true);
  assert.equal(validate(example('AnswerReceipt'), 'AnswerResponse'), true);
  assert.deepEqual(example('AnswerResponse'), example('AnswerReceipt'));
  assert.equal(validate(receipt(), 'AnswerResponse'), true);
  assert.equal(validate({...receipt(), acceptedPreviewDigest: old.previewDigest}, 'AnswerResponse'), false);
  assert.equal(validate(example('Question'), 'QuestionItem'), true);
  assert.equal(validate(example('RunningQuestion'), 'QuestionItem'), true);
  const policy = example('PlanInteraction'), plan = example('Plan');
  assert.equal(validate(plan, 'Plan'), true); assert.equal(Object.hasOwn(plan, 'interaction'), false);
  assert.equal(validate({...plan, interaction: policy}, 'Plan'), true);
  for (const interaction of [null, {...policy, profile: 'other'}, {...policy, maxQuestions: 4}, {...policy, maxQuestions: 0},
    {...policy, maxQuestions: '1'}, {...policy, maxWaitMs: 120001}, {...policy, maxWaitMs: 0}, {...policy, executable: '/bin/sh'}])
    assert.equal(validate({...plan, interaction}, 'Plan'), false);
});

test('real HTTP preserves old answer 202 and new answer 202 with exact original request and separate current projection', async t => {
  const seen = [];
  const {client} = await loopback(t, async command => {
    seen.push(structuredClone(command));
    if (command.operation === 'task.questions') return page();
    if (Object.hasOwn(command.body, 'previewDigest')) return example('AnswerReceipt');
    const value = receipt();
    if (seen.filter(item => item.operation === 'task.answer').length > 2) {
      value.replayed = true; value.currentTask.revision += 2; value.currentTask.status = 'running';
    }
    return value;
  });
  assert.deepEqual(structuredClone((await client.request('task.questions', {path: {taskId: request.taskId}})).items), page().items);
  assert.deepEqual(await answer(client, example('PreapprovalAnswerQuestion')), example('AnswerReceipt'));
  const first = await answer(client); assert.deepEqual(first, receipt());
  const replay = await answer(client);
  assert.equal(replay.replayed, true); assert.equal(replay.currentTask.revision, first.acceptedRevision + 2);
  assert.deepEqual(replay.task, first.task); assert.deepEqual(replay.operation, first.operation);
  assert.equal(replay.deliveryStatus, first.deliveryStatus);
  for (const command of seen.filter(item => item.operation === 'task.answer')) {
    assert.equal(command.taskId, request.taskId); assert.equal(command.questionId, request.questionId);
    assert.equal(command.key, 'original-answer'); assert.equal(command.operation, 'task.answer');
  }
  assert.deepEqual(seen.slice(-2).map(item => item.body), [request.body, request.body]);
});

test('real HTTP rejects mixed answer authority and malformed runtime bodies before dispatch', async t => {
  let calls = 0; const {raw} = await loopback(t, async () => {calls++; return receipt();});
  const old = example('PreapprovalAnswerQuestion');
  for (const body of [{...request.body, previewDigest: old.previewDigest}, {...old, questionDigest: request.body.questionDigest},
    {...request.body, questionDigest: null}, {...request.body, answer: {outcome: 'selected', optionId: 'allow-once'}},
    {...request.body, expectedRevision: 0}, {...request.body, answer: '中'.repeat(1366)}, {...request.body, answer: '\0'},
    {...request.body, questionRevision: 2}, {...request.body, requestId: 'protocol-secret'}]) {
    const response = await raw(body); assert.equal(response.status, 400); assert.equal((await response.json()).code, 'invalid_request');
  }
  assert.equal(calls, 0);
});

test('runtime receipt binds request family, route, original question digest, revision and Operation without hidden protocol fields', async t => {
  let current = receipt(); const {client} = await loopback(t, async () => current);
  const mutations = [value => {value.questionId = 'foreign';}, value => {value.taskId = 'foreign';},
    value => {value.questionDigest = 'sha256:' + 'f'.repeat(64);}, value => {value.operation.kind = 'task.cancel';},
    value => {value.operation.taskId = 'foreign';}, value => {value.task.id = 'foreign';}, value => {value.currentTask.id = 'foreign';},
    value => {value.acceptedRevision++;}, value => {value.operation.taskRevision++;}, value => {value.task.revision++;},
    value => {value.currentTask.revision--;}, value => {value.currentTask.plan.digest = 'sha256:' + 'f'.repeat(64);},
    value => {value.deliveryStatus = null;}, value => {value.deliveryNonce = 'secret';}, value => {value.preview = example('AnswerReceipt').preview;}];
  for (const mutate of mutations) {
    current = receipt(); mutate(current);
    assert.equal(validAnswerResponse(request, current), false);
    await assert.rejects(answer(client), {code: 'invalid_application_response', status: 503});
  }
  current = example('AnswerReceipt'); await assert.rejects(answer(client), {code: 'invalid_application_response', status: 503});
  current = receipt(); await assert.rejects(answer(client, example('PreapprovalAnswerQuestion')), {code: 'invalid_application_response', status: 503});
});

test('running question pages reject foreign bindings, leaked protocol fields and invalid finite input/select projections', async t => {
  let value = page(); const {client} = await loopback(t, async () => value);
  const get = () => client.request('task.questions', {path: {taskId: request.taskId}}).then(structuredClone);
  assert.deepEqual(await get(), value);
  const mutations = [q => {q.workerId = null;}, q => {q.nodeId = null;}, q => {delete q.questionDigest;},
    q => {q.taskId = 'foreign';}, q => {q.subject = 'sha256:' + 'f'.repeat(64);}, q => {q.revision = 2;},
    q => {q.options = Array.from({length: 17}, (_, n) => ({value: String(n), label: String(n)}));},
    q => {q.options = [{value: 'x', label: 'X'}, {value: 'x', label: 'alias'}];},
    q => {q.prompt = '中'.repeat(683);}, q => {q.prompt = '\0';}, q => {q.questionNonce = 'secret';},
    q => {q.kind = 'permission';}, q => {q.deliveryStatus = 'pending';}, q => {q.status = 'answered';}, q => {q.answer = 'not-accepted';},
    q => {q.status = 'answered'; q.deliveryStatus = 'pending';},
    q => {q.options = [{value: 'x', label: 'X'}]; q.answer = 'other';}];
  for (const mutate of mutations) {
    value = page(); mutate(value.items[0]);
    await assert.rejects(get(), {code: 'invalid_application_response', status: 503});
  }
  for (const status of ['cancelled', 'expired']) {
    value = page(); value.items[0].status = status; assert.deepEqual(await get(), value);
  }
  for (const deliveryStatus of ['pending', 'dispatched', 'acknowledged', 'cancelled', 'expired', 'unknown']) {
    value = page(); Object.assign(value.items[0], {status: 'answered', deliveryStatus, answer: 'x', options: [{value: 'x', label: 'X'}]});
    assert.deepEqual(await get(), value);
  }
});

test('runtime answer errors remain bounded 409/410/501 facts, never implicit resume/approve or retry', async t => {
  let calls = 0, code = 'revision_conflict';
  const {client} = await loopback(t, async command => {calls++; assert.equal(command.operation, 'task.answer'); throw new TaskApiError(code);});
  for (const [next, status] of [['revision_conflict', 409], ['idempotency_conflict', 409], ['state_conflict', 409],
    ['question_expired', 410], ['unsupported_operation', 501]]) {
    code = next; await assert.rejects(answer(client), {code, status});
  }
  assert.equal(calls, 5);
});
