import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {createHash} from 'node:crypto';
import {TaskClient} from './index.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {contract, operations, resolve, TaskApiError} from '../task-api/contract.mjs';

const token = 'fixture-local-token-never-forward-secret-001';
const at = '2026-09-08T00:00:00.000Z';
const bytes = Buffer.from('independent HTTP consumer fixture\n');
const digest = 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const artifact = {id: 'artifact-example', taskId: 'task-example', kind: 'delivery', status: 'ready', name: 'result.txt',
  mediaType: 'text/plain', bytes: bytes.length, digest, createdAt: at};
async function loopback(t, application) {
  let handler; const server = createServer((req, res) => handler(req, res));
  t.after(() => { server.closeAllConnections(); server.close(); });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  handler = createTaskApiHandler({application, token, expectedHost: new URL(baseURL).host});
  return {baseURL, client: new TaskClient({baseURL, token})};
}
function example(schema) { return structuredClone((typeof schema === 'string' ? contract.components.schemas[schema] : resolve(schema.$ref)).examples[0]); }
function options(entry) {
  const paths = Object.fromEntries([...entry.path.matchAll(/{([^}]+)}/g)].map(([, name]) => [name, name.replace('Id', '') + '-example']));
  return {path: paths, ...(entry.request ? {body: example(entry.request), idempotencyKey: 'key-' + entry.operation.replace('.', '-')} : {}),
    ...(entry.paged ? {query: {limit: 2}} : {})};
}

test('all 25 contract operations traverse real loopback HTTP and one injected Application', {timeout: 10000}, async t => {
  const received = [];
  const {client} = await loopback(t, async (request, context) => {
    assert.equal(context.principal, 'local-operator'); received.push(request);
    const entry = operations.find(item => item.operation === request.operation);
    if (entry.operation === 'artifact.content') return {artifact, content: bytes};
    if (entry.operation === 'artifact.get' || entry.operation === 'input.create') return artifact;
    const value = example(entry.response);
    if (entry.response === 'Operation' && entry.operation !== 'operation.get') value.kind = entry.operation;
    return value;
  });
  for (const entry of operations) {
    const result = await client.request(entry.operation, options(entry));
    if (entry.operation === 'artifact.content') assert.deepEqual(result.content, bytes);
  }
  assert.equal(received.length, 26); // Download includes a fresh manifest GET.
  for (const entry of operations) assert.ok(received.some(request => request.operation === entry.operation), entry.operation);
  for (const request of received.filter(request => request.body)) assert.equal(request.key, 'key-' + request.operation.replace('.', '-'));
});

test('explicit create/plan approval preserves original key/revision through conflict and response loss', {timeout: 10000}, async t => {
  let current = {id: 'task-example', revision: 1, status: 'draft', phase: 'intake', intent: '写一份说明',
    createdAt: at, updatedAt: at, allowedActions: ['cancel'], plan: null, artifactIds: []};
  let writes = 0; const receipts = new Map();
  const {baseURL, client} = await loopback(t, async request => {
    if (request.operation === 'task.create') {
      if (!receipts.has(request.key)) { writes++; receipts.set(request.key, structuredClone(current)); }
      return receipts.get(request.key);
    }
    if (request.operation === 'task.get') return current;
    if (request.operation === 'task.plan') return {...example('Plan'), taskId: current.id};
    if (request.operation === 'task.approve') {
      if (receipts.has(request.key)) return receipts.get(request.key);
      if (request.body.expectedRevision !== current.revision) throw new TaskApiError('revision_conflict');
      writes++; current = {...current, revision: current.revision + 1, status: 'running', phase: 'execution'};
      const op = {id: 'operation-example', taskId: current.id, kind: 'task.approve', status: 'accepted', taskRevision: current.revision, createdAt: at, updatedAt: at};
      receipts.set(request.key, op); return op;
    }
    throw new TaskApiError('unsupported_operation');
  });
  const draft = await client.createTask({intent: current.intent}, 'create-original'); assert.equal(writes, 1);
  const plan = await client.request('task.plan', {path: {taskId: draft.id}});
  const approval = {expectedRevision: 1, planRevision: plan.revision, planDigest: plan.digest};
  let attempts = 0;
  const lossy = new TaskClient({baseURL, token, fetch: async (...args) => {
    attempts++; const response = await fetch(...args); await response.arrayBuffer(); throw new Error('PRIVATE_TRANSPORT ' + token);
  }});
  await assert.rejects(lossy.approveTask(draft.id, approval, 'approve-original'), {code: 'client_transport_error'});
  assert.equal(attempts, 1); assert.equal(writes, 2);
  const replay = await client.approveTask(draft.id, approval, 'approve-original'); assert.equal(replay.taskRevision, 2); assert.equal(writes, 2);
  await assert.rejects(client.approveTask(draft.id, approval, 'another-key'), {code: 'revision_conflict', status: 409});
  assert.equal(writes, 2);
});

