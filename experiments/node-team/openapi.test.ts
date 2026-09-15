import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile, mkdtemp, chmod} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as delay} from 'node:timers/promises';
import {serve, metadataFromRoot, rpc} from './main.mjs';
import {artifactHash, digest} from './store.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const api = JSON.parse(await readFile(join(here, 'openapi.json'), 'utf8'));
function resolve(ref) {
  assert.ok(ref.startsWith('#/'), 'only local document references');
  let value = api;
  for (const key of ref.slice(2).split('/')) value = value?.[key.replace(/~1/g, '/').replace(/~0/g, '~')];
  assert.ok(value, `unresolved reference ${ref}`);
  return value;
}

// Deliberately limited assertion interpreter for this document, not a general
// JSON Schema implementation. Unknown validation keywords fail the test rather
// than silently weaken it. Draft 2020-12 metaschema is separately checked.
const keywords = new Set(['$ref', 'type', 'const', 'enum', 'required', 'properties',
  'additionalProperties', 'items', 'minItems', 'maxItems', 'minimum', 'maximum',
  'minLength', 'maxLength', 'pattern', 'format', 'anyOf', 'description', 'examples',
  'x-maxUtf8Bytes', 'x-wellFormedUnicode']);
function validate(value, schema) {
  for (const key of Object.keys(schema)) assert.ok(keywords.has(key), `unsupported schema keyword ${key}`);
  if (schema.$ref) validate(value, resolve(schema.$ref));
  if (schema.anyOf) assert.ok(schema.anyOf.some(s => {
    try { validate(value, s); return true; } catch { return false; }
  }), 'no anyOf branch matches');
  if (schema.type) {
    const actual = value === null ? 'null' : Array.isArray(value) ? 'array' : typeof value;
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    assert.ok(types.some(t => t === actual || t === 'integer' && Number.isInteger(value)), `expected ${types}, got ${actual}`);
  }
  if (Object.hasOwn(schema, 'const')) assert.deepEqual(value, schema.const);
  if (schema.enum) assert.ok(schema.enum.some(v => Object.is(v, value)), 'enum mismatch');
  if (typeof value === 'number') {
    if (schema.minimum !== undefined) assert.ok(value >= schema.minimum, 'minimum');
    if (schema.maximum !== undefined) assert.ok(value <= schema.maximum, 'maximum');
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined) assert.ok([...value].length >= schema.minLength, 'minLength');
    if (schema.maxLength !== undefined) assert.ok([...value].length <= schema.maxLength, 'maxLength');
    if (schema.pattern) assert.match(value, new RegExp(schema.pattern, 'u'));
    if (schema.format === 'date-time') assert.ok(/^\d{4}-\d{2}-\d{2}T/.test(value) && Number.isFinite(Date.parse(value)), 'date-time');
    if (schema['x-maxUtf8Bytes']) assert.ok(Buffer.byteLength(value) <= schema['x-maxUtf8Bytes'], 'UTF-8 byte limit');
    if (schema['x-wellFormedUnicode']) assert.ok(value.isWellFormed(), 'well-formed Unicode');
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined) assert.ok(value.length >= schema.minItems, 'minItems');
    if (schema.maxItems !== undefined) assert.ok(value.length <= schema.maxItems, 'maxItems');
    if (schema.items) for (const v of value) validate(v, schema.items);
  }
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of schema.required ?? []) assert.ok(Object.hasOwn(value, key), `required ${key}`);
    for (const [key, v] of Object.entries(value)) {
      if (schema.additionalProperties === false) assert.ok(Object.hasOwn(schema.properties, key), `unexpected ${key}`);
      if (schema.properties?.[key]) validate(v, schema.properties[key]);
    }
  }
}
function check(name, value) { validate(value, api.components.schemas[name]); }
function traverse(value) {
  if (!value || typeof value !== 'object') return;
  if (value.$ref) resolve(value.$ref);
  for (const child of Object.values(value)) traverse(child);
}
function responseSchema(route, method, status) {
  const spec = api.paths[route]?.[method.toLowerCase()]?.responses?.[status];
  assert.ok(spec, `undocumented ${method} ${route} ${status}`);
  return spec.content['application/json'].schema;
}

