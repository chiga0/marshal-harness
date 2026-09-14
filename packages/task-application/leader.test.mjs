import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture,proposal,hash} from './leader-fixture.mjs';
export {fixture,proposal,hash} from './leader-fixture.mjs';

for (const observed of [false, true]) test('v7 real SQLite: necessary reply → plan approval → two authors → independent Review → stage verification → deliver/conclude → cold exact bytes; observation=' + observed, async t => {
  const f = fixture(t, observed ? {observability: {profile: 'task-observation/v1', retainPrompts:true}} : {}), task = await f.call({operation: 'task.create', key: 'create', body: {intent: '交付两区域结果，但区域待用户明确',
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
