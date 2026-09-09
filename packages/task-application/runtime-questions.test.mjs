import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {TaskApplication, createRuntimeQuestionPort, createVerificationPort} from './application.mjs';
import {Store, INTERACTION_FORMAT, CUSTODY_FORMAT, digest, encode} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {createFileBusiness} from '../task-business/index.mjs';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const proposal = {summary: '业务输入明确后并行产出和独立验收', nodes: ['code', 'docs', 'verify'].map(id => ({id,
  role: id === 'verify' ? 'verifier' : 'author', goal: '完成' + id, scope: [id], providerId: null})),
  edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}], deliverables: ['程序及说明'], acceptance: ['独立检查完整交付']};
const layout = () => ({nodeId: 'verify', description: '保留两个原分支及用户业务输入。',
  layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
  deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});
const question = (extra = {}) => ({sessionId: 'native-session', nativeRequestId: 'ui-1', toolCallId: 'tool-1', questionNonce: 'a'.repeat(64),
  kind: 'select', prompt: '请选择当前业务区域', options: ['north', 'south'], ...extra});
const cleanFact = ticket => ({executionId: 'fixture-' + ticket.workerId, startedAt: '2026-09-09T00:00:00.000Z'});

// Real SQLite/Depot/reducers. Process observations and checker below are explicit
// controlled fixtures; full native/HTTP/custody proof is tested at composition.
function fixture(t, {consumer = true, validator = () => true, proposalValue = proposal, layoutValue = layout, nodeIds = ['code']} = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-question-'))), root = path.join(parent, 'store');
  let now = Date.parse('2026-09-09T00:00:00Z'), failSQL = false;
  let store = Store.create(root, {format: INTERACTION_FORMAT, clock: () => now});
  let owner = store.claimOwner(0, 'first', now + 3600000);
  const depot = ArtifactDepot.create(path.join(parent, 'objects'));
  const runtimeQuestions = createRuntimeQuestionPort({policy: {id: 'region', version: '1', description: '区域只能选择 north 或 south'}, nodeIds,
    maxQuestions: 3, maxWaitMs: 10000, applies: () => true, validateQuestion: value => value.prompt === '请选择当前业务区域', validateAnswer: validator});
  const verification = createVerificationPort({id: 'checker', policy: {id: 'exact-files', version: '1', description: '独立检查器测试替身'},
    interactionPolicyDigests: consumer ? [runtimeQuestions.policyDigest] : [], bindPlan: layoutValue,
    start({ticket}) {const raw = {type: 'verification', status: 'passed', cleanup: {started: cleanFact(ticket), cleaned: true},
      evidence: {name: 'verification.json', mediaType: 'application/json', content: Buffer.from('{}')},
      delivery: {name: 'delivery.txt', mediaType: 'text/plain', content: Buffer.from('controlled fixture') }};
      return {started: Promise.resolve(cleanFact(ticket)), completion: Promise.resolve(raw), stop: () => Promise.resolve(raw)};}});
  const wrapper = {info: () => store.info(), read: (...args) => store.read(...args), write(current, fn) {
    return store.write(current, tx => {const result = fn(tx); if (failSQL) throw Error('controlled rollback'); return result;});
  }};
  const config = {store: wrapper, owner, depot, verification, runtimeQuestions, clock: () => now,
    execution: {maxWorkers: 2, providerIds: ['pi'], defaultProvider: 'pi', questionProviderIds: ['pi']}};
  let app = new TaskApplication(config);
  t.after(() => {store.close(); depot.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const f = {get app() {return app;}, get execution() {return app.execution;}, runtimeQuestions, verification, root,
    closeStore: () => store.close(),
    call: request => app.dispatch(request, context), read: fn => store.read(owner, fn), now: () => now,
    advance: ms => {now += ms;}, failSQL: value => {failSQL = value;},
    get: taskId => app.dispatch({operation: 'task.get', taskId}, context),
    async prompt(ticket) {
      const business = createFileBusiness({parent, depot, clock: () => now, layoutFor: value => value.input.fileLayout,
        approvedLayout: value => app.execution.approvedLayout(value), observeExecution: value => app.execution.observeExecution(value)});
      try {return (await business.prepare(ticket, {signal: new AbortController().signal, deadline: ticket.deadline})).prompt;}
      finally {business.close();}
    },
    async start() {
      const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '业务分析交付', limits: {timeoutMs: 60000, maxAttempts: 8, maxWorkers: 2}}});
      const plan = app.proposePlan(task.id, task.revision, proposalValue), current = await f.get(task.id);
      await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}});
      const dispatch = f.read(tx => tx.commands()).find(row => JSON.parse(row.payload).action === 'dispatch');
      app.execution.expandDispatch(dispatch.id, dispatch.revision); return task.id;
    },
    take(nodeId) {const row = f.read(tx => tx.commands()).find(row => JSON.parse(row.payload).nodeId === nodeId);
      return app.execution.nextWork(row.id, row.revision);},
    started(ticket) {app.execution.started(ticket, cleanFact(ticket));},
    candidate(ticket) {const file = {path: ticket.nodeId + '.txt', ...depot.put(Buffer.from(ticket.nodeId))};
      return {profile: 'task-file-business/v1', taskId: ticket.taskId, nodeId: ticket.nodeId, workerId: ticket.workerId,
        planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest, layoutDigest: hash({profile: 'task-file-business/v1', ...ticket.input.fileLayout}),
        files: [file], inputDigest: digest(Buffer.from('[]')), manifestDigest: digest(Buffer.from(JSON.stringify([file])))};},
    finish(ticket, success = true) {return app.execution.finish(ticket, {status: success ? 'completed' : 'failed', stopReason: 'end_turn',
      cleanup: {started: cleanFact(ticket), cleaned: true}, result: f.candidate(ticket)});},
    async answer(taskId, q, extra = {}) {return {operation: 'task.answer', taskId, questionId: q.questionId, key: 'answer',
      body: {expectedRevision: (await f.get(taskId)).revision, questionDigest: q.questionDigest, questionRevision: 1, answer: 'north', ...extra}};},
    reopen() {store.close(); store = Store.openExisting(root, {format: INTERACTION_FORMAT, clock: () => now});
      owner = store.claimOwner(owner.generation, 'second', now + 3600000); app = new TaskApplication({...config, owner});},
  }; return f;
}

