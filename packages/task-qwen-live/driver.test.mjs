import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {parseOptions, taskBody, validatePlan, filePermission, consumeDelivery, executionFact, assertTeam, data} from './driver.fixture.mjs';
import {proposal, policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';
import {TaskVerification, createVerificationPort} from '../task-application/verification.mjs';
import {encode} from '../task-store/store.mjs';

const options = ['--execute-real', '--run-dir', '/private/tmp/qwen-private-new', '--node', '/installed/node', '--qwen-entry', '/installed/cli-entry.js'];
test('real execution is explicit; arguments cannot select a model, fallback, existing state mode or arbitrary argv', () => {
  assert.equal(parseOptions(options).timeoutMs, 600000);
  assert.equal(parseOptions(options).scenario, 'team');
  assert.equal(parseOptions([...options, '--scenario', 'team']).scenario, 'team');
  assert.equal(parseOptions([...options, '--scenario', 'cancel']).scenario, 'cancel');
  assert.deepEqual(parseOptions(['--help']), {help: true});
  for (const args of [[], options.slice(1), [...options, '--execute-real'], [...options, '--node', '/other'],
    [...options, '--model', 'fake'], [...options, '--timeout-ms', '0'], [...options, '--timeout-ms', '900001'],
    [...options, '--mode', 'open'], [...options, '--scenario', 'shell'], [...options, '--scenario', 'cancel', '--scenario', 'team'],
    ['--execute-real', '--run-dir', '../state', '--node', '/n', '--qwen-entry', '/e']])
    assert.throws(() => parseOptions(args));
});
function planFixture() {
  const body = taskBody('input-a', 600000), plan = {...proposal(), taskId: 'task-a', revision: 1, digest: 'sha256:' + '1'.repeat(64), budget: body.limits};
  // Exercise the original Core producer; no hand-authored "trusted" suffix or
  // fake model result can make a consumer-only compatibility test self-fulfil.
  const port = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan,
    start() { throw new Error('This test never starts an execution'); }});
  const verification = new TaskVerification({artifacts: {requireDepot() {}}}, port);
  verification.bind({input: body, inputArtifacts: [{id: 'input-a'}]}, plan);
  return {body, plan, task: {id: 'task-a', status: 'awaiting-approval', revision: 4, plan: {revision: 1, digest: plan.digest}}};
}
test('real proposal is checked, never rewritten; exact Task/plan revision become one approve body', () => {
  const {body, plan, task} = planFixture(), before = structuredClone(plan);
  assert.deepEqual(validatePlan(task, plan, body.limits, 'input-a'), {expectedRevision: 4, planRevision: 1, planDigest: plan.digest});
  assert.deepEqual(plan, before);
  assert.equal(data.rows.length, 6);
  assert.doesNotMatch(body.context.text, /1275|550|1825/);
});
test('stale identity, serial authors, extra author, budget drift or weakened acceptance refuses approval', () => {
  const mutations = [f => f.plan.taskId = 'other', f => f.task.plan.revision++, f => f.plan.digest = 'wrong',
    f => f.task.status = 'executing', f => f.plan.nodes.push(f.plan.nodes[0]), f => f.plan.nodes[0].role = 'planner',
    f => f.plan.edges.unshift({from: 'east', to: 'west'}), f => f.plan.nodes[0].providerId = 'other-provider',
    f => f.plan.budget = {...f.body.limits, maxAttempts: 5}, f => f.plan.acceptance.splice(1, 1),
    f => f.plan.deliverables.push('extra'), f => f.plan.assumptions.push('允许外部发布')];
  for (const change of mutations) { const f = planFixture(); change(f); assert.throws(() => validatePlan(f.task, f.plan, f.body.limits, 'input-a')); }
});
test('reasonable planner rephrasing passes with the exact original Core policy/layout/delivery projections', () => {
  const {body, plan, task} = planFixture();
  plan.acceptance[0] = '每区仅汇总已付款交易，退款作为负额，零额仍计数，并核对最终的两个 JSON。';
  assert.ok(!plan.acceptance.includes(body.requirements.acceptance[0]));
  assert.equal(validatePlan(task, plan, body.limits, 'input-a').planDigest, plan.digest);
});
test('missing or altered trusted policy/description cannot be replaced by a policy ID in model prose', () => {
  const mutations = [plan => plan.acceptance.splice(1, 1), plan => plan.acceptance[1] = '检查通过，policy=' + policy.id,
    plan => { const value = JSON.parse(plan.acceptance[1]); value.policy.version = '999'; plan.acceptance[1] = JSON.stringify(value); },
    plan => { const value = JSON.parse(plan.acceptance[1]); value.description = '仅看作者通过标签'; plan.acceptance[1] = JSON.stringify(value); },
    plan => { const value = JSON.parse(plan.acceptance[1]); value.policy.description = '弱化标准'; plan.acceptance[1] = JSON.stringify(value); }];
  for (const change of mutations) { const f = planFixture(); change(f.plan);
    assert.throws(() => validatePlan(f.task, f.plan, f.body.limits, 'input-a'), /missing_business_acceptance/); }
});
test('trusted projections bind exact uploaded input, both author outputs and complete final delivery', () => {
  const mutations = [plan => { const value = JSON.parse(plan.acceptance[2]); value.layout.inputs[0].source.id = 'other-input'; plan.acceptance[2] = JSON.stringify(value); },
    plan => { const value = JSON.parse(plan.acceptance[2]); value.layout.allowedPaths.push('extra.json'); plan.acceptance[2] = JSON.stringify(value); },
    plan => plan.acceptance.pop(),
    plan => { const value = JSON.parse(plan.acceptance.at(-1)); value.delivery.targetPath = 'other.json'; plan.acceptance[plan.acceptance.length - 1] = JSON.stringify(value); }];
  for (const change of mutations) { const f = planFixture(); change(f.plan);
    assert.throws(() => validatePlan(f.task, f.plan, f.body.limits, 'input-a'), /missing_business_acceptance/); }
  const f = planFixture();
  assert.throws(() => validatePlan(f.task, f.plan, f.body.limits, 'other-input'), /missing_business_acceptance/);
  assert.throws(() => validatePlan(f.task, f.plan, f.body.limits), /missing_business_input/);
});
function permissions(t) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'qwen-permission-test-')));
  fs.writeFileSync(path.join(cwd, 'sales.json'), '{}'); t.after(() => fs.rmSync(cwd, {recursive: true}));
  const identity = {cwd, role: 'author', nodeId: 'east'};
  const request = (kind, rawInput) => ({toolCall: {kind, rawInput}, options: [{kind: 'allow_once', optionId: 'proceed_once'}]});
  return {cwd, identity, request};
}
test('bounded native file read/write/replace may select only offered once permission', t => {
  const {cwd, identity, request} = permissions(t);
  for (const input of [request('read', {file_path: 'sales.json'}), request('read', {file_path: path.join(cwd, 'sales.json'), offset: 0, limit: 100}),
    request('edit', {file_path: path.join(cwd, 'east.json'), content: '{}'}),
    request('edit', {file_path: 'east.json', old_string: 'a', new_string: 'b', replace_all: false})])
    assert.equal(filePermission(identity, input).outcome.optionId, 'proceed_once');
});
test('shell, unknown rawInput, peer output, input overwrite, traversal, broad option, planner and link are denied', t => {
  const {cwd, identity, request} = permissions(t);
  const bad = [request('execute', {command: 'cat sales.json'}), request('edit', {file_path: 'sales.json', content: '{}'}),
    request('edit', {file_path: 'west.json', content: '{}'}), request('edit', {file_path: '../east.json', content: '{}'}),
    request('edit', {file_path: 'east.json', content: '{}', command: 'malicious'}), request('edit', {file_path: 'east.json', content: 'x'.repeat(4097)}),
    request('read', {file_path: '/etc/passwd'}), request('read', {file_path: 'sales.json', limit: -1}),
    {...request('edit', {file_path: 'east.json', content: '{}'}), options: [{kind: 'allow_always', optionId: 'proceed_always'}]}];
  for (const input of bad) assert.equal(filePermission(identity, input).outcome.outcome, 'cancelled');
  assert.equal(filePermission({...identity, role: 'planner'}, request('read', {file_path: 'sales.json'})).outcome.outcome, 'cancelled');
  fs.symlinkSync(path.join(cwd, 'sales.json'), path.join(cwd, 'east.json'));
  assert.equal(filePermission(identity, request('edit', {file_path: 'east.json', content: '{}'})).outcome.outcome, 'cancelled');
});
const delivery = () => ({files: [{path: 'east.json', content: '{"region":"east","count":2,"netCents":1275}'},
  {path: 'west.json', content: '{"region":"west","count":2,"netCents":550}'}]});
