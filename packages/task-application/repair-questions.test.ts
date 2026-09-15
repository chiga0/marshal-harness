import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, REPAIR_FORMAT, encode, digest} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication, createRepairPort, createRuntimeQuestionPort, createVerificationPort} from './application.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const checkerPath = fileURLToPath(new URL('./repair-checker.fixture.mjs', import.meta.url));
const proposal = {summary: '修正代码并保留已答复的文档分支',
  nodes: ['code', 'docs', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
    goal: '完成 ' + id, scope: [id], providerId: null})),
  edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}],
  deliverables: ['代码与说明完整输出'], acceptance: ['内容与结构由独立检查器验证'], assumptions: []};
const bindPlan = () => ({nodeId: 'verify', description: '保留文档的原始回答，独立复验全部输出。',
  layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({
    nodeId: 'verify', allowedPaths: [], inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt',
      source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
  deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});

// Real SQLite/Depot/Application/FileBusiness and the checked-in command checker.
// Authors, native question callbacks and author cleanup are controlled fixtures;
// this is not a model, native Provider or service-process crash acceptance test.
function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-repair-questions-')));
  const root = path.join(parent, 'store'), executionRoot = path.join(parent, 'executions');
  fs.mkdirSync(executionRoot, {mode: 0o700});
  let store = Store.create(root, {format: REPAIR_FORMAT});
  let owner = store.claimOwner(0, 'first', Date.now() + 3600000);
  const depot = ArtifactDepot.create(path.join(parent, 'objects'));
  const runtimeQuestions = createRuntimeQuestionPort({
    policy: {id: 'region', version: '1', description: '原文档分支的区域回答只能是 north 或 south'},
    nodeIds: ['docs'], maxQuestions: 3, maxWaitMs: 10000, applies: () => true,
    validateQuestion: value => value.prompt === '请选择当前业务区域',
    validateAnswer: answer => answer === 'north' || answer === 'south'});
  const repair = createRepairPort({policy: {id: 'repair-content', version: '1', description: '仅业务内容失败可以修正'},
    nodeIds: ['code', 'docs'], assertions: ['business']});
  const policy = {id: 'checker', version: '1', description: '完整原始内容及结构断言'};
  const command = createVerificationCommand({executable: process.execPath, checkerPath,
    checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: hash(policy),
    assertions: [{name: 'business', validate: actual => actual === true}, {name: 'structure', validate: actual => actual === true}],
    repair: {policyDigest: repair.policyDigest, assertions: ['business']},
    delivery: ({prepared}) => ({name: 'all.txt', mediaType: 'text/plain',
      content: Buffer.concat(['code', 'docs'].map(node => fs.readFileSync(path.join(prepared.cwd, node + '.txt'))))})});
  const verification = createVerificationPort({id: 'checker', policy, bindPlan, start: command.start,
    interactionPolicyDigests: [runtimeQuestions.policyDigest], repairPolicyDigests: [repair.policyDigest]});
  const wrapper = {info: () => store.info(), read: (...args) => store.read(...args), write: (...args) => store.write(...args)};
  const config = {store: wrapper, depot, verification, repair, runtimeQuestions,
    execution: {maxWorkers: 2, providerIds: ['fixture-agent'], defaultProvider: 'fixture-agent', questionProviderIds: ['fixture-agent']}};
  let app = new TaskApplication({...config, owner});
  const business = createFileBusiness({parent: executionRoot, depot, layoutFor: ticket => ticket.input.fileLayout,
    approvedLayout: ticket => app.execution.approvedLayout(ticket), observeExecution: ticket => app.execution.observeExecution(ticket)});
  const handles = [];
  t.after(async () => {
    try {await Promise.all(handles.map(handle => handle.stop()));}
    finally {business.close(); depot.close(); store.close(); fs.rmSync(parent, {recursive: true, force: true});}
  });
  const f = {
    get app() {return app;}, call: request => app.dispatch(request, context),
    get: taskId => app.dispatch({operation: 'task.get', taskId}, context),
    read: fn => app.transaction(false, fn), commands: () => f.read(tx => tx.commands()),
    head: taskId => f.read(tx => tx.head(taskId)),
    questions: taskId => f.call({operation: 'task.questions', taskId}),
    facts(taskId) {return f.read(tx => app.get(tx, taskId).runtimeQuestions.questions.map(q => ({state: q,
      records: [q.id, q.answerId, q.dispatchId, q.ackId].map(id => tx.projection('interaction', id))})));},
    refs(ticket) {return f.read(tx => app.execution.worker(tx, ticket.workerId).record.interactionRefs);},
    take(nodeId) {
      const row = f.commands().find(row => row.status === 'pending' && JSON.parse(row.payload).nodeId === nodeId);
      assert.ok(row, 'original pending execute command for ' + nodeId);
      return app.execution.nextWork(row.id, row.revision);
    },
    async setup() {
      const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '仅修错误代码，原文档及区域回答不变',
        limits: {timeoutMs: 60000, maxAttempts: 6, maxWorkers: 2}}});
      const row = f.commands().find(row => JSON.parse(row.payload).action === 'plan');
      const ticket = app.execution.nextWork(row.id, row.revision);
      const started = {executionId: 'controlled-planner', startedAt: new Date().toISOString()};
      app.execution.started(ticket, started);
      app.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true}, plan: proposal});
      const current = await f.get(task.id), plan = await f.call({operation: 'task.plan', taskId: task.id});
      await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {
        expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}});
      const dispatch = f.commands().find(row => JSON.parse(row.payload).action === 'dispatch');
      app.execution.expandDispatch(dispatch.id, dispatch.revision);
      return {taskId: task.id, plan};
    },
    async author(ticket, content, afterStarted = null) {
      const ctx = {signal: new AbortController().signal, deadline: ticket.deadline};
      const prepared = await business.prepare(ticket, ctx);
      const started = {executionId: 'controlled-' + ticket.workerId, startedAt: new Date().toISOString()};
      app.execution.started(ticket, started); if (afterStarted) await afterStarted();
      fs.writeFileSync(path.join(prepared.cwd, ticket.nodeId + '.txt'), content, {mode: 0o600});
      const result = {providerId: ticket.providerId, status: 'completed', stopReason: 'end_turn',
        outputText: 'controlled author data', cleanup: {started, cleaned: true}};
      const complete = {...result, ...await business.collect(ticket, result, ctx)};
      app.execution.finish(ticket, complete); business.release(ticket); return {prepared, complete};
    },
    async verify(inspect = () => {}) {
      const ticket = f.take('verify'); inspect(ticket);
      const prepared = await business.prepare(ticket, {signal: new AbortController().signal, deadline: ticket.deadline});
      const handle = verification.start({ticket, prepared}); handles.push(handle);
      app.execution.started(ticket, await handle.started);
      const result = await handle.completion;
      app.execution.finish(ticket, result); business.release(ticket); app.execution.reconcile(ticket.taskId);
      for (const row of f.commands().filter(row => row.status === 'pending')) app.execution.settleControl(row.id, row.revision);
      return {ticket, result};
    },
    reopen() {
      store.close(); store = Store.openExisting(root, {format: REPAIR_FORMAT});
      owner = store.claimOwner(owner.generation, 'reopened', Date.now() + 3600000);
      app = new TaskApplication({...config, owner});
    },
  };
  return f;
}

