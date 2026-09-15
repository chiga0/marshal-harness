import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer, request as httpRequest} from 'node:http';
import {once} from 'node:events';
import {createHash} from 'node:crypto';
import {contract, validate, validLeaderView, validLeaderReplyResponse, leaderRequestDigest, leaderReplyDigest, TaskApiError} from './contract.mjs';
import {createTaskApiHandler} from './http-handler.mjs';
import {TaskClient} from '../task-client/index.mjs';

// DI / transport proof only: no SQLite, real Leader, authorization or publication.
const token = 'leader-boundary-fixture-local-token-00001';
const taskId = 'task-example', requestId = 'request-example';
const example = (name, index = 0) => structuredClone(contract.components.schemas[name].examples[index]);
const reply = () => example('LeaderBusinessReply');
const jsonHash = value => {
  const canonical = v => Array.isArray(v) ? '[' + v.map(canonical).join(',') + ']' : v && typeof v === 'object' ?
    '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + canonical(v[k])).join(',') + '}' : JSON.stringify(v);
  return 'sha256:' + createHash('sha256').update(canonical(value)).digest('hex');
};
function rebind(view) {
  const q = view.pendingRequest;
  if (q.kind === 'publication' && q.authorization) q.subject = jsonHash(q.authorization);
  q.requestDigest = leaderRequestDigest(view.taskId, q); return view;
}
async function loopback(t, application, timeout = 2000) {
  let handler; const server = createServer((req, res) => handler(req, res));
  t.after(() => {server.closeAllConnections(); server.close();});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const baseURL = 'http://127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({application, token, expectedHost: new URL(baseURL).host, requestTimeoutMs: timeout});
  return {client: new TaskClient({baseURL, token}), raw: (body, overrides = {}) => {
    const {path = '/v1/tasks/' + taskId + '/leader/requests/' + requestId + '/reply', method = 'POST', headers = {}, raw} = overrides;
    // fetch owns Host; use the real HTTP client to put a foreign Host on wire.
    if (Object.hasOwn(headers, 'Host')) return new Promise((resolve, reject) => {
      const req = httpRequest(baseURL + path, {method, headers: {Authorization: 'Bearer ' + token,
        'Content-Type': 'application/json', 'Idempotency-Key': 'original-reply', ...headers}}, res => {
        const chunks = []; res.on('data', chunk => chunks.push(chunk)); res.on('error', reject);
        res.on('end', () => resolve({status: res.statusCode, json: async () => JSON.parse(Buffer.concat(chunks))}));
      });
      req.on('error', reject); req.end(raw ?? JSON.stringify(body));
    });
    return fetch(baseURL + path, {method, headers: {Authorization: 'Bearer ' + token,
      ...(method === 'POST' ? {'Content-Type': 'application/json', 'Idempotency-Key': 'original-reply'} : {}), ...headers},
      ...(body === undefined ? {} : {body: raw ?? JSON.stringify(body)})});
  }};
}

test('Leader schemas are closed, finite and separate from original Task/Worker/Operation/answer/repair', () => {
  for (const [name, schema] of Object.entries(contract.components.schemas))
    for (const value of schema.examples ?? []) assert.equal(validate(value, schema), true, name);
  for (const value of [reply(), example('LeaderPublicationReply')]) assert.equal(validate(value, 'LeaderReply'), true);
  for (const value of [{...reply(), decision: 'allow'}, {...reply(), answer: {value: 'north'}}, {...reply(), authorization: example('LeaderAuthorization')},
    {...reply(), requestId}, {...reply(), questionDigest: reply().requestDigest}, {...reply(), expectedRevision: 0},
    {...reply(), answer: ''}, {...reply(), answer: ' '}, {...reply(), answer: '\0'}, {...reply(), answer: '\ud800'},
    {...reply(), answer: '中'.repeat(1366)}, {...reply(), answer: 'a'.repeat(4097)},
    {...example('LeaderPublicationReply'), decision: 'proceed_once'}, {requestDigest: reply().requestDigest, answer: 'north'}])
    assert.equal(validate(value, 'LeaderReply'), false);
  for (const answer of ['a'.repeat(4096), '中'.repeat(1365) + 'a']) assert.equal(validate({...reply(), answer}, 'LeaderReply'), true);
  for (const [name, field, value] of [['Task', 'leader', {}], ['Worker', 'role', 'leader'], ['Operation', 'kind', 'task.leader.reply'],
    ['AnswerReceipt', 'requestDigest', reply().requestDigest], ['RepairReceipt', 'leaderDecision', reply().requestDigest]])
    assert.equal(validate({...example(name), [field]: value}, name), false, name);
  assert.equal(validate(example('AnswerReceipt'), 'AnswerResponse'), true);
  assert.equal(validate(example('RuntimeAnswerReceipt'), 'AnswerResponse'), true);
  assert.equal(validate(example('RepairReceipt'), 'RepairReceipt'), true);
  assert.equal(validate(example('LeaderReplyReceipt'), 'Operation'), false);
});

