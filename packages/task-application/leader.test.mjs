import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {Store, LEADER_FORMAT, encode, digest} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication, createLeaderPort, createReviewPort, createVerificationPort, parseManagedOutput, renderLeaderPrompt, renderReviewPrompt} from './application.mjs';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const fileDigest = files => digest(Buffer.from(JSON.stringify(files)));
const proposal = {summary: '两个原分支完整交付', nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
  goal: '完成' + id, scope: [id], providerId: null})), edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
  deliverables: ['原两分支'], acceptance: ['独立完整核验'], assumptions: []};
const binding = () => ({nodeId: 'verify', description: '固定两个分支独立核对', layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']}))
  .concat({nodeId: 'verify', inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}})), allowedPaths: []}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))});
export function fixture(t, options = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-core-'))), root = path.join(parent, 'store');
  const depot = ArtifactDepot.create(path.join(parent, 'objects'));
  let store = Store.create(root, {format: LEADER_FORMAT}), owner = store.claimOwner(0, 'test', Date.now() + 3600000), serial = 0;
  const reviewPolicy = {id: 'review', version: '1', description: '只读完整选果'}, policy = {profile: 'task-managed-leader/v1', maxCalls: 9,
    maxActions: 4, maxRequests: 3, repair: {nodeIds: ['east', 'west'], maxRounds: 1},
    review: {providerId: 'fixture', policyDigest: hash(reviewPolicy)}, publication: options.publication ? {targetId: options.publication.id, policyDigest: options.publication.policyDigest} : null};
  const leader = createLeaderPort({id: 'leader', providerId: 'fixture', policy,
    prepare: ({input}) => ({prompt: renderLeaderPrompt(input)}), parseDecision: parseManagedOutput});
  const review = createReviewPort({id: 'review', providerId: 'fixture', policy: reviewPolicy,
    prepare: ({input}) => ({prompt: renderReviewPrompt(input)}), parseReport: parseManagedOutput});
  let response;
  // These are explicitly controlled Provider/cleanup facts, never an OS crash,
  // model, ACP transport or production business proof. SQLite/Depot are real.
  const provider = {id: 'fixture', start() {const fact = {executionId: 'fake-' + ++serial, startedAt: new Date().toISOString()};
    return {started: Promise.resolve(fact), stop() {}, completion: Promise.resolve({providerId: 'fixture', status: 'completed', stopReason: 'end_turn',
      outputText: JSON.stringify(response), cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}})};}};
  const verification = createVerificationPort({id: 'verify', policy: {id: 'check', version: '1', description: '本测试受控独立断言'}, bindPlan: binding,
    publicationExpected: options.publicationExpected ?? null,
    start({ticket}) {const fact = {executionId: 'checker-' + ticket.workerId, startedAt: new Date().toISOString()};
      assert.equal(ticket.input.verification.manifests.length, 2);
      assert.equal(ticket.input.leaderReplies[0].answer, 'north');
      return {started: Promise.resolve(fact), stop() {}, completion: Promise.resolve({type: 'verification', status: 'passed', cleanup: {started: fact, cleaned: true},
        evidence: {name: 'check.json', mediaType: 'application/json', content: encode({actual: 'north', bothBranches: true})},
        delivery: {name: 'delivery.json', mediaType: 'application/json', content: encode({east: 10, west: 20, region: 'north'})}})};}});
  const execution = {maxWorkers: 3, providerIds: ['fixture'], defaultProvider: 'fixture'};
  let app;
  t.after(() => {store.close(); depot.close(); fs.rmSync(parent, {recursive: true, force: true});});
  app = new TaskApplication({store, owner, execution, leader, review, verification, depot, publication: options.publication ?? null});
  const f = {get app() {return app;}, provider, parent, results: new Map(), call: request => app.dispatch(request, context),
    read: callback => app.transaction(false, callback),
    get: taskId => f.call({operation: 'task.get', taskId}),
    take(action, nodeId) {const command = f.read(tx => tx.commands().find(command => command.status === 'pending' &&
      JSON.parse(command.payload).action === action && (nodeId === undefined || JSON.parse(command.payload).nodeId === nodeId)));
      assert.ok(command, action + ':' + nodeId); return app.execution.nextWork(command.id, command.revision);},
    async decision(ticket, actions) {response = {profile: 'task-managed-leader/v1', callId: ticket.input.leader.callId,
      inputDigest: ticket.input.leader.inputDigest, summary: '只依据原证据推进', actions}; return f.run(ticket, leader);},
    async run(ticket, port) {const handle = port.start({ticket, provider, prepared: {cwd: parent, prompt: '受控模型夹具'}});
      app.execution.started(ticket, await handle.started); const result = await handle.completion; f.results.set(ticket.workerId, result); return app.execution.finish(ticket, result);},
    author(ticket, started = {executionId: 'author-' + ticket.workerId, startedAt: new Date().toISOString()}) {app.execution.started(ticket, started);
      const file = {path: ticket.nodeId + '.json', ...depot.put(encode({value: ticket.nodeId === 'east' ? 10 : 20}))};
      return app.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true}, result: {
        profile: 'task-file-business/v1', taskId: ticket.taskId, nodeId: ticket.nodeId, workerId: ticket.workerId,
        planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest, layoutDigest: hash({profile: 'task-file-business/v1', ...ticket.input.fileLayout}),
        files: [file], inputDigest: fileDigest([]), manifestDigest: fileDigest([file])}});},
    async review(ticket) {response = {profile: 'task-independent-review/v1', inputDigest: ticket.input.review.inputDigest,
      selectionDigest: ticket.input.review.selectionDigest, verdict: 'accept', summary: '独立审阅两原分支', findings: []}; return f.run(ticket, review);},
    async verify(ticket) {const handle = verification.start({ticket, prepared: {cwd: parent, prompt: '固定受控检查器'}});
      app.execution.started(ticket, await handle.started); return app.execution.finish(ticket, await handle.completion);},
    reopen() {store.close(); store = Store.openExisting(root, {format: LEADER_FORMAT}); owner = store.claimOwner(owner.generation, 'cold', Date.now() + 3600000);
      app = new TaskApplication({store, owner, execution, leader, review, verification, depot, publication: options.publication ?? null});},
  }; return f;
}

