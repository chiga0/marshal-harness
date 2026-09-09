import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {fixture, proposal, hash} from '../task-application/leader.test.mjs';
import {encode} from '../task-store/store.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
import {launchCommand} from '../agent-runtime/index.mjs';
import {verifyObservation} from '../agent-runtime/custody-contract.mjs';

// Core decisions use the existing controlled Provider fixture. Only the
// interrupted execution's cleanup below comes from the real custodian/guard.
// These cases are not real publication, model, or service-crash evidence.
const readTask = (f, task) => f.read(tx => f.app.get(tx, task.id));
function publication() {
  return {id: 'reports', policyDigest: hash({id: 'policy'}), configuration: {profile: 'controlled-fixture'}, configurationDigest: hash({profile: 'controlled-fixture'}),
    assertDisjoint() {}, lookup() {return {status: 'unknown'};}, postverify: {id: 'reports-postverify', start() {throw Error('not dispatched');}},
    start({ticket}) {const started = {executionId: 'effect-' + ticket.workerId, startedAt: new Date().toISOString()};
      return {started: Promise.resolve(started), stop() {}, completion: Promise.resolve({type: 'publication', status: 'created',
        cleanup: {started, cleaned: true, scope: 'controlled-fixture'}, evidence: {name: 'receipt.json', mediaType: 'application/json',
          content: encode({status: 'created', binding: ticket.input.publication.binding})}})};}};
}
export async function setup(t, type, options = {}) {
  const f = fixture(t, ['publication', 'postverify'].includes(type) ? {publication: publication(), publicationExpected: () => ({original: 'expected'}), ...options} : options);
  const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '附属执行原义务恢复', limits: {timeoutMs: 120000, maxAttempts: options.maxAttempts ?? 17, maxWorkers: 3}}});
  await f.decision(f.take('leader'), [{type: 'ask', kind: 'business', prompt: '请确认地区', options: [], subject: readTask(f, task).inputDigest, nodeIds: []}]);
  const question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: question.id, key: 'answer',
    body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: question.requestDigest, answer: 'north'}});
  await f.decision(f.take('leader'), [{type: 'plan', proposal}]);
  const plan = await f.call({operation: 'task.plan', taskId: task.id});
  await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: (await f.get(task.id)).revision, planDigest: plan.digest, planRevision: plan.revision}});
  const dispatch = f.read(tx => tx.commands().find(c => JSON.parse(c.payload).action === 'dispatch')); f.app.execution.expandDispatch(dispatch.id, dispatch.revision);
  f.author(f.take('execute', 'east')); f.author(f.take('execute', 'west'));
  let ticket = f.take('leader'); if (type === 'leader') return {f, task, ticket};
  const selectionDigest = hash(ticket.input.leader.snapshot.selection);
  await f.decision(ticket, [{type: 'work', kind: 'review', nodeIds: ['east', 'west'], selectionDigest}]);
  ticket = f.take('review'); if (type === 'review') return {f, task, ticket};
  await f.review(ticket); await f.decision(f.take('leader'), [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest}]);
  await f.verify(f.take('execute', 'verify'));
  const record = readTask(f, task), artifact = f.read(tx => record.task.artifactIds.map(id => f.app.artifacts.metadata(tx, id)).find(a => a.kind === 'delivery'));
  await f.decision(f.take('leader'), [{type: 'deliver', artifactId: artifact.id, acceptanceDigest: record.acceptance.digest, reviewDigest: record.leader.review.digest}]);
  const authorization = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: authorization.id, key: 'allow',
    body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: authorization.requestDigest, decision: 'allow'}});
  if (type === 'publication' && options.unreserved) return {f, task};
  ticket = f.take('publication'); if (type === 'publication') return {f, task, ticket};
  const handle = f.app.leader.effects.publication.start({ticket, provider: f.provider, prepared: {cwd: f.parent, prompt: 'controlled publication'}});
  f.app.execution.started(ticket, await handle.started); f.app.execution.finish(ticket, await handle.completion);
  return {f, task, ...(options.unreserved ? {} : {ticket: f.take('postverify')})};
}
export async function bound(f, ticket, start) {
  const root = path.join(f.parent, 'custody'); fs.mkdirSync(root, {mode: 0o700});
  const manager = createExecutionCustody({root}), profile = {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true};
  try {
    const handle = await manager.prepare(f.app.execution.custodyBinding(ticket, profile)); f.app.execution.bindCustody(ticket, handle.descriptor, profile);
    if (start) {
      handle.permit(); const runtime = await launchCommand({executable: process.execPath,
        args: [fileURLToPath(new URL('../agent-runtime/command.fixture.mjs', import.meta.url)), 'sum'], cwd: f.parent, env: {}, deadline: ticket.deadline,
        input: Buffer.from('{"nonce":"original","values":[5]}\n'), executionContext: {launch: handle.launch}});
      f.app.execution.started(ticket, runtime.started); assert.equal((await runtime.completion).cleanup.cleaned, true);
    } else await handle.stop();
    const observation = manager.read(handle.descriptor); assert.equal(verifyObservation(handle.descriptor, observation), true);
    assert.equal(observation.payload.cleanup.cleaned, true); return observation;
  } finally {await manager.close();}
}
const snapshot = (f, task) => f.read(tx => ({head: tx.head(task.id), task: f.app.get(tx, task.id), capacity: f.app.execution.capacity(tx).value,
  commands: tx.commands().map(c => ({id: c.id, revision: c.revision, status: c.status}))}));

