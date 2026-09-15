import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {Store, encode, digest} from '../task-store/store.mjs';
import {TaskApplication, createClarificationPort} from './application.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {contract, validate} from '../task-api/contract.mjs';

const principal = {principal: 'local-operator'}, hash = value => digest(encode(value));
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
const identity = name => ({id: name, version: '1', digest: hash({fixtureImplementation: name})});
const input = () => ({intent: '澄清：按指定语言和格式交付统计结果', limits: {timeoutMs: 60000, maxAttempts: 4, maxWorkers: 2}});
const plan = () => ({summary: '在固定 report.txt 中交付指定语言及数据格式，不改变权限与验收',
  nodes: [{id: 'author', role: 'author', goal: '根据冻结业务输入写统计结果', scope: ['report.txt'], providerId: null}],
  edges: [], deliverables: ['完整 report.txt'], acceptance: ['统计值正确，语言和内容格式符合冻结业务输入'], assumptions: []});
function template(hooks = {}, change = {}) {
  return createClarificationPort({template: identity('test-bounded-statistics'), applies: body => body.intent.startsWith('澄清：'),
    slots: ['language', 'format'].map(id => ({id, prompt: id === 'language' ? '输出使用 zh 还是 en？' : '内容使用 csv 还是 json？',
      validator: identity('validate-' + id), read(body) {
        const found = new RegExp('(?:^|\n)' + id + '=(zh|en|csv|json)(?:\n|$)').exec(body.context?.text ?? '');
        return found ? found[1] : null;
      }, validate: value => (id === 'language' ? ['zh', 'en'] : ['csv', 'json']).includes(value)})),
    renderer: {...identity('render-fixed-report'), render: async value => {hooks.renders = (hooks.renders ?? 0) + 1;
      await hooks.render?.(value); return hooks.proposal ? hooks.proposal(value) : plan();}}, ...change});
}
function fixture(t, options = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-clarification-'))), root = path.join(parent, 'state');
  let offset = 0, fault = false, store = Store.create(root), owner = store.claimOwner(0, 'questions', Date.now() + 3600000);
  const hooks = {}, port = options.port ?? template(hooks);
  const storePort = {read: (...args) => store.read(...args), write(owner, callback) {return store.write(owner, tx => {
    const result = callback(tx); if (fault) throw Error('controlled transaction failure'); return result;
  });}};
  let app = new TaskApplication({store: storePort, owner, clarification: port, clock: () => Date.now() + offset});
  t.after(() => {store.close(); fs.rmSync(parent, {recursive: true, force: true});});
  return {parent, root, hooks, port, get app() {return app;}, get store() {return store;},
    call: request => app.dispatch(request, principal), read: callback => store.read(owner, callback),
    fault(value) {fault = value;}, advance(value) {offset += value;},
    async create(body = input(), key = 'create') {return app.dispatch({operation: 'task.create', key, body}, principal);},
    questions(taskId) {return app.dispatch({operation: 'task.questions', taskId}, principal);},
    answer(taskId, questionId, body, key = 'answer') {return app.dispatch({operation: 'task.answer', taskId, questionId, body, key}, principal);},
    get(taskId) {return app.dispatch({operation: 'task.get', taskId}, principal);},
    reopen(clarification = port) {store.close(); store = Store.openExisting(root); owner = store.claimOwner(owner.generation, 'questions-reopen', Date.now() + 3600000);
      app = new TaskApplication({store: storePort, owner, clarification, clock: () => Date.now() + offset});},
  };
}
const answerBody = (questions, answer) => ({expectedRevision: questions.taskRevision, previewDigest: questions.previewDigest, questionRevision: 1, answer});
const bySlot = (questions, slot) => questions.items.find(item => item.slotId === slot).id;
const approvedBody = task => ({expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest});

