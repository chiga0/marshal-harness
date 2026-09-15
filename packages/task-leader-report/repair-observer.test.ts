import test from 'node:test';
import assert from 'node:assert/strict';
import {repairObserver} from './live-consumer.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
const hash = value => digest(encode(value)), taskId = 'task-one', task = {id: taskId, revision: 3};
const inputDigest = 'sha256:' + 'a'.repeat(64), selectionBefore = 'sha256:' + 'b'.repeat(64), selectionAfter = 'sha256:' + 'c'.repeat(64);
const worker = {id: 'worker-east-original', nodeId: 'east', role: 'author', attempt: 1, status: 'completed'};
const workers = {taskId, items: [worker, {...worker, id: 'worker-west-original', nodeId: 'west'}], nextCursor: null};
function setup(options = {}) {
  let now = 1000, reads = 0;
  const observer = repairObserver(taskId, {clock: () => now, intervalMs: 0, ...options});
  const artifacts = new Map(), requests = [];
  const view = {taskId, taskRevision: 3, stage: 'review', review: null, lastDecision: null};
  const client = {async getLeader() {reads++; return parseJson(encode(view));}, async request(operation) {requests.push(operation); return parseJson(encode(workers));},
    async downloadArtifact(id) {reads++; return artifacts.get(id);}};
  function artifact(id, report) {
    const content = encode({report}); artifacts.set(id, {artifact: {id, taskId, digest: digest(content)}, content});
  }
  return {observer, client, view, artifact, artifacts, requests, get reads() {return reads;}, advance() {now += 1000;},
    sample: () => observer.sample(client, task, 600000)};
}
test('negative Review plus original accepted repair preserves digest links, never claims unaffected selected result bytes', async () => {
  const f = setup();
  const review = {verdict: 'rework', selectionDigest: selectionBefore, policyDigest: inputDigest, workerId: 'review-worker', evidenceIds: ['negative-evidence']};
  f.view.review = {...review, digest: hash(review)};
  f.artifact('negative-evidence', {profile: 'task-independent-review/v1', verdict: 'rework', selectionDigest: selectionBefore,
    findings: [{nodeIds: ['east'], requirement: 'PRIVATE_REQUIREMENT', observation: 'PRIVATE_FINDING', requestedChange: 'PRIVATE_CHANGE'}]});
  await f.sample();
  assert.equal(f.observer.evidence.repairObserved, null, 'negative opinion alone is not an accepted repair');
  const decision = {profile: 'task-managed-leader/v1', callId: 'call-one', inputDigest, summary: 'PRIVATE_SUMMARY',
    actions: [{type: 'repair', nodeIds: ['east'], basis: {kind: 'review', digest: hash(review)}, feedback: 'PRIVATE_FEEDBACK'}]};
  f.view.review = null; f.view.stage = 'work'; f.view.lastDecision = {digest: hash(decision), callId: 'call-one', evidenceId: 'repair-evidence'};
  f.artifact('repair-evidence', decision); await f.sample();
  f.observer.finish({reworkCount: 1}, {review: {selectionDigest: selectionAfter}}, workers);
  const result = f.observer.evidence;
  assert.equal(result.repairObserved, true); assert.equal(result.negativeReviews[0].reviewDigest, hash(review));
  assert.deepEqual(result.negativeReviews[0].affectedAuthorNodeIds, ['east']);
  assert.equal(result.repairs[0].beforeSelectionDigest, selectionBefore); assert.equal(result.repairs[0].afterSelectionDigest, selectionAfter);
  assert.equal(result.unaffectedBranchPreserved, null); assert.equal(result.repairs[0].affectedNodes, null);
  assert.equal(result.atomic, false); assert.equal(result.authority, false);
  assert.equal(result.independentTerminalSQLiteRequired, true); assert.doesNotMatch(JSON.stringify(result), /PRIVATE_/);
  assert.deepEqual([...new Set(f.requests)], ['task.workers']);
});
test('no repair, missed repair, and negative review alone are explicitly different observations', async () => {
  for (const count of [0, 1]) {
    const f = setup(); await f.sample(); f.observer.finish({reworkCount: count}, {review: {selectionDigest: selectionAfter}}, workers);
    assert.equal(f.observer.evidence.repairObserved, count === 0 ? false : null);
    assert.equal(f.observer.evidence.unaffectedBranchPreserved, null);
  }
});
test('observation bounds truncate instead of failing or extending ordinary Task execution', async () => {
  const f = setup({maxSamples: 2, maxSnapshots: 1, maxArtifacts: 0});
  await f.sample(); f.view.stage = 'work'; await f.sample(); const reads = f.reads;
  for (let i = 0; i < 1000; i++) await f.sample();
  assert.equal(f.reads, reads); assert.equal(f.observer.evidence.samples, 2); assert.equal(f.observer.evidence.snapshots.length, 1);
  assert.equal(f.observer.evidence.truncated, true); assert.equal(f.observer.evidence.observationError, false);
  f.observer.finish({reworkCount: 0}, {review: null}, workers); assert.equal(f.observer.evidence.repairObserved, false);
});
test('failed or forged artifact observation is recorded once, not retried or promoted to repair evidence', async () => {
  const f = setup(); f.view.lastDecision = {digest: inputDigest, callId: 'call-one', evidenceId: 'bad-evidence'};
  f.artifact('bad-evidence', {profile: 'task-managed-leader/v1', callId: 'call-one', actions: []});
  await f.sample(); const reads = f.reads; await f.sample();
  assert.equal(f.reads, reads); assert.equal(f.observer.evidence.observationError, true);
  assert.equal(f.observer.evidence.repairs.length, 0);
  f.observer.finish({reworkCount: 1}, {review: null}, workers); assert.equal(f.observer.evidence.repairObserved, null);
});
test('each optional sample shares one bounded AbortSignal, and deadline proximity skips new reads', async () => {
  const f = setup(); let signal;
  f.client.getLeader = async (_id, options) => {signal = options.signal; throw new Error('PRIVATE_NETWORK_FAILURE');};
  f.client.request = async (_operation, options) => {assert.equal(options.signal, signal); return workers;};
  await f.sample(); assert.ok(signal instanceof AbortSignal); assert.equal(f.observer.evidence.observationError, true);
  const other = setup(); await other.observer.sample(other.client, task, 1100); assert.equal(other.reads, 0);
});
