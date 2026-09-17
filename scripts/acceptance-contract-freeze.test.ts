import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {Store, encode, digest, makeEvent} from '../packages/task-store/store.ts';
import {TaskApplication} from '../packages/task-application/application.ts';
import {ACCEPTANCE_EVIDENCE_PROFILE, ATTEMPT_REF_PROFILE, bytesDigest, canonicalDigest,
  validateAcceptanceEvidence, validateAttemptRef, validateSourceArtifact} from './acceptance-contract-freeze.ts';

const NOW = 1_800_000_000_000;
const hash = value => digest(encode(value));

async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-acceptance-freeze-')));
  const root = path.join(parent, 'store');
  let sequence = 0;
  const store = Store.create(root, {clock: () => NOW});
  t.after(() => { store.close(); fs.rmSync(parent, {recursive: true, force: true}); });
  const owner = store.claimOwner(0n, 'freeze-fixture-owner', NOW + 60000);
  const app = new TaskApplication({store, owner, clock: () => NOW,
    makeId: prefix => `${prefix}-freeze-${++sequence}`,
    execution: {maxWorkers: 2, providerIds: ['fixture-provider'], defaultProvider: 'fixture-provider'}});
  const context = {principal: 'local-operator'};
  const created = await app.dispatch({operation: 'task.create', key: 'create-freeze', body: {
    intent: '完成合同冻结测试夹具', limits: {timeoutMs: 60000, maxAttempts: 8, maxWorkers: 2}}}, context);
  const proposal = {summary: '执行一个作者节点后读取真实预留事实',
    nodes: [{id: 'author', role: 'author', goal: '产生冻结测试候选', scope: ['result'], providerId: null}],
    edges: [], deliverables: ['result'], acceptance: ['独立检查候选绑定'], assumptions: []};
  const frozenPlan = app.proposePlan(created.id, created.revision, proposal);
  const task = await app.dispatch({operation: 'task.get', taskId: created.id}, context);
  await app.dispatch({operation: 'task.approve', key: 'approve-freeze', taskId: task.id,
    body: {expectedRevision: task.revision, planRevision: frozenPlan.revision, planDigest: frozenPlan.digest}}, context);
  const dispatch = store.read(owner, tx => tx.commands().find(command =>
    JSON.parse(command.payload.toString()).action === 'dispatch'));
  assert.ok(dispatch);
  assert.equal(app.execution.expandDispatch(dispatch.id, Number(dispatch.revision)), true);
  const command = store.read(owner, tx => tx.commands().find(item =>
    JSON.parse(item.payload.toString()).action === 'execute'));
  assert.ok(command);
  const ticket = app.execution.nextWork(command.id, Number(command.revision));
  assert.ok(ticket);
  const workerId = ticket.workerId, taskId = ticket.taskId, inputRef = store.read(owner, tx =>
    JSON.parse(tx.projection('attempt', workerId).bytes.toString()).inputRef);
  const inputBytes = store.read(owner, tx => tx.projection('attempt', inputRef).bytes);
  const input = JSON.parse(inputBytes.toString());
  const record = store.read(owner, tx => JSON.parse(tx.projection('attempt', workerId).bytes.toString()));
  const planDigest = ticket.planDigest;
  const reservationDigest = ticket.reservationDigest;
  const candidateDigest = hash({profile: 'candidate/v1', files: [{path: 'result.json', digest: hash({result: true}), bytes: 15}]});
  const selectionDigest = hash({selected: [workerId]});
  const contractDigest = hash({profile: 'task-acceptance-contract/v1', taskId, checks: [{id: 'check-1', method: 'postcondition'}]});
  const sourceBytes = encode({profile: 'fixture-review-evidence/v1', taskId, verdict: 'accept', checkId: 'check-1'});
  const sourceDigest = bytesDigest(sourceBytes), artifactId = 'artifact-freeze-1';
  const reservationEvent = store.read(owner, tx => tx.eventsWithField(taskId, 'workerId', workerId, 2, ['worker.reserved'])[0]);
  assert.ok(reservationEvent);
  const artifact = {id: artifactId, taskId, name: 'freeze-review.json', kind: 'evidence', status: 'ready', mediaType: 'application/json', digest: sourceDigest, bytes: sourceBytes.length, createdAt: '2026-01-01T00:00:00.000Z'};
  const attemptRef = {profile: ATTEMPT_REF_PROFILE, taskId, workerId, commandId: ticket.commandId,
    generation: owner.generation.toString(), reservationDigest,
    reservationEvent: {stream: taskId, sequence: String(reservationEvent.sequence), digest: reservationEvent.digest}};
  const evidence = {profile: ACCEPTANCE_EVIDENCE_PROFILE, taskId, contractDigest, entries: [{
    contractDigest, checkId: 'check-1', method: 'postcondition', capabilityId: 'fixture-postcondition/v1', capabilityDigest: hash({capability: 'fixture'}),
    owner: {kind: 'worker', workerId, attemptRef, generation: owner.generation.toString()}, planDigest, repairId: null,
    candidateDigest, selectionDigest, externalAction: null, receiptDigest: hash({receipt: 'fixture', taskId, workerId}),
    sourceArtifact: {id: artifactId, digest: sourceDigest}, applicability: 'applicable', result: 'pass',
  }]};
  store.write(owner, tx => {
    const head = tx.head(taskId), artifactEvent = makeEvent(taskId, head.sequence + 1n, {type: 'artifact.committed', artifactId, digest: sourceDigest});
    const event3 = {stream: taskId, ...tx.append(taskId, head, [artifactEvent])};
    tx.putProjection('artifact', artifactId, 0, event3, encode({type: 'manifest', owner: 'local-operator', artifact}));
    tx.putProjection('artifact', 'digest-' + sourceDigest.slice(7), 0, event3, encode({type: 'blob', digest: sourceDigest, bytes: sourceBytes.length}));
  });
  return {store, owner, taskId, workerId, inputRef, inputBytes, input, record, sourceBytes, artifactId,
    sourceDigest, attemptRef, evidence, contractDigest, planDigest, candidateDigest, selectionDigest};
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