test('retained original ACK survives cold owner change and code-only repair without redelivery or stale completion', {timeout: 30000}, async t => {
  const f = fixture(t), {taskId, plan} = await f.setup(), originalCode = f.take('code'), docs = f.take('docs');
  const oldResult = await f.author(originalCode, 'wrong');
  let question, answerRequest, ack;
  await f.author(docs, 'retained', async () => {
    question = f.app.execution.registerQuestion(docs, {sessionId: 'controlled-session', nativeRequestId: 'ui:original|docs',
      toolCallId: 'call_original|item_docs', questionNonce: 'a'.repeat(64), kind: 'select',
      prompt: '请选择当前业务区域', options: ['north', 'south']});
    answerRequest = {operation: 'task.answer', taskId, questionId: question.questionId, key: 'answer', body: {
      expectedRevision: (await f.get(taskId)).revision, questionDigest: question.questionDigest, questionRevision: 1, answer: 'north'}};
    const receipt = await f.call(answerRequest); assert.equal(receipt.deliveryStatus, 'pending');
    const delivery = f.app.execution.dispatchAnswer(docs, question.questionId); assert.equal(delivery.answer, 'north');
    assert.throws(() => f.app.execution.dispatchAnswer(docs, question.questionId), {code: 'state_conflict'});
    ack = {questionDigest: delivery.questionDigest, answerDigest: delivery.answerDigest, deliveryNonce: delivery.deliveryNonce};
    const beforeBadACK = f.head(taskId);
    assert.throws(() => f.app.execution.acknowledgeAnswer(docs, question.questionId, {...ack, deliveryNonce: 'b'.repeat(64)}), {code: 'state_conflict'});
    assert.deepEqual(f.head(taskId), beforeBadACK);
    assert.equal(f.app.execution.acknowledgeAnswer(docs, question.questionId, ack), true);
    const acknowledged = f.head(taskId);
    assert.equal(f.app.execution.acknowledgeAnswer(docs, question.questionId, ack), true);
    assert.deepEqual(f.head(taskId), acknowledged);
  });
  const originalRefs = f.refs(docs); assert.equal(originalRefs.length, 1);
  assert.equal(originalRefs[0].questionDigest, question.questionDigest);
  assert.equal(originalRefs[0].answer.answer, 'north');
  assert.equal(originalRefs[0].question.workerId, docs.workerId);
  const first = await f.verify(ticket => assert.deepEqual(ticket.input.interactionRefs, originalRefs));
  assert.equal(first.result.status, 'failed');
  const rejected = await f.get(taskId), oldAudit = await f.call({operation: 'task.audit', taskId});
  assert.equal(rejected.status, 'failed'); assert.ok(oldAudit.decision.contentRejection); assert.equal(oldAudit.attempts, 4);
  const originalQuestions = await f.questions(taskId), originalFacts = f.facts(taskId);
  assert.equal(originalQuestions.items.length, 1); assert.equal(originalQuestions.items[0].deliveryStatus, 'acknowledged');
  assert.equal(originalQuestions.taskRevision, rejected.revision);
  f.reopen();
  assert.deepEqual(await f.get(taskId), rejected);
  assert.deepEqual(await f.questions(taskId), originalQuestions); assert.deepEqual(f.facts(taskId), originalFacts);

  const receipt = await f.call({operation: 'task.repair', taskId, key: 'repair', body: {expectedRevision: rejected.revision,
    planDigest: plan.digest, decisionDigest: oldAudit.decision.digest, nodeIds: ['code'], feedback: '按原业务断言将代码结果修正为 correct'}});
  assert.deepEqual(receipt.affectedNodes, ['code', 'verify']); assert.equal(receipt.acceptedRevision, rejected.revision + 1);
  const code = f.take('code'); assert.notEqual(code.generation, docs.generation);
  assert.equal(code.repairId, receipt.repairId); assert.deepEqual(code.input.interactionRefs, []);
  const beforeOldFinish = f.head(taskId);
  assert.throws(() => f.app.execution.finish(originalCode, oldResult.complete), {code: 'recovery_required'});
  assert.throws(() => f.app.execution.dispatchAnswer(docs, question.questionId), {code: 'recovery_required'});
  assert.throws(() => f.app.execution.acknowledgeAnswer(docs, question.questionId, ack), {code: 'recovery_required'});
  assert.deepEqual(f.head(taskId), beforeOldFinish); assert.deepEqual(f.facts(taskId), originalFacts);
  await f.author(code, 'correct');
  const second = await f.verify(ticket => {
    assert.deepEqual(ticket.input.interactionRefs, originalRefs);
    assert.equal(ticket.input.verification.manifests.find(item => item.nodeId === 'docs').workerId, docs.workerId);
    assert.notEqual(ticket.generation, originalRefs[0].question.generation);
    assert.equal(hash(ticket.input), ticket.inputDigest);
  });
  assert.equal(second.result.status, 'passed');
  const completed = await f.get(taskId), audit = await f.call({operation: 'task.audit', taskId});
  assert.equal(completed.status, 'completed'); assert.equal(completed.deadlineAt, rejected.deadlineAt);
  assert.equal(audit.attempts, 6); assert.equal(audit.reworkCount, 1); assert.equal(audit.acceptance.status, 'passed');
  assert.equal(audit.workers.filter(worker => worker.nodeId === 'docs').length, 1);
  assert.equal((await f.call({operation: 'operation.get', operationId: receipt.operation.id})).status, 'succeeded');
  const currentQuestions = await f.questions(taskId);
  // The earlier diagnostic compared the entire envelope and failed on 20 -> 27.
  // Task transitions advance its revision; the original question facts do not.
  assert.deepEqual(currentQuestions.items, originalQuestions.items);
  assert.equal(currentQuestions.taskRevision, completed.revision);
  assert.ok(currentQuestions.taskRevision > originalQuestions.taskRevision);
  assert.deepEqual(f.facts(taskId), originalFacts); assert.deepEqual(f.refs(docs), originalRefs);
  const beforeReplay = f.head(taskId);
  assert.equal((await f.call(answerRequest)).replayed, true); assert.deepEqual(f.head(taskId), beforeReplay);
  const delivery = audit.decision.artifacts.find(item => item.kind === 'delivery'); assert.ok(delivery);
  assert.equal((await f.call({operation: 'artifact.content', artifactId: delivery.id})).content.toString(), 'correctretained');
});