test('one frozen batch, append-only answers/previews, one final approval and no planning/attempt obligation before approval', async t => {
  const f = fixture(t), task = await f.create(), original = await f.questions(task.id);
  assert.equal(task.status, 'awaiting-answer'); assert.deepEqual(task.allowedActions, ['answer', 'cancel']);
  assert.equal(original.items.length, 2); assert.equal(original.previewRevision, 1); assert.ok(validate(original, 'Questions'));
  assert.equal(original.preview.input.intent, input().intent); assert.equal(original.preview.missingSlots.length, 2);
  assert.ok(original.items.every(item => item.subject === original.previewDigest && item.revision === 1));
  assert.equal(f.read(tx => tx.commands()).length, 0); assert.equal((await f.call({operation: 'task.audit', taskId: task.id})).attempts, 0);
  await assert.rejects(f.call({operation: 'task.approve', taskId: task.id, key: 'too-early', body: approvedBody(task)}), {code: 'state_conflict'});
  const firstBody = answerBody(original, 'zh'), first = await f.answer(task.id, bySlot(original, 'language'), firstBody);
  assert.equal(first.acceptedRevision, 2); assert.equal(first.preview.revision, 2); assert.equal(first.task.status, 'awaiting-answer');
  const next = await f.questions(task.id); assert.equal(next.items[0].subject, original.items[0].subject);
  const second = await f.answer(task.id, bySlot(next, 'format'), answerBody(next, 'csv'), 'answer-format');
  assert.equal(second.task.status, 'awaiting-confirmation'); assert.deepEqual(second.task.allowedActions, ['approve', 'cancel']);
  assert.equal(second.acceptedRevision, 3); assert.equal(second.preview.revision, 3);
  assert.equal(second.task.deadlineAt, task.deadlineAt); assert.equal(second.task.createdAt, task.createdAt);
  assert.deepEqual(second.preview.plan.budget, original.preview.plan.budget); assert.deepEqual(second.preview.plan.acceptance, original.preview.plan.acceptance);
  assert.deepEqual(second.preview.plan.nodes, original.preview.plan.nodes); assert.deepEqual(second.preview.plan.edges, original.preview.plan.edges);
  assert.equal(f.read(tx => tx.commands()).length, 0); assert.equal(f.hooks.renders, 3);
  const previews = f.read(tx => tx.projections('interaction')).map(row => ({row, value: JSON.parse(row.bytes)})).filter(item => item.value.plan);
  assert.equal(previews.length, 3); assert.ok(previews.every(item => item.row.revision === 1n));
  assert.deepEqual(previews.map(item => item.value.revision).sort(), [1, 2, 3]);
  assert.equal(previews.find(item => item.value.revision === 2).value.previousDigest, original.previewDigest);
  const before = f.read(tx => tx.head(task.id));
  const replay = await f.answer(task.id, bySlot(original, 'language'), firstBody);
  assert.equal(replay.replayed, true); assert.deepEqual(replay.operation, first.operation); assert.deepEqual(replay.preview, first.preview);
  assert.equal(replay.currentTask.revision, 3); assert.deepEqual(f.read(tx => tx.head(task.id)), before);
  const approval = await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: approvedBody(second.task)});
  assert.equal(approval.status, 'accepted'); assert.equal(f.read(tx => tx.commands()).length, 1);
  const record = JSON.parse(f.read(tx => tx.projection('task', task.id)).bytes);
  assert.deepEqual(record.approved.preview, {digest: second.acceptedPreviewDigest, revision: 3, inputsDigest: second.preview.inputsDigest});
  assert.throws(() => f.app.proposePlan(task.id, record.task.revision, plan()), {code: 'state_conflict'});
  await assert.rejects(f.answer(task.id, bySlot(next, 'format'), {...answerBody(next, 'csv'), expectedRevision: record.task.revision}, 'after-approval'), {code: 'state_conflict'});
  assert.deepEqual((await f.answer(task.id, bySlot(original, 'language'), firstBody)).operation, first.operation);
});