test('local validation rejects unsafe routes/configuration and never invents write keys or confirmation', async () => {
  let calls = 0; const options = {baseURL: 'http://127.0.0.1:39999', token, fetch: async () => { calls++; }};
  for (const baseURL of ['https://example.com', 'http://localhost:39999', 'http://127.0.0.1:39999/x', 'http://user@127.0.0.1:39999', 'http://127.0.0.1:39999?x'])
    assert.throws(() => new TaskClient({...options, baseURL}), {code: 'client_invalid_configuration'});
  const client = new TaskClient(options);
  await assert.rejects(client.createTask({intent: 'x'}), {code: 'client_invalid_idempotency_key'});
  await assert.rejects(client.approveTask('task-one', {expectedRevision: 1}, 'key'), {code: 'client_invalid_request'});
  await assert.rejects(client.getTask('../escape'), {code: 'client_invalid_request'});
  await assert.rejects(client.request('task.list', {query: {limit: 101}}), {code: 'client_invalid_request'});
  await assert.rejects(client.request('task.cancel', {path: {taskId: 'x'}, body: {expectedRevision: 1}, idempotencyKey: 'key', url: 'http://evil'}), {code: 'client_invalid_request'});
  assert.equal(calls, 0);
});

test('redirect/foreign response/secret errors are bounded, sanitized and never retried', async () => {
  for (const mode of ['redirect', 'foreign', 'secret', 'wrong-id', 'encoded', 'duplicate']) {
    let calls = 0;
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async (_url, init) => {
      calls++; assert.equal(init.redirect, 'manual'); assert.equal(init.headers.Authorization, 'Bearer ' + token);
      if (mode === 'redirect') return new Response('', {status: 302, headers: {Location: 'https://example.com'}});
      if (mode === 'foreign') return {url: 'https://example.com', status: 200, body: null};
      const data = mode === 'secret' ? {code: 'application_unavailable', message: token, requestId: 'req-one', allowedActions: ['query']} : example('Task');
      if (mode === 'wrong-id') data.id = 'another-task';
      return new Response(mode === 'duplicate' ? '{"id":"x","id":"y"}' : JSON.stringify(data), {
        status: mode === 'secret' ? 503 : 200, headers: {'Content-Type': 'application/json', ...(mode === 'encoded' ? {'Content-Encoding': 'gzip'} : {})}});
    }});
    await assert.rejects(client.getTask('task-example'), failure => {
      assert.match(failure.code, /^client_/); assert.doesNotMatch(JSON.stringify(failure), /PRIVATE_|fixture-local-token/); return true;
    });
    assert.equal(calls, 1);
  }
});

test('deadline and caller abort cancel pending transport/reader without cancelling Task or retry', {timeout: 5000}, async () => {
  for (const mode of ['transport', 'body', 'abort']) {
    let calls = 0, cancelled = false, observedSignal;
    const controller = new AbortController();
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, timeoutMs: 30, fetch: async (_url, init) => {
      calls++; observedSignal = init.signal;
      if (mode === 'transport' || mode === 'abort') return new Promise(() => {});
      return new Response(new ReadableStream({cancel() { cancelled = true; }}), {headers: {'Content-Type': 'application/json'}});
    }});
    const pending = client.getTask('task-example', {signal: controller.signal});
    if (mode === 'abort') controller.abort('PRIVATE_ABORT ' + token);
    await assert.rejects(pending, {code: mode === 'abort' ? 'client_aborted' : 'client_timeout'});
    assert.equal(calls, 1); assert.equal(observedSignal.aborted, true);
    if (mode === 'body') assert.equal(cancelled, true);
  }
});

test('Unicode-escaped token cannot reflect through decoded error requestId or success content', async () => {
  for (const failure of [true, false]) {
    const value = failure ? {code: 'application_unavailable', message: 'unavailable', requestId: token, allowedActions: ['query']} : {...example('Task'), intent: token};
    const escaped = [...token].map(character => '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0')).join('');
    const wire = JSON.stringify(value).replace(token, escaped);
    assert.equal(wire.includes(token), false); assert.equal(JSON.parse(wire)[failure ? 'requestId' : 'intent'], token);
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async () => new Response(wire, {
      status: failure ? 503 : 200, headers: {'Content-Type': 'application/json'}})});
    await assert.rejects(client.getTask('task-example'), error => {
      assert.equal(error.code, 'client_invalid_response'); assert.equal(error.requestId, null);
      assert.equal(JSON.stringify(error).includes(token), false); assert.equal(error.message.includes(token), false); return true;
    });
  }
});

