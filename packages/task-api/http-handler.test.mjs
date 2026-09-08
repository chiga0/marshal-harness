import test from 'node:test';
import assert from 'node:assert/strict';
import {Readable} from 'node:stream';
import {EventEmitter, once} from 'node:events';
import {createServer} from 'node:http';
import {connect} from 'node:net';
import {createHash} from 'node:crypto';
import {readFileSync} from 'node:fs';
import {createTaskApiHandler} from './http-handler.mjs';
import {contract, operations, validate, resolve, TaskApiError} from './contract.mjs';
import {parseJson} from './http-boundary.mjs';

const token = 'fixture-only-local-access-token-not-a-secret';
const host = '127.0.0.1:39877';
const at = '2026-09-08T00:00:00.000Z';
const digest = 'sha256:' + 'a'.repeat(64);
const clone = value => structuredClone(value);
const task = {id: 'task-one', revision: 1, status: 'draft', phase: 'intake', intent: '生成订单分析说明',
  createdAt: at, updatedAt: at, allowedActions: ['cancel'], plan: null, artifactIds: []};
const plan = {taskId: task.id, revision: 1, digest, summary: '生成并独立验证说明文档',
  nodes: [{id: 'node-one', role: 'author', goal: '完成说明文档', scope: ['输出文档'], providerId: null}],
  edges: [], budget: {timeoutMs: 300000, maxAttempts: 2, maxWorkers: 1},
  deliverables: ['说明文档'], acceptance: ['覆盖需求定义的范围'], assumptions: []};
const usage = {tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0};
const worker = {id: 'worker-one', taskId: task.id, nodeId: 'node-one', providerId: 'fake', role: 'author',
  status: 'running', phase: 'development', attempt: 1, startedAt: at, finishedAt: null,
  lastObservedAt: at, progress: null, usage};
const question = {id: 'question-one', taskId: task.id, nodeId: null, revision: 1, subject: '输出语言',
  kind: 'clarification', prompt: '使用哪种语言？', options: [{value: 'zh', label: '中文'}], deadlineAt: at, status: 'open'};
const content = Buffer.from('fixture delivery, not model output\n');
const artifact = {id: 'artifact-one', taskId: task.id, name: 'result.txt', kind: 'delivery', status: 'ready',
  mediaType: 'text/plain', bytes: content.length, digest: 'sha256:' + createHash('sha256').update(content).digest('hex'), createdAt: at};
function operation(kind = 'task.approve', status = 'accepted') {
  return {id: 'operation-one', taskId: task.id, kind, status, taskRevision: 2, createdAt: at, updatedAt: at};
}
const fixtures = {
  Task: task, Plan: plan, Worker: worker, Question: question, Operation: operation(), Artifact: artifact,
  Tasks: {items: [task], nextCursor: null}, Workers: {taskId: task.id, items: [worker], nextCursor: null},
  Questions: {taskId: task.id, taskRevision: 1, previewRevision: null, previewDigest: null, confirmBefore: at, preview: null, items: [question], nextCursor: null},
  Graph: {taskId: task.id, planRevision: 1, nodes: [{id: 'node-one', role: 'author', status: 'running', workerIds: [worker.id]}], edges: []},
  Events: {taskId: task.id, items: [{id: 'event-one', taskId: task.id, sequence: 1, type: 'task_created', at, workerId: null, summary: '已受理', source: 'application'}], nextCursor: null},
  Audit: {taskId: task.id, elapsedMs: null, attempts: 1, retryCount: 0, reworkCount: 0,
    firstReview: {passed: 0, total: 0, pending: 1}, acceptance: {status: 'pending', evidenceIds: [], digest: null}, usage, workers: [worker], prompts: []},
  Providers: {items: [{id: 'fake', displayName: '确定性替身', availability: 'ready', coreCapabilities: ['input', 'execution-identity', 'terminal', 'artifacts', 'owned-stop'], enhancedCapabilities: []}], nextCursor: null},
  Supervisor: {status: 'ready', activeWorkers: 1, maxWorkers: 2, queuedTasks: 0, blockedTasks: 0, observedAt: at},
  Health: {status: 'ok', profile: 'node-task-service/v1'}, Readiness: {ready: true, profile: 'node-task-service/v1'},
};
const inputs = {
  CreateTask: {intent: task.intent, requirements: {deliverables: ['说明文档'], acceptance: ['有明确结论']}},
  ApproveTask: {expectedRevision: 2, planRevision: 1, planDigest: digest},
  ControlTask: {expectedRevision: 2}, AnswerQuestion: {expectedRevision: 2, questionRevision: 1, previewDigest: digest, answer: 'zh'},
  CreateInput: {name: 'requirements.txt', mediaType: 'text/plain', contentBase64: 'aGVsbG8='},
};
fixtures.ClarificationPreview = {revision: 1, digest, inputsDigest: digest, input: {intent: task.intent}, plan, missingSlots: []};
fixtures.AnswerReceipt = {taskId: task.id, questionId: question.id, operation: {...operation('task.answer', 'succeeded'), taskRevision: 3},
  acceptedRevision: 3, acceptedPreviewDigest: digest, preview: fixtures.ClarificationPreview,
  task: {...task, revision: 3, status: 'awaiting-confirmation', plan: {revision: 1, digest}},
  currentTask: {...task, revision: 3, status: 'awaiting-confirmation', plan: {revision: 1, digest}}, replayed: false};

