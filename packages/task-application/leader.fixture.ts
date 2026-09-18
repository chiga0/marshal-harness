import {registerLeaderJsonCorrection,startLeaderWithJsonCorrection} from './leader-protocol-correction.ts';
import {registerReviewAssessments,startReviewWithAssessments} from './review-assessment.ts';
import {parseAssessmentProposal} from './review-assessment-contract.ts';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {Store, LEADER_FORMAT, encode, digest} from '../task-store/store.ts';
import {ArtifactDepot} from '../task-artifacts/depot.ts';
import {TaskApplication, createLeaderPort, createReviewPort, createVerificationPort, parseManagedOutput, renderLeaderPrompt, renderReviewPrompt} from './application.ts';

const context = {principal: 'local-operator'}, hash = value => digest(encode(value));
const fileDigest = files => digest(Buffer.from(JSON.stringify(files)));
const proposal = {summary: '两个原分支完整交付', nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
  goal: '完成' + id, scope: [id], providerId: null})), edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
  deliverables: ['原两分支'], acceptance: ['独立完整核验'], assumptions: []};
const binding = () => ({nodeId: 'verify', description: '固定两个分支独立核对', layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']}))
  .concat({nodeId: 'verify', inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}})), allowedPaths: []}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))});
export async function fixture(t, options = {}) {
  const parent = options.existingParent ?? fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-core-'))), root = path.join(parent, 'store');
  const depot = options.existingParent ? ArtifactDepot.openExisting(path.join(parent, 'objects')) : ArtifactDepot.create(path.join(parent, 'objects'));
  let store = options.existingParent ? Store.openExisting(root, {format: LEADER_FORMAT}) : Store.create(root, {format: LEADER_FORMAT}), owner = await store.claimOwner(store.info().generation, 'test', Date.now() + 3600000), serial = 0;
  const reviewPolicy = {id: 'review', version: '1', description: '只读完整选果'}, policy = {profile: 'task-managed-leader/v1', maxCalls: options.maxCalls ?? 9,
    maxActions: 4, maxRequests: 3, repair: options.leaderRepair ?? {nodeIds: ['east', 'west'], maxRounds: 1},
    review: {providerId: 'fixture', policyDigest: hash(reviewPolicy)}, publication: options.publication ? {targetId: options.publication.id, policyDigest: options.publication.policyDigest} : null};
  const leader = createLeaderPort({id: 'leader', providerId: 'fixture', policy,
    prepare: ({input}) => ({prompt: options.preparePrompt ?? renderLeaderPrompt(input)}), parseDecision: parseManagedOutput});
  if(options.protocolCorrection)registerLeaderJsonCorrection(leader,{profile:'leader-json-correction/v1',maxPerTask:1});
  const review = createReviewPort({id: 'review', providerId: 'fixture', policy: reviewPolicy,
    prepare: ({input}) => ({prompt: renderReviewPrompt(input)}),
    parseReport: options.reviewParser ?? (options.reviewAssessments ? args => parseAssessmentProposal(args).report : parseManagedOutput)});
  if(options.reviewAssessments)registerReviewAssessments(review,{profile:'task-review-assessment/v1'});
  let response;
  // These are explicitly controlled Provider/cleanup facts, never an OS crash,
  // model, ACP transport or production business proof. SQLite/Depot are real.
  const provider = {id: 'fixture', start() {const fact = {executionId: 'fake-' + ++serial, startedAt: new Date().toISOString()};
    return {started: Promise.resolve(fact), stop() {}, completion: Promise.resolve({providerId: 'fixture', status: 'completed', stopReason: 'end_turn',
      outputText: typeof response==='string'?response:JSON.stringify(response), cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}})};}};
  const verification = createVerificationPort({id: 'verify', policy: {id: 'check', version: '1', description: '本测试受控独立断言'}, bindPlan: options.binding ?? binding,
    publicationExpected: options.publicationExpected ?? null,
    start({ticket}) {const fact = {executionId: 'checker-' + ticket.workerId, startedAt: new Date().toISOString()};
      if (options.verificationCheck) options.verificationCheck(ticket);
      else {assert.equal(ticket.input.verification.manifests.length, 2);assert.equal(ticket.input.leaderReplies[0].answer, 'north');}
      return {started: Promise.resolve(fact), stop() {}, completion: Promise.resolve({type: 'verification', status: 'passed', cleanup: {started: fact, cleaned: true},
        evidence: {name: 'check.json', mediaType: 'application/json', content: encode({actual: 'north', bothBranches: true})},
        delivery: {name: 'delivery.json', mediaType: 'application/json', content: encode({east: 10, west: 20, region: 'north'})}})};}});
  const execution = {maxWorkers: 3, providerIds: ['fixture'], defaultProvider: 'fixture'};
  let app;
  t.after(async () => {await store.drained; store.close(); depot.close(); fs.rmSync(parent, {recursive: true, force: true});});
  app = new TaskApplication({store, owner, execution, leader, review, verification, depot, observability: options.observability ?? null, publication: options.publication ?? null});
  const f = {get app() {return app;}, provider, parent, results: new Map(), call: request => app.dispatch(request, context),
    read: async callback => await app.transaction(false, callback),
    get: taskId => f.call({operation: 'task.get', taskId}),
    take(action, nodeId) {const command = f.read(tx => tx.commands().find(command => command.status === 'pending' &&
      JSON.parse(command.payload).action === action && (nodeId === undefined || JSON.parse(command.payload).nodeId === nodeId)));
      assert.ok(command, action + ':' + nodeId); return app.execution.nextWork(command.id, command.revision);},
    async decision(ticket, actions) {response = {profile: 'task-managed-leader/v1', callId: ticket.input.leader.callId,
      inputDigest: ticket.input.leader.inputDigest, summary: '只依据原证据推进', actions}; return f.run(ticket, leader);},
    async rawDecision(ticket,text){response=text;return f.run(ticket,leader);},
    leaderPort:leader, reviewPort:review, closeStore:()=>store.close(),
    async run(ticket, port) {
      if (options.observability) {app.execution.observeInput(ticket, 'prepared', '受控模型夹具 password=fixture-secret'); app.execution.observeInput(ticket, 'handed-off');}
      const handle = (ticket.executionType==='review' ? startReviewWithAssessments : startLeaderWithJsonCorrection)(port,{ticket, provider, prepared: {cwd: parent, prompt: '受控模型夹具'}});
      app.execution.started(ticket, await handle.started); const result = await handle.completion; f.results.set(ticket.workerId, result); return app.execution.finish(ticket, result);},
    author(ticket, started = {executionId: 'author-' + ticket.workerId, startedAt: new Date().toISOString()}) {app.execution.started(ticket, started);
      const file = {path: ticket.nodeId + '.json', ...depot.put(encode({value: ticket.nodeId === 'east' ? 10 : 20}))};
      return app.execution.finish(ticket, {status: 'completed', stopReason: 'end_turn', cleanup: {started, cleaned: true}, result: {
        profile: 'task-file-business/v1', taskId: ticket.taskId, nodeId: ticket.nodeId, workerId: ticket.workerId,
        planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest, layoutDigest: hash({profile: 'task-file-business/v1', ...ticket.input.fileLayout}),
        files: [file], inputDigest: fileDigest([]), manifestDigest: fileDigest([file])}});},
    async review(ticket, extra = {}) {response = {profile: 'task-independent-review/v1', inputDigest: ticket.input.review.inputDigest,
      selectionDigest: ticket.input.review.selectionDigest, verdict: 'accept', summary: '独立审阅两原分支', findings: [], ...extra}; return f.run(ticket, review);},
    async rawReview(ticket,value){response=value;return f.run(ticket,review);},
    async verify(ticket) {const handle = verification.start({ticket, prepared: {cwd: parent, prompt: '固定受控检查器'}});
      app.execution.started(ticket, await handle.started); return app.execution.finish(ticket, await handle.completion);},
    async reopen() {await store.drained; store.close(); store = Store.openExisting(root, {format: LEADER_FORMAT}); owner = await store.claimOwner(owner.generation, 'cold', Date.now() + 3600000);
      app = new TaskApplication({store, owner, execution, leader, review, verification, depot, observability: options.observability ?? null, publication: options.publication ?? null});},
  }; return f;
}

export {proposal, hash};