test('answer route is part of only the new receipt digest; wrong subject/key/CAS, old preview and invalid shape do not mutate', async t => {
  const f = fixture(t), task = await f.create(), q = await f.questions(task.id), lang = bySlot(q, 'language'), fmt = bySlot(q, 'format');
  const oldRequest = {operation: 'task.create', key: 'create', body: input()};
  assert.equal(f.app.receiptKey(oldRequest).requestDigest, hash({operation: 'task.create', taskId: null, body: input()}));
  const body = answerBody(q, 'zh'); await f.answer(task.id, lang, body);
  const head = f.read(tx => tx.head(task.id));
  await assert.rejects(f.answer(task.id, fmt, body), {code: 'idempotency_conflict'});
  await assert.rejects(f.answer(task.id, lang, {...body, answer: 'en'}), {code: 'idempotency_conflict'});
  await assert.rejects(f.answer(task.id, fmt, {...body, answer: 'csv'}, 'old-revision'), {code: 'revision_conflict'});
  await assert.rejects(f.answer(task.id, fmt, {...body, expectedRevision: 2, answer: 'csv'}, 'old-preview'), {code: 'plan_conflict'});
  for (const change of [{expectedRevision: 0}, {expectedRevision: -1}, {questionRevision: 2}, {questionRevision: '1'}, {answer: '\0'}, {answer: '中'.repeat(1366)}, {extra: true}])
    await assert.rejects(f.answer(task.id, fmt, {...body, ...change}, 'bad-shape'), {code: 'invalid_request'});
  const other = await f.create(input(), 'other');
  await assert.rejects(f.answer(other.id, lang, answerBody(await f.questions(other.id), 'zh'), 'cross-task'), {code: 'not_found'});
  assert.deepEqual(f.read(tx => tx.head(task.id)), head);
});

test('renderer/validator failures and boundary changes roll back answer, preview, Operation and receipt', async t => {
  const f = fixture(t), task = await f.create(), q = await f.questions(task.id), body = answerBody(q, 'zh'), questionId = bySlot(q, 'language');
  const before = f.read(tx => ({head: tx.head(task.id), interactions: tx.projections('interaction').length, operations: tx.projections('operation').length}));
  await assert.rejects(f.answer(task.id, questionId, {...body, answer: 'not-valid'}), {code: 'unsupported_task'});
  for (const proposal of [() => ({...plan(), acceptance: ['skip checks']}), () => ({...plan(), nodes: [{...plan().nodes[0], scope: ['/etc']}]}),
    () => ({...plan(), budget: {timeoutMs: 60000, maxAttempts: 5, maxWorkers: 2}}), () => ({...plan(), nodes: [{...plan().nodes[0], providerId: 'other'}]})]) {
    f.hooks.proposal = proposal; await assert.rejects(f.answer(task.id, questionId, body), {code: 'unsupported_task'});
  }
  delete f.hooks.proposal; f.hooks.render = () => {throw Error('private renderer error');};
  await assert.rejects(f.answer(task.id, questionId, body), {code: 'unsupported_task'}); delete f.hooks.render;
  f.fault(true); await assert.rejects(f.answer(task.id, questionId, body), {code: 'application_unavailable'}); f.fault(false);
  assert.deepEqual(f.read(tx => ({head: tx.head(task.id), interactions: tx.projections('interaction').length, operations: tx.projections('operation').length})), before);
  assert.equal((await f.questions(task.id)).items.find(item => item.id === questionId).status, 'open');
  assert.equal((await f.answer(task.id, questionId, body)).replayed, false);
});