test('same Task/Worker answer is durable, exact-once dispatched/ACKed and reaches independent verification input', async t => {
  const f = fixture(t), taskId = await f.start(), code = f.take('code'); f.started(code);
  const originalInput = code.inputDigest, originalDeadline = code.deadline;
  const q = f.execution.registerQuestion(code, question()), head = f.read(tx => tx.head(taskId));
  assert.deepEqual(f.execution.registerQuestion(code, question()), q); assert.deepEqual(f.read(tx => tx.head(taskId)), head);
  const visible = await f.call({operation: 'task.questions', taskId});
  assert.equal(visible.items[0].deliveryStatus, null); assert.equal(visible.items[0].kind, 'business');
  const docs = f.take('docs'); assert.ok(docs); f.started(docs); // Independent branch stays dispatchable.
  const request = await f.answer(taskId, q), receipt = await f.call(request);
  assert.equal(receipt.operation.status, 'accepted'); assert.equal(receipt.deliveryStatus, 'pending');
  const dispatched = f.execution.dispatchAnswer(code, q.questionId);
  assert.equal(dispatched.answer, 'north'); assert.equal(code.inputDigest, originalInput); assert.equal(code.deadline, originalDeadline);
  assert.throws(() => f.execution.dispatchAnswer(code, q.questionId), error => error.code === 'state_conflict');
  const ack = {questionDigest: dispatched.questionDigest, answerDigest: dispatched.answerDigest, deliveryNonce: dispatched.deliveryNonce};
  assert.throws(() => f.execution.acknowledgeAnswer(code, q.questionId, {...ack, deliveryNonce: 'b'.repeat(64)}), error => error.code === 'state_conflict');
  assert.equal(f.execution.acknowledgeAnswer(code, q.questionId, ack), true);
  const afterACK = f.read(tx => tx.head(taskId)); assert.equal(f.execution.acknowledgeAnswer(code, q.questionId, ack), true);
  assert.deepEqual(f.read(tx => tx.head(taskId)), afterACK);
  const replay = await f.call(request); assert.equal(replay.replayed, true); assert.equal(replay.operation.status, 'accepted');
  assert.equal((await f.call({operation: 'operation.get', operationId: receipt.operation.id})).status, 'succeeded');
  assert.equal(f.finish(code).status, 'completed'); assert.equal(f.finish(docs).status, 'completed');
  const verify = f.take('verify'); assert.equal(verify.input.interactionRefs.length, 1);
  assert.equal(verify.input.interactionRefs[0].answer.answer, 'north'); assert.equal(hash(verify.input), verify.inputDigest);
  const handle = f.verification.start({ticket: verify, prepared: {cwd: '/controlled-fixture'}}); f.started(verify);
  assert.equal(f.execution.finish(verify, await handle.completion).status, 'completed');
  assert.equal((await f.get(taskId)).status, 'completed');
  f.reopen(); assert.equal((await f.call(request)).replayed, true);
  assert.equal((await f.call({operation: 'task.questions', taskId})).items[0].deliveryStatus, 'acknowledged');
});