test('durable Store facts satisfy the candidate attemptRef and sourceArtifact bindings', async t => {
  const f = await fixture(t);
  f.store.read(f.owner, tx => {
    const attempt = validateAttemptRef(tx, f.attemptRef, {taskId: f.taskId, inputBytesById: new Map([[f.inputRef, f.inputBytes]])});
    assert.equal(attempt.worker.ticket.reservationDigest, f.attemptRef.reservationDigest);
    const artifact = validateSourceArtifact(tx, {id: f.artifactId, digest: f.sourceDigest}, {taskId: f.taskId, bytesByArtifactId: new Map([[f.artifactId, f.sourceBytes]])});
    assert.equal(artifact.id, f.artifactId);
  });
});

test('complete candidate evidence binds Task, contract, plan, selection, candidate and durable source', async t => {
  const f = await fixture(t);
  assert.equal(validate(f), f.evidence);
  assert.equal(f.store.info().generation, 1n);
});

test('changing any binding digest, owner identity, reservation event or source artifact is rejected', async t => {
  const f = await fixture(t);
  const mutations = [
    e => ({...e, contractDigest: hash({changed: 'contract'})}),
    e => ({...e, entries: [{...e.entries[0], planDigest: hash({changed: 'plan'})}]}),
    e => ({...e, entries: [{...e.entries[0], candidateDigest: hash({changed: 'candidate'})}]}),
    e => ({...e, entries: [{...e.entries[0], selectionDigest: hash({changed: 'selection'})}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, workerId: 'worker-other'}}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, attemptRef: {...e.entries[0].owner.attemptRef, commandId: 'command-other'}}}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, attemptRef: {...e.entries[0].owner.attemptRef, reservationDigest: hash({changed: 'reservation'})}}}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, attemptRef: {...e.entries[0].owner.attemptRef, reservationEvent: {...e.entries[0].owner.attemptRef.reservationEvent, digest: hash({changed: 'event'})}}}}]}),
    e => ({...e, entries: [{...e.entries[0], sourceArtifact: {id: e.entries[0].sourceArtifact.id, digest: hash({changed: 'artifact'})}}]}),
    e => ({...e, entries: [{...e.entries[0], sourceArtifact: {...e.entries[0].sourceArtifact, extra: true}}]}),
    e => ({...e, entries: [{...e.entries[0], extra: true}]}),
    e => ({...e, entries: [{...e.entries[0], owner: {...e.entries[0].owner, kind: 'core', workerId: null}}]}),
  ];
  for (const mutate of mutations) assert.throws(() => validate(f, mutate(f.evidence)));
});

test('changing exact source bytes or input snapshot is rejected even when IDs remain unchanged', async t => {
  const f = await fixture(t);
  assert.throws(() => validate(f, f.evidence, {bytesByArtifactId: new Map([[f.artifactId, Buffer.from('changed')]])}));
  assert.throws(() => validate(f, f.evidence, {inputBytesById: new Map([[f.inputRef, Buffer.from('changed')]])}));
});

test('reservationDigest is recomputed over the real ticket and nested input', async t => {
  const f = await fixture(t);
  f.store.write(f.owner, tx => {
    const workerRow = tx.projection('attempt', f.workerId);
    const inputRow = tx.projection('attempt', f.inputRef);
    const worker = JSON.parse(workerRow.bytes.toString('utf8'));
    worker.ticket.nodeId = 'tampered-node';
    const head = tx.head(f.taskId);
    const mutation = makeEvent(f.taskId, head.sequence + 1n, {type: 'freeze.test.ticket-mutated', workerId: f.workerId});
    const source = {stream: f.taskId, ...tx.append(f.taskId, head, [mutation])};
    tx.putProjection('attempt', f.workerId, workerRow.revision, source, encode(worker));
    tx.putProjection('attempt', f.inputRef, inputRow.revision, source, inputRow.bytes);
  });
  const mutatedTicket = {...f.record.ticket, nodeId: 'tampered-node'};
  delete mutatedTicket.reservationDigest;
  assert.notEqual(canonicalDigest({...mutatedTicket, input: f.input}), f.attemptRef.reservationDigest);
  assert.throws(() => validate(f));
});

test('context digests cannot override the durable Task projection', async t => {
  const f = await fixture(t);
  assert.throws(() => validate(f, f.evidence, {planDigest: hash({changed: 'plan'})}));
  assert.throws(() => validate(f, f.evidence, {candidateDigest: hash({changed: 'candidate'})}));
  assert.throws(() => validate(f, f.evidence, {selectionDigest: hash({changed: 'selection'})}));
});

test('unknown fields, duplicate reservation events and missing catalog entries cannot downgrade into acceptance', async t => {
  const f = await fixture(t);
  assert.throws(() => validate(f, {...f.evidence, unknown: true}));
  assert.throws(() => validate(f, {...f.evidence, entries: [...f.evidence.entries, f.evidence.entries[0]]}));
  assert.throws(() => validate(f, f.evidence, {checkIds: ['check-1', 'check-2']}));
});