export {proposal, hash};

test('v7 real SQLite: necessary reply → plan approval → two authors → independent Review → stage verification → deliver/conclude → cold exact bytes', async t => {
  const f = fixture(t), task = await f.call({operation: 'task.create', key: 'create', body: {intent: '交付两区域结果，但区域待用户明确',
    limits: {timeoutMs: 60000, maxAttempts: 17, maxWorkers: 3}}});
  let ticket = f.take('leader'); assert.ok(ticket);
  const originalDeadline = ticket.deadline;
  assert.equal((await f.decision(ticket, [{type: 'ask', kind: 'business', prompt: '请指定业务区域', options: [],
    subject: f.read(tx => f.app.get(tx, task.id)).inputDigest, nodeIds: []}])).status, 'completed');
  let current = await f.get(task.id), view = await f.call({operation: 'task.leader', taskId: task.id});
  assert.equal(current.status, 'awaiting-answer'); assert.deepEqual(current.allowedActions, ['cancel']);
  const request = {operation: 'task.leader.reply', taskId: task.id, requestId: view.pendingRequest.id, key: 'answer',
    body: {expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest, answer: 'north'}};
  const answered = await f.call(request); assert.equal(answered.acceptedRevision, current.revision + 1);
  assert.equal((await f.call(request)).replayed, true);
  ticket = f.take('leader'); assert.equal(ticket.deadline, originalDeadline); assert.equal(ticket.input.leader.snapshot.interactions.replies[0].answer, 'north');
  assert.equal((await f.decision(ticket, [{type: 'plan', proposal}])).status, 'completed');
  const plan = await f.call({operation: 'task.plan', taskId: task.id}); current = await f.get(task.id);
  assert.ok(plan.acceptance.some(value => value.includes('leader-delivery')));
  await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: current.revision, planDigest: plan.digest, planRevision: plan.revision}});
  const dispatch = f.read(tx => tx.commands().find(command => JSON.parse(command.payload).action === 'dispatch'));
  f.app.execution.expandDispatch(dispatch.id, dispatch.revision);
  const east = f.take('execute', 'east'), west = f.take('execute', 'west'); assert.ok(east && west); assert.equal(f.take('execute', 'verify'), null);
  f.author(east); f.author(west); assert.equal(f.take('execute', 'verify'), null);
  ticket = f.take('leader'); const selected = ticket.input.leader.snapshot.selection;
  assert.equal((await f.decision(ticket, [{type: 'work', kind: 'review', nodeIds: ['east', 'west'], selectionDigest: hash(selected)}])).status, 'completed');
  assert.equal((await f.review(f.take('review'))).status, 'completed');
  ticket = f.take('leader');
  assert.equal((await f.decision(ticket, [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(selected)}])).status, 'completed');
  assert.equal((await f.verify(f.take('execute', 'verify'))).status, 'completed');
  current = await f.get(task.id); assert.equal(current.status, 'running'); assert.equal(current.phase, 'delivery');
  view = await f.call({operation: 'task.leader', taskId: task.id}); const audit = await f.call({operation: 'task.audit', taskId: task.id});
  const delivery = f.read(tx => current.artifactIds.map(id => f.app.artifacts.metadata(tx, id)).find(item => item.kind === 'delivery'));
  assert.deepEqual(audit.firstReview, {passed: 0, total: 0, pending: 0}); assert.equal(view.review.verdict, 'accept');
  ticket = f.take('leader'); assert.equal((await f.decision(ticket, [{type: 'deliver', artifactId: delivery.id,
    acceptanceDigest: audit.acceptance.digest, reviewDigest: view.review.digest}])).status, 'completed');
  ticket = f.take('leader'); assert.equal((await f.decision(ticket, [{type: 'conclude', outcome: 'succeeded', summary: '两原分支独立检查后完整交付',
    basisDigests: [audit.acceptance.digest, view.review.digest]}])).status, 'completed');
  current = await f.get(task.id); assert.equal(current.status, 'completed');
  const before = f.read(tx => tx.head(task.id)), bytes = await f.call({operation: 'artifact.content', artifactId: delivery.id});
  assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
  f.reopen(); assert.deepEqual(await f.get(task.id), current); assert.deepEqual(f.read(tx => tx.head(task.id)), before);
  assert.deepEqual((await f.call({operation: 'artifact.content', artifactId: delivery.id})).content, bytes.content);
  assert.equal((await f.call(request)).replayed, true);
});