test('cold read/replay uses original facts; missing or changed renderer/validator rejects new writes without hiding history', async t => {
  const f = fixture(t), task = await f.create(), q = await f.questions(task.id), body = answerBody(q, 'zh'), questionId = bySlot(q, 'language');
  const first = await f.answer(task.id, questionId, body), current = await f.questions(task.id), calls = f.hooks.renders;
  for (const port of [null, template({}, {renderer: {...identity('changed-renderer'), render: plan}})]) {
    f.reopen(port); assert.deepEqual(await f.questions(task.id), current); assert.equal(f.hooks.renders, calls);
    const replay = await f.answer(task.id, questionId, body); assert.equal(replay.replayed, true); assert.deepEqual(replay.operation, first.operation);
    await assert.rejects(f.answer(task.id, bySlot(current, 'format'), answerBody(current, 'csv'), 'format'), {code: 'unsupported_task'});
  }
  f.reopen(); await f.answer(task.id, bySlot(current, 'format'), answerBody(current, 'csv'), 'format');
  const ready = await f.get(task.id); f.reopen(null);
  await assert.rejects(f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: approvedBody(ready)}), {code: 'unsupported_task'});
});

test('cancel during out-of-transaction rendering wins, answer first keeps original cancel CAS, exact replay never revives Task', async t => {
  const f = fixture(t), task = await f.create(), q = await f.questions(task.id), entered = deferred(), release = deferred();
  f.hooks.render = async () => {entered.resolve(); await release.promise;};
  const request = {operation: 'task.answer', taskId: task.id, questionId: bySlot(q, 'language'), key: 'answer', body: answerBody(q, 'zh')};
  const answering = f.call(request); await entered.promise;
  const cancel = await f.call({operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: 1}});
  release.resolve(); await assert.rejects(answering, {code: 'state_conflict'}); assert.equal(cancel.status, 'accepted');
  assert.equal(f.read(tx => tx.projections('interaction')).length, 1); assert.equal((await f.get(task.id)).status, 'cancelling');
  delete f.hooks.render; const other = await f.create(input(), 'other'), qs = await f.questions(other.id);
  const accepted = await f.answer(other.id, bySlot(qs, 'language'), answerBody(qs, 'zh'), 'accepted');
  await assert.rejects(f.call({operation: 'task.cancel', taskId: other.id, key: 'old-cancel', body: {expectedRevision: 1}}), {code: 'revision_conflict'});
  await f.call({operation: 'task.cancel', taskId: other.id, key: 'current-cancel', body: {expectedRevision: 2}});
  const replay = await f.answer(other.id, bySlot(qs, 'language'), answerBody(qs, 'zh'), 'accepted');
  assert.deepEqual(replay.operation, accepted.operation); assert.equal(replay.currentTask.status, 'cancelling');
});

test('original deadline and declaration limits hold; complete input stays on original zero-question planner path', async t => {
  const f = fixture(t), task = await f.create(), q = await f.questions(task.id);
  f.advance(60001); await assert.rejects(f.answer(task.id, bySlot(q, 'language'), answerBody(q, 'zh')), {code: 'question_expired'});
  assert.equal((await f.questions(task.id)).items[0].status, 'expired'); assert.equal((await f.get(task.id)).deadlineAt, task.deadlineAt);
  assert.deepEqual((await f.get(task.id)).allowedActions, ['cancel']);
  const complete = await f.create({...input(), context: {text: 'language=zh\nformat=csv'}}, 'complete');
  assert.equal(complete.status, 'draft'); assert.deepEqual((await f.questions(complete.id)).items, []);
  assert.equal(f.read(tx => tx.commands()).filter(command => command.taskId === complete.id).length, 1);
  const plain = await f.create({...input(), intent: '已冻结的 order-quote 团队任务'}, 'plain');
  assert.equal(plain.status, 'draft'); assert.deepEqual((await f.questions(plain.id)).items, []);
  await assert.rejects(f.answer(plain.id, 'missing', answerBody(q, 'zh')), {code: 'not_found'});
  assert.equal(f.hooks.renders, 1);
});

