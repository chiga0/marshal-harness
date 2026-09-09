import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath} from 'node:url';
import {Store, REPAIR_FORMAT, CUSTODY_FORMAT, INTERACTION_FORMAT, encode, digest} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication, createRepairPort, createVerificationPort} from './application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {createServer} from 'node:http';
import {once} from 'node:events';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const checkerPath = fileURLToPath(new URL('./repair-checker.fixture.mjs', import.meta.url));
const proposal = {summary: '两分支与独立验收', nodes: ['code', 'docs', 'verify'].map(id => ({id,
  role: id === 'verify' ? 'verifier' : 'author', goal: '完成 ' + id, scope: [id], providerId: null})),
edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}], deliverables: ['全部输出'], acceptance: ['内容与结构都正确'], assumptions: []};
const bindPlan = () => ({nodeId: 'verify', description: '内容错误可修，结构错误不可修。',
  layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
  deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});

function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-repair-test-')));
  const root = path.join(parent, 'store'), artifactRoot = path.join(parent, 'objects'), executionRoot = path.join(parent, 'executions');
  fs.mkdirSync(executionRoot, {mode: 0o700});
  let store = Store.create(root, {format: REPAIR_FORMAT}), owner = store.claimOwner(0, 'fixture', Date.now() + 3600000);
  const depot = ArtifactDepot.create(artifactRoot); let failSQL = false, advance = 0;
  const wrapper = {info: () => store.info(), read: (...args) => store.read(...args), write: (owner, fn) => store.write(owner, tx => {
    const value = fn(tx); if (failSQL) throw Error('fixture-rollback'); return value;
  })};
  const repair = createRepairPort({policy: {id: 'fixture-repair', version: '1', description: '只修业务内容'}, nodeIds: ['code', 'docs'], assertions: ['business']});
  const policy = {id: 'fixture-verifier', version: '1', description: '固定原始内容与结构断言'};
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: hash(policy), assertions: [{name: 'business', validate: actual => actual === true}, {name: 'structure', validate: actual => actual === true}],
    repair: {policyDigest: repair.policyDigest, assertions: ['business']},
    delivery: ({prepared}) => ({name: 'all.txt', mediaType: 'text/plain', content: Buffer.concat(['code', 'docs'].map(n => fs.readFileSync(path.join(prepared.cwd, n + '.txt'))))})});
  const verification = createVerificationPort({id: 'checker', policy, bindPlan, start: command.start, repairPolicyDigests: [repair.policyDigest]});
  const config = {store: wrapper, depot, verification, repair, clock: () => Date.now() + advance,
    execution: {maxWorkers: 2, providerIds: ['fixture-agent'], defaultProvider: 'fixture-agent'}};
  let app = new TaskApplication({...config, owner});
  const business = createFileBusiness({parent: executionRoot, depot, layoutFor: ticket => ticket.input.fileLayout,
    approvedLayout: ticket => app.execution.approvedLayout(ticket), observeExecution: ticket => app.execution.observeExecution(ticket)});
  t.after(() => {business.close(); depot.close(); store.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const f = {parent, root, repair, get app() {return app;}, get store() {return store;}, get owner() {return owner;},
    call: request => app.dispatch(request, context), get: taskId => app.dispatch({operation: 'task.get', taskId}, context),
    commands: () => app.transaction(false, tx => tx.commands()), head: taskId => app.transaction(false, tx => tx.head(taskId)),
    setSQLFailure(value) {failSQL = value;}, advance(ms) {advance += ms;},
    take(nodeId) {const c = f.commands().find(c => c.status === 'pending' && JSON.parse(c.payload).nodeId === nodeId); assert.ok(c); return app.execution.nextWork(c.id, c.revision);},
    async setup() {
      const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '完整任务：仅修错误分支', limits: {timeoutMs: 60000, maxAttempts: 6, maxWorkers: 2}}});
      const c = f.commands().find(c => JSON.parse(c.payload).action === 'plan'), ticket = app.execution.nextWork(c.id, c.revision);
      assert.equal(ticket.repairId, null);
      const started = {executionId: 'fixture-planner', startedAt: new Date().toISOString()}; app.execution.started(ticket, started);
      app.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true}, plan: proposal});
      const current = await f.get(task.id), plan = await f.call({operation: 'task.plan', taskId: task.id});
      await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}});
      const dispatch = f.commands().find(c => JSON.parse(c.payload).action === 'dispatch'); app.execution.expandDispatch(dispatch.id, dispatch.revision);
      return {taskId: task.id, plan};
    },
    async author(ticket, content) {
      const ctx = {signal: new AbortController().signal, deadline: ticket.deadline}, prepared = await business.prepare(ticket, ctx);
      const started = {executionId: 'fixture-' + ticket.workerId, startedAt: new Date().toISOString()}; app.execution.started(ticket, started);
      fs.writeFileSync(path.join(prepared.cwd, ticket.nodeId + '.txt'), content, {mode: 0o600});
      const result = {providerId: ticket.providerId, status: 'completed', stopReason: 'end_turn', outputText: 'fixture simulated author', cleanup: {started, cleaned: true}};
      const collected = await business.collect(ticket, result, ctx);
      app.execution.finish(ticket, {...result, ...collected}); return prepared;
    },
    async verify() {
      const ticket = f.take('verify'), prepared = await business.prepare(ticket, {signal: new AbortController().signal, deadline: ticket.deadline});
      const handle = verification.start({ticket, prepared}); t.after(() => handle.stop());
      app.execution.started(ticket, await handle.started); const result = await handle.completion;
      app.execution.finish(ticket, result); business.release(ticket); app.execution.reconcile(ticket.taskId);
      for (const c of f.commands().filter(c => c.status === 'pending')) app.execution.settleControl(c.id, c.revision);
      return {ticket, result};
    },
    async rejected() {const ready = await f.setup(), code = f.take('code'), docs = f.take('docs');
      await f.author(code, 'wrong'); await f.author(docs, 'retained'); await f.verify();
      const task = await f.get(ready.taskId), audit = await f.call({operation: 'task.audit', taskId: ready.taskId});
      assert.equal(task.status, 'failed'); assert.ok(audit.decision.contentRejection); assert.deepEqual(task.allowedActions, ['repair']);
      return {...ready, code, docs, task, audit};},
    request(value) {return {operation: 'task.repair', taskId: value.taskId, key: 'repair', body: {expectedRevision: value.task.revision,
      planDigest: value.plan.digest, decisionDigest: value.audit.decision.digest, nodeIds: ['code'], feedback: '按原业务断言修正为正确结果'}};},
    reopen() {store.close();
      for (const options of [{}, {format: CUSTODY_FORMAT}, {format: INTERACTION_FORMAT}]) assert.throws(() => Store.openExisting(root, options));
      store = Store.openExisting(root, {format: REPAIR_FORMAT}); assert.equal(store.info().generation, owner.generation);
      owner = store.claimOwner(store.info().generation, 'reopened', Date.now() + 3600000);
      app = new TaskApplication({...config, owner});}
  }; return f;
}

