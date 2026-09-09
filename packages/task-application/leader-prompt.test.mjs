import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture, proposal, hash} from './leader.test.mjs';
import {createLeaderPort, parseManagedOutput, renderLeaderPrompt} from './application.mjs';

// The original fixture uses real SQLite/Depot and explicit fake Provider facts.
// These tests prove renderer/parse/admission compatibility, not model/OS behavior.
function rendered(ticket) {
  const input = ticket.input.leader, before = structuredClone(input), prompt = renderLeaderPrompt(input);
  const references = JSON.parse(prompt.split('\n冻结机器引用：')[1].split('\n独立返回示例：')[0]);
  const examples = JSON.parse(prompt.split('\n独立返回示例：')[1].split('\n完整冻结输入：')[0]);
  assert.deepEqual(input, before); assert.ok(prompt.endsWith(JSON.stringify(input)));
  for (const example of Object.values(examples)) if (typeof example === 'object') {
    assert.equal(example.actions.length, 1); assert.equal(example.callId, input.callId); assert.equal(example.inputDigest, input.inputDigest);
  }
  return {references, examples, prompt};
}
async function initial(t) {
  const f = fixture(t), task = await f.call({operation: 'task.create', key: 'create', body: {intent: '交付两地区，区域需明确',
    limits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}}});
  return {f, task, ticket: f.take('leader')};
}
async function authors(t) {
  const {f, task, ticket} = await initial(t);
  assert.equal((await f.decision(ticket, rendered(ticket).examples.ask.actions)).status, 'completed');
  const pending = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: pending.id, key: 'answer',
    body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: pending.requestDigest, answer: 'north'}});
  const planning = f.take('leader'), example = rendered(planning).examples.plan;
  // Business fields are supplied from the original test requirement, not by a
  // digest substitution or by treating the renderer's generic Plan as authority.
  example.actions[0].proposal = proposal;
  assert.equal((await f.decision(planning, example.actions)).status, 'completed');
  const plan = await f.call({operation: 'task.plan', taskId: task.id});
  await f.call({operation: 'task.approve', taskId: task.id, key: 'approve', body: {expectedRevision: (await f.get(task.id)).revision,
    planRevision: plan.revision, planDigest: plan.digest}});
  const command = f.read(tx => tx.commands().find(row => JSON.parse(row.payload).action === 'dispatch'));
  f.app.execution.expandDispatch(command.id, command.revision);
  return {f, task, east: f.take('execute', 'east'), west: f.take('execute', 'west')};
}
async function parseExample(ticket, value) {
  const port = createLeaderPort({id: 'renderer-test', providerId: ticket.providerId, policy: {...ticket.input.leader.snapshot.policy, maxActions: 1},
    prepare: ({input}) => ({prompt: renderLeaderPrompt(input)}), parseDecision: parseManagedOutput});
  const started = {executionId: 'controlled-parser', startedAt: new Date().toISOString()};
  const provider = {id: ticket.providerId, start() {return {started: Promise.resolve(started), stop() {},
    completion: Promise.resolve({providerId: ticket.providerId, status: 'completed', stopReason: 'end_turn', outputText: JSON.stringify(value),
      cleanup: {started, cleaned: true, scope: 'controlled-fixture'}})};}};
  return port.start({ticket, provider, prepared: {prompt: renderLeaderPrompt(ticket.input.leader)}}).completion;
}
test('original intake input yields one parseable ask, exact distinct envelope/subject and no six-action max1 example', async t => {
  const {f, task, ticket} = await initial(t), {examples, references, prompt} = rendered(ticket);
  const originalInput = ticket.input.leader.snapshot.readSet.find(item => item.kind === 'input').digest;
  assert.notEqual(originalInput, ticket.input.leader.inputDigest);
  assert.equal(examples.ask.actions[0].subject, originalInput); assert.deepEqual(examples.ask.actions[0].nodeIds, []);
  assert.equal(references.askSubjects[0].digest, originalInput); assert.equal(typeof examples.work, 'string');
  assert.ok(!prompt.includes('ask.kind为business/publication')); assert.ok(prompt.includes('publication授权问题由Core在deliver后生成'));
  assert.equal((await parseExample(ticket, examples.ask)).status, 'completed');
  for (const mutate of [v => {v.callId = 'call-foreign';}, v => {v.inputDigest = originalInput;},
    v => {v.actions[0].subject = '原缺项摘要';}, v => {v.actions.push(v.actions[0]);}]) {
    const value = structuredClone(examples.ask); mutate(value); assert.equal((await parseExample(ticket, value)).status, 'failed');
  }
  assert.equal((await f.decision(ticket, examples.ask.actions)).status, 'completed');
  const question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  assert.equal(question.subject, originalInput); assert.equal(question.kind, 'business');
});
test('valid-SHA wrong ask subject remains rejected by original transactional admission', async t => {
  const {f, task, ticket} = await initial(t), actions = rendered(ticket).examples.ask.actions;
  actions[0].subject = ticket.input.leader.inputDigest;
  assert.equal((await f.decision(ticket, actions)).status, 'failed');
  assert.equal(f.read(tx => f.app.get(tx, task.id)).failureCode, 'invalid_leader_decision');
  assert.equal((await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest, null);
});
test('original frozen selected/review/acceptance/delivery/history refs traverse original gates without model hashing', async t => {
  const {f, task, east, west} = await authors(t); f.author(east); f.author(west);
  let ticket = f.take('leader'), value = rendered(ticket);
  assert.equal(value.examples.work.actions[0].selectionDigest, ticket.input.leader.snapshot.readSet.find(item => item.kind === 'selected').digest);
  assert.equal((await f.decision(ticket, value.examples.work.actions)).status, 'completed');
  assert.equal((await f.review(f.take('review'))).status, 'completed');
  ticket = f.take('leader'); value = rendered(ticket);
  const verification = {...value.examples.work.actions[0], kind: 'verify', nodeIds: value.references.verifierNodeIds};
  assert.equal((await f.decision(ticket, [verification])).status, 'completed');
  assert.equal((await f.verify(f.take('execute', 'verify'))).status, 'completed');
  ticket = f.take('leader'); value = rendered(ticket);
  const delivery = ticket.input.leader.materials.find(item => item.kind === 'delivery');
  assert.equal(value.examples.deliver.actions[0].artifactId, delivery.id);
  assert.notEqual(value.examples.deliver.actions[0].acceptanceDigest, delivery.digest);
  assert.equal((await f.decision(ticket, value.examples.deliver.actions)).status, 'completed');
  ticket = f.take('leader'); value = rendered(ticket);
  assert.ok(!value.examples.conclude.actions[0].basisDigests.includes(ticket.input.leader.snapshot.readSet.find(item => item.kind === 'history').digest));
  assert.equal((await f.decision(ticket, value.examples.conclude.actions)).status, 'completed');
  assert.equal((await f.get(task.id)).status, 'completed');
});
for (const wrong of [false, true]) test('execution-failure repair copies original evidence and wrong digest is refused: wrong=' + wrong, async t => {
  const {f, task, east, west} = await authors(t); f.author(west);
  const started = {executionId: 'failed-original', startedAt: new Date().toISOString()}; f.app.execution.started(east, started);
  f.app.execution.finish(east, {status: 'failed', reason: 'agent_max_tokens', stopReason: 'max_tokens', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
  const ticket = f.take('leader'), actions = rendered(ticket).examples.repair.actions;
  assert.equal(actions[0].basis.kind, 'execution-failure');
  assert.equal(actions[0].basis.digest, ticket.input.leader.snapshot.evidence.find(item => item.kind === 'execution-failure').digest);
  if (wrong) actions[0].basis.digest = hash('foreign');
  assert.equal((await f.decision(ticket, actions)).status, wrong ? 'failed' : 'completed');
  assert.equal(!!f.read(tx => f.app.get(tx, task.id)).activeRepair, !wrong);
});
test('correct rendered reference does not authorize a stale frozen readSet after another branch completes', async t => {
  const {f, task, east, west} = await authors(t);
  const started = {executionId: 'failed-original', startedAt: new Date().toISOString()}; f.app.execution.started(east, started);
  f.app.execution.finish(east, {status: 'failed', reason: 'agent_max_tokens', stopReason: 'max_tokens', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
  const ticket = f.take('leader'), actions = rendered(ticket).examples.ask.actions;
  const originalQuestion = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  assert.equal(originalQuestion.status, 'replied'); f.author(west);
  assert.equal((await f.decision(ticket, actions)).status, 'failed');
  assert.equal((await f.get(task.id)).status, 'running');
  assert.deepEqual((await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest, originalQuestion);
  assert.equal(f.read(tx => f.app.get(tx, task.id)).leader.requestIds.length, 1);
  assert.ok(f.take('leader')); // Original Core creates the bounded successor, not the renderer.
});
test('review/content-rejection and conclusion mappings copy producer fields, never aggregate hashes or invented digests', () => {
  const review = hash('review'), acceptance = hash('acceptance'), selected = hash('selected'), history = hash('history'), inputDigest = hash('expanded');
  const input = {callId: 'call-original', inputDigest, snapshot: {readSet: [{kind: 'input', digest: hash('input')},
    {kind: 'selected', digest: selected}, {kind: 'review', digest: review}, {kind: 'acceptance', digest: acceptance}, {kind: 'history', digest: hash('aggregate')}],
    selection: [{nodeId: 'east', resultDigest: hash('result')}], history: [{digest: history}],
    evidence: [{kind: 'review', digest: review, verdict: 'rework'}, {kind: 'verification', digest: acceptance, status: 'failed'}]}, materials: []};
  const {references, examples} = rendered({input: {leader: input}});
  assert.deepEqual(references.repairBases.map(({kind, digest}) => ({kind, digest})), [{kind: 'review', digest: review}, {kind: 'content-rejection', digest: acceptance}]);
  assert.deepEqual(examples.conclude.actions[0].basisDigests, [review, acceptance, history]);
  assert.equal(examples.work.actions[0].selectionDigest, selected);
  assert.equal(typeof examples.deliver, 'string');
});