async function request(application, method, url, body, options = {}) {
  const handler = createTaskApiHandler({application, token, expectedHost: host, requestTimeoutMs: options.timeout ?? 2000});
  const headers = {host, authorization: 'Bearer ' + token, ...options.headers};
  if (body !== undefined) { headers['content-type'] ??= 'application/json'; headers['idempotency-key'] ??= 'key-one'; }
  const req = Readable.from(body === undefined ? [] : [Buffer.from(options.raw ?? JSON.stringify(body))]);
  req.method = method; req.url = url; req.headers = headers;
  req.rawHeaders = Object.entries(headers).flatMap(([k, v]) => v === undefined ? [] : [k, v]);
  if (options.duplicate) req.rawHeaders.push(options.duplicate, headers[options.duplicate]);
  const res = responseDouble();
  await handler(req, res);
  assert.equal(res.headers['Cache-Control'], 'no-store');
  assert.equal(res.headers['Content-Length'], res.bytes.length);
  return {...res, body: res.headers['Content-Type'] === 'application/json' ? JSON.parse(res.bytes) : undefined};
}
function responseDouble() {
  return Object.assign(new EventEmitter(), {headersSent: false, destroyed: false, headers: {},
    setHeader(name, value) { this.headers[name] = value; },
    writeHead(status, values) { this.status = status; Object.assign(this.headers, values); this.headersSent = true; },
    end(value) { this.bytes = Buffer.from(value); this.emit('finish'); }});
}

test('unfinished body deadline and peer abort release readers without dispatching or cancelling Tasks', async () => {
  for (const peerAbort of [false, true]) {
    let calls = 0;
    const handler = createTaskApiHandler({application: async () => { calls++; }, token, expectedHost: host, requestTimeoutMs: 20});
    const req = new Readable({read() {}});
    Object.assign(req, {method: 'POST', url: '/v1/tasks', headers: {host, authorization: 'Bearer ' + token,
      'content-type': 'application/json', 'idempotency-key': 'key-one'}});
    req.rawHeaders = Object.entries(req.headers).flat();
    const res = responseDouble(); const pending = handler(req, res);
    req.push(Buffer.from('{"intent":'));
    if (peerAbort) req.emit('aborted');
    await pending;
    assert.equal(res.status, 504); assert.equal(JSON.parse(res.bytes).code, 'request_timeout');
    assert.equal(res.headers.Connection, 'close'); assert.equal(res.shouldKeepAlive, false);
    assert.equal(req.destroyed, true); assert.equal(calls, 0);
    for (const event of ['data', 'end', 'error', 'close', 'aborted']) assert.equal(req.listenerCount(event), 0, event);
    assert.equal(res.listenerCount('finish'), 0); assert.equal(res.listenerCount('close'), 0);
  }
});

