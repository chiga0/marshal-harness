import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {Store, LEADER_FORMAT, encode, digest, makeEvent} from '../packages/task-store/store.mjs';
import {ACCEPTANCE_EVIDENCE_PROFILE, ATTEMPT_REF_PROFILE, bytesDigest, canonicalDigest,
  validateAcceptanceEvidence, validateAttemptRef, validateSourceArtifact} from './acceptance-contract-freeze.mjs';

const NOW = 1_800_000_000_000;
const empty = () => ({sequence: 0n, digest: ''});
const ref = (stream, event) => ({stream, sequence: event.sequence, digest: event.digest});
const hash = value => digest(encode(value));

function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-acceptance-freeze-')));
  const root = path.join(parent, 'store');
  const store = Store.create(root, {format: LEADER_FORMAT, clock: () => NOW});
  t.after(() => { store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  const owner = store.claimOwner(0n, 'freeze-fixture-owner', NOW + 60000);
  const taskId = 'task-freeze-1', workerId = 'worker-freeze-1', commandId = 'command-freeze-1', inputRef = 'input-freeze-1';
  const input = {profile: 'task-worker-input/v1', taskId, nodeId: 'node-freeze-1', source: 'frozen'};
  const inputDigest = hash(input);
  const frozen = {workerId, taskId, nodeId: input.nodeId, role: 'author', providerId: 'fixture-provider', executionType: 'agent',
    generation: owner.generation.toString(), commandId, inputDigest, planDigest: hash({plan: 'freeze'}), deadline: NOW + 30000};
  const reservationDigest = hash(frozen);
  const record = {ticket: {...frozen, reservationDigest}, inputRef,
    worker: {id: workerId, taskId, nodeId: input.nodeId, providerId: frozen.providerId, role: frozen.role, status: 'completed', attempt: 1,
      startedAt: '2026-01-01T00:00:00.000Z', finishedAt: '2026-01-01T00:00:01.000Z', lastObservedAt: '2026-01-01T00:00:01.000Z', progress: null, usage: {}},
    executionId: 'execution-freeze-1', cleanup: {cleaned: true}, resultRef: null, resultDigest: null, progressSequence: 0};
  const planDigest = frozen.planDigest;
  const candidateDigest = hash({profile: 'candidate/v1', files: [{path: 'result.json', digest: hash({result: true}), bytes: 15}]});
  const selectionDigest = hash({selected: [workerId]});
  const contractDigest = hash({profile: 'task-acceptance-contract/v1', taskId, checks: [{id: 'check-1', method: 'postcondition'}]});
  const sourceBytes = encode({profile: 'fixture-review-evidence/v1', taskId, verdict: 'accept', checkId: 'check-1'});
  const sourceDigest = bytesDigest(sourceBytes), artifactId = 'artifact-freeze-1';
  const taskEvent = makeEvent(taskId, 1, {type: 'task.approved', taskId, planDigest, contractDigest});
  const reservationEvent = makeEvent(taskId, 2, {type: 'worker.reserved', workerId, reservationDigest});
  const artifactEvent = makeEvent(taskId, 3, {type: 'artifact.committed', artifactId, digest: sourceDigest});
  const artifact = {id: artifactId, taskId, name: 'freeze-review.json', kind: 'evidence', status: 'ready', mediaType: 'application/json', digest: sourceDigest, bytes: sourceBytes.length, createdAt: '2026-01-01T00:00:00.000Z'};
  const attemptRef = {profile: ATTEMPT_REF_PROFILE, taskId, workerId, commandId, generation: owner.generation.toString(), reservationDigest,
    reservationEvent: {stream: taskId, sequence: '2', digest: reservationEvent.digest}};
  const plan = {taskId, planDigest, contractDigest};
  const evidence = {profile: ACCEPTANCE_EVIDENCE_PROFILE, taskId, contractDigest, entries: [{
    contractDigest, checkId: 'check-1', method: 'postcondition', capabilityId: 'fixture-postcondition/v1', capabilityDigest: hash({capability: 'fixture'}),
    owner: {kind: 'worker', workerId, attemptRef, generation: owner.generation.toString()}, planDigest, repairId: null,
    candidateDigest, selectionDigest, externalAction: null, receiptDigest: hash({receipt: 'fixture', taskId, workerId}),
    sourceArtifact: {id: artifactId, digest: sourceDigest}, applicability: 'applicable', result: 'pass',
  }]};
  store.write(owner, tx => {
    tx.append(taskId, empty(), [taskEvent, reservationEvent, artifactEvent]);
    const event1 = ref(taskId, taskEvent), event2 = ref(taskId, reservationEvent), event3 = ref(taskId, artifactEvent);
    tx.putProjection('task', taskId, 0, event1, encode({taskId, status: 'completed', planDigest, contractDigest, candidateDigest, selectionDigest, checkIds: ['check-1']}));
    tx.putProjection('attempt', inputRef, 0, event2, encode(input));
    tx.putProjection('attempt', workerId, 0, event2, encode(record));
    tx.putProjection('artifact', artifactId, 0, event3, encode({type: 'manifest', owner: 'local-operator', artifact}));
    tx.putProjection('artifact', 'digest-' + sourceDigest.slice(7), 0, event3, encode({type: 'blob', digest: sourceDigest, bytes: sourceBytes.length}));
  });
  return {store, owner, taskId, workerId, inputRef, inputBytes: encode(input), sourceBytes, artifactId,
    sourceDigest, attemptRef, plan, evidence, contractDigest, planDigest, candidateDigest, selectionDigest};
}

function validate(f, evidence = f.evidence, extra = {}) {
  return f.store.read(f.owner, tx => validateAcceptanceEvidence(tx, evidence, {
    taskId: f.taskId, contractDigest: f.contractDigest, planDigest: f.planDigest,
    candidateDigest: f.candidateDigest, selectionDigest: f.selectionDigest,
    checkIds: ['check-1'], inputBytesById: new Map([[f.inputRef, f.inputBytes]]),
    bytesByArtifactId: new Map([[f.artifactId, f.sourceBytes]]), ...extra,
  }));
}

test('Store canonical digest is independent of object key order but exact bytes remain distinct', () => {
  const left = {z: 1, nested: {b: false, a: '文本'}, a: [1, 2]};
  const right = {a: [1, 2], nested: {a: '文本', b: false}, z: 1};
  assert.equal(canonicalDigest(left), canonicalDigest(right));
  assert.deepEqual(encode(left), encode(right));
  assert.notEqual(bytesDigest(Buffer.from('{"z":1,"a":2}')), bytesDigest(Buffer.from('{"a":2,"z":1}')));
  assert.notEqual(canonicalDigest({a: [1, 2]}), canonicalDigest({a: [2, 1]}));
  assert.throws(() => canonicalDigest({a: undefined}));
  assert.throws(() => canonicalDigest({a: NaN}));
});

test('durable Store facts satisfy the candidate attemptRef and sourceArtifact bindings', t => {
  const f = fixture(t);
  f.store.read(f.owner, tx => {
    const attempt = validateAttemptRef(tx, f.attemptRef, {taskId: f.taskId, inputBytesById: new Map([[f.inputRef, f.inputBytes]])});
    assert.equal(attempt.worker.ticket.reservationDigest, f.attemptRef.reservationDigest);
    const artifact = validateSourceArtifact(tx, {id: f.artifactId, digest: f.sourceDigest}, {taskId: f.taskId, bytesByArtifactId: new Map([[f.artifactId, f.sourceBytes]])});
    assert.equal(artifact.id, f.artifactId);
  });
});

test('complete candidate evidence binds Task, contract, plan, selection, candidate and durable source', t => {
  const f = fixture(t);
  assert.equal(validate(f), f.evidence);
  assert.equal(f.store.info().generation, 1n);
});

test('changing any binding digest, owner identity, reservation event or source artifact is rejected', t => {
  const f = fixture(t);
  const mutations = [
    e => ({...e, contractDigest: hash({changed: 'contract'})}),
    e => ({...e, entries: [{...e.entries[0], planDigest: hash({changed: 'plan'})}]}),
    e => ({...e, entries: [{...e.entries[0], candidateDigest: hash({changed: 'candidate'})}]}),
    e => ({...e, entries: [{...e.entries[0], selectionDigest: hash({changed: 'selection'})}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, workerId: 'worker-other'}}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, attemptRef: {...e.entries[0].owner.attemptRef, commandId: 'command-other'}}}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, attemptRef: {...e.entries[0].owner.attemptRef, reservationEvent: {...e.entries[0].owner.attemptRef.reservationEvent, digest: hash({changed: 'event'})}}}}]}),
    e => ({...e, entries: [{...e.entries[0], sourceArtifact: {id: e.entries[0].sourceArtifact.id, digest: hash({changed: 'artifact'})}}]}),
    e => ({...e, entries: [{...e.entries[0], sourceArtifact: {...e.entries[0].sourceArtifact, extra: true}}]}),
    e => ({...e, entries: [{...e.entries[0], extra: true}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, kind: 'core', workerId: null}}]}),
  ];
  for (const mutate of mutations) assert.throws(() => validate(f, mutate(f.evidence)));
});

test('changing exact source bytes or input snapshot is rejected even when IDs remain unchanged', t => {
  const f = fixture(t);
  assert.throws(() => validate(f, f.evidence, {bytesByArtifactId: new Map([[f.artifactId, Buffer.from('changed')]])}));
  assert.throws(() => validate(f, f.evidence, {inputBytesById: new Map([[f.inputRef, Buffer.from('changed')]])}));
});

test('context digests cannot override the durable Task projection', t => {
  const f = fixture(t);
  assert.throws(() => validate(f, f.evidence, {planDigest: hash({changed: 'plan'})}));
  assert.throws(() => validate(f, f.evidence, {candidateDigest: hash({changed: 'candidate'})}));
  assert.throws(() => validate(f, f.evidence, {selectionDigest: hash({changed: 'selection'})}));
});

test('unknown fields, duplicate reservation events and missing catalog entries cannot downgrade into acceptance', t => {
  const f = fixture(t);
  assert.throws(() => validate(f, {...f.evidence, unknown: true}));
  assert.throws(() => validate(f, {...f.evidence, entries: [...f.evidence.entries, f.evidence.entries[0]]}));
  assert.throws(() => validate(f, f.evidence, {checkIds: ['check-1', 'check-2']}));
});
