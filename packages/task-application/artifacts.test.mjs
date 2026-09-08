import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {Store} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication} from './application.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {TaskClient} from '../task-client/index.mjs';

const context = {principal: 'local-operator'};
const fail = (code, status) => error => error.code === code && error.status === status;
const upload = (key = 'upload', text = '部门甲,17\n部门乙,29\n') => ({operation: 'input.create', key,
  body: {name: '业务数据.csv', mediaType: 'text/csv', contentBase64: Buffer.from(text).toString('base64')}});
function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-input-app-')));
  const state = path.join(parent, 'state'), objects = path.join(parent, 'objects');
  let store = Store.create(state), depot = ArtifactDepot.create(objects);
  let owner = store.claimOwner(0, 'server', Date.now() + 3600000);
  let app = new TaskApplication({store, owner, depot});
  t.after(() => { store.close(); depot.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  return {get app() { return app; }, get depot() { return depot; }, get store() { return store; }, objects,
    call: request => app.dispatch(request, context),
    read: callback => store.read(owner, callback),
    reopen() {
      store.close(); depot.close(); store = Store.openExisting(state); depot = ArtifactDepot.openExisting(objects);
      owner = store.claimOwner(owner.generation, 'next-server', Date.now() + 3600000);
      app = new TaskApplication({store, owner, depot});
    }};
}

test('real SQLite/depot input upload, exact idempotency, binding and cold reopen', async t => {
  const f = fixture(t), req = upload(), artifact = await f.call(req);
  assert.equal(artifact.kind, 'input'); assert.equal(artifact.taskId, null);
  assert.deepEqual(await f.call(req), artifact);
  const content = await f.call({operation: 'artifact.content', artifactId: artifact.id});
  assert.equal(content.content.toString(), '部门甲,17\n部门乙,29\n');
  const create = {operation: 'task.create', key: 'task', body: {intent: '核对并交付业务程序', context: {inputRefs: [artifact.id]}}};
  const task = await f.call(create);
  const record = JSON.parse(f.read(tx => tx.projection('task', task.id)).bytes);
  assert.deepEqual(record.inputArtifacts, [artifact]);
  assert.notEqual(record.inputDigest, artifact.digest);
  f.reopen();
  assert.deepEqual(await f.call(req), artifact); assert.deepEqual(await f.call(create), task);
  assert.deepEqual((await f.call({operation: 'artifact.content', artifactId: artifact.id})).content, content.content);
  assert.equal(f.read(tx => tx.commands()).length, 1);
  await assert.rejects(f.call({...req, body: {...req.body, name: 'different'}}), fail('idempotency_conflict', 409));
  // Same bytes under a distinct upload are distinct manifests, one blob.
  const next = await f.call(upload('next')); assert.notEqual(next.id, artifact.id);
  assert.equal(f.read(tx => tx.projections('artifact')).length, 3);
});

test('unknown/duplicate refs and malformed uploads never create Task or publish objects', async t => {
  const f = fixture(t);
  for (const refs of [['unknown'], ['same', 'same']]) {
    await assert.rejects(f.call({operation: 'task.create', key: 'bad', body: {intent: 'test', context: {inputRefs: refs}}}));
  }
  for (const change of [{contentBase64: 'Zh=='}, {contentBase64: '!!!!'}, {mediaType: 'text/plain\n'},
    {name: ''}, {path: '/private/input'}, {contentBase64: Buffer.alloc(262145).toString('base64')}]) {
    await assert.rejects(f.call({...upload(), body: {...upload().body, ...change}}), fail('invalid_request', 400));
  }
  assert.equal(f.read(tx => tx.projections('task')).length, 0);
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  assert.equal(f.read(tx => tx.commands()).length, 0);
});

test('committed missing bytes are never repaired by same-key replay or different-key reupload', async t => {
  const f = fixture(t), req = upload(), artifact = await f.call(req);
  const names = fs.readdirSync(f.objects);
  const blob = names.find(name => name.includes(artifact.digest.slice(7)));
  assert.ok(blob); fs.unlinkSync(path.join(f.objects, blob));
  assert.deepEqual(await f.call(req), artifact, 'original receipt remains historical');
  for (const operation of ['artifact.get', 'artifact.content']) {
    await assert.rejects(f.call({operation, artifactId: artifact.id}), fail('application_unavailable', 503));
  }
  await assert.rejects(f.call(upload('repair')), fail('application_unavailable', 503));
  await assert.rejects(f.call({operation: 'task.create', key: 'task', body: {intent: 'test', context: {inputRefs: [artifact.id]}}}), fail('application_unavailable', 503));
  assert.equal(fs.existsSync(path.join(f.objects, blob)), false);
  assert.equal(f.read(tx => tx.projections('task')).length, 0);
});

test('failed SQL commit leaves only orphan bytes, never a manifest or receipt', async t => {
  const f = fixture(t), req = upload();
  const write = t.mock.method(f.store, 'write', () => { throw new Error('private-storage-detail'); });
  await assert.rejects(f.call(req), fail('application_unavailable', 503));
  write.mock.restore();
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  assert.equal(f.app.replay(req), null);
  assert.ok(fs.readdirSync(f.objects).length > 1);
  const accepted = await f.call(req); assert.equal(accepted.status, 'ready');
  assert.equal(f.read(tx => tx.projections('artifact')).length, 2);
});

test('depot identity/content lies and stale owner cannot publish success', async t => {
  const f = fixture(t);
  const put = t.mock.method(f.depot, 'put', () => ({digest: 'sha256:' + '0'.repeat(64), bytes: 0}));
  await assert.rejects(f.call(upload()), fail('application_unavailable', 503)); put.mock.restore();
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  const artifact = await f.call(upload());
  const get = t.mock.method(f.depot, 'get', () => Buffer.from('wrong'));
  await assert.rejects(f.call({operation: 'artifact.content', artifactId: artifact.id}), fail('application_unavailable', 503)); get.mock.restore();
  const oldApp = f.app; f.reopen();
  await assert.rejects(oldApp.dispatch(upload('later'), context), fail('application_unavailable', 503));
});

test('real HTTP + TaskClient upload, input binding and verified binary download', {timeout: 10000}, async t => {
  const f = fixture(t), token = 'public-artifacts-http-test-token-0000';
  let handler;
  const server = createServer((req, res) => void handler(req, res));
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(async () => { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); });
  const expectedHost = '127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({application: f.app.dispatch, token, expectedHost});
  const client = new TaskClient({baseURL: 'http://' + expectedHost, token});
  const artifact = await client.request('input.create', {body: upload().body, idempotencyKey: 'http-upload'});
  const downloaded = await client.downloadArtifact(artifact.id);
  assert.deepEqual(downloaded.content, Buffer.from(upload().body.contentBase64, 'base64'));
  const created = await client.createTask({intent: '交付业务程序', context: {inputRefs: [artifact.id]}}, 'http-task');
  assert.equal(created.status, 'draft');
  assert.equal(f.read(tx => tx.commands()).length, 1);
});
