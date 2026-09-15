import test from 'node:test';
import assert from 'node:assert/strict';
import { Readable } from 'node:stream';
import { createTaskHandler } from './http-handler.mjs';
import { FORMAT, Fault } from './store.mjs';

const token = 'test-only-connection-token';
const expectedHost = '127.0.0.1:32123';
const previewDigest = 'sha256:' + 'a'.repeat(64);

// Tests exercise the actual handler with in-memory HTTP streams and an async
// Application recording port. No socket, Store, Supervisor or Agent is started.
function request({ method = 'GET', url = '/v1/tasks/task-1', headers = {}, rawExtra = [], body, chunks } = {}) {
  const entries = { host: expectedHost, authorization: 'Bearer ' + token, ...headers };
  if (method === 'POST') {
    if (!Object.hasOwn(entries, 'content-type')) entries['content-type'] = 'application/json';
    if (!Object.hasOwn(entries, 'idempotency-key')) entries['idempotency-key'] = 'request-1';
  }
  for (const key of Object.keys(entries)) if (entries[key] === undefined) delete entries[key];
  const bytes = body === undefined ? Buffer.from('') : Buffer.from(typeof body === 'string' ? body : JSON.stringify(body));
  const req = Readable.from(chunks ?? [bytes]);
  Object.assign(req, { method, url, headers: entries, rawHeaders: [...Object.entries(entries).flat(), ...rawExtra] });
  return req;
}
async function invoke(options, result = { id: 'task-1', status: 'awaiting-approval', revision: 1 }) {
  const calls = [];
  const application = async input => { calls.push(structuredClone(input)); if (result instanceof Error) throw result; return result; };
  const handler = createTaskHandler({ application, token, expectedHost });
  let status, headers, bytes, writes = 0;
  const res = { writeHead(code, value) { status = code; headers = value; }, end(value) { writes++; bytes = Buffer.from(value); } };
  await handler(request(options), res);
  assert.equal(writes, 1);
  assert.equal(headers['Content-Type'], 'application/json');
  assert.equal(headers['Cache-Control'], 'no-store');
  assert.equal(headers['Content-Length'], bytes.length);
  return { status, body: JSON.parse(bytes), calls };
}

test('create/approve/cancel delegate exact bodies and idempotency keys once with original statuses', async () => {
  for (const [url, operation, body, status] of [
    ['/v1/tasks', 'create', { intent: '订单规范化与按 SKU 汇总' }, 201],
    ['/v1/tasks/task-1/approve', 'approve', { expectedRevision: 1, previewDigest }, 202],
    ['/v1/tasks/task-1/cancel', 'cancel', { expectedRevision: 2 }, 202],
  ]) {
    const output = { id: 'task-1', status: operation === 'cancel' ? 'cancelling' : 'approved', revision: 2, attempts: 1 };
    const reply = await invoke({ method: 'POST', url, body, headers: { 'idempotency-key': 'exact-Key_123', 'content-type': 'application/json; charset=utf-8' } }, output);
    assert.equal(reply.status, status); assert.deepEqual(reply.body, output);
    assert.deepEqual(reply.calls, [{ operation, ...(operation === 'create' ? {} : { taskId: 'task-1' }), key: 'exact-Key_123', body }]);
  }
});

test('all GET routes issue only one read operation without mutation body or key', async () => {
  for (const [url, operation] of [['/v1/tasks', 'list'], ['/v1/tasks/task-1', 'get'], ...['workers', 'audit', 'delivery'].map(name => ['/v1/tasks/task-1/' + name, name])]) {
    const result = { response: operation, usage: null };
    const reply = await invoke({ url, headers: { 'idempotency-key': 'not-forwarded' }, body: { expectedRevision: 22 } }, result);
    assert.equal(reply.status, 200); assert.deepEqual(reply.body, result);
    assert.deepEqual(reply.calls, [{ operation, ...(operation === 'list' ? {} : { taskId: 'task-1' }) }]);
  }
});

test('health is minimal and does not invoke the Application', async () => {
  const reply = await invoke({ url: '/health', headers: { authorization: undefined } });
  assert.equal(reply.status, 200); assert.deepEqual(reply.body, { status: 'ok', profile: FORMAT }); assert.deepEqual(reply.calls, []);
});

test('every protected route rejects missing or wrong credentials before Application calls', async () => {
  for (const [method, url] of [['POST', '/v1/tasks'], ['GET', '/v1/tasks'], ['GET', '/v1/tasks/task-1'], ...['workers', 'audit', 'delivery'].map(name => ['GET', '/v1/tasks/task-1/' + name]), ...['approve', 'cancel'].map(name => ['POST', '/v1/tasks/task-1/' + name])]) {
    for (const authorization of [undefined, 'Bearer incorrect']) {
      const reply = await invoke({ method, url, headers: { authorization }, body: {} });
      assert.equal(reply.status, 401); assert.deepEqual(reply.body, { error: 'unauthorized' }); assert.deepEqual(reply.calls, []);
    }
  }
});

test('wrong/missing/duplicate Host, Origin and duplicate authorization have no Application effects', async () => {
  for (const options of [
    { headers: { host: 'example.invalid' } }, { headers: { host: undefined } },
    { rawExtra: ['Host', expectedHost] }, { headers: { origin: 'http://untrusted.invalid' } },
    { url: '/health', headers: { origin: 'http://untrusted.invalid' } },
  ]) { const reply = await invoke(options); assert.equal(reply.status, 403); assert.deepEqual(reply.calls, []); }
  const duplicate = await invoke({ rawExtra: ['Authorization', 'Bearer ' + token] });
  assert.equal(duplicate.status, 401); assert.deepEqual(duplicate.calls, []);
});

