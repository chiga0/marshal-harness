import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {setImmediate as turn} from 'node:timers/promises';
import {Store, encode, digest} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication, createVerificationPort} from './application.mjs';
import {TaskSupervisor} from '../task-supervisor/controller.mjs';
import {validate} from '../task-api/contract.mjs';
import {createFileBusiness} from '../task-business/index.mjs';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const fileDigest = value => digest(Buffer.from(JSON.stringify(value)));
const deferred = () => { let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve}; };
const proposal = () => ({summary: '程序与文档并行，固定验收完整交付', nodes: ['code', 'docs', 'verify'].map(id => ({id,
  role: id === 'verify' ? 'verifier' : 'author', goal: '完成' + id, scope: [id], providerId: null})),
edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}], deliverables: ['全部程序和文档'], acceptance: ['原始业务约束不变'], assumptions: []});
const binding = () => ({nodeId: 'verify', description: '分别核对 code 和 docs 内容并保留两个完整分支。',
  layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
  deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});
const fact = ticket => ({executionId: 'fixture-' + ticket.workerId, startedAt: new Date().toISOString()});

// Real Store + depot. Only the explicitly labelled checker/cleanup is a controlled
// fixture; these tests do not claim a live model, real subprocess or business oracle.
function fixture(t, options = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-verification-')));
  const root = path.join(parent, 'state'), objects = path.join(parent, 'objects');
  let store = Store.create(root), depot = ArtifactDepot.create(objects), owner = store.claimOwner(0, 'fixture', Date.now() + 3600000);
  let failSQL = false, advance = 0, calls = [];
  const clock = () => Date.now() + advance;
  const port = createVerificationPort({id: 'trusted-checker', policy: {id: 'fixture-policy', version: '1', description: '真实策略须独立执行固定断言；本测试用受控检查器。'},
    bindPlan: options.bindPlan ?? binding, start: options.start ?? function ({ticket}) {
      const started = fact(ticket), completion = deferred();
      const item = {ticket, started, completion, stopped: 0}; calls.push(item);
      return {started: Promise.resolve(started), completion: completion.promise, stop() {item.stopped++; return completion.promise;}};
    }});
  const wrapper = {read: (...args) => store.read(...args), write(owner, callback) {
    return store.write(owner, tx => {const result = callback(tx); if (failSQL) throw Error('injected commit failure'); return result;});
  }};
  const execution = {maxWorkers: 2, providerIds: ['agent'], defaultProvider: 'agent'};
  let app = new TaskApplication({store: wrapper, owner, depot, execution, verification: port, clock});
  t.after(() => {store.close(); depot.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const f = {parent, objects, port, calls, get app() {return app;}, get depot() {return depot;}, get execution() {return app.execution;},
    call: request => app.dispatch(request, context), read: callback => store.read(owner, callback),
    get: taskId => app.dispatch({operation: 'task.get', taskId}, context),
    capacity: () => store.read(owner, tx => app.execution.capacity(tx).value.active),
    commands: () => store.read(owner, tx => tx.commands()), setSQLFailure(value) {failSQL = value;}, advance(ms) {advance += ms;},
    async cancel(taskId) {const task = await f.get(taskId); return f.call({operation: 'task.cancel', taskId, key: 'cancel', body: {expectedRevision: task.revision}});},
    async plan() {
      const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '交付所有要求，不能只返回末 patch', limits: {timeoutMs: 60000, maxAttempts: 8, maxWorkers: 2}}});
      const plan = app.proposePlan(task.id, task.revision, proposal()), current = await f.get(task.id);
      const operation = await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: current.revision,
        planRevision: plan.revision, planDigest: plan.digest}});
      const dispatch = f.commands().find(item => JSON.parse(item.payload).action === 'dispatch');
      app.execution.expandDispatch(dispatch.id, dispatch.revision); return {taskId: task.id, plan, operation};
    },
    take(nodeId) {const command = f.commands().find(item => JSON.parse(item.payload).nodeId === nodeId); return app.execution.nextWork(command.id, command.revision);},
    candidate(ticket) {
      const file = {path: ticket.nodeId + '.txt', ...depot.put(Buffer.from(ticket.nodeId + ' bytes'))};
      return {profile: 'task-file-business/v1', taskId: ticket.taskId, nodeId: ticket.nodeId, workerId: ticket.workerId,
        planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest,
        layoutDigest: hash({profile: 'task-file-business/v1', ...ticket.input.fileLayout}), files: [file],
        inputDigest: fileDigest([]), manifestDigest: fileDigest([file]), report: '作者报告不是验收'};
    },
    finishAuthor(ticket, change = {}) {const started = fact(ticket); app.execution.started(ticket, started);
      return app.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true}, result: {...f.candidate(ticket), ...change}});},
    async ready() {const planned = await f.plan(), code = f.take('code'), docs = f.take('docs');
      assert.equal(f.take('verify'), null); f.finishAuthor(code); assert.equal(f.take('verify'), null); f.finishAuthor(docs);
      return {...planned, code, docs, ticket: f.take('verify')};},
    start(ticket) {const handle = port.start({ticket, prepared: {cwd: parent, prompt: 'trusted fixture'}}), item = calls.at(-1);
      app.execution.started(ticket, item.started); return {handle, item};},
    async receipt(ticket, overrides = {}) {const {handle, item} = f.start(ticket);
      item.completion.resolve({type: 'verification', status: 'passed', cleanup: {started: item.started, cleaned: true, scope: 'controlled-fixture'},
        evidence: {name: 'verification.json', mediaType: 'application/json', content: Buffer.from('{"fixture":true}')},
        delivery: {name: 'full.bundle', mediaType: 'application/octet-stream', content: Buffer.from('code bytes\ndocs bytes')}, ...overrides});
      return handle.completion;},
    changeWorker(ticket, edit) {app.transaction(true, tx => {const {row, record, task} = app.execution.ticket(tx, ticket);
      edit(record); task.task.revision++; const source = app.save(tx, task, 'fixture.worker-drift'); app.execution.putWorker(tx, row, record, source);});},
    reopen(verification = port) {store.close(); depot.close(); store = Store.openExisting(root); depot = ArtifactDepot.openExisting(objects);
      owner = store.claimOwner(owner.generation, 'reopened', Date.now() + 3600000);
      app = new TaskApplication({store: wrapper, owner, depot, execution, verification, clock});}
  }; return f;
}