for (const type of ['leader', 'review', 'publication', 'postverify']) test('attached ' + type + ': original signed cleanup, no DAG fiction or retry', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, type), observation = await bound(f, ticket, true), before = readTask(f, task);
  f.reopen(); f.app.now = () => ticket.deadline + 1;
  const result = f.app.execution.reconcileCleanup(ticket.workerId, observation), after = readTask(f, task);
  assert.equal(result.status, 'failed'); assert.equal(after.task.status, type === 'publication' ? 'intervention' : 'failed');
  assert.equal(after.attempts, before.attempts); assert.deepEqual(after.plan, before.plan); assert.deepEqual(after.selectedResults, before.selectedResults);
  assert.deepEqual(after.leader.history, before.leader.history); assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
  assert.equal(f.read(tx => tx.command(ticket.commandId)).status, type === 'publication' ? 'unknown' : 'observed');
  assert.equal(after.nodes.find(node => node.id === ticket.nodeId), undefined);
  if (type === 'leader') {assert.equal(after.leader.activeCallId, null); assert.equal(after.leader.activeWorkerId, null); assert.equal(after.leader.obligationId, null);
    assert.equal(f.read(tx => JSON.parse(tx.projection('interaction', ticket.input.leader.obligationId).bytes)).status, 'closed');}
  if (type === 'publication') {assert.equal(after.leader.publication.status, 'unknown'); assert.equal(after.leader.publication.receiptArtifactId, null); assert.equal(after.leader.postverify, null);}
  if (type === 'postverify') {assert.deepEqual(after.leader.publication, before.leader.publication); assert.equal(after.leader.postverify.status, 'failed'); assert.equal(after.leader.postverify.evidenceArtifactId, null);}
  const settled = snapshot(f, task); assert.deepEqual(f.app.execution.reconcileCleanup(ticket.workerId, observation), result); assert.deepEqual(snapshot(f, task), settled);
  f.reopen(); assert.deepEqual(f.app.execution.reconcileCleanup(ticket.workerId, observation), result); assert.deepEqual(snapshot(f, task), settled);
});
test('attached publication original signed none-start is not an unknown created effect', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, 'publication'), observation = await bound(f, ticket, false); f.reopen(); f.app.now = () => ticket.deadline + 1;
  assert.equal(f.app.execution.reconcileCleanup(ticket.workerId, observation).status, 'failed');
  const record = readTask(f, task); assert.equal(record.task.status, 'failed'); assert.equal(record.leader.publication.status, 'failed');
  assert.equal(record.leader.publication.receiptArtifactId, null); assert.equal(f.read(tx => tx.command(ticket.commandId)).status, 'observed');
});
test('attached Leader mismatched obligation/call/input/command stays unresolved; original signed proof remains usable', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, 'leader'), observation = await bound(f, ticket, false); f.reopen(); f.app.now = () => ticket.deadline + 1;
  const before = snapshot(f, task), originalTransaction = f.app.transaction.bind(f.app);
  // Transparent read-fault injection only. No private Store bytes are edited;
  // every rejected settlement still executes through the actual SQLite TX.
  for (const fault of ['obligation', 'input', 'activeCall', 'command']) {
    f.app.transaction = (write, callback) => originalTransaction(write, tx => callback(new Proxy(tx, {get(target, key) {
      if (key === 'projection') return (kind, id) => {const row = target.projection(kind, id); if (!row) return row;
        const value = JSON.parse(row.bytes);
        if (fault === 'obligation' && id === ticket.input.leader.obligationId) value.workerId = 'foreign-worker';
        if (fault === 'input' && kind === 'attempt' && value.leader?.callId === ticket.input.leader.callId) value.leader.callId = 'foreign-call';
        if (fault === 'activeCall' && kind === 'task' && id === task.id) value.leader.activeCallId = 'foreign-call';
        return {...row, bytes: encode(value)};};
      if (key === 'command' && fault === 'command') return id => {const command = target.command(id); return id === ticket.commandId ? {...command, generation: 99n} : command;};
      const value = Reflect.get(target, key); return typeof value === 'function' ? value.bind(target) : value;
    }})));
    try {assert.throws(() => f.app.execution.reconcileCleanup(ticket.workerId, observation), {code: 'recovery_required'});}
    finally {f.app.transaction = originalTransaction;}
    assert.deepEqual(snapshot(f, task), before, fault + ' must roll back');
  }
  assert.equal(f.app.execution.reconcileCleanup(ticket.workerId, observation).status, 'failed');
});
test('unbound attached reservations never inherit staging-only eligibility from v7 format', async t => {
  const {f, task, ticket} = await setup(t, 'leader'); assert.equal(ticket.startProtocol, undefined); f.reopen();
  const before = snapshot(f, task); assert.throws(() => f.app.execution.settleUnpermitted(ticket.workerId), {code: 'recovery_required'});
  assert.deepEqual(snapshot(f, task), before);
});
