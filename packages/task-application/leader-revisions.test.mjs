import test from 'node:test';
import assert from 'node:assert/strict';
import {setImmediate as turn} from 'node:timers/promises';
import {fixture, proposal, hash} from './leader.test.mjs';
import {encode} from '../task-store/store.mjs';
import {TaskExecutionCoordinator} from '../task-execution/controller.mjs';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {startTaskService} from '../task-service/composition.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, parseManagedOutput} from './application.mjs';

// This suite uses real SQLite/Depot and explicitly controlled Provider facts.
// It is not an OS cleanup/publication test; the HTTP tests cover original guard.
const record = (f, task) => f.read(tx => f.app.get(tx, task.id));
const head = (f, task) => f.read(tx => tx.head(task.id));
const start = (f, ticket) => {const fact = {executionId: 'author-' + ticket.workerId, startedAt: new Date().toISOString()};
  f.app.execution.started(ticket, fact); return fact;};
const failAuthor = (f, ticket, fact, extra = {}) => f.app.execution.finish(ticket, {status: 'failed', reason: 'provider_failed',
  stopReason: null, cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}, ...extra});
async function setup(t, options) {
  const f = fixture(t, options), task = await f.call({operation: 'task.create', key: 'create', body: {intent: '完整原两分支',
    limits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}}});
  let ticket = f.take('leader'); await f.decision(ticket, [{type: 'ask', kind: 'business', prompt: '地区待明确', options: [],
    subject: record(f, task).inputDigest, nodeIds: []}]);
  const current = await f.get(task.id), question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', key: 'answer', taskId: task.id, requestId: question.id,
    body: {expectedRevision: current.revision, requestDigest: question.requestDigest, answer: 'north'}});
  ticket = f.take('leader'); await f.decision(ticket, [{type: 'plan', proposal}]);
  const plan = await f.call({operation: 'task.plan', taskId: task.id});
  await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: (await f.get(task.id)).revision,
    planRevision: plan.revision, planDigest: plan.digest}});
  const dispatch = f.read(tx => tx.commands().find(value => JSON.parse(value.payload).action === 'dispatch'));
  f.app.execution.expandDispatch(dispatch.id, dispatch.revision);
  const east = f.take('execute', 'east'), west = f.take('execute', 'west');
  return {f, task, east, west};
}
for (const stale of [false, true]) test('Leader wait and claimed obligation preserve subsequent facts; stale=' + stale, async t => {
  const {f, task, east, west} = await setup(t), westStarted = start(f, west);
  failAuthor(f, east, start(f, east));
  const leader = f.take('leader'), sources = leader.input.leader.snapshot.obligation;
  if (stale) f.author(west, westStarted);
  const outcome = await f.decision(leader, [{type: 'conclude', outcome: 'wait', summary: '等待原西分支完成', basisDigests: []}]);
  assert.equal(outcome.status, stale ? 'failed' : 'completed'); assert.equal((await f.get(task.id)).status, 'running');
  if (!stale) f.author(west, westStarted);
  const successor = f.take('leader'); assert.ok(successor);
  assert.equal(successor.input.leader.snapshot.selection[0].nodeId, 'west');
  if (stale) for (const source of sources) assert.ok(successor.input.leader.snapshot.obligation.some(value => hash(value) === hash(source)));
  assert.equal(record(f, task).leader.calls, 4); // Two initial calls + original wait + one successor, no refund.
  const before = head(f, task); f.app.execution.finish(leader, f.results.get(leader.workerId)); assert.deepEqual(head(f, task), before);
  assert.equal(f.read(tx => tx.commands().filter(value => value.status === 'pending' && JSON.parse(value.payload).action === 'leader')).length, 0);
});
for (const reason of ['structure', 'permission_denied', 'provider_failed', 'missing']) test('only classified ordinary execution failure may repair: ' + reason, async t => {
  const {f, task, east, west} = await setup(t); f.author(west);
  failAuthor(f, east, start(f, east), reason === 'structure' ? {status: 'completed', stopReason: 'end_turn', result: null} : {reason: reason === 'missing' ? undefined : reason});
  const leader = f.take('leader'), basis = leader.input.leader.snapshot.evidence.find(value => value.kind === 'execution-failure');
  assert.equal(basis.retryable, reason === 'provider_failed');
  const before = record(f, task).attempts;
  const result = await f.decision(leader, [{type: 'repair', nodeIds: ['east'], basis: {kind: 'execution-failure', digest: basis.digest}, feedback: '依据原普通失败重做该节点'}]);
  assert.equal(result.status, reason === 'provider_failed' ? 'completed' : 'failed');
  assert.equal(record(f, task).attempts, before);
  if (reason === 'provider_failed') assert.ok(f.take('execute', 'east'));
  else {assert.equal(record(f, task).activeRepair, undefined); assert.equal((await f.get(task.id)).status, 'cancelling');}
});
function publicationFixture() {
  let starts = 0, resolve;
  const value = {id: 'reports', policyDigest: hash({id: 'policy'}), configuration: {profile: 'controlled-fixture'}, configurationDigest: hash({profile: 'controlled-fixture'}),
    assertDisjoint() {}, lookup() {return {status: 'unknown'};},
    start({ticket}) {starts++; const fact = {executionId: 'effect-' + ticket.workerId, startedAt: new Date().toISOString()};
      return {started: Promise.resolve(fact), stop() {}, completion: new Promise(done => {resolve = () => done({type: 'publication', status: 'created',
        cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}, evidence: {name: 'receipt.json', mediaType: 'application/json',
          content: encode({binding: ticket.input.publication.binding, status: 'created'})}});})};},
    postverify: {id: 'reports-postverify', start() {throw Error('forbidden successor');}}};
  return {value, get starts() {return starts;}, complete() {assert.ok(resolve); resolve();}};
}
async function delivery(t, options) {
  const result = await setup(t, options), {f, task, east, west} = result; f.author(east); f.author(west);
  let leader = f.take('leader'), selectionDigest = hash(leader.input.leader.snapshot.selection);
  await f.decision(leader, [{type: 'work', kind: 'review', nodeIds: ['east', 'west'], selectionDigest}]); await f.review(f.take('review'));
  leader = f.take('leader'); await f.decision(leader, [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest}]); await f.verify(f.take('execute', 'verify'));
  const taskRecord = record(f, task), artifact = f.read(tx => taskRecord.task.artifactIds.map(id => f.app.artifacts.metadata(tx, id)).find(value => value.kind === 'delivery'));
  leader = f.take('leader'); await f.decision(leader, [{type: 'deliver', artifactId: artifact.id, acceptanceDigest: taskRecord.acceptance.digest, reviewDigest: taskRecord.leader.review.digest}]);
  const question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', key: 'allow', taskId: task.id, requestId: question.id,
    body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: question.requestDigest, decision: 'allow'}});
  return result;
}
test('publication capability is required before any Task; concrete expected failure precedes reservation/effect', async t => {
  const publication = publicationFixture();
  assert.throws(() => fixture(t, {publication: publication.value}), {code: 'invalid_leader_config'}); assert.equal(publication.starts, 0);
  const {f, task} = await delivery(t, {publication: publication.value, publicationExpected() {throw Error('cannot construct original expectation');}});
  const before = head(f, task), attempts = record(f, task).attempts;
  assert.throws(() => f.take('publication'), {code: 'unsupported_task'});
  assert.deepEqual(head(f, task), before); assert.equal(record(f, task).attempts, attempts); assert.equal(publication.starts, 0);
});
test('Service rejects missing private postverify expectation before root create/claim', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-invalid-'))), root = path.join(parent, 'service');
  t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  const publication = publicationFixture(), reviewPolicy = {id: 'review', version: '1', description: 'controlled'},
    review = createReviewPort({id: 'review', providerId: 'fixture', policy: reviewPolicy, prepare: () => ({prompt: 'unused'}), parseReport: parseManagedOutput}),
    leader = createLeaderPort({id: 'leader', providerId: 'fixture', policy: {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 4, maxRequests: 3,
      repair: {nodeIds: ['east'], maxRounds: 1}, review: {providerId: 'fixture', policyDigest: hash(reviewPolicy)},
      publication: {targetId: publication.value.id, policyDigest: publication.value.policyDigest}}, prepare: () => ({prompt: 'unused'}), parseDecision: parseManagedOutput});
  const verification = createVerificationPort({id: 'verify', policy: {id: 'verify', version: '1', description: 'controlled'}, bindPlan() {}, start() {throw Error('not reached');}});
  await assert.rejects(startTaskService({root, mode: 'create', providers: new Map([['fixture', {id: 'fixture', start() {throw Error('not reached');}}]]),
    custody: {profile: 'node-execution-custody/v1'}, leader, review, verification, publication: publication.value,
    businessFactory() {}, applicationOptions: {execution: {maxWorkers: 3}, defaultLimits: {timeoutMs: 60000, maxAttempts: 17, maxWorkers: 3}}}), {code: 'invalid_leader_config'});
  assert.equal(fs.existsSync(root), false); assert.equal(publication.starts, 0);
});
for (const stop of ['cancel', 'deadline']) test('original publication receipt survives ' + stop + ' without authorizing postverify', async t => {
  const publication = publicationFixture(), {f, task} = await delivery(t, {publication: publication.value, publicationExpected: () => ({expected: 'frozen-before-create'})});
  const ticket = f.take('publication'); assert.deepEqual(ticket.input.publicationExpected, {expected: 'frozen-before-create'});
  const handle = f.app.leader.effects.publication.start({ticket, prepared: {cwd: f.parent, prompt: 'controlled'}, provider: f.provider});
  f.app.execution.started(ticket, await handle.started); publication.complete(); const result = await handle.completion;
  if (stop === 'cancel') await f.call({operation: 'task.cancel', key: 'cancel', taskId: task.id, body: {expectedRevision: (await f.get(task.id)).revision}});
  else f.app.now = () => ticket.deadline + 1;
  const worker = f.app.execution.finish(ticket, result); assert.equal(worker.status, 'cancelled');
  const stored = record(f, task); assert.equal(stored.leader.publication.status, 'created'); assert.ok(stored.leader.publication.receiptArtifactId);
  assert.equal(stored.leader.postverify, null); assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
  const artifact = f.read(tx => f.app.artifacts.metadata(tx, stored.leader.publication.receiptArtifactId));
  assert.equal(JSON.parse(f.app.artifacts.bytes(artifact)).status, 'created');
  assert.equal(f.read(tx => tx.command(ticket.commandId)).status, 'observed');
  const before = head(f, task); f.app.execution.finish(ticket, result); assert.deepEqual(head(f, task), before);
});
test('clean process without original effect receipt stays unknown, never failed-and-cleared', async t => {
  const publication = publicationFixture(), {f, task} = await delivery(t, {publication: publication.value, publicationExpected: () => ({expected: 'frozen'})});
  const ticket = f.take('publication'), fact = start(f, ticket);
  f.app.execution.finish(ticket, {type: 'publication', status: 'failed', cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}});
  assert.equal(record(f, task).leader.publication.status, 'unknown'); assert.equal(record(f, task).leader.publication.receiptArtifactId, null);
  assert.equal((await f.get(task.id)).status, 'intervention'); assert.equal(f.read(tx => tx.command(ticket.commandId)).status, 'unknown');
  assert.equal(record(f, task).leader.postverify, null); assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
});
test('Execution forwards original effect capability after stop instead of dropping it', async t => {
  const publication = publicationFixture(), {f, task} = await delivery(t, {publication: publication.value, publicationExpected: () => ({expected: 'frozen'})});
  const execution = new TaskExecutionCoordinator({execution: f.app.execution, providers: new Map([[f.provider.id, f.provider]]),
    prepare: async () => ({cwd: f.parent, prompt: 'controlled'}), collect: async () => ({}),
    managed: {prepare: async () => ({cwd: f.parent, prompt: 'controlled'}), validate() {},
      provider: ticket => f.app.leader.effects[ticket.executionType], start: options => f.app.leader.effects[options.ticket.executionType].start(options)}});
  t.after(() => execution.close());
  await execution.tick(); for (let i = 0; i < 100 && !publication.starts; i++) await turn(); assert.equal(publication.starts, 1);
  await f.call({operation: 'task.cancel', key: 'cancel', taskId: task.id, body: {expectedRevision: (await f.get(task.id)).revision}});
  await execution.tick(); publication.complete();
  for (let i = 0; i < 100 && execution.snapshot().owned.length; i++) {await turn(); await execution.tick();}
  assert.equal(execution.snapshot().failure, null); assert.equal(record(f, task).leader.publication.status, 'created');
  assert.ok(record(f, task).leader.publication.receiptArtifactId); assert.equal(record(f, task).leader.postverify, null);
  assert.equal((await execution.close()).clean, true);
});