test('download binds fresh artifact manifest, exact bytes and Content-Digest, with one shared deadline', async () => {
  for (const mode of ['ok', 'digest', 'length', 'content-digest', 'not-ready', 'oversize']) {
    let calls = 0;
    const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async url => {
      calls++;
      if (!url.endsWith('/content')) {
        const manifest = {...artifact};
        if (mode === 'digest') manifest.digest = 'sha256:' + '0'.repeat(64);
        if (mode === 'length') manifest.bytes++;
        if (mode === 'not-ready') manifest.status = 'pending';
        return new Response(JSON.stringify(manifest), {headers: {'Content-Type': 'application/json'}});
      }
      return new Response(bytes, {headers: {'Content-Type': 'application/octet-stream', 'Content-Length': mode === 'oversize' ? String(8 * 1024 * 1024 + 1) : String(bytes.length),
        'Content-Digest': mode === 'content-digest' ? 'wrong' : 'sha-256=:' + createHash('sha256').update(bytes).digest('base64') + ':'}});
    }});
    if (mode === 'ok') assert.deepEqual((await client.downloadArtifact(artifact.id)).content, bytes);
    else await assert.rejects(client.downloadArtifact(artifact.id), failure => /^client_/.test(failure.code));
    assert.equal(calls, mode === 'not-ready' ? 1 : 2);
  }
});

test('manifest and content share the original deadline signal instead of starting a second observation budget', {timeout: 5000}, async () => {
  const signals = [];
  const client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, timeoutMs: 50, fetch: async (_url, init) => {
    signals.push(init.signal);
    if (signals.length === 1) return new Response(JSON.stringify(artifact), {headers: {'Content-Type': 'application/json'}});
    return new Promise(() => {});
  }});
  await assert.rejects(client.downloadArtifact(artifact.id), {code: 'client_timeout'});
  assert.equal(signals.length, 2); assert.equal(signals[0], signals[1]); assert.equal(signals[0].aborted, true);
});

test('audit client binds snapshot to original worker/task and exact audit manifest, with no writes or retries', async () => {
  const audit = example('Audit'), worker = example('Worker'); audit.workers = [worker];
  const snapshot = {...artifact, kind: 'evidence', name: worker.id + '.input.txt'};
  const prompt = {workerId: worker.id, text: bytes.toString(), contextRefs: [], source: 'handed-off-redacted', observation: {
    stage: 'handed-off', promptDigest: digest, promptBytes: bytes.length, inputDigest: digest, reservationDigest: digest,
    preparedAt: at, handedOffAt: at, coverage: 'policy-redacted', policy: {id: 'public-fixture', version: '1'}, snapshot, previewTruncated: false}};
  audit.prompts = [prompt];
  for (const mode of ['ok', 'foreign-audit', 'changed-manifest', 'changed-preview']) {
    const calls = [], client = new TaskClient({baseURL: 'http://127.0.0.1:39999', token, fetch: async (url, init) => {
      calls.push(url); assert.equal(init.method, 'GET');
      if (url.endsWith('/audit')) {
        const response = structuredClone(audit); if (mode === 'foreign-audit') response.prompts[0].observation.snapshot.taskId = 'foreign';
        return new Response(JSON.stringify(response), {headers: {'Content-Type': 'application/json'}});
      }
      if (!url.endsWith('/content')) return new Response(JSON.stringify({...snapshot, ...(mode === 'changed-manifest' ? {createdAt: '2026-09-09T00:00:00.000Z'} : {})}),
        {headers: {'Content-Type': 'application/json'}});
      return new Response(bytes, {headers: {'Content-Type': 'application/octet-stream', 'Content-Digest': 'sha-256=:' + createHash('sha256').update(bytes).digest('base64') + ':'}});
    }});
    if (mode === 'foreign-audit') {await assert.rejects(client.getAudit('task-example'), {code: 'client_invalid_response'}); assert.equal(calls.length, 1); continue;}
    const read = await client.getAudit('task-example'); if (mode === 'changed-preview') read.prompts[0].text = 'x'.repeat(bytes.length);
    if (mode === 'ok') assert.deepEqual((await client.downloadInputSnapshot('task-example', read.prompts[0])).content, bytes);
    else await assert.rejects(client.downloadInputSnapshot('task-example', read.prompts[0]), {code: 'client_artifact_integrity'});
    assert.equal(calls.length, 3);
  }
});
