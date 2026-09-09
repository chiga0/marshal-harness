import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture, proposal, hash} from './leader.test.mjs';
import {createLeaderPort, createReviewPort, parseManagedOutput, renderLeaderPrompt, renderReviewPrompt} from './application.mjs';

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
async function parseExample(ticket, value, maxActions = 1) {
  const port = createLeaderPort({id: 'renderer-test', providerId: ticket.providerId, policy: {...ticket.input.leader.snapshot.policy, maxActions},
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

for (const valid of [false, true]) test('observed string-options rejection shape through original SQLite/port: objectOptions=' + valid, async t => {
  const {f, task, ticket} = await initial(t), {examples, prompt} = rendered(ticket);
  // The real 686-byte output was privately replayed with digest 14583515…c9af.
  // Do not commit that private model prose/session input: reproduce its precise
  // failure shape with synthetic business values and a fresh original ticket.
  const value = structuredClone(examples.ask); value.actions[0].options = ['option-a', 'option-b'];
  assert.equal((await parseExample(ticket, value)).status, 'failed');
  const sample = JSON.parse(prompt.split('非空形状是')[1].split('，')[0]);
  assert.deepEqual(sample.map(option => Object.keys(option)), [['value', 'label'], ['value', 'label']]);
  if (valid) value.actions[0].options = sample;
  const result = await f.decision(ticket, value.actions), view = await f.call({operation: 'task.leader', taskId: task.id});
  assert.equal(result.status, valid ? 'completed' : 'failed');
  if (valid) assert.deepEqual(view.pendingRequest.options, sample);
  else {assert.equal(view.pendingRequest, null); assert.equal(f.read(tx => f.app.get(tx, task.id)).failureCode, 'invalid_leader_decision');}
});
test('nonempty option objects obey original closed shape, UTF-8 bounds and uniqueness without normalization', async t => {
  const {ticket} = await initial(t), value = rendered(ticket).examples.ask;
  const choices = [{value: 'a'.repeat(256), label: '中'.repeat(341)}, {value: 'other', label: '另一个选项'}];
  value.actions[0].options = choices; assert.equal((await parseExample(ticket, value)).status, 'completed');
  for (const options of [['first', 'second'], [null], [{value: 'a'}], [{value: 'a', label: 'A', selected: true}],
    [{value: 'a', label: 'A'}, {value: 'a', label: 'B'}], [{value: 'a'.repeat(257), label: 'A'}],
    [{value: 'a', label: '中'.repeat(342)}], [{value: '', label: 'A'}], [{value: 'a', label: '\0'}],
    Array.from({length: 17}, (_, i) => ({value: 'v' + i, label: '选项'}))]) {
    const candidate = structuredClone(value); candidate.actions[0].options = options;
    assert.equal((await parseExample(ticket, candidate)).status, 'failed');
    assert.deepEqual(candidate.actions[0].options, options); // Parser did not coerce/mutate the candidate.
  }
});
test('all action array and enum guidance matches original parser; strings/objects and enum aliases remain rejected', async t => {
  const {ticket} = await initial(t), {examples, prompt} = rendered(ticket), value = examples.ask;
  for (const text of ['planner/author/reviewer/integrator/verifier', 'providerId必须显式为null', 'proposal.edges是0至256个{from,to}对象',
    'proposal.deliverables/acceptance分别为1至32个字符串', 'nodeIds均为唯一节点ID字符串数组', 'conclude.basisDigests是0至64个唯一摘要字符串数组']) assert.ok(prompt.includes(text));
  const work = {type: 'work', kind: 'review', nodeIds: ['east'], selectionDigest: hash('selected')};
  const repair = {type: 'repair', nodeIds: ['east'], basis: {kind: 'review', digest: hash('review')}, feedback: '原负面证据'};
  const conclude = {type: 'conclude', outcome: 'failed', summary: '原业务失败', basisDigests: []};
  for (const action of [work, repair, conclude]) assert.equal((await parseExample(ticket, {...value, actions: [action]})).status, 'completed');
  for (const action of [{...work, nodeIds: [{id: 'east'}]}, {...work, nodeIds: 'east'}, {...work, nodeIds: ['east', 'east']},
    {...work, kind: 'integrate'}, {...repair, basis: [{kind: 'review', digest: hash('review')}]},
    {...repair, basis: {kind: 'rejection', digest: hash('review')}}, {...conclude, outcome: 'success'},
    {...conclude, basisDigests: [{digest: hash('review')}]}, {...conclude, basisDigests: [hash('review'), hash('review')]}])
    assert.equal((await parseExample(ticket, {...value, actions: [action]})).status, 'failed');
  const deliver = {type: 'deliver', artifactId: 'artifact-original', acceptanceDigest: hash('acceptance'), reviewDigest: hash('review')};
  for (const actions of [[repair, deliver], [work, {...work, kind: 'verify'}], [work, work], [value.actions[0], work]])
    assert.equal((await parseExample(ticket, {...value, actions}, 4)).status, 'failed');
  for (const candidate of [{...value, summary: ''}, {...value, summary: '\0'}, {...value, summary: '\ud800'},
    {...value, extra: 'unrecognized'}, {...value, summary: 'a'.repeat(65537)}]) assert.equal((await parseExample(ticket, candidate)).status, 'failed');
  assert.throws(() => parseManagedOutput({completion: {outputText: '\uFEFF' + JSON.stringify(value)}}));
});
test('nonempty Review findings are explicitly described and retain exact original report parser limits', async t => {
  const {f, east, west} = await authors(t); f.author(east); f.author(west);
  const leader = f.take('leader'); await f.decision(leader, rendered(leader).examples.work.actions);
  const ticket = f.take('review'), input = ticket.input.review, prompt = renderReviewPrompt(input);
  const finding = JSON.parse(prompt.split('非空元素形状是')[1].split('。')[0]); finding.nodeIds = [input.selection[0].nodeId];
  assert.deepEqual(Object.keys(finding), ['id', 'nodeIds', 'requirement', 'observation', 'requestedChange']);
  const value = {profile: 'task-independent-review/v1', inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
    verdict: 'rework', summary: '受控负面报告，仅测试形状', findings: [finding]};
  const port = createReviewPort({id: 'renderer-review', providerId: ticket.providerId, policy: {id: 'review', version: '1', description: '受控形状'},
    prepare: ({input}) => ({prompt: renderReviewPrompt(input)}), parseReport: parseManagedOutput});
  const run = candidate => {
    const fact = {executionId: 'controlled-review-parser', startedAt: new Date().toISOString()};
    const provider = {id: ticket.providerId, start() {return {started: Promise.resolve(fact), stop() {}, completion: Promise.resolve({
      providerId: ticket.providerId, status: 'completed', stopReason: 'end_turn', outputText: JSON.stringify(candidate),
      cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture'}})};}};
    return port.start({ticket, provider, prepared: {prompt}}).completion;
  };
  assert.equal((await run(value)).status, 'completed');
  assert.equal((await run({...value, verdict: 'accept', findings: []})).status, 'completed');
  for (const candidate of [{...value, findings: ['problem']}, {...value, verdict: 'accepted'}, {...value, verdict: 'accept'},
    {...value, findings: [{...finding, nodeIds: [{id: finding.nodeIds[0]}]}]}, {...value, findings: [{...finding, nodeIds: ['foreign']}]},
    {...value, findings: [{...finding, requirement: '中'.repeat(683)}]}, {...value, findings: [{...finding, pass: false}]},
    {...value, findings: [finding, finding]}, {...value, findings: [{...finding, id: '非法ID'}]},
    {...value, findings: [{...finding, nodeIds: []}]}, {...value, findings: [{...finding, observation: ''}]},
    {...value, summary: 'a'.repeat(4097)}, {...value, findings: Array.from({length: 17}, (_, i) => ({...finding, id: 'finding-' + i}))}])
    assert.equal((await run(candidate)).status, 'failed');
});
test('documented Plan arrays, roles, DAG, verifier sink and v7 budget are still enforced by original Core', async t => {
  for (const mutate of [v => {v.nodes[0].scope = {write: ['east.json']};}, v => {v.nodes[0].role = 'leader';},
    v => {delete v.nodes[0].providerId;}, v => {v.nodes[0].id = '非法ID';}, v => {v.edges = ['east->verify'];},
    v => {v.edges.push({from: 'verify', to: 'east'});}, v => {v.edges = [];},
    v => {v.deliverables = [{name: 'report'}];}, v => {v.acceptance = [];}, v => {v.assumptions = [{value: 'none'}];},
    v => {v.nodes[0].role = 'reviewer';}, v => {v.budget = {timeoutMs: 60000, maxAttempts: 17, maxWorkers: 2};},
    v => {v.budget = {timeoutMs: 60000, maxAttempts: 3, maxWorkers: 3};}]) {
    const {f, task, ticket} = await initial(t), value = structuredClone(proposal); mutate(value);
    assert.equal((await f.decision(ticket, [{type: 'plan', proposal: value}])).status, 'failed');
    assert.equal(f.read(tx => f.app.get(tx, task.id)).plan, null);
  }
});

for (const maxAttempts of [undefined, 8, 12]) test('original publication Plan after two Leader attempts: optional budget=' + (maxAttempts ?? 'omitted'), async t => {
  // No private model output is copied. This is the observed failure shape:
  // original budget17, question/reply then second Leader, three DAG nodes,
  // configured publication. Effects must never execute in this admission test.
  const unexpected = () => {throw new Error('publication must not execute during plan admission');};
  const publication = {id: 'reports', policyDigest: hash({id: 'test-publication'}), configuration: {profile: 'controlled-plan-admission'},
    configurationDigest: hash({profile: 'controlled-plan-admission'}), start: unexpected, lookup: unexpected, assertDisjoint: unexpected,
    postverify: {id: 'reports-postverify', start: unexpected}};
  const f = fixture(t, {publication, publicationExpected: () => ({original: 'expected'})}), limits = {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3};
  const task = await f.call({operation: 'task.create', key: 'budget-create', body: {intent: '完整交付两个地区并明确授权发布；地区尚需答复', limits}});
  const intake = f.take('leader'); await f.decision(intake, rendered(intake).examples.ask.actions);
  const question = (await f.call({operation: 'task.leader', taskId: task.id})).pendingRequest;
  await f.call({operation: 'task.leader.reply', taskId: task.id, requestId: question.id, key: 'budget-answer',
    body: {expectedRevision: (await f.get(task.id)).revision, requestDigest: question.requestDigest, answer: 'north'}});
  const ticket = f.take('leader'), before = f.read(tx => f.app.get(tx, task.id)), {prompt, examples} = rendered(ticket);
  assert.equal(before.attempts, 2); assert.equal(before.leader.calls, 2); assert.equal(proposal.nodes.length, 3);
  assert.deepEqual(ticket.input.leader.snapshot.task.limits, limits); assert.equal(Object.hasOwn(examples.plan.actions[0].proposal, 'budget'), false);
  for (const text of ['省略整个proposal.budget', '沿用snapshot.task.limits', '不要为省token', 'maxAttempts是整个Task累计执行上限', '用户明确要求合法缩减时仍可提供budget'])
    assert.ok(prompt.includes(text));
  const candidate = structuredClone(proposal);
  if (maxAttempts !== undefined) candidate.budget = {...limits, maxAttempts};
  const original = structuredClone(candidate), result = await f.decision(ticket, [{type: 'plan', proposal: candidate}]);
  assert.deepEqual(candidate, original); // Guidance/parser never rewrites the returned model budget.
  const after = f.read(tx => f.app.get(tx, task.id));
  assert.equal(after.attempts, 2); assert.deepEqual(after.limits, limits); assert.equal(after.approved, null);
  assert.equal(f.read(tx => f.app.execution.capacity(tx).value.active.length), 0);
  // Original Core minimum: 3 nodes + 2 consumed + 5 following managed calls /
  // checks + 2 publication/postverify =12. Keep8 rejected;12 is legal reduction.
  if (maxAttempts === 8) {
    assert.equal(result.status, 'failed'); assert.equal(after.failureCode, 'capacity_exceeded'); assert.equal(after.plan, null);
  } else {
    assert.equal(result.status, 'completed'); assert.equal(after.task.status, 'awaiting-approval');
    assert.deepEqual(after.plan.budget, {...limits, maxAttempts: maxAttempts ?? 17});
  }
});