test('real HTTP routes Leader view and both explicit replies to the original DI port without extra requests', async t => {
  const calls = [];
  const {client} = await loopback(t, async (command, context) => {
    calls.push(structuredClone(command)); assert.equal(context.principal, 'local-operator');
    if (command.operation === 'task.leader') return example('LeaderView');
    assert.equal(command.operation, 'task.leader.reply'); assert.equal(command.key, 'original-reply');
    assert.notEqual(context.requestId, command.requestId); assert.ok(context.signal instanceof AbortSignal);
    return {...example('LeaderReplyReceipt'), requestId: command.requestId, requestDigest: command.body.requestDigest,
      replyDigest: leaderReplyDigest(taskId, command.requestId, command.body)};
  });
  assert.deepEqual(structuredClone(await client.getLeader(taskId)), example('LeaderView'));
  for (const [id, body] of [[requestId, reply()], ['request-publication', example('LeaderPublicationReply')]]) {
    const original = structuredClone(body);
    const value = await client.replyLeader(taskId, id, body, 'original-reply');
    assert.equal(value.requestId, id); assert.equal(value.acceptedRevision, 3); assert.deepEqual(body, original);
  }
  assert.equal(calls.length, 3); assert.deepEqual(calls[0], {operation: 'task.leader', taskId});
  assert.deepEqual(calls[1], {operation: 'task.leader.reply', taskId, requestId, key: 'original-reply', body: reply()});
});

test('Leader HTTP rejects malformed authorization/reply/route/auth before Application dispatch', async t => {
  let calls = 0; const {raw} = await loopback(t, async () => {calls++; return example('LeaderReplyReceipt');});
  for (const body of [{...reply(), decision: 'allow'}, {...reply(), authorization: example('LeaderAuthorization')},
    {...reply(), targetId: 'other'}, {...reply(), expectedRevision: '2'}, {...reply(), requestDigest: null},
    {...example('LeaderPublicationReply'), answer: 'yes'}, {...reply(), answer: '\ud800'}, {...reply(), answer: '中'.repeat(1366)}])
    assert.equal((await raw(body)).status, 400);
  assert.equal((await raw(reply(), {raw: '{"expectedRevision":2,"expectedRevision":2,"requestDigest":"x","answer":"x"}'})).status, 400);
  assert.equal((await raw(reply(), {headers: {Authorization: 'Bearer not-authorized'}})).status, 401);
  assert.equal((await raw(reply(), {headers: {Origin: 'http://127.0.0.1:1'}})).status, 403);
  assert.equal((await raw(reply(), {headers: {Host: 'evil.invalid'}})).status, 403);
  assert.equal((await raw(reply(), {headers: {'Idempotency-Key': ''}})).status, 400);
  assert.equal((await raw(undefined, {method: 'GET', path: '/v1/tasks/task-example/leader?cursor=one'})).status, 400);
  assert.equal((await raw(reply(), {path: '/v1/tasks/task-example/leader/requests/x%2Fy/reply'})).status, 404);
  assert.equal((await raw(reply(), {path: '/v1/tasks/task-example/leader/actions'})).status, 404);
  assert.equal((await raw(reply(), {path: '/v1/tasks/task-example/leader'})).status, 405);
  assert.equal(calls, 0);
});

test('Leader view binds authorization full bytes and original Task without claiming current authority', async t => {
  let value; const {client} = await loopback(t, async () => value);
  for (const index of [0, 1]) {value = example('LeaderView', index); assert.equal(validLeaderView(value, taskId), true); await client.getLeader(taskId);}
  const mutations = [v => {v.taskId = 'foreign';}, v => {v.pendingRequest.authorization.taskId = 'foreign'; rebind(v);},
    v => {v.pendingRequest.authorization.targetId = 'other';}, v => {v.pendingRequest.authorization.artifactDigest = 'sha256:' + 'f'.repeat(64);},
    v => {v.pendingRequest.authorization.operation = 'overwrite';}, v => {v.pendingRequest.authorization.url = 'http://127.0.0.1:2';},
    v => {v.pendingRequest.authorization.name = '../report.json';}, v => {v.pendingRequest.authorization = null; rebind(v);},
    v => {v.pendingRequest.subject = 'sha256:' + 'f'.repeat(64); v.pendingRequest.requestDigest = leaderRequestDigest(taskId, v.pendingRequest);},
    v => {v.pendingRequest.authorization.expiresAt = '2026-09-11T00:00:00.000Z'; rebind(v);},
    v => {v.pendingRequest.options = [{value: 'allow', label: 'A'}, {value: 'allow', label: 'B'}]; rebind(v);},
    v => {v.pendingRequest.nodeIds = ['foreign']; rebind(v);}, v => {v.pendingRequest.replyDigest = 'sha256:' + 'f'.repeat(64);},
    v => {v.pendingRequest.status = 'replied';}, v => {v.pendingRequest.prompt += ' changed';}, v => {v.stage = 'completed';}];
  for (const mutate of mutations) {
    value = example('LeaderView', 1); mutate(value); assert.equal(validLeaderView(value, taskId), false);
    await assert.rejects(client.getLeader(taskId), {code: 'invalid_application_response', status: 503});
  }
  value = example('LeaderView'); value.pendingRequest.nodeIds = ['same', 'same']; rebind(value);
  await assert.rejects(client.getLeader(taskId), {code: 'invalid_application_response'});
  for (const status of ['closed', 'replied']) {
    value = example('LeaderView'); value.pendingRequest.status = status;
    if (status === 'replied') value.pendingRequest.replyDigest = example('LeaderReplyReceipt').replyDigest;
    await client.getLeader(taskId); // Original reply is a fact, not an Agent consumption assertion.
  }
  value = {...example('LeaderView'), pendingRequest: null, stage: 'terminal'}; await client.getLeader(taskId);
});