test('approved policy/layout + both branches bind one trusted reservation, Decision/artifacts/completed commit and cold replay', async t => {
  const f = fixture(t), {taskId, plan, operation, code, ticket} = await f.ready();
  assert.equal(validate(plan, 'Plan'), true); assert.ok(plan.acceptance.some(text => text.includes('fixture-policy')));
  assert.ok(plan.acceptance.some(text => text.includes('docs.txt'))); assert.ok(plan.acceptance.includes('原始业务约束不变'));
  assert.equal(ticket.executionType, 'verification'); assert.equal(ticket.input.verification.manifests.length, 2);
  assert.equal(ticket.input.fileLayout.inputs[0].source.workerId, code.workerId);
  assert.equal(f.execution.approvedLayout(ticket).layoutDigest, hash({profile: 'task-file-business/v1', ...ticket.input.fileLayout}));
  const result = await f.receipt(ticket); assert.deepEqual(f.execution.observeExecution(ticket), result.cleanup.started);
  assert.equal(f.execution.finish(ticket, result).status, 'completed'); assert.equal(f.capacity().length, 0);
  const task = await f.get(taskId), audit = await f.call({operation: 'task.audit', taskId});
  assert.equal(task.status, 'completed'); assert.equal(task.artifactIds.length, 2); assert.equal(audit.acceptance.status, 'passed');
  assert.equal(validate(audit, 'Audit'), true); assert.equal((await f.call({operation: 'operation.get', operationId: operation.id})).status, 'succeeded');
  const artifacts = await Promise.all(task.artifactIds.map(artifactId => f.call({operation: 'artifact.content', artifactId})));
  assert.equal(artifacts.find(item => item.artifact.kind === 'delivery').content.toString(), 'code bytes\ndocs bytes');
  const before = f.read(tx => tx.head(taskId)); f.execution.finish(ticket, result); assert.deepEqual(f.read(tx => tx.head(taskId)), before);
  f.reopen(null); assert.deepEqual(await f.get(taskId), task); // Durable facts query without any fresh checker capability.
  for (const artifact of artifacts) assert.deepEqual(await f.call({operation: 'artifact.content', artifactId: artifact.artifact.id}), artifact);
  assert.deepEqual((await f.call({operation: 'task.audit', taskId})).acceptance, audit.acceptance);
});

