import test from 'node:test';
import assert from 'node:assert/strict';
import {setup, bound} from './leader-recovery-core.test.mjs';
import {fixture, proposal, hash} from '../task-application/leader.test.mjs';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {createLocalReportPublication} from '../task-publication-report/index.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
import {encode} from '../task-store/store.mjs';
const current = (f, task) => f.read(tx => f.app.get(tx, task.id));
async function finish(f, task, ticket, type, beforeConclude = false) {
  if (type === 'review') await f.review(ticket);
  else await f.decision(ticket, [{type: 'work', kind: 'review', nodeIds: ['east', 'west'], selectionDigest: hash(ticket.input.leader.snapshot.selection)}]);
  if (type !== 'review') await f.review(f.take('review'));
  let leader = f.take('leader');
  await f.decision(leader, [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(leader.input.leader.snapshot.selection)}]);
  await f.verify(f.take('execute', 'verify'));
  const record = current(f, task), artifact = f.read(tx => record.task.artifactIds.map(id => f.app.artifacts.metadata(tx, id)).find(a => a.kind === 'delivery'));
  await f.decision(f.take('leader'), [{type: 'deliver', artifactId: artifact.id, acceptanceDigest: record.acceptance.digest, reviewDigest: record.leader.review.digest}]);
  if (beforeConclude) return f.take('leader');
  await f.decision(f.take('leader'), [{type: 'conclude', outcome: 'succeeded', summary: '原需求、原两分支和独立验证已完整完成', basisDigests: [record.acceptance.digest, record.leader.review.digest]}]);
}
for (const type of ['leader', 'review']) test(type + ' interruption: one budgeted current-generation successor completes the original team', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, type), observation = await bound(f, ticket, true), before = current(f, task);
  f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
  assert.equal((await f.get(task.id)).status, 'running'); assert.equal(current(f, task).attempts, before.attempts);
  const successor = f.take(type); assert.notEqual(successor.workerId, ticket.workerId); assert.notEqual(successor.generation, ticket.generation);
  assert.equal(successor.deadline, ticket.deadline); assert.equal(successor.input.leaderReplies[0].answer, 'north');
  assert.deepEqual(current(f, task).selectedResults, before.selectedResults);
  assert.throws(() => f.app.execution.finish(ticket, {cleanup: observation.payload.cleanup}), {code: 'recovery_required'});
  await finish(f, task, successor, type); const completed = await f.get(task.id); assert.equal(completed.status, 'completed');
  const head = f.read(tx => tx.head(task.id)); f.reopen(); f.app.leader.recover(task.id);
  assert.deepEqual(await f.get(task.id), completed); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
});