test('real negative command report -> same-plan repair -> retained source + new files -> accepted delivery and cold receipt', {timeout: 20000}, async t => {
  const f = fixture(t), original = await f.rejected(), request = f.request(original), before = f.head(original.taskId);
  const receipt = await f.call(request); assert.equal(receipt.operation.status, 'accepted'); assert.equal(receipt.acceptedRevision, original.task.revision + 1);
  assert.deepEqual(receipt.affectedNodes, ['code', 'verify']);
  assert.equal((await f.get(original.taskId)).deadlineAt, original.task.deadlineAt);
  const after = f.head(original.taskId), replay = await f.call(request); assert.equal(replay.replayed, true); assert.deepEqual(after, f.head(original.taskId));
  assert.notDeepEqual(before, after);
  const code = f.take('code'); assert.equal(code.repairId, receipt.repairId); assert.equal(code.input.repair.evidence.id, original.audit.acceptance.evidenceIds[0]);
  const prepared = await f.author(code, 'correct'); assert.ok(prepared.prompt.includes(request.body.feedback)); assert.ok(prepared.prompt.includes('originalNegativeReport'));
  const {ticket} = await f.verify(); assert.equal(ticket.input.verification.manifests.find(m => m.nodeId === 'docs').workerId, original.docs.workerId);
  const task = await f.get(original.taskId), audit = await f.call({operation: 'task.audit', taskId: task.id});
  assert.equal(task.status, 'completed'); assert.equal(audit.attempts, 6); assert.equal(audit.reworkCount, 1); assert.equal(audit.acceptance.status, 'passed');
  assert.equal((await f.call({operation: 'operation.get', operationId: receipt.operation.id})).status, 'succeeded');
  assert.equal(audit.decision.contentRejection, null); assert.equal(audit.workers.filter(w => w.nodeId === 'docs').length, 1);
  const artifactId = audit.decision.artifacts.find(a => a.kind === 'delivery').id;
  assert.equal((await f.call({operation: 'artifact.content', artifactId})).content.toString(), 'correctretained');
  f.reopen(); assert.deepEqual(await f.get(task.id), task); assert.equal((await f.call(request)).replayed, true);
  assert.equal((await f.call({operation: 'artifact.content', artifactId})).content.toString(), 'correctretained');
});