test('initial preview is atomic and finite; unsupported declarations or initial rendering never fall back to a planner', async t => {
  const slot = {id: 'language', prompt: '选择语言', validator: identity('language'), read: () => null, validate: () => true};
  for (const slots of [[], [null], [slot, slot], Array.from({length: 4}, (_, i) => ({...slot, id: 'slot-' + i}))])
    assert.throws(() => template({}, {slots}), {code: 'unsupported_task'});
  const f = fixture(t);
  f.hooks.render = () => {throw Error('fixture renderer unavailable');};
  await assert.rejects(f.create(), {code: 'unsupported_task'});
  delete f.hooks.render; f.fault(true);
  await assert.rejects(f.create(), {code: 'application_unavailable'}); f.fault(false);
  assert.deepEqual(f.read(tx => [tx.projections('task'), tx.projections('interaction'), tx.projections('operation'), tx.commands()]), [[], [], [], []]);
  const created = await f.create(); assert.equal(created.status, 'awaiting-answer');
  assert.equal((await f.create()).id, created.id); // Failed transaction did not retain a receipt.
  for (const change of [{applies: () => Promise.reject(Error('invalid async selection'))},
    {slots: [{...slot, read: () => undefined}]}, {slots: [{...slot, read: () => 'supplied', validate: async () => true}]}]) {
    const other = fixture(t, {port: template({}, change)});
    await assert.rejects(other.create(), {code: 'unsupported_task'});
    assert.equal(other.read(tx => tx.projections('task')).length, 0); assert.equal(other.read(tx => tx.commands()).length, 0);
  }
});

test('renderer wait cannot cross owner generation or original deadline; validator drift blocks writes and future facts fail closed', async t => {
  const f = fixture(t), task = await f.create(), questions = await f.questions(task.id), entered = deferred(), release = deferred();
  const request = {operation: 'task.answer', taskId: task.id, questionId: bySlot(questions, 'language'), key: 'generation', body: answerBody(questions, 'zh')};
  f.hooks.render = async () => {entered.resolve(); await release.promise;};
  const oldApp = f.app, pending = oldApp.dispatch(request, principal); await entered.promise; f.reopen(); release.resolve();
  await assert.rejects(pending, {code: 'recovery_required'});
  assert.deepEqual(await f.questions(task.id), questions); assert.equal(f.read(tx => tx.projections('interaction')).length, 1);
  delete f.hooks.render;
  f.reopen(template({}, {slots: ['language', 'format'].map(id => ({id, prompt: id === 'language' ? '输出使用 zh 还是 en？' : '内容使用 csv 还是 json？',
    validator: identity('changed-' + id), read: () => null, validate: () => true}))}));
  await assert.rejects(f.call(request), {code: 'unsupported_task'}); assert.deepEqual(await f.questions(task.id), questions);
  f.reopen(); f.hooks.render = () => {f.advance(60001);};
  await assert.rejects(f.call(request), {code: 'question_expired'}); assert.deepEqual(f.read(tx => tx.projections('interaction')).length, 1);
  // Explicit unsupported-version corruption fixture, not a production writer.
  f.app.transaction(true, tx => {const record = f.app.get(tx, task.id); record.clarification.profile = 'task-clarification/future';
    record.task.revision++; f.app.save(tx, record, 'test.unsupported-profile');});
  for (const operation of ['task.get', 'task.questions', 'task.list'])
    await assert.rejects(f.call({operation, taskId: task.id}), {code: 'recovery_required'});
});