// Controlled effect facts exercise reducer branches, not external publication
// or HTTP proof. The separate actual-local-publication case uses the real port.
function controlledEffects(status = 'absent', onLookup = () => {}) {
  let starts = 0;
  const data = encode({east: 10, west: 20, region: 'north'});
  const cleanup = ticket => ({started: {executionId: 'effect-' + ticket.workerId, startedAt: new Date().toISOString()}, cleaned: true});
  return {id: 'reports', policyDigest: hash({id: 'policy'}), configuration: {profile: 'controlled-recovery'}, configurationDigest: hash({profile: 'controlled-recovery'}),
    assertDisjoint() {}, get starts() {return starts;}, lookup(binding) {onLookup(); return {status, binding,
      evidence: {name: 'lookup.json', mediaType: 'application/json', content: encode({status, binding, createdByThisExecution: false})}};},
    start({ticket}) {starts++; const c = cleanup(ticket); return {started: Promise.resolve(c.started), stop() {}, completion: Promise.resolve({type: 'publication',
      status: 'created', cleanup: c, evidence: {name: 'receipt.json', mediaType: 'application/json', content: encode({status: 'created', binding: ticket.input.publication.binding})}})};},
    postverify: {id: 'reports-postverify', start({ticket}) {const c = cleanup(ticket); return {started: Promise.resolve(c.started), stop() {}, completion: Promise.resolve({
      type: 'verification', status: 'passed', cleanup: c, evidence: {name: 'postverify.json', mediaType: 'application/json', content: encode({expected: ticket.input.postverify.expected})},
      delivery: {name: 'delivery.json', mediaType: 'application/json', content: data}})};}}};
}
const effectOptions = port => ({publication: port, publicationExpected: () => ({east: 10, west: 20, region: 'north'})});
async function finishPostverify(f, task, ticket) {
  const handle = f.app.leader.effects.postverify.start({ticket, prepared: {cwd: f.parent, prompt: '受控原后验'}});
  f.app.execution.started(ticket, await handle.started); f.app.execution.finish(ticket, await handle.completion);
  const record = current(f, task);
  await f.decision(f.take('leader'), [{type: 'conclude', outcome: 'succeeded', summary: '原交付与发布后验完整完成',
    basisDigests: [record.acceptance.digest, record.leader.review.digest]}]);
  assert.equal((await f.get(task.id)).status, 'completed');
}
for (const paused of [false, true]) test('postverify interruption preserves publication and pause fence=' + paused, {timeout: 20000}, async t => {
  const port = controlledEffects(), {f, task, ticket} = await setup(t, 'postverify', effectOptions(port));
  const observation = await bound(f, ticket, true), before = current(f, task);
  if (paused) await f.call({operation: 'task.pause', taskId: task.id, key: 'pause', body: {expectedRevision: (await f.get(task.id)).revision}});
  f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
  const record = current(f, task); assert.equal(record.task.status, paused ? 'paused' : 'running');
  assert.deepEqual(record.leader.publication, before.leader.publication); assert.equal(record.attempts, before.attempts); assert.equal(port.starts, 1);
  if (paused) {
    assert.equal(f.take('postverify'), null);
    await f.call({operation: 'task.resume', taskId: task.id, key: 'resume', body: {expectedRevision: record.task.revision}});
  }
  const successor = f.take('postverify'); assert.notEqual(successor.generation, ticket.generation);
  assert.equal(successor.input.postverify.publicationReceiptDigest, ticket.input.postverify.publicationReceiptDigest);
  await finishPostverify(f, task, successor); const done = current(f, task), head = f.read(tx => tx.head(task.id));
  f.reopen(); f.app.leader.recover(task.id); assert.deepEqual(current(f, task), done); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
});
test('final conclude interruption retains accepted decision, Review and verification instead of rerunning the team', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, 'leader'), conclude = await finish(f, task, ticket, 'leader', true);
  const before = current(f, task), observation = await bound(f, conclude, false); f.reopen();
  f.app.execution.reconcileCleanup(conclude.workerId, observation); f.app.leader.recover(task.id);
  const successor = f.take('leader'); assert.equal(successor.input.leader.snapshot.evidence.some(x => x.kind === 'verification'), true);
  await f.decision(successor, [{type: 'conclude', outcome: 'succeeded', summary: '所有原需求交付已完成，只补原中断总结',
    basisDigests: [before.acceptance.digest, before.leader.review.digest]}]);
  const done = current(f, task); assert.equal(done.task.status, 'completed'); assert.equal(done.attempts, before.attempts + 1);
  assert.deepEqual(done.selectedResults, before.selectedResults); assert.deepEqual(done.acceptance, before.acceptance);
});
for (const status of ['absent', 'matched', 'conflict', 'unknown']) test('unreserved publication action cold lookup=' + status + ' preserves original command and authority', async t => {
  const port = controlledEffects(status), {f, task} = await setup(t, 'publication', {...effectOptions(port), unreserved: true});
  const before = current(f, task), old = f.read(tx => tx.taskCommands(task.id).find(c => JSON.parse(c.payload).action === 'publication'));
  assert.equal(old.attemptId, ''); f.reopen(); f.app.leader.recover(task.id);
  const record = current(f, task); assert.equal(record.attempts, before.attempts); assert.equal(port.starts, 0);
  const original = f.read(tx => tx.command(old.id)); assert.equal(original.status, status === 'unknown' ? 'unknown' : 'observed');
  assert.equal(original.generation, old.generation);
  if (status === 'absent') {
    const next = f.take('publication'); assert.equal(next.input.publication.binding.actionId, before.leader.publication.actionId);
    assert.notEqual(next.commandId, old.id); assert.notEqual(next.generation, old.generation.toString());
    const handle = f.app.leader.effects.publication.start({ticket: next, prepared: {cwd: f.parent, prompt: '原动作首次许可'}});
    f.app.execution.started(next, await handle.started); f.app.execution.finish(next, await handle.completion);
    await finishPostverify(f, task, f.take('postverify')); assert.equal(port.starts, 1);
  } else if (status === 'matched') {
    assert.equal(record.leader.publication.status, 'matched'); await finishPostverify(f, task, f.take('postverify')); assert.equal(port.starts, 0);
  } else assert.equal(record.task.status, status === 'unknown' ? 'intervention' : 'failed');
  const head = f.read(tx => tx.head(task.id)); f.app.leader.recover(task.id); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
});
test('second cold open before postverify reservation closes exhausted handoff instead of dangling old verify', async t => {
  const {f, task} = await setup(t, 'postverify', {...effectOptions(controlledEffects()), unreserved: true});
  const before = current(f, task); f.reopen(); f.app.leader.recover(task.id);
  assert.equal(current(f, task).attempts, before.attempts); f.reopen(); f.app.leader.recover(task.id);
  assert.equal((await f.get(task.id)).status, 'failed');
  assert.equal(f.read(tx => tx.taskCommands(task.id).filter(c => c.status !== 'observed' && JSON.parse(c.payload).action === 'postverify')).length, 0);
  assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
});
for (const fence of ['cancel', 'deadline']) test('publication lookup preserves matched effect after ' + fence + ' without successors', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, 'publication', effectOptions(controlledEffects('matched')));
  const observation = await bound(f, ticket, true); let cancel;
  if (fence === 'cancel') cancel = await f.call({operation: 'task.cancel', taskId: task.id, key: 'cancel', body: {expectedRevision: (await f.get(task.id)).revision}});
  const before = current(f, task); f.reopen(); if (fence === 'deadline') f.app.now = () => ticket.deadline + 1;
  f.app.execution.reconcileCleanup(ticket.workerId, observation);
  if (cancel) {
    const commandId = current(f, task).cancelIntent.commandId, command = f.read(tx => tx.command(commandId));
    f.app.execution.settleControl(command.id, command.revision); // Original public control legitimately becomes unknown while effect unresolved.
  }
  f.app.leader.recover(task.id); const closed = current(f, task);
  assert.equal(closed.task.status, fence === 'cancel' ? 'cancelled' : 'failed'); assert.equal(closed.leader.publication.status, 'matched');
  assert.ok(closed.leader.publication.receiptArtifactId); assert.equal(closed.leader.postverify, null); assert.equal(closed.attempts, before.attempts);
  assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
  if (cancel) assert.equal((await f.call({operation: 'operation.get', operationId: cancel.id})).status, 'succeeded');
  const head = f.read(tx => tx.head(task.id)); f.reopen(); f.app.leader.recover(task.id); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
});
test('wrong lookup binding and rollback preserve original obligation; retry only repeats read-only lookup', {timeout: 20000}, async t => {
  const port = controlledEffects('matched'), lookup = port.lookup; let foreign = true;
  port.lookup = binding => {const value = lookup(binding); return foreign ? {...value, binding: {...binding, actionId: 'foreign'}} : value;};
  const {f, task, ticket} = await setup(t, 'publication', effectOptions(port));
  const observation = await bound(f, ticket, true); f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation);
  const head = f.read(tx => tx.head(task.id));
  assert.throws(() => f.app.leader.recover(task.id), {code: 'invalid_leader_receipt'}); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
  foreign = false; const transaction = f.app.transaction.bind(f.app); let failed = false;
  f.app.transaction = (write, callback) => transaction(write, tx => {
    const value = callback(tx); if (write && !failed) {failed = true; throw new Error('one-shot SQL callback rollback');} return value;
  });
  try {assert.throws(() => f.app.leader.recover(task.id));} finally {f.app.transaction = transaction;}
  assert.equal(failed, true); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
  assert.equal(current(f, task).leader.publication.status, 'unknown'); f.app.leader.recover(task.id);
  assert.equal(current(f, task).leader.publication.status, 'matched'); assert.equal(port.starts, 0);
});
test('unreserved publication does not infer permission after deadline inside lookup', async t => {
  let f, task, deadline; const port = controlledEffects('absent', () => {
    // The trusted read seam advances time, not the original task deadline.
    f.app.now = () => deadline + 1;
  });
  ({f, task} = await setup(t, 'publication', {...effectOptions(port), unreserved: true}));
  const before = current(f, task); deadline = Date.parse(before.task.deadlineAt); f.reopen(); f.app.leader.recover(task.id);
  assert.equal((await f.get(task.id)).status, 'failed'); assert.equal(current(f, task).attempts, before.attempts); assert.equal(port.starts, 0);
});
test('matched publication requires only the original two remaining postverify/conclude attempts, not another create', {timeout: 20000}, async t => {
  const port = controlledEffects('matched'), {f, task, ticket} = await setup(t, 'publication', {...effectOptions(port), maxAttempts: 12});
  const observation = await bound(f, ticket, true); assert.equal(current(f, task).attempts, 10);
  f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
  assert.equal((await f.get(task.id)).status, 'running'); await finishPostverify(f, task, f.take('postverify'));
  assert.equal(current(f, task).attempts, 12); assert.equal(port.starts, 0);
});