test('pause keeps accepted answer pending, stale CAS cannot cancel and resumed delivery uses original deadline', async t => {
  const f = fixture(t), taskId = await f.start(), code = f.take('code'); f.started(code);
  const q = f.execution.registerQuestion(code, question()), request = await f.answer(taskId, q);
  await f.call(request);
  await assert.rejects(f.call({operation: 'task.cancel', taskId, key: 'old-cancel', body: {expectedRevision: request.body.expectedRevision}}), error => error.code === 'revision_conflict');
  let task = await f.get(taskId);
  await f.call({operation: 'task.pause', taskId, key: 'pause', body: {expectedRevision: task.revision}});
  assert.equal(f.execution.dispatchAnswer(code, q.questionId), null);
  const deadline = q.deadlineAt; f.advance(500); task = await f.get(taskId);
  await f.call({operation: 'task.resume', taskId, key: 'resume', body: {expectedRevision: task.revision}});
  assert.equal(f.execution.dispatchAnswer(code, q.questionId).deadlineAt, deadline);
});

test('cancel/expiry/SQL failure never fabricate ACK or new execution and clean failure releases delivery obligation', async t => {
  for (const mode of ['cancel-before-answer', 'cancel-before-dispatch', 'cancel-after-dispatch', 'expiry', 'ack-sql']) {
    const f = fixture(t), taskId = await f.start(), code = f.take('code'); f.started(code);
    const q = f.execution.registerQuestion(code, question()), request = await f.answer(taskId, q);
    let delivery;
    if (mode !== 'cancel-before-answer' && mode !== 'expiry') await f.call(request);
    if (['cancel-after-dispatch', 'ack-sql'].includes(mode)) delivery = f.execution.dispatchAnswer(code, q.questionId);
    if (mode === 'ack-sql') {
      f.failSQL(true); assert.throws(() => f.execution.acknowledgeAnswer(code, q.questionId, {questionDigest: q.questionDigest,
        answerDigest: delivery.answerDigest, deliveryNonce: delivery.deliveryNonce}), error => error.code === 'application_unavailable');
      f.failSQL(false); assert.equal((await f.call({operation: 'task.questions', taskId})).items[0].deliveryStatus, 'dispatched');
      f.execution.fail(code, 'worker_failed');
    } else if (mode === 'expiry') {f.advance(10001); f.execution.reconcile(taskId);}
    else {const task = await f.get(taskId); await f.call({operation: 'task.cancel', taskId, key: 'cancel', body: {expectedRevision: task.revision}});}
    assert.throws(() => f.execution.dispatchAnswer(code, q.questionId));
    if (mode === 'cancel-before-answer') await assert.rejects(f.call(request));
    else if (mode !== 'expiry') assert.equal((await f.call(request)).replayed, true);
    f.finish(code, false); f.execution.reconcile(taskId);
    assert.equal(f.read(tx => f.execution.capacity(tx).value.active.length), 0);
    const visible = (await f.call({operation: 'task.questions', taskId})).items[0];
    assert.equal(visible.deliveryStatus, delivery ? 'unknown' : mode === 'cancel-before-answer' || mode === 'expiry' ? null : 'cancelled');
    assert.equal(f.read(tx => tx.commands()).filter(row => JSON.parse(row.payload).action === 'answer').every(row => row.status === 'observed'), true);
  }
});

