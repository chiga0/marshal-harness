import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {Store} from '../task-store/store.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {TaskApplication} from './application.mjs';

const token = 'public-fixture-only-http-application-test-token';
async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-http-sqlite-')));
  const root = path.join(parent, 'state');
  let store = Store.create(root), owner = store.claimOwner(0, 'http-server-1', Date.now() + 3600000);
  let app = new TaskApplication({store, owner}), server, base;
  async function openHttp() {
    let handler;
    server = createServer((req, res) => { void handler(req, res); });
    server.listen(0, '127.0.0.1'); await once(server, 'listening');
    const expectedHost = '127.0.0.1:' + server.address().port;
    base = 'http://' + expectedHost;
    handler = createTaskApiHandler({application: app.dispatch, token, expectedHost});
  }
  async function closeHttp() {
    if (!server?.listening) return;
    server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
  t.after(async () => { await closeHttp(); store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  await openHttp();
  return {get app() { return app; },
    async request(route, body, key) {
      const headers = {authorization: 'Bearer ' + token};
      if (body !== undefined) { headers['content-type'] = 'application/json'; headers['idempotency-key'] = key; }
      const response = await fetch(base + route, {method: body === undefined ? 'GET' : 'POST', headers,
        ...(body !== undefined ? {body: JSON.stringify(body)} : {}), signal: AbortSignal.timeout(5000)});
      return {status: response.status, body: await response.json()};
    },
    countCommands() { return store.read(owner, tx => tx.commands()).length; },
    async restart() {
      await closeHttp(); store.close(); store = Store.openExisting(root);
      owner = store.claimOwner(owner.generation, 'http-server-2', Date.now() + 3600000);
      app = new TaskApplication({store, owner}); await openHttp();
    }};
}
const input = {intent: '交付一个业务数据核对程序及说明', limits: {timeoutMs: 60000, maxAttempts: 8, maxWorkers: 2}};
const proposal = {summary: '计算与输出并行实现，独立核对',
  nodes: [{id: 'compute', role: 'author', goal: '实现计算程序', scope: ['compute'], providerId: null},
    {id: 'output', role: 'author', goal: '实现输出程序', scope: ['output'], providerId: null},
    {id: 'verify', role: 'verifier', goal: '独立验证实际成果', scope: ['checks'], providerId: null}],
  edges: [{from: 'compute', to: 'verify'}, {from: 'output', to: 'verify'}],
  deliverables: ['程序及使用说明'], acceptance: ['独立运行检查'], assumptions: []};

test('real HTTP + real SQLite: confirmation, original Operation, DAG/events and cancel survive server reopen', {timeout: 15000}, async t => {
  const f = await fixture(t);
  const created = await f.request('/v1/tasks', input, 'create');
  assert.equal(created.status, 201); assert.equal(created.body.status, 'draft');
  const id = created.body.id, taskPath = '/v1/tasks/' + id;
  const plan = f.app.proposePlan(id, 1, proposal);
  const fetchedPlan = await f.request(taskPath + '/plan');
  assert.equal(fetchedPlan.status, 200); assert.deepEqual(fetchedPlan.body, plan);
  const approval = {expectedRevision: 2, planRevision: plan.revision, planDigest: plan.digest};
  const approved = await f.request(taskPath + '/plan/approve', approval, 'approve');
  assert.equal(approved.status, 202); assert.equal(approved.body.status, 'accepted');
  const opPath = '/v1/operations/' + approved.body.id;
  assert.deepEqual((await f.request(opPath)).body, approved.body);
  assert.equal((await f.request(taskPath + '/graph')).body.nodes.length, 3);
  const first = await f.request(taskPath + '/events?limit=2');
  assert.equal(first.status, 200); assert.equal(first.body.items.length, 2);
  const second = await f.request(taskPath + '/events?limit=2&cursor=' + first.body.nextCursor);
  assert.equal(second.status, 200); assert.equal(second.body.items.length, 1);
  const audit = await f.request(taskPath + '/audit');
  assert.equal(audit.status, 200); assert.equal(audit.body.usage.tokens, null);
  await f.restart();
  assert.deepEqual(await f.request(taskPath + '/plan/approve', approval, 'approve'), approved);
  assert.equal(f.countCommands(), 2);
  const cancelled = await f.request(taskPath + '/cancel', {expectedRevision: 3}, 'cancel');
  assert.equal(cancelled.status, 202); assert.equal(cancelled.body.status, 'accepted');
  await f.restart();
  assert.deepEqual(await f.request(taskPath + '/cancel', {expectedRevision: 3}, 'cancel'), cancelled);
  const current = await f.request(taskPath);
  assert.equal(current.status, 200); assert.equal(current.body.status, 'cancelling');
  assert.equal(f.countCommands(), 3);
  assert.equal((await f.request('/v1/tasks')).body.items.length, 1);
});

test('concurrent HTTP receipt replays create one Task; stale control races cannot dispatch twice', {timeout: 15000}, async t => {
  const f = await fixture(t);
  const replies = await Promise.all(Array.from({length: 12}, () => f.request('/v1/tasks', input, 'concurrent-create')));
  assert.ok(replies.every(reply => reply.status === 201));
  assert.ok(replies.every(reply => reply.body.id === replies[0].body.id));
  assert.equal(f.countCommands(), 1);
  const id = replies[0].body.id, taskPath = '/v1/tasks/' + id;
  const plan = f.app.proposePlan(id, 1, proposal);
  const approval = {expectedRevision: 2, planRevision: plan.revision, planDigest: plan.digest};
  const raced = await Promise.all(['a', 'b'].map(key => f.request(taskPath + '/plan/approve', approval, key)));
  assert.deepEqual(raced.map(result => result.status).sort(), [202, 409]);
  assert.equal(raced.find(result => result.status === 409).body.code, 'revision_conflict');
  assert.equal(f.countCommands(), 2);
});

test('real domain failures keep specified HTTP status instead of becoming storage 503', {timeout: 10000}, async t => {
  const f = await fixture(t);
  assert.equal((await f.request('/v1/tasks/missing')).status, 404);
  const created = await f.request('/v1/tasks', input, 'create');
  const taskPath = '/v1/tasks/' + created.body.id;
  assert.equal((await f.request(taskPath + '/graph')).status, 409);
  const conflict = await f.request('/v1/tasks', {...input, intent: '不同任务'}, 'create');
  assert.equal(conflict.status, 409); assert.equal(conflict.body.code, 'idempotency_conflict');
  const questions = await f.request(taskPath + '/questions');
  assert.equal(questions.status, 200); assert.deepEqual(questions.body.items, []); assert.equal(questions.body.preview, null);
  const notImplemented = await f.request('/v1/workers/missing/cancel', {expectedRevision: 1}, 'worker-cancel');
  assert.equal(notImplemented.status, 501); assert.equal(notImplemented.body.code, 'unsupported_operation');
  assert.equal(f.countCommands(), 1);
});