test('OpenAPI has exact implemented routes, resolved refs and complete examples', () => {
  assert.equal(api.openapi, '3.1.0');
  assert.equal(api.jsonSchemaDialect, 'https://json-schema.org/draft/2020-12/schema');
  assert.deepEqual(Object.keys(api.paths).sort(), ['/health', '/v1/tasks', '/v1/tasks/{id}',
    '/v1/tasks/{id}/approve', '/v1/tasks/{id}/audit', '/v1/tasks/{id}/cancel',
    '/v1/tasks/{id}/delivery', '/v1/tasks/{id}/workers'].sort());
  traverse(api);
  const operations = Object.entries(api.paths).flatMap(([route, p]) =>
    Object.entries(p).filter(([method]) => ['get', 'post'].includes(method)).map(([method, op]) => ({route, method, op})));
  assert.equal(operations.length, 9);
  assert.equal(new Set(operations.map(v => v.op.operationId)).size, operations.length);
  for (const {route, method, op} of operations) {
    assert.ok(op.responses);
    if (method === 'post') {
      assert.ok(op.parameters.some(p => resolve(p.$ref).name === 'Idempotency-Key'));
      assert.equal(op.requestBody.required, true);
    }
    assert.deepEqual(op.security ?? api.security, route === '/health' ? [] : [{localBearer: []}]);
  }
  for (const schema of Object.values(api.components.schemas)) {
    if (schema.type === 'object') {
      assert.equal(schema.additionalProperties, false);
      assert.ok(Object.keys(schema.properties).length > 0);
    }
    for (const example of schema.examples ?? []) validate(example, schema);
  }
});

test('contract negative matrix rejects forged fields, types, byte limits and digest formats', () => {
  const clone = name => structuredClone(api.components.schemas[name].examples[0]);
  for (const request of [{}, {intent: ''}, {intent: ' '}, {intent: 'x', executable: '/bin/sh'},
    {intent: 'x\0'}, {intent: '\ud800'}, {intent: '中'.repeat(1366)}]) {
    assert.throws(() => check('CreateTask', request));
  }
  for (const revision of [0, -1, 1.1, '1', null, 9007199254740992]) {
    assert.throws(() => check('CancelTask', {expectedRevision: revision}));
  }
  const draft = clone('Task');
  assert.throws(() => check('Task', {...draft, status: 'ACCEPTED'}));
  assert.throws(() => check('Task', {...draft, phase: 'done'}));
  assert.throws(() => check('Task', {...draft, usage: {tokens: 0}}));
  assert.throws(() => check('Task', {...draft, previewDigest: 'a'.repeat(64)}));
  const worker = draft.workers[0];
  assert.throws(() => check('Worker', {...worker, cleaned: 'true'}));
  assert.throws(() => check('Worker', {...worker, secret: 'not-a-real-secret'}));
  const delivery = clone('Delivery');
  assert.throws(() => check('Delivery', {...delivery, files: []}));
  assert.throws(() => check('File', {...delivery.files[0], sha256: 'sha256:' + 'a'.repeat(64)}));
  assert.throws(() => check('File', {...delivery.files[0], name: '../escape.mjs'}));
  assert.throws(() => check('Error', {error: 'new-undocumented-error'}));
  assert.throws(() => check('Error', {error: 'unauthorized', token: 'not-a-real-secret'}));
});