test('question proof, validator, same-key shape and cold generation all remain fail-closed', async t => {
  const f = fixture(t), taskId = await f.start(), code = f.take('code'); f.started(code);
  f.failSQL(true); assert.throws(() => f.execution.registerQuestion(code, question()), error => error.code === 'application_unavailable');
  f.failSQL(false); assert.equal((await f.call({operation: 'task.questions', taskId})).items.length, 0);
  const q = f.execution.registerQuestion(code, question());
  assert.throws(() => f.execution.registerQuestion(code, question({prompt: '替换原问题'})), error => error.code === 'state_conflict');
  for (const extra of [{answer: 'elsewhere'}, {questionDigest: 'sha256:' + 'b'.repeat(64)}, {questionRevision: 2}, {answer: '\0'}, {answer: 'a'.repeat(4097)}])
    await assert.rejects(f.call(await f.answer(taskId, q, extra)));
  const request = await f.answer(taskId, q); await f.call(request);
  await assert.rejects(f.call({...request, body: {...request.body, answer: 'south'}}), error => error.code === 'idempotency_conflict');
  f.reopen(); assert.equal((await f.call(request)).replayed, true);
  assert.throws(() => f.execution.dispatchAnswer(code, q.questionId), error => error.code === 'recovery_required');
  f.execution.reconcile(taskId); assert.equal((await f.call({operation: 'task.questions', taskId})).items[0].deliveryStatus, 'cancelled');
  assert.equal(f.read(tx => f.execution.capacity(tx).value.active.length), 1); // No invented old-process cleanup.
});

test('missing final consumer rejects proposal and old v1/v2 readers reject new root before claim', async t => {
  const f = fixture(t, {consumer: false}); await assert.rejects(f.start(), error => error.code === 'unsupported_task');
  f.closeStore(); // Isolate format rejection from the live connection lock.
  assert.throws(() => Store.openExisting(f.root), error => error.code === 'unavailable');
  assert.throws(() => Store.openExisting(f.root, {format: CUSTODY_FORMAT}), error => error.code === 'unavailable');
});