test('role/provider/pass, cloned receipt and different ticket cannot self-accept or publish', async t => {
  const f = fixture(t), {taskId, ticket} = await f.ready(), result = await f.receipt(ticket), before = f.read(tx => tx.head(taskId));
  for (const forged of [{...result, receipt: {}}, structuredClone(result), {...result, cleanup: {...result.cleanup, cleaned: false}}])
    assert.throws(() => f.execution.finish(ticket, forged), error => error.code === 'invalid_verification_receipt');
  assert.throws(() => f.execution.finish({...ticket, reservationDigest: 'sha256:' + '0'.repeat(64)}, result));
  assert.deepEqual(f.read(tx => tx.head(taskId)), before); assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  f.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', result: {pass: true, reviewer: 'system'}, cleanup: result.cleanup});
  assert.notEqual((await f.get(taskId)).status, 'completed'); assert.equal(f.capacity().length, 0);
});

test('cancel during verifier fences immediately, keeps capacity until ORIGINAL cleanup, never accepts late pass', async t => {
  const f = fixture(t), {taskId, ticket} = await f.ready(), {handle, item} = f.start(ticket);
  const operation = await f.cancel(taskId); assert.equal(f.capacity().length, 1); assert.equal((await f.get(taskId)).status, 'cancelling');
  assert.equal((await f.call({operation: 'operation.get', operationId: operation.id})).status, 'accepted');
  item.completion.resolve({type: 'verification', status: 'passed', cleanup: {started: item.started, cleaned: true}, evidence: null, delivery: null});
  assert.equal(f.execution.finish(ticket, await handle.completion).status, 'cancelled'); f.execution.reconcile(taskId);
  assert.equal(f.capacity().length, 0); assert.equal((await f.get(taskId)).status, 'cancelled');
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
});

test('unknown cleanup and old owner retain capacity; no recovery redispatch or new-generation receipt', async t => {
  const f = fixture(t), {taskId, ticket} = await f.ready(), {handle, item} = f.start(ticket);
  item.completion.resolve({type: 'verification', status: 'passed', cleanup: {started: item.started, cleaned: false}});
  const result = await handle.completion;
  assert.equal(f.execution.finish(ticket, result).status, 'unknown'); assert.equal((await f.get(taskId)).status, 'intervention');
  f.reopen(); assert.throws(() => f.execution.finish(ticket, result), error => error.code === 'recovery_required');
  assert.equal(f.capacity().length, 1); assert.equal(f.execution.reconcile(taskId).status, 'intervention');
  assert.equal(f.take('verify'), null);
});

test('candidate source drift fails before any artifact/Decision commit', async t => {
  const f = fixture(t), {taskId, code, ticket} = await f.ready(), result = await f.receipt(ticket);
  f.changeWorker(code, record => {record.candidate.files[0].digest = 'sha256:' + 'b'.repeat(64);});
  assert.throws(() => f.execution.finish(ticket, result), error => error.code === 'candidate_manifest_conflict');
  assert.equal((await f.get(taskId)).status, 'running'); assert.equal(f.capacity().length, 1);
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
});