test('actual Node HTTP fixture responses conform through approval, delivery, replay and cancellation', {timeout: 45000}, async t => {
  // This invokes only the checked-in deterministic Pi protocol fixture, not Pi
  // or a model. Keep the socket path short on both supported host platforms.
  const root = await mkdtemp(join(process.platform === 'darwin' ? '/private/tmp' : tmpdir(), 'napi-'));
  await chmod(root, 0o700);
  const directory = join(root, 'state');
  const fixture = join(here, 'fixtures/pi.mjs');
  const front = await serve(directory, {provider: 'pi', executable: fixture});
  t.after(async () => {
    await front.close();
    await rpc(await metadataFromRoot(directory), {operation: 'shutdown'}, 12000);
    // Preserve the bounded private test root; never broad-delete user state.
  });
  const {token} = JSON.parse(await readFile(front.connectionFile, 'utf8'));
  async function request(route, concrete, method = 'GET', body, key, headers = {}) {
    const res = await fetch(front.url + concrete, {method, signal: AbortSignal.timeout(5000),
      headers: {Authorization: `Bearer ${token}`, 'Content-Type': 'application/json',
        ...(key ? {'Idempotency-Key': key} : {}), ...headers},
      body: body === undefined ? undefined : JSON.stringify(body)});
    const value = await res.json();
    validate(value, responseSchema(route, method, String(res.status)));
    assert.equal(res.headers.get('cache-control'), 'no-store');
    assert.ok(!JSON.stringify(value).includes(token), 'private token not in HTTP projection');
    return {status: res.status, value};
  }
  async function until(id, predicate) {
    const deadline = Date.now() + 12000;
    while (Date.now() < deadline) {
      const {value} = await request('/v1/tasks/{id}', `/v1/tasks/${id}`);
      if (predicate(value)) return value;
      await delay(30);
    }
    assert.fail('bounded fixture observation expired, not a Task failure');
  }
  assert.equal((await request('/health', '/health', 'GET', undefined, undefined, {Authorization: ''})).status, 200);
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'GET', undefined, undefined, {Authorization: ''})).status, 401);
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'GET', undefined, undefined, {Origin: 'https://invalid.test'})).status, 403);
  for (const body of [{intent: 'x', extra: true}, {intent: '中'.repeat(1366)}, {intent: '\ud800'}]) {
    assert.equal((await request('/v1/tasks', '/v1/tasks', 'POST', body, 'bad-create')).status, 400);
  }
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'POST', {intent: 'x'.repeat(17000)}, 'oversize')).status, 413);
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'POST', {intent: 'x'}, 'type', {'Content-Type': 'text/plain'})).status, 415);
  const intent = 'Build order report';
  const draft = (await request('/v1/tasks', '/v1/tasks', 'POST', {intent}, 'create')).value;
  assert.equal(draft.previewDigest, digest(draft.plan));
  const base = `/v1/tasks/${draft.id}`;
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'POST', {intent: 'changed'}, 'create')).status, 409);
  const approval = {expectedRevision: draft.revision, previewDigest: draft.previewDigest};
  assert.equal((await request('/v1/tasks/{id}/approve', base + '/approve', 'POST', {...approval, expectedRevision: 9}, 'stale')).status, 409);
  assert.equal((await request('/v1/tasks/{id}/delivery', base + '/delivery')).status, 409);
  assert.equal((await request('/v1/tasks/{id}/approve', base + '/approve', 'POST', approval, 'approve')).status, 202);
  const completed = await until(draft.id, value => ['completed', 'failed', 'intervention'].includes(value.status));
  assert.equal(completed.status, 'completed');
  assert.equal(completed.attempts, 1);
  assert.ok(completed.workers.every(w => w.cleaned));
  // Lost-response replays return the CURRENT projection, even after revision
  // progressed. They do not reset a Task or demand another approval.
  assert.equal((await request('/v1/tasks', '/v1/tasks', 'POST', {intent}, 'create')).value.status, 'completed');
  assert.equal((await request('/v1/tasks/{id}/approve', base + '/approve', 'POST', approval, 'approve')).value.revision, completed.revision);
  const delivery = (await request('/v1/tasks/{id}/delivery', base + '/delivery')).value;
  assert.deepEqual(delivery.files.map(f => f.name).sort(), ['normalize.mjs', 'report.mjs']);
  for (const file of delivery.files) assert.equal(file.sha256, artifactHash(file.content));
  assert.equal((await request('/v1/tasks/{id}/audit', base + '/audit')).value.acceptance.checks, delivery.checks);
  await request('/v1/tasks/{id}/workers', base + '/workers');
  await request('/v1/tasks', '/v1/tasks');
  assert.equal((await request('/v1/tasks/{id}/cancel', base + '/cancel', 'POST', {expectedRevision: completed.revision}, 'late')).status, 409);
  assert.equal((await request('/v1/tasks/{id}', '/v1/tasks/missing')).status, 404);

  const cancelDraft = (await request('/v1/tasks', '/v1/tasks', 'POST', {intent: 'fixture-slow orders'}, 'create-slow')).value;
  const slowBase = `/v1/tasks/${cancelDraft.id}`;
  await request('/v1/tasks/{id}/approve', slowBase + '/approve', 'POST', {expectedRevision: 1, previewDigest: cancelDraft.previewDigest}, 'approve-slow');
  const running = await until(cancelDraft.id, value => value.status === 'running' && value.workers.every(w => w.startedAt));
  const cancelBody = {expectedRevision: running.revision};
  assert.equal((await request('/v1/tasks/{id}/cancel', slowBase + '/cancel', 'POST', cancelBody, 'cancel-slow')).status, 202);
  const cancelled = await until(cancelDraft.id, value => value.status === 'cancelled');
  assert.ok(cancelled.workers.every(w => w.cleaned));
  assert.equal((await request('/v1/tasks/{id}/cancel', slowBase + '/cancel', 'POST', cancelBody, 'cancel-slow')).value.status, 'cancelled');
  assert.equal((await request('/v1/tasks/{id}/audit', slowBase + '/audit')).value.acceptance, null);
  assert.equal((await request('/v1/tasks/{id}/delivery', slowBase + '/delivery')).status, 409);
});