test('three-level and diamond dependencies preserve exactly original inherited answer in actual prepared prompt and final receipt', async t => {
  const names = ['code', 'middle', 'docs'], plan = {...proposal,
    nodes: [...names, 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', goal: '完成' + id, scope: [id], providerId: null})),
    edges: [{from: 'code', to: 'middle'}, {from: 'middle', to: 'docs'}, {from: 'code', to: 'docs'}, ...names.map(from => ({from, to: 'verify'}))]};
  const layouts = () => ({nodeId: 'verify', description: '三级依赖与菱形汇合保留原答案',
    layouts: names.map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: names.map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
    deliveries: names.map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});
  const f = fixture(t, {proposalValue: plan, layoutValue: layouts, nodeIds: ['code', 'middle']}), taskId = await f.start(), code = f.take('code'); f.started(code);
  const q = f.execution.registerQuestion(code, question()); await f.call(await f.answer(taskId, q));
  const d = f.execution.dispatchAnswer(code, q.questionId); f.execution.acknowledgeAnswer(code, q.questionId,
    {questionDigest: d.questionDigest, answerDigest: d.answerDigest, deliveryNonce: d.deliveryNonce});
  assert.equal(f.finish(code).status, 'completed');
  const middle = f.take('middle'); f.started(middle); assert.equal(middle.input.interactionRefs.length, 1);
  const middleQ = f.execution.registerQuestion(middle, question({nativeRequestId: 'middle-ui', questionNonce: 'b'.repeat(64)}));
  const middleAnswer = await f.answer(taskId, middleQ); middleAnswer.key = 'middle-answer'; await f.call(middleAnswer);
  const middleD = f.execution.dispatchAnswer(middle, middleQ.questionId);
  f.execution.acknowledgeAnswer(middle, middleQ.questionId, {questionDigest: middleD.questionDigest, answerDigest: middleD.answerDigest, deliveryNonce: middleD.deliveryNonce});
  assert.equal(f.finish(middle).status, 'completed');
  const docs = f.take('docs'); f.started(docs);
  assert.equal(docs.input.upstream.length, 2); assert.equal(docs.input.interactionRefs.length, 2);
  assert.deepEqual(docs.input.upstream.map(item => item.interactionRefs.length), [1, 2]);
  const prepared = await f.prompt(docs); assert.match(prepared, /"answer":"north"/);
  assert.equal(docs.input.interactionRefs[0].questionDigest, q.questionDigest);
  const before = f.read(tx => tx.head(taskId));
  for (const change of [ticket => ticket.input.interactionRefs[0].ackDigest = 'sha256:' + 'f'.repeat(64),
    ticket => ticket.input.upstream[0].workerId = docs.workerId,
    ticket => ticket.input.upstream[0].nodeId = 'non-dependency',
    ticket => ticket.input.upstream[0].result.manifestDigest = 'sha256:' + 'e'.repeat(64),
    ticket => ticket.input.upstream[0].interactionRefs = [],
    ticket => ticket.input.interactionRefs[0].question.generation = '0']) {
    const wrong = structuredClone(docs); change(wrong);
    assert.throws(() => f.app.transaction(false, tx => f.app.runtimeQuestions.resultRefs(tx, f.app.get(tx, taskId), wrong)), error => error.code === 'recovery_required');
  }
  assert.deepEqual(f.read(tx => tx.head(taskId)), before);
  assert.equal(f.finish(docs).status, 'completed');
  const verify = f.take('verify'); assert.deepEqual(verify.input.interactionRefs.map(ref => ref.questionDigest), [q.questionDigest, middleQ.questionDigest]); f.started(verify);
  const handle = f.verification.start({ticket: verify, prepared: {cwd: '/controlled-fixture'}});
  assert.equal(f.execution.finish(verify, await handle.completion).status, 'completed');
  assert.equal((await f.get(taskId)).status, 'completed');
});

test('own acknowledged refs merge without leaking another branch pending question; unACKed inheritance refuses', async t => {
  const f = fixture(t, {nodeIds: ['code', 'docs']}), taskId = await f.start(), code = f.take('code'), docs = f.take('docs');
  f.started(code); f.started(docs);
  const first = f.execution.registerQuestion(code, question()), other = f.execution.registerQuestion(docs, question({nativeRequestId: 'ui-2', questionNonce: 'b'.repeat(64)}));
  await f.call(await f.answer(taskId, first)); const d = f.execution.dispatchAnswer(code, first.questionId);
  f.execution.acknowledgeAnswer(code, first.questionId, {questionDigest: d.questionDigest, answerDigest: d.answerDigest, deliveryNonce: d.deliveryNonce});
  const values = f.read(tx => f.app.runtimeQuestions.resultRefs(tx, f.app.get(tx, taskId), code));
  assert.equal(values.length, 1); assert.equal(values[0].questionDigest, first.questionDigest);
  assert.throws(() => f.app.transaction(false, tx => f.app.runtimeQuestions.inherited(tx, f.app.get(tx, taskId), [{questionDigest: other.questionDigest}])),
    error => error.code === 'state_conflict');
  assert.equal(f.finish(code).status, 'completed');
  assert.equal((await f.call({operation: 'task.questions', taskId})).items.find(item => item.id === other.questionId).status, 'open');
});

test('question registered while paused resumes awaiting-answer; pause never dispatches an answer', async t => {
  const f = fixture(t), taskId = await f.start(), code = f.take('code'); f.started(code);
  let current = await f.get(taskId); await f.call({operation: 'task.pause', taskId, key: 'pause-before-question', body: {expectedRevision: current.revision}});
  const q = f.execution.registerQuestion(code, question()); current = await f.get(taskId);
  assert.equal(current.status, 'paused'); assert.equal(f.execution.dispatchAnswer(code, q.questionId), null);
  await assert.rejects(f.call(await f.answer(taskId, q)), error => error.code === 'state_conflict');
  await f.call({operation: 'task.resume', taskId, key: 'resume-after-question', body: {expectedRevision: current.revision}});
  current = await f.get(taskId); assert.equal(current.status, 'awaiting-answer'); assert.ok(current.allowedActions.includes('answer'));
  await f.call(await f.answer(taskId, q)); const d = f.execution.dispatchAnswer(code, q.questionId);
  f.execution.acknowledgeAnswer(code, q.questionId, {questionDigest: d.questionDigest, answerDigest: d.answerDigest, deliveryNonce: d.deliveryNonce});
  assert.equal((await f.get(taskId)).status, 'running'); assert.equal(f.finish(code).status, 'completed');
});