test('second interruption cannot reset the same obligation retry count or budget', {timeout: 20000}, async t => {
  const {f, task, ticket} = await setup(t, 'leader'), observation = await bound(f, ticket, false);
  f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
  const successor = f.take('leader');
  // Another independently owned original custodian uses a fresh directory.
  fs.renameSync(path.join(f.parent, 'custody'), path.join(f.parent, 'custody-first'));
  const again = await bound(f, successor, false); f.reopen();
  const attempts = current(f, task).attempts; f.app.execution.reconcileCleanup(successor.workerId, again); f.app.leader.recover(task.id);
  assert.equal((await f.get(task.id)).status, 'failed'); assert.equal(current(f, task).attempts, attempts);
  assert.equal(f.read(tx => tx.taskCommands(task.id).filter(c => c.status === 'pending' && JSON.parse(c.payload).action === 'leader')).length, 0);
});

test('actual local publication created before SQL receipt: exact lookup preserves inode/bytes and never creates again', {timeout: 20000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-resume-target-'))), root = path.join(parent, 'reports');
  fs.mkdirSync(root, {mode: 0o700});
  const native = createLocalReportPublication({id: 'reports', root, readBaseURL: 'http://127.0.0.1:49152/', policy: {profile: 'task-local-json-report/v1', id: 'policy', version: '1'}});
  let starts = 0; const port = {...native, start(options) {starts++; return native.start(options);}};
  t.after(() => {native.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const {f, task, ticket} = await setup(t, 'publication', {publication: port, publicationExpected: () => ({east: 10, west: 20, region: 'north'})});
  native.assertDisjoint([f.parent, path.join(f.parent, 'store'), path.join(f.parent, 'objects'), path.join(f.parent, 'execution')]);
  const cwd = path.join(f.parent, 'publication-original'); fs.mkdirSync(cwd, {mode: 0o700});
  fs.writeFileSync(path.join(cwd, 'publication-input.json'), f.app.artifacts.bytes(ticket.input.publicationArtifact), {flag: 'wx', mode: 0o600});
  const custodyRoot = path.join(f.parent, 'custody'); fs.mkdirSync(custodyRoot, {mode: 0o700});
  const manager = createExecutionCustody({root: custodyRoot}); let observation;
  try {
    const custody = await manager.prepare(f.app.execution.custodyBinding(ticket, native.custodyProfile));
    f.app.execution.bindCustody(ticket, custody.descriptor, native.custodyProfile); custody.permit();
    const handle = f.app.leader.effects.publication.start({ticket, prepared: {cwd, prompt: '原精确发布材料'}, executionContext: {launch: custody.launch}});
    f.app.execution.started(ticket, await handle.started); const result = await handle.completion;
    assert.equal(result.status, 'completed'); observation = manager.read(custody.descriptor);
    assert.equal(observation.payload.cleanup.cleaned, true);
    // Deliberately lose only the parent's result before application.finish.
  } finally {await manager.close();}
  const target = path.join(root, ticket.input.publication.binding.name), before = fs.statSync(target), bytes = fs.readFileSync(target);
  const attempts = current(f, task).attempts; f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
  const record = current(f, task); assert.equal(record.leader.publication.status, 'matched'); assert.ok(record.leader.publication.receiptArtifactId);
  assert.equal(record.leader.postverify.status, 'pending'); assert.equal(record.attempts, attempts); assert.equal(starts, 1);
  assert.equal(fs.statSync(target).ino, before.ino); assert.deepEqual(fs.readFileSync(target), bytes);
  const ref = f.read(tx => f.app.artifacts.metadata(tx, record.leader.publication.receiptArtifactId)), evidence = JSON.parse(f.app.artifacts.bytes(ref));
  assert.equal(evidence.status, 'matched'); assert.equal(evidence.createdByThisExecution, false);
  const head = f.read(tx => tx.head(task.id)); f.app.leader.recover(task.id); assert.deepEqual(f.read(tx => tx.head(task.id)), head); assert.equal(starts, 1);
});

for (const [budget, answered, allowed] of [[10, false, false], [12, true, false], [13, true, true]])
  test('unplanned publication recovery includes original complete cost: budget=' + budget + ', answered=' + answered, {timeout: 20000}, async t => {
    const port = controlledEffects(), f = fixture(t, effectOptions(port));
    const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '保留原两作者、独立审查与授权发布的完整义务',
      limits: {timeoutMs: 120000, maxAttempts: budget, maxWorkers: 3}}});
    let ticket = f.take('leader');
    if (answered) {
      await f.decision(ticket, [{type: 'ask', kind: 'business', prompt: '请确认地区', options: [], subject: current(f, task).inputDigest, nodeIds: []}]);
      const question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
      await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: question.id, key: 'answer',
        body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: question.requestDigest, answer: 'north'}});
      ticket = f.take('leader');
    }
    const before = current(f, task), observation = await bound(f, ticket, false);
    assert.equal(before.plan, null); assert.equal(before.attempts, answered ? 2 : 1);
    f.reopen(); f.app.execution.reconcileCleanup(ticket.workerId, observation); f.app.leader.recover(task.id);
    const resumed = current(f, task); assert.equal(resumed.attempts, before.attempts); assert.deepEqual(resumed.limits, before.limits);
    assert.equal(resumed.task.deadlineAt, before.task.deadlineAt); assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
    if (!allowed) {
      assert.equal(resumed.task.status, 'failed'); assert.equal(resumed.plan, null);
      assert.equal(f.read(tx => tx.taskCommands(task.id).filter(c => c.status === 'pending' && JSON.parse(c.payload).action === 'leader')).length, 0);
    } else {
      assert.equal(resumed.task.status, 'running'); const successor = f.take('leader');
      assert.notEqual(successor.generation, ticket.generation); assert.equal(successor.input.leaderReplies[0].answer, 'north');
      assert.equal((await f.decision(successor, [{type: 'plan', proposal}])).status, 'completed');
      const plan = await f.call({operation: 'task.plan', taskId: task.id}); assert.equal(plan.budget.maxAttempts, budget);
      await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: (await f.get(task.id)).revision,
        planRevision: plan.revision, planDigest: plan.digest}});
      const dispatch = f.read(tx => tx.taskCommands(task.id).find(c => c.status === 'pending' && JSON.parse(c.payload).action === 'dispatch'));
      f.app.execution.expandDispatch(dispatch.id, dispatch.revision); f.author(f.take('execute', 'east')); f.author(f.take('execute', 'west'));
      let leader = f.take('leader');
      await f.decision(leader, [{type: 'work', kind: 'review', nodeIds: ['east', 'west'], selectionDigest: hash(leader.input.leader.snapshot.selection)}]);
      await f.review(f.take('review')); leader = f.take('leader');
      await f.decision(leader, [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(leader.input.leader.snapshot.selection)}]);
      await f.verify(f.take('execute', 'verify')); const record = current(f, task);
      const artifact = f.read(tx => record.task.artifactIds.map(id => f.app.artifacts.metadata(tx, id)).find(value => value.kind === 'delivery'));
      await f.decision(f.take('leader'), [{type: 'deliver', artifactId: artifact.id, acceptanceDigest: record.acceptance.digest, reviewDigest: record.leader.review.digest}]);
      const authorization = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
      await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: authorization.id, key: 'allow',
        body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: authorization.requestDigest, decision: 'allow'}});
      const publication = f.take('publication'), handle = f.app.leader.effects.publication.start({ticket: publication, prepared: {cwd: f.parent, prompt: '受控原授权发布'}});
      f.app.execution.started(publication, await handle.started); f.app.execution.finish(publication, await handle.completion);
      await finishPostverify(f, task, f.take('postverify')); assert.equal(current(f, task).attempts, 13); assert.equal(port.starts, 1);
    }
    const done = current(f, task), head = f.read(tx => tx.head(task.id));
    f.reopen(); f.app.leader.recover(task.id); assert.deepEqual(current(f, task), done); assert.deepEqual(f.read(tx => tx.head(task.id)), head);
  });
