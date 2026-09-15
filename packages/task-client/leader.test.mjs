import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {TaskClient} from './index.mjs';
import {contract, leaderReplyDigest} from '../task-api/contract.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';

const token = 'leader-client-fixture-local-token-0000001';
const taskId = 'task-example', requestId = 'request-example';
const example = (name, index = 0) => structuredClone(contract.components.schemas[name].examples[index]);
const body = () => example('LeaderBusinessReply');
const response = (value, status = 200) => new Response(JSON.stringify(value), {status, headers: {'Content-Type': 'application/json'}});
const peer = fetch => new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch});

test('Leader client independently rejects wrong route, reply body, authorization and trace-shaped substitutes', async () => {
  for (const mutate of [v => {v.taskId = 'foreign';}, v => {v.requestId = 'trace-request-id';},
    v => {v.replyDigest = leaderReplyDigest(taskId, requestId, {...body(), answer: 'south'});},
    v => {v.requestDigest = 'sha256:' + 'f'.repeat(64);}, v => {v.acceptedRevision++;}, v => {v.operation = example('Operation');}]) {
    let calls = 0; const value = example('LeaderReplyReceipt'); mutate(value);
    const client = peer(async () => {calls++; return response(value, 202);});
    await assert.rejects(client.replyLeader(taskId, requestId, body(), 'original-reply'), {code: 'client_invalid_response'});
    assert.equal(calls, 1);
  }
  for (const mutate of [v => {v.pendingRequest.authorization.targetId = 'foreign';}, v => {v.pendingRequest.authorization.name = 'other.json';},
    v => {v.pendingRequest.authorization.artifactDigest = 'sha256:' + 'f'.repeat(64);}, v => {v.pendingRequest.authorization.expiresAt = '2030-01-01T00:00:00Z';},
    v => {v.pendingRequest.authorization = null;}, v => {v.pendingRequest.prompt += token;}]) {
    let calls = 0; const value = example('LeaderView', 1); mutate(value);
    const client = peer(async () => {calls++; return response(value);});
    await assert.rejects(client.getLeader(taskId), {code: 'client_invalid_response'}); assert.equal(calls, 1);
  }
});

test('Leader client never invents body/key/CAS, target or publication consent', async () => {
  let calls = 0; const client = peer(async () => {calls++;});
  for (const value of [{...body(), decision: 'allow'}, {...body(), authorization: example('LeaderAuthorization')},
    {...body(), answer: {value: 'north'}}, {...body(), expectedRevision: 0}, {...body(), answer: '\0'},
    {...body(), answer: '中'.repeat(1366)}, {...body(), requestDigest: null}, {...example('LeaderPublicationReply'), targetId: 'other'}])
    await assert.rejects(client.replyLeader(taskId, requestId, value, 'original-reply'), {code: 'client_invalid_request'});
  await assert.rejects(client.replyLeader(taskId, requestId, body()), {code: 'client_invalid_idempotency_key'});
  await assert.rejects(client.replyLeader(taskId, '../other', body(), 'original-reply'), {code: 'client_invalid_request'});
  await assert.rejects(client.getLeader(taskId, {query: {cursor: 'one'}}), {code: 'client_invalid_request'});
  assert.equal(calls, 0);
});

test('Leader view client enforces 64 KiB before parsing even with absent Content-Length', async () => {
  for (const advertised of [true, false]) {
    let sent = 0, cancelled = 0;
    const client = peer(async () => new Response(new ReadableStream({pull(controller) {
      sent++; controller.enqueue(new Uint8Array(32769));
    }, cancel() {cancelled++;}}), {headers: {'Content-Type': 'application/json', ...(advertised ? {'Content-Length': '65537'} : {})}}));
    await assert.rejects(client.getLeader(taskId), {code: 'client_response_limit'});
    assert.ok(sent <= 3); assert.equal(cancelled, 1);
  }
});