test('Leader reply response binds route, body family, original digest and CAS without Task Operation substitution', async t => {
  let value; const {client} = await loopback(t, async () => value);
  const request = {taskId, requestId, body: reply()};
  for (const mutate of [v => {v.taskId = 'foreign';}, v => {v.requestId = 'foreign';}, v => {v.requestDigest = 'sha256:' + 'f'.repeat(64);},
    v => {v.acceptedRevision++;}, v => {v.replyDigest = leaderReplyDigest(taskId, requestId, {...reply(), answer: 'south'});},
    v => {v.replyDigest = leaderReplyDigest(taskId, requestId, {requestDigest: reply().requestDigest, decision: 'allow'});},
    v => {v.operation = example('Operation');}, v => {v.deliveryStatus = 'acknowledged';}]) {
    value = example('LeaderReplyReceipt'); mutate(value); assert.equal(validLeaderReplyResponse(request, value), false);
    await assert.rejects(client.replyLeader(taskId, requestId, reply(), 'original-reply'), {code: 'invalid_application_response', status: 503});
  }
  value = {...example('LeaderReplyReceipt'), replayed: true};
  assert.equal((await client.replyLeader(taskId, requestId, reply(), 'original-reply')).acceptedRevision, 3);
});

test('Leader 64 KiB serialized view limit is independent of valid per-field limits', async t => {
  let value = example('LeaderView');
  value.pendingRequest.prompt = 'p'.repeat(4096);
  value.pendingRequest.options = Array.from({length: 16}, (_, i) => ({value: String(i).padEnd(1024, 'v'), label: 'l'.repeat(2048)}));
  value.pendingRequest.nodeIds = Array.from({length: 64}, (_, i) => 'n' + String(i).padEnd(119, 'n'));
  value.review = {digest: value.policyDigest, verdict: 'accept', selectionDigest: value.policyDigest, policyDigest: value.policyDigest,
    workerId: 'reviewer', evidenceIds: Array.from({length: 64}, (_, i) => 'e' + String(i).padEnd(119, 'e'))};
  rebind(value); let excess = Buffer.byteLength(JSON.stringify(value)) - 65536; assert.ok(excess > 0);
  for (const option of value.pendingRequest.options) {const trim = Math.min(excess, option.label.length - 1); option.label = option.label.slice(trim); excess -= trim;}
  assert.equal(excess, 0); rebind(value); assert.equal(Buffer.byteLength(JSON.stringify(value)), 65536);
  assert.equal(validate(value, 'LeaderView'), true);
  const {client} = await loopback(t, async () => value); await client.getLeader(taskId);
  value.pendingRequest.options[0].label += 'x'; rebind(value); assert.equal(validate(value, 'LeaderView'), true);
  await assert.rejects(client.getLeader(taskId), {code: 'invalid_application_response', status: 503});
});

test('Leader DI forwards existing conflict/unsupported errors and HTTP timeout never becomes a cancel', async t => {
  let code = 'unsupported_operation', calls = 0;
  const {client} = await loopback(t, async command => {calls++; assert.match(command.operation, /^task\.leader/); throw new TaskApiError(code);});
  await assert.rejects(client.getLeader(taskId), {code, status: 501});
  for (const [next, status] of [['revision_conflict', 409], ['state_conflict', 409], ['idempotency_conflict', 409], ['forbidden', 403]]) {
    code = next; await assert.rejects(client.replyLeader(taskId, requestId, reply(), 'original-reply'), {code, status});
  }
  assert.equal(calls, 5);
  let observed;
  const timed = await loopback(t, async (command, context) => {observed = {command, context}; await once(context.signal, 'abort');}, 30);
  await assert.rejects(timed.client.getLeader(taskId), {code: 'request_timeout', status: 504});
  assert.equal(observed.command.operation, 'task.leader'); assert.equal(observed.context.signal.aborted, true);
});