test('repair exact CAS/key, budget, unknown roots and SQL rollback never mutate old facts or reserve budget', {timeout: 20000}, async t => {
  const f = fixture(t), original = await f.rejected(), request = f.request(original), before = f.head(original.taskId);
  for (const body of [{...request.body, expectedRevision: 1}, {...request.body, nodeIds: ['verify']}, {...request.body, nodeIds: ['code','docs']},
    {...request.body, feedback: '\0'}, {...request.body, decisionDigest: 'sha256:' + 'f'.repeat(64)}]) await assert.rejects(f.call({...request, body}));
  assert.deepEqual(before, f.head(original.taskId));
  f.setSQLFailure(true); await assert.rejects(f.call(request)); f.setSQLFailure(false); assert.deepEqual(before, f.head(original.taskId));
  const receipt = await f.call(request); await assert.rejects(f.call({...request, body: {...request.body, feedback: 'different'}}), {code: 'idempotency_conflict'});
  assert.equal((await f.call({operation: 'task.audit', taskId: original.taskId})).attempts, 4);
  f.reopen(); assert.equal(f.app.execution.reconcile(original.taskId).status, 'failed');
  assert.equal((await f.call({operation: 'operation.get', operationId: receipt.operation.id})).status, 'failed');
  assert.equal(f.commands().some(c => c.status !== 'observed'), false); assert.equal((await f.call(request)).replayed, true);
});

test('actual HTTP null-prototype repair body and TaskClient original receipt identity traverse same Application', {timeout: 20000}, async t => {
  const f = fixture(t), original = await f.rejected(); let handler;
  const server = createServer((req, res) => void handler(req, res)); t.after(() => {server.closeAllConnections(); server.close();});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const host = '127.0.0.1:' + server.address().port, token = 'fixture-repair-token-not-a-production-secret';
  handler = createTaskApiHandler({application: f.app.dispatch, token, expectedHost: host});
  const client = new TaskClient({baseURL: 'http://' + host, token}), request = f.request(original);
  const receipt = await client.repairTask(original.taskId, request.body, request.key);
  assert.equal(receipt.replayed, false); assert.equal((await client.repairTask(original.taskId, request.body, request.key)).replayed, true);
  assert.equal((await client.getTask(original.taskId)).status, 'queued');
});