test('Leader response loss allows caller-owned exact replay only; this DI test does not prove durable Core effects', async t => {
  let handler, first = true, accepted = 0; const commands = [], original = example('LeaderReplyReceipt');
  const server = createServer((req, res) => handler(req, res));
  t.after(() => {server.closeAllConnections(); server.close();});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const baseURL = 'http://127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({token, expectedHost: new URL(baseURL).host, application: async command => {
    commands.push(structuredClone(command));
    assert.equal(command.operation, 'task.leader.reply'); assert.equal(command.key, 'original-reply');
    assert.deepEqual(structuredClone(command.body), body()); assert.equal(command.requestId, requestId);
    if (first) {first = false; accepted++; return original;}
    return {...original, replayed: true};
  }});
  let fetches = 0; const lossy = new TaskClient({baseURL, token, fetch: async (...args) => {
    fetches++; const value = await fetch(...args); await value.arrayBuffer(); throw Error('fixture response loss');
  }});
  await assert.rejects(lossy.replyLeader(taskId, requestId, body(), 'original-reply'), {code: 'client_transport_error'});
  assert.equal(fetches, 1); assert.equal(accepted, 1); assert.equal(commands.length, 1);
  const replay = await new TaskClient({baseURL, token}).replyLeader(taskId, requestId, body(), 'original-reply');
  assert.deepEqual(structuredClone(replay), {...original, replayed: true});
  assert.equal(accepted, 1); assert.deepEqual(commands[0], commands[1]);
});

test('Leader client preserves HTTP unsupported and timeout facts without follow-up controls', async () => {
  let calls = 0; const error = {code: 'unsupported_operation', message: 'unsupported', requestId: 'trace-one', allowedActions: ['query']};
  const client = peer(async () => {calls++; return response(error, 501);});
  await assert.rejects(client.getLeader(taskId), {code: 'unsupported_operation', status: 501}); assert.equal(calls, 1);
  let signal; const slow = peer(async (_url, options) => {calls++; signal = options.signal; await once(signal, 'abort'); throw Error('stopped observing');});
  await assert.rejects(slow.getLeader(taskId, {timeoutMs: 20}), {code: 'client_timeout'});
  assert.equal(signal.aborted, true); assert.equal(calls, 2);
});

test('optional correction view is closed and paired, and old missing shape remains valid',async()=>{
 const original={stage:'wire-json',code:'invalid_json',outputDigest:'sha256:'+'a'.repeat(64),outputBytes:48,workerId:'worker-old',callId:'call-old',ticketDigest:'sha256:'+'b'.repeat(64),cleanupDigest:'sha256:'+'c'.repeat(64),at:'2026-09-14T00:00:00Z'};
 const correction={profile:'leader-json-correction/v1',used:1,max:1,reason:'wire-json',original,successorWorkerId:null,successorCallId:null};
 for(const current of [undefined,{profile:correction.profile,used:0,max:1,reason:null,original:null,successorWorkerId:null,successorCallId:null},correction,{...correction,successorWorkerId:'worker-new',successorCallId:'call-new'}]){
  const value=example('LeaderView');if(current)value.protocolCorrection=current;assert.deepEqual(structuredClone(await peer(async()=>response(value)).getLeader(taskId)),value);
 }
 for(const mutate of [v=>v.used=2,v=>v.max=2,v=>v.reason='permission',v=>v.original.stage='business',v=>v.original.raw='private',v=>v.original.outputBytes=-1,v=>v.original.workerId='../foreign',v=>v.used=0,v=>v.successorWorkerId='worker-new',v=>{v.successorWorkerId='worker-old';v.successorCallId='call-new';}]){
  const value=example('LeaderView');value.protocolCorrection=structuredClone(correction);mutate(value.protocolCorrection);await assert.rejects(peer(async()=>response(value)).getLeader(taskId),{code:'client_invalid_response'});
 }
});