test('independent consumer verifies complete named outputs and totals, not a worker pass marker', () => {
  const result = consumeDelivery(encode(delivery())); assert.equal(result.netCents, 1825); assert.equal(result.count, 4);
  assert.match(result.digest, /^sha256:[0-9a-f]{64}$/);
  for (const mutate of [v => v.files.pop(), v => v.files.reverse(), v => v.files[0].path = '../east.json',
    v => v.files[0].content = '{"region":"east","count":3,"netCents":1275}', v => v.files[0].content = '{"status":"passed"}',
    v => v.files[0].content = '{"region":"east","count":2,"netCents":1275,"netCents":1275}', v => v.extra = true]) {
    const value = delivery(); mutate(value); assert.throws(() => consumeDelivery(encode(value)));
  }
  assert.throws(() => consumeDelivery(Buffer.alloc(16385)));
});
function raw(start, end, executionId) {
  return {status: 'completed', stopReason: 'end_turn', outputText: 'PRIVATE_DO_NOT_RECORD', cleanup: {cleaned: true,
    started: {executionId, startedAt: new Date(start).toISOString()}, agentExit: {observed: true, at: new Date(end).toISOString()}}};
}
function facts() {
  return [['planning', 'planner', 0, 10], ['east', 'author', 20, 50], ['west', 'author', 30, 60]].map(([nodeId, role, start, end], index) =>
    executionFact({nodeId, role, workerId: 'worker-' + index, taskId: 'task-a'}, raw(start, end, 'execution-' + index)));
}
test('overlap uses original agent exit, requires distinct process/worker identities and never includes raw report', () => {
  const value = facts(); assert.equal(assertTeam(value), 20); assert.doesNotMatch(JSON.stringify(value), /PRIVATE/);
  value[1].agentExitedAt = new Date(30).toISOString(); assert.throws(() => assertTeam(value), /authors_did_not_overlap/);
  const duplicate = facts(); duplicate[1].executionId = duplicate[2].executionId; assert.throws(() => assertTeam(duplicate));
});
test('missing cleanup, inferred terminal time or protocol failure cannot provide overlap evidence', () => {
  for (const mutate of [r => r.cleanup.cleaned = false, r => delete r.cleanup.agentExit.at,
    r => r.cleanup.agentExit.observed = false, r => r.status = 'failed', r => r.stopReason = 'cancelled']) {
    const result = raw(1, 2, 'execution'); mutate(result); assert.throws(() => executionFact({workerId: 'w'}, result));
  }
});