test('POST rejects media types, encodings and malformed idempotency headers before reading application input', async () => {
  for (const [headers, rawExtra, status, error] of [
    [{ 'content-type': undefined }, [], 415, 'invalid-content-type'],
    [{ 'content-type': 'text/plain' }, [], 415, 'invalid-content-type'],
    [{ 'content-encoding': 'gzip' }, [], 415, 'invalid-content-type'],
    [{ 'idempotency-key': undefined }, [], 400, 'invalid-idempotency-key'],
    [{ 'idempotency-key': 'not a key' }, [], 400, 'invalid-idempotency-key'],
    [{ 'idempotency-key': 'x'.repeat(129) }, [], 400, 'invalid-idempotency-key'],
    [{}, ['Idempotency-Key', 'request-1'], 400, 'invalid-idempotency-key'],
  ]) {
    const reply = await invoke({ method: 'POST', url: '/v1/tasks', body: { intent: 'test' }, headers, rawExtra });
    assert.equal(reply.status, status); assert.deepEqual(reply.body, { error }); assert.deepEqual(reply.calls, []);
  }
});

test('POST parses bounded chunked JSON and rejects invalid or excessive bytes', async () => {
  const body = { intent: 'test' }, raw = Buffer.from(JSON.stringify(body));
  const valid = await invoke({ method: 'POST', url: '/v1/tasks', chunks: [raw.subarray(0, 5), raw.subarray(5)] });
  assert.deepEqual(valid.calls[0].body, body);
  for (const body of ['', '{', 'undefined']) {
    const reply = await invoke({ method: 'POST', url: '/v1/tasks', body });
    assert.equal(reply.status, 400); assert.deepEqual(reply.body, { error: 'invalid-json' }); assert.deepEqual(reply.calls, []);
  }
  const tooLarge = await invoke({ method: 'POST', url: '/v1/tasks', chunks: [Buffer.alloc(8192, 32), Buffer.alloc(8193, 32)] });
  assert.equal(tooLarge.status, 413); assert.deepEqual(tooLarge.body, { error: 'request-too-large' }); assert.deepEqual(tooLarge.calls, []);
});

test('body shape rejection belongs to Application and its exact error is returned', async () => {
  // The adapter must not add a parallel schema/state machine or turn a valid
  // JSON primitive into a different object before the owning Application sees it.
  for (const body of [null, [], { executable: '/forbidden', intent: 'test' }, { expectedRevision: 0 }, { expectedRevision: 1 }]) {
    const reply = await invoke({ method: 'POST', url: '/v1/tasks/task-1/approve', body }, new Fault('invalid-request', 400));
    assert.equal(reply.status, 400); assert.deepEqual(reply.body, { error: 'invalid-request' }); assert.deepEqual(reply.calls[0].body, body);
  }
});

test('application conflicts/not-found/unavailable are passed through without retries or authority remapping', async () => {
  for (const [code, status] of [['revision-conflict', 409], ['idempotency-conflict', 409], ['delivery-not-ready', 409], ['task-not-found', 404], ['storage-needs-intervention', 503]]) {
    const reply = await invoke({ url: '/v1/tasks/task-1/delivery' }, new Fault(code, status));
    assert.equal(reply.status, status); assert.deepEqual(reply.body, { error: code }); assert.equal(reply.calls.length, 1);
  }
  const generic = await invoke({}, new Error('private upstream detail must not escape'));
  assert.equal(generic.status, 503); assert.deepEqual(generic.body, { error: 'internal-unavailable' }); assert.equal(generic.calls.length, 1);
});

test('unsupported or encoded routes never reach the Application', async () => {
  for (const url of ['/rpc', '/shutdown', '/v1/tasks/task-1/start', '/v1/tasks/task-1/questions', '/v1/tasks/task-1/delivery/path', '/v1/tasks/task-1?x=y', '/v1/tasks/task%2D1', '/v1/tasks//task-1', '/v1/tasks/task-1/', '/v1/tasks/' + 'x'.repeat(129)]) {
    const reply = await invoke({ url }); assert.equal(reply.status, 404); assert.deepEqual(reply.calls, []);
  }
  for (const [method, url, status] of [['POST', '/v1/tasks/task-1', 405], ['GET', '/v1/tasks/task-1/cancel', 405], ['GET', '/v1/tasks/task-1/approve', 405], ['DELETE', '/v1/tasks', 404]]) {
    const reply = await invoke({ method, url, body: {} }); assert.equal(reply.status, status); assert.deepEqual(reply.calls, []);
  }
});

test('handler freezes its own composition and does not cache or synthesize idempotent outcomes', async () => {
  const calls = [], options = { application: async input => { calls.push(input); return { observed: calls.length }; }, token, expectedHost };
  const handler = createTaskHandler(options); options.token = 'changed'; options.expectedHost = 'changed'; options.application = () => { throw new Error('not used'); };
  for (let n = 1; n <= 2; n++) {
    let body;
    await handler(request({ method: 'POST', url: '/v1/tasks/task-1/approve', body: { expectedRevision: 1, previewDigest } }), { writeHead(status) { assert.equal(status, 202); }, end(value) { body = JSON.parse(value); } });
    assert.deepEqual(body, { observed: n });
  }
  assert.deepEqual(calls[0], calls[1]);
});