test('real HTTP incomplete body receives complete 504 and cannot reuse the connection', {timeout: 5000}, async t => {
  let handler, calls = 0, requests = 0, incoming;
  const server = createServer((req, res) => { requests++; incoming = req; void handler(req, res); });
  t.after(() => { server.closeAllConnections(); server.close(); });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const port = server.address().port;
  handler = createTaskApiHandler({application: async () => { calls++; }, token, expectedHost: `127.0.0.1:${port}`, requestTimeoutMs: 40});
  const socket = connect(port, '127.0.0.1'); t.after(() => socket.destroy());
  const chunks = []; socket.on('data', chunk => chunks.push(chunk));
  const closed = once(socket, 'close');
  await once(socket, 'connect');
  socket.write(`POST /v1/tasks HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\nAuthorization: Bearer ${token}\r\nContent-Type: application/json\r\nIdempotency-Key: key-one\r\nContent-Length: 10000\r\nConnection: keep-alive\r\n\r\n{"intent":`);
  await closed;
  const raw = Buffer.concat(chunks).toString(); const [headers, body] = raw.split('\r\n\r\n');
  assert.match(headers, /^HTTP\/1.1 504 /); assert.match(headers, /connection: close/i);
  assert.equal(Buffer.byteLength(body), Number(/content-length: (\d+)/i.exec(headers)[1]));
  assert.equal(JSON.parse(body).code, 'request_timeout');
  assert.equal(socket.destroyed, true); assert.equal(incoming.destroyed, true);
  assert.equal(incoming.listenerCount('data'), 0); assert.equal(requests, 1); assert.equal(calls, 0);
});
function pathFor(entry) {
  return entry.path.replace('{taskId}', task.id).replace('{workerId}', worker.id)
    .replace('{questionId}', question.id).replace('{operationId}', 'operation-one').replace('{artifactId}', artifact.id);
}