test('missing source bytes or expired original deadline cannot publish a final delivery', async t => {
  for (const scenario of ['missing', 'deadline']) await t.test(scenario, async t => {
    const f = fixture(t), {taskId, ticket} = await f.ready(), result = await f.receipt(ticket);
    if (scenario === 'missing') {
      const file = ticket.input.verification.manifests[0].manifest.files[0];
      const object = fs.readdirSync(f.objects).find(name => name.includes(file.digest.slice(7)));
      assert.ok(object); fs.unlinkSync(path.join(f.objects, object));
      assert.throws(() => f.execution.finish(ticket, result), error => error.code === 'application_unavailable');
    } else {
      f.advance(60001); assert.equal(f.execution.finish(ticket, result).status, 'failed');
    }
    assert.notEqual((await f.get(taskId)).status, 'completed'); assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  });
});

test('SQL failure after durable bytes rolls back Decision/artifacts/capacity; exact original receipt can commit once', async t => {
  const f = fixture(t), {taskId, ticket} = await f.ready(), result = await f.receipt(ticket), head = f.read(tx => tx.head(taskId));
  f.setSQLFailure(true); assert.throws(() => f.execution.finish(ticket, result), error => error.code === 'application_unavailable');
  assert.deepEqual(f.read(tx => tx.head(taskId)), head); assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  assert.equal(f.capacity().length, 1); assert.equal((await f.get(taskId)).status, 'running');
  const orphan = Buffer.from('code bytes\ndocs bytes');
  assert.deepEqual(f.depot.get({digest: digest(orphan), bytes: orphan.length}), orphan, 'durable orphan is not a published artifact');
  f.setSQLFailure(false); f.execution.finish(ticket, result); assert.equal((await f.get(taskId)).status, 'completed');
});

test('unsupported layout/omitted branch never shrinks proposal into an approvable plan', async t => {
  const f = fixture(t, {bindPlan() {const result = binding(); result.deliveries.pop(); return result;}});
  const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '保留两分支'}});
  assert.throws(() => f.app.proposePlan(task.id, task.revision, proposal()), error => error.code === 'unsupported_task');
  assert.equal((await f.get(task.id)).plan, null); assert.equal((await f.get(task.id)).revision, 1);
});

test('actual FileBusiness candidate producer, original observed execution and resolved approved inputs feed final verification', async t => {
  const f = fixture(t), {taskId} = await f.plan(), executionParent = path.join(f.parent, 'executions');
  fs.mkdirSync(executionParent, {mode: 0o700});
  const business = createFileBusiness({parent: executionParent, depot: f.depot, layoutFor: ticket => ticket.input.fileLayout,
    approvedLayout: ticket => f.execution.approvedLayout(ticket), observeExecution: ticket => f.execution.observeExecution(ticket)});
  t.after(() => business.close());
  for (const nodeId of ['code', 'docs']) {
    const ticket = f.take(nodeId), context = {signal: new AbortController().signal, deadline: ticket.deadline};
    const prepared = await business.prepare(ticket, context), started = fact(ticket); f.execution.started(ticket, started);
    // Simulated author file activity; all collection, source identity, Depot and
    // Core admission below are the actual production producers/consumers.
    fs.writeFileSync(path.join(prepared.cwd, nodeId + '.txt'), nodeId + ' actual files', {mode: 0o600});
    const result = {providerId: 'agent', status: 'completed', stopReason: 'end_turn', outputText: '真实采集夹具，非业务验收',
      cleanup: {started, cleaned: true, scope: 'controlled-fixture'}};
    const collected = await business.collect(ticket, result, context);
    assert.equal(f.execution.finish(ticket, {...result, ...collected}).status, 'completed');
  }
  const ticket = f.take('verify'), prepared = await business.prepare(ticket, {signal: new AbortController().signal, deadline: ticket.deadline});
  assert.equal(fs.readFileSync(path.join(prepared.cwd, 'code.txt'), 'utf8'), 'code actual files');
  assert.equal(fs.readFileSync(path.join(prepared.cwd, 'docs.txt'), 'utf8'), 'docs actual files');
  const result = await f.receipt(ticket); f.execution.finish(ticket, result); business.release(ticket); business.release(ticket);
  assert.equal((await f.get(taskId)).status, 'completed');
});