test('legacy approval predicate and legacy Task schema cannot accept clarification final preview; zero-question approval is unchanged', async t => {
  const f = fixture(t), task = await f.create();
  for (const [slot, answer] of [['language', 'zh'], ['format', 'csv']]) {
    const qs = await f.questions(task.id); await f.answer(task.id, bySlot(qs, slot), answerBody(qs, answer), slot);
  }
  const record = JSON.parse(f.read(tx => tx.projection('task', task.id)).bytes), body = approvedBody(record.task);
  // Exact prior control predicate (e8ffa181), not an invented legacy reader.
  const legacyApprove = record.task.status === 'awaiting-approval' && record.plan && body.planRevision === record.plan.revision && body.planDigest === record.plan.digest;
  assert.equal(legacyApprove, false);
  const oldSchema = structuredClone(contract.components.schemas.Task); oldSchema.properties.status.enum = oldSchema.properties.status.enum.filter(value => value !== 'awaiting-confirmation');
  assert.equal(validate(await f.get(task.id), oldSchema), false); assert.equal(validate(await f.get(task.id), 'Task'), true);
  const legacy = await f.create({...input(), intent: '原零问题'}, 'legacy'), p = f.app.proposePlan(legacy.id, 1, plan());
  const ready = await f.get(legacy.id); assert.equal(ready.status, 'awaiting-approval'); assert.equal(validate(ready, oldSchema), true);
  assert.equal((await f.call({operation: 'task.approve', taskId: legacy.id, key: 'approve', body: {expectedRevision: ready.revision, planRevision: p.revision, planDigest: p.digest}})).status, 'accepted');
});

test('real loopback HTTP + SQLite exposes frozen questions, exact subject receipt, final approval and cold query', {timeout: 15000}, async t => {
  const f = fixture(t), token = 'public-clarification-http-fixture-token-000000'; let handler;
  const server = createServer((req, res) => void handler(req, res)); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(async () => {server.closeAllConnections(); await new Promise(resolve => server.close(resolve));});
  const host = '127.0.0.1:' + server.address().port;
  handler = createTaskApiHandler({application: (request, context) => f.app.dispatch(request, context), token, expectedHost: host});
  const request = async (route, body, key = 'key', auth = token) => {
    const response = await fetch('http://' + host + route, {method: body ? 'POST' : 'GET', headers: {authorization: 'Bearer ' + auth,
      ...(body ? {'content-type': 'application/json', 'idempotency-key': key} : {})}, body: body ? JSON.stringify(body) : undefined});
    return {status: response.status, body: await response.json()};
  };
  const created = await request('/v1/tasks', input(), 'create'); assert.equal(created.status, 201);
  const route = '/v1/tasks/' + created.body.id, questions = (await request(route + '/questions')).body;
  assert.equal((await request(route + '/questions', undefined, 'unused', 'wrong')).status, 401);
  assert.equal((await request(route + '/plan/approve', approvedBody(created.body), 'early')).status, 409);
  const answerRoute = route + '/questions/' + bySlot(questions, 'language') + '/answers', body = answerBody(questions, 'zh');
  assert.equal((await request(answerRoute, {...body, answer: '中'.repeat(1366)}, 'bytes')).status, 400);
  const first = await request(answerRoute, body, 'answer'); assert.equal(first.status, 202); assert.ok(validate(first.body, 'AnswerReceipt'));
  f.reopen(); const replay = await request(answerRoute, body, 'answer'); assert.equal(replay.status, 202); assert.equal(replay.body.replayed, true);
  assert.deepEqual(replay.body.operation, first.body.operation);
  assert.equal((await request(route + '/questions/' + bySlot(questions, 'format') + '/answers', body, 'answer')).status, 409);
  const current = (await request(route + '/questions')).body;
  const second = await request(route + '/questions/' + bySlot(current, 'format') + '/answers', answerBody(current, 'csv'), 'format');
  assert.equal(second.status, 202); assert.equal(second.body.task.status, 'awaiting-confirmation');
  assert.equal((await request(route + '/plan/approve', approvedBody(created.body), 'stale')).status, 409);
  assert.equal((await request(route + '/plan/approve', approvedBody(second.body.task), 'approve')).status, 202);
  assert.equal(f.read(tx => tx.commands()).length, 1); assert.equal((await request(route + '/audit')).body.attempts, 0);
});