test('single contract resolves refs, validates complete independent fixtures and closed schemas', () => {
  assert.equal(contract.openapi, '3.1.0');
  assert.equal(contract.jsonSchemaDialect, 'https://json-schema.org/draft/2020-12/schema');
  assert.equal(operations.length, 24);
  assert.equal(new Set(operations.map(o => o.operation)).size, operations.length);
  for (const entry of operations) if (entry.operation !== 'artifact.content')
    assert.ok(contract.components.schemas[entry.response].examples?.length, entry.operation + ' response example required by TaskClient');
  function walk(value) { if (!value || typeof value !== 'object') return; if (value.$ref) resolve(value.$ref); for (const v of Object.values(value)) walk(v); }
  walk(contract);
  for (const [name, value] of Object.entries({...fixtures, ...inputs})) assert.ok(validate(value, name), name);
  for (const schema of Object.values(contract.components.schemas)) {
    if (schema.type === 'object') { assert.equal(schema.additionalProperties, false); assert.ok(Object.keys(schema.properties).length); }
    for (const example of schema.examples ?? []) assert.ok(validate(example, schema));
  }
  assert.equal(validate({...task, status: 'some-new-success'}, 'Task'), false);
  assert.equal(validate({...task, secret: 'hidden'}, 'Task'), false);
  assert.equal(validate({intent: 'x', executable: '/bin/sh'}, 'CreateTask'), false);
  assert.equal(validate({intent: '中'.repeat(2731)}, 'CreateTask'), false);
  assert.equal(validate({...inputs.ApproveTask, expectedRevision: Number.MAX_SAFE_INTEGER + 1}, 'ApproveTask'), false);
  assert.throws(() => validate('x', {type: 'string', minUnknown: 4}), /unsupported-schema/);
  for (const file of ['contract.mjs', 'http-handler.mjs', 'http-boundary.mjs']) {
    const text = readFileSync(new URL(file, import.meta.url), 'utf8');
    assert.doesNotMatch(text, /from\s+['"][^'"]*(?:experiments|supervisor|providers|sqlite|child_process)/);
  }
});

test('all 24 operations dispatch to the same injected port with validated shape and identifiers', async () => {
  for (const entry of operations) {
    let received, context;
    const app = async (value, ctx) => {
      received = value; context = ctx;
      if (entry.operation === 'artifact.content') return {artifact, content};
      if (entry.response === 'Operation') return operation(entry.operation === 'operation.get' ? 'task.approve' : entry.operation);
      return clone(fixtures[entry.response]);
    };
    const requestSchema = entry.request?.$ref.split('/').at(-1);
    const result = await request(app, entry.method, pathFor(entry), requestSchema ? inputs[requestSchema] : undefined);
    assert.equal(result.status, entry.status, entry.operation);
    assert.equal(received.operation, entry.operation);
    assert.equal(context.principal, 'local-operator');
    assert.ok(context.signal instanceof AbortSignal);
    assert.ok(validate(context.requestId, 'Id'));
    assert.ok(!JSON.stringify(received).includes(token));
    if (entry.request) assert.equal(received.key, 'key-one');
    if (entry.paged) assert.deepEqual(received.page, {limit: 50, cursor: null});
    if (entry.operation === 'artifact.content') {
      assert.deepEqual(result.bytes, content); assert.equal(result.headers['Content-Type'], 'application/octet-stream');
    }
  }
});

test('in-memory application fixture completes create, external plan, approve, operation and download without HTTP state', async () => {
  // Only a contract fixture. No Store durability, Agent, actual verification or
  // delivery authority is claimed by these in-memory changes.
  let current, frozen, op; let creationCount = 0; let approvals = 0;
  const receipts = new Map();
  const app = async command => {
    if (command.operation === 'task.create') {
      const hash = JSON.stringify(command.body), found = receipts.get(command.key);
      if (found) { if (found !== hash) throw new TaskApiError('idempotency_conflict'); return current; }
      creationCount++; current = clone(task); receipts.set(command.key, hash); return current;
    }
    if (command.operation === 'task.get') return current;
    if (command.operation === 'task.plan') return frozen;
    if (command.operation === 'task.approve') {
      const key = command.operation + command.key, hash = JSON.stringify(command.body), found = receipts.get(key);
      if (found) { if (found !== hash) throw new TaskApiError('idempotency_conflict'); return op; }
      if (command.body.expectedRevision !== current.revision) throw new TaskApiError('revision_conflict');
      if (command.body.planRevision !== frozen.revision || command.body.planDigest !== frozen.digest) throw new TaskApiError('plan_conflict');
      approvals++; current = {...current, revision: 3, status: 'queued', phase: 'execution'};
      op = {...operation(), taskRevision: 3}; receipts.set(key, hash); return op;
    }
    if (command.operation === 'operation.get') return op;
    if (command.operation === 'artifact.get') return artifact;
    if (command.operation === 'artifact.content') return {artifact, content};
    throw new TaskApiError('unsupported_operation');
  };
  assert.equal((await request(app, 'POST', '/v1/tasks', inputs.CreateTask)).status, 201);
  frozen = clone(plan); current = {...current, revision: 2, status: 'awaiting-approval', phase: 'planning', plan: {revision: 1, digest}};
  assert.deepEqual((await request(app, 'GET', `/v1/tasks/${task.id}/plan`)).body, frozen);
  assert.equal((await request(app, 'POST', `/v1/tasks/${task.id}/plan/approve`, inputs.ApproveTask)).status, 202);
  assert.equal((await request(app, 'POST', `/v1/tasks/${task.id}/plan/approve`, inputs.ApproveTask)).status, 202);
  assert.equal(approvals, 1); assert.equal(creationCount, 1);
  assert.equal((await request(app, 'GET', '/v1/operations/operation-one')).body.status, 'accepted');
  op = {...op, status: 'succeeded'};
  current = {...current, revision: 4, status: 'completed', phase: 'terminal', artifactIds: [artifact.id], allowedActions: []};
  assert.equal((await request(app, 'GET', `/v1/tasks/${task.id}`)).body.artifactIds[0], artifact.id);
  assert.equal((await request(app, 'GET', `/v1/artifacts/${artifact.id}`)).body.digest, artifact.digest);
  assert.deepEqual((await request(app, 'GET', `/v1/artifacts/${artifact.id}/content`)).bytes, content);
  assert.equal((await request(app, 'POST', '/v1/tasks', inputs.CreateTask)).body.status, 'completed');
  assert.equal((await request(app, 'POST', '/v1/tasks', {...inputs.CreateTask, intent: 'other'})).status, 409);
});

test('input and access rejection occurs before application, including nested duplicate JSON keys', async () => {
  let calls = 0; const app = async () => { calls++; return task; };
  const cases = [
    [{headers: {authorization: ''}}, 401], [{duplicate: 'authorization'}, 401],
    [{headers: {host: 'localhost:39877'}}, 403], [{duplicate: 'host'}, 403],
    [{headers: {origin: 'https://other.invalid'}}, 403],
    [{headers: {'content-type': 'text/plain'}}, 415], [{headers: {'content-encoding': 'gzip'}}, 415],
    [{duplicate: 'content-type'}, 415], [{duplicate: 'idempotency-key'}, 400],
    [{headers: {'idempotency-key': ''}}, 400],
    [{raw: '{"intent":"one","intent":"two"}'}, 400],
    [{raw: '{"intent":"one","context":{"text":"a","text":"b"}}'}, 400],
    [{raw: '{"intent":"one","context":{"text":"a","\\u0074ext":"b"}}'}, 400],
    [{raw: '{"intent":"x","executable":"/bin/sh"}'}, 400],
    [{raw: '{"intent":"\\ud800"}'}, 400], [{raw: '{"intent":"\\u0000"}'}, 400],
    [{raw: '{"intent":"' + 'x'.repeat(270000) + '"}'}, 413],
  ];
  for (const [options, status] of cases) assert.equal((await request(app, 'POST', '/v1/tasks', inputs.CreateTask, options)).status, status);
  assert.equal(calls, 0);
  for (const raw of ['{"a":1,}', '[1,]', '{"a":1e999}', '{"a":01}', '{"a":1}x', '['.repeat(34) + '0' + ']'.repeat(34)]) {
    assert.throws(() => parseJson(Buffer.from(raw)), /invalid_json/);
  }
  assert.throws(() => parseJson(Buffer.from([0xff, 0xfe])), /invalid_json/);
});

test('method, IDs and pagination are bounded and never fall back to another controller', async () => {
  const app = async command => { assert.deepEqual(command.page, {limit: 1, cursor: 'page-two'}); return {items: [task], nextCursor: null}; };
  assert.equal((await request(app, 'GET', '/v1/tasks?limit=1&cursor=page-two')).status, 200);
  const never = async () => { assert.fail('must not reach application'); };
  for (const [method, path, status] of [
    ['GET', '/v1/tasks?limit=0', 400], ['GET', '/v1/tasks?limit=101', 400],
    ['GET', '/v1/tasks?limit=1&limit=2', 400], ['GET', '/v1/tasks?cursor=%2Fetc', 400],
    ['GET', '/v1/tasks?unknown=yes', 400], ['GET', '/v1/tasks/task-one?x=1', 400],
    ['GET', '/v1/tasks/..', 404], ['GET', '/v1/tasks/%2e%2e', 404],
    ['GET', '/v1//tasks', 404], ['GET', '/v1/tasks/task-one/approve', 404],
    ['POST', '/v1/tasks/task-one/plan', 405], ['GET', '/v1/workspaces', 404],
  ]) assert.equal((await request(never, method, path)).status, status);
});

test('domain failures are closed and unknown response text never leaks', async () => {
  const secret = 'NEVER-RETURN-EXCEPTION-CONTENT';
  for (const [code, status] of [['revision_conflict', 409], ['unsupported_operation', 501], ['question_expired', 410], ['capacity_exceeded', 429]]) {
    const res = await request(async () => { throw Object.assign(new Error(secret), {code, status}); }, 'GET', `/v1/tasks/${task.id}`);
    assert.equal(res.status, status); assert.equal(res.body.code, code); assert.ok(validate(res.body, 'Error')); assert.ok(!res.bytes.includes(secret));
  }
  for (const error of [new Error(secret), {code: secret, status: 409}, {code: 'revision_conflict', status: 200}]) {
    const res = await request(async () => { throw error; }, 'GET', `/v1/tasks/${task.id}`);
    assert.equal(res.status, 503); assert.equal(res.body.code, 'application_unavailable'); assert.ok(!res.bytes.includes(secret));
  }
  for (const result of [undefined, {}, {...task, id: 'another-task'}, {...task, intent: token}, {...task, secret}, {...task, revision: 0}]) {
    assert.equal((await request(async () => result, 'GET', `/v1/tasks/${task.id}`)).status, 503);
  }
  assert.equal((await request(async () => operation('task.cancel'), 'POST', `/v1/tasks/${task.id}/plan/approve`, inputs.ApproveTask)).status, 503);
  assert.equal((await request(async () => ({...fixtures.Tasks, items: [task, task]}), 'GET', '/v1/tasks?limit=1')).status, 503);
});

test('answer requires exact new subject and 4096 UTF-8 bytes, and rejects cross-bound receipts', async () => {
  const route = `/v1/tasks/${task.id}/questions/${question.id}/answers`;
  for (const answer of ['a'.repeat(4096), '中'.repeat(1365) + 'a'])
    assert.equal((await request(async () => clone(fixtures.AnswerReceipt), 'POST', route, {...inputs.AnswerQuestion, answer})).status, 202);
  const {previewDigest: _omitted, ...legacyBody} = inputs.AnswerQuestion;
  for (const body of [legacyBody, {...inputs.AnswerQuestion, answer: 'a'.repeat(4097)}, {...inputs.AnswerQuestion, answer: '中'.repeat(1366)},
    {...inputs.AnswerQuestion, answer: '\0'}, {...inputs.AnswerQuestion, questionRevision: 2}, {...inputs.AnswerQuestion, questionRevision: '1'}])
    assert.equal((await request(async () => {assert.fail('shape must fail before dispatch');}, 'POST', route, body)).status, 400);
  const changes = [value => {value.questionId = 'foreign-question';}, value => {value.operation.taskId = 'foreign-task';},
    value => {value.acceptedRevision = 4;}, value => {value.preview.digest = 'sha256:' + 'b'.repeat(64);},
    value => {value.currentTask.plan.digest = 'sha256:' + 'b'.repeat(64);}, value => {value.currentTask.revision = 2;},
    value => {value.operation.kind = 'task.cancel';}];
  for (const change of changes) {const value = clone(fixtures.AnswerReceipt); change(value);
    assert.equal((await request(async () => value, 'POST', route, inputs.AnswerQuestion)).status, 503);}
});

test('artifact content is verified against typed metadata before any bytes are emitted', async () => {
  for (const value of [{artifact: {...artifact, id: 'other'}, content}, {artifact: {...artifact, bytes: 0}, content},
    {artifact: {...artifact, digest}, content}, {artifact: {...artifact, status: 'partial'}, content},
    {artifact, content: 'not-bytes'}]) {
    const result = await request(async () => value, 'GET', `/v1/artifacts/${artifact.id}/content`);
    assert.equal(result.status, 503); assert.ok(!result.bytes.includes(content));
  }
});

test('HTTP deadline aborts observation, not Task authority, and original receipt can be queried', async () => {
  let committed = false, observedSignal; const receipt = operation('task.cancel');
  const app = async (command, context) => {
    if (command.operation === 'operation.get') return receipt;
    committed = true; observedSignal = context.signal;
    return new Promise(() => {});
  };
  const result = await request(app, 'POST', `/v1/tasks/${task.id}/cancel`, inputs.ControlTask, {timeout: 15});
  assert.equal(result.status, 504); assert.equal(result.body.code, 'request_timeout');
  assert.equal(committed, true); assert.equal(observedSignal.aborted, true);
  assert.equal((await request(app, 'GET', '/v1/operations/operation-one')).body.status, 'accepted');
});