test('Supervisor owns verifier in SAME lane, skips agent collect, preserves receipt identity and always releases FD hook', async t => {
  const f = fixture(t), {taskId} = await f.plan(), released = [], errors = [], authors = [];
  const supervisor = new TaskSupervisor({execution: f.execution, verification: f.port,
    providers: new Map([['agent', {id: 'agent', start(options) {
      const ticket = JSON.parse(options.prompt), started = fact(ticket); authors.push(ticket);
      return {started: Promise.resolve(started), completion: Promise.resolve({providerId: 'agent', status: 'completed', stopReason: 'end_turn',
        cleanup: {started, cleaned: true}}), stop() {}};
    }}]]), prepare: ticket => ({cwd: f.parent, prompt: JSON.stringify(ticket)}),
    collect(ticket) {assert.equal(ticket.executionType, 'agent'); return {result: f.candidate(ticket)};},
    release: ticket => released.push(ticket.workerId), onError: error => errors.push(error), intervalMs: 5});
  t.after(() => supervisor.close()); supervisor.start();
  const until = async predicate => {const deadline = Date.now() + 3000; while (!await predicate()) {assert.ok(Date.now() < deadline); await turn();}};
  await until(() => f.calls.length === 1); assert.equal(authors.length, 2); assert.equal(f.capacity().length, 1);
  const item = f.calls[0]; item.completion.resolve({type: 'verification', status: 'passed', cleanup: {started: item.started, cleaned: true},
    evidence: {name: 'evidence.json', mediaType: 'application/json', content: Buffer.from('{}')},
    delivery: {name: 'delivery.bin', mediaType: 'application/octet-stream', content: Buffer.from('complete controlled bundle')}});
  await until(async () => (await f.get(taskId)).status === 'completed'); await supervisor.close();
  assert.equal(released.length, 3); assert.equal(f.capacity().length, 0); assert.deepEqual(errors, []);
});

test('Supervisor cancellation during verifier bootstrap stops original handle and waits for original cleanup before FD release', async t => {
  const start = deferred(), completion = deferred(), released = []; let captured, stopped = 0;
  const f = fixture(t, {start({ticket}) {
    captured = ticket; return {started: start.promise, completion: completion.promise, stop() {stopped++; return completion.promise;}};
  }}), {taskId} = await f.plan(); f.finishAuthor(f.take('code')); f.finishAuthor(f.take('docs'));
  const supervisor = new TaskSupervisor({execution: f.execution, verification: f.port, providers: new Map(),
    prepare: () => ({cwd: f.parent, prompt: 'only trusted checker'}), collect() {assert.fail('verifier must not collect as Agent');},
    release: ticket => released.push(ticket.workerId), intervalMs: 5});
  const until = async predicate => {const deadline = Date.now() + 3000; while (!await predicate()) {assert.ok(Date.now() < deadline); await turn();}};
  supervisor.start(); await until(() => captured); await f.cancel(taskId); await supervisor.tick();
  assert.ok(stopped > 0); assert.equal(f.capacity().length, 1); assert.deepEqual(released, []);
  let closed = false; const closing = supervisor.close().then(value => {closed = true; return value;});
  await turn(); assert.equal(closed, false); const started = fact(captured); start.resolve(started);
  completion.resolve({type: 'verification', status: 'failed', cleanup: {started, cleaned: true}, evidence: null, delivery: null});
  assert.equal((await closing).clean, true); assert.equal(f.capacity().length, 0); assert.equal(released.length, 1);
  assert.equal((await f.get(taskId)).status, 'cancelled'); assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
});
