import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {encode, digest} from '../task-store/store.mjs';
import {TaskVerification, createVerificationPort} from '../task-application/verification.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {parseOptions, validatePlan, classify, repairRequest, repairOnce, validateDeliveryProof, runLive} from './driver.fixture.mjs';
import {data, regions, policy, repairPolicyDigest, contentAssertions, proposal, taskBody, bindPlan, originalInput,
  expectedReports, reportShape, consumeDelivery, equal} from './scenario.fixture.mjs';

const sha = value => 'sha256:' + value.repeat(64);
const initialArgs = ['--execute-real', '--phase', 'initial', '--run-dir', '/private/tmp/repair-new', '--node', '/installed/node',
  '--pi-entry', '/installed/pi/dist/bundle/cli.js', '--pi-sdk', '/installed/pi/dist/index.js'];
const repairArgs = initialArgs.map(value => value === 'initial' ? 'repair' : value).concat('--task-id', 'task-original', '--node-id', 'west',
  '--expected-revision', '8', '--plan-digest', sha('a'), '--decision-digest', sha('b'), '--feedback', '请先按最新版本取记录，再筛选 paid');
function failed() {
  const task = {id: 'task-original', status: 'failed', revision: 8, allowedActions: ['repair'], deadlineAt: new Date(Date.now() + 60000).toISOString()};
  const audit = {acceptance: {status: 'failed', digest: sha('b')}, attempts: 4, retryCount: 0, reworkCount: 0,
    decision: {status: 'rejected', digest: sha('b'), contentRejection: {policyDigest: repairPolicyDigest, failedAssertions: ['west-content'], reportDigest: sha('c')}}};
  return {task, audit, old: {created: {id: task.id}, failed: structuredClone(task), audit: structuredClone(audit), plan: {digest: sha('a')}}};
}
test('explicit two-phase authorization rejects hidden initial feedback, extra flags and malformed exact repair fields', async () => {
  assert.deepEqual(parseOptions(['--help']), {help: true}); assert.equal(parseOptions(initialArgs).phase, 'initial');
  assert.equal(parseOptions(repairArgs).nodeId, 'west');
  for (const argv of [[], initialArgs.slice(1), [...initialArgs, '--feedback', '故意算错'], [...initialArgs, '--phase', 'initial'],
    [...initialArgs, '--retry', '2'], [...initialArgs, '--token', 'PRIVATE'], repairArgs.filter((_, at) => ![19, 20].includes(at)),
    repairArgs.map(value => value === '8' ? '8.0' : value), repairArgs.map(value => value === 'west' ? 'verify' : value),
    repairArgs.slice(0, -1).concat('x'.repeat(4097)), repairArgs.slice(0, -1).concat('x\0y')]) assert.throws(() => parseOptions(argv));
  await assert.rejects(runLive({}), /explicit_real_execution_required/);
});
test('the actual Task input declares the complete enforced proposal and rule, with no manufactured error or repair answer', () => {
  const body = taskBody('input-original', 900000), declared = proposal();
  assert.ok(body.context.text.includes(encode(declared).toString()));
  assert.equal(body.limits.maxAttempts, 6); assert.equal(body.limits.maxWorkers, 2);
  assert.equal(Object.hasOwn(body, 'feedback'), false);
  assert.equal(body.context.text.includes('3515'), false); assert.equal(body.context.text.includes('-1237'), false);
  assert.equal(body.context.text.includes(':71'), false); assert.equal(body.context.text.includes(':46'), false);
  assert.equal(data.rows.length, 168);
  assert.deepEqual(expectedReports(), [{region: 'east', count: 71, netCents: 3515}, {region: 'west', count: 46, netCents: -1237}]);
  const changed = proposal(); changed.nodes[0].goal = '近似但没有相同规则';
  assert.throws(() => bindPlan({inputArtifacts: [{id: 'input-original'}], proposal: changed}));
});
test('the original FileBusiness planner prompt actually carries the complete enforced proposal', async t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-repair-prompt-'))), parent = path.join(root, 'executions');
  fs.mkdirSync(parent, {mode: 0o700}); const depot = ArtifactDepot.create(path.join(root, 'depot'));
  const business = createFileBusiness({parent, depot, layoutFor: () => ({inputs: [], allowedPaths: []}),
    approvedLayout() {throw Error('unapproved planner must not request author authority');}, observeExecution() {throw Error('no process started');}});
  t.after(() => {business.close(); depot.close(); fs.rmSync(root, {recursive: true, force: true});});
  const input = {task: taskBody('input-original', 900000), plan: null, node: {id: 'planning', role: 'planner'}, upstream: [],
    inputArtifacts: [{id: 'input-original', kind: 'input', taskId: null, status: 'ready', ...depot.put(encode(data))}]};
  const frozen = {workerId: 'worker-planning', taskId: 'task-original', nodeId: 'planning', role: 'planner', providerId: 'pi', commandId: 'command-original',
    generation: '1', repairId: null, inputDigest: digest(encode(input)), planDigest: null, deadline: Date.now() + 10000, input};
  const ticket = {...frozen, reservationDigest: digest(encode(frozen))};
  const prepared = await business.prepare(ticket, {signal: new AbortController().signal, deadline: ticket.deadline});
  const sent = JSON.parse(prepared.prompt.slice(prepared.prompt.indexOf('{"task":')));
  const visibleProposal = JSON.parse(sent.task.context.text.slice(sent.task.context.text.lastIndexOf('\n') + 1));
  assert.deepEqual(visibleProposal, proposal());
  assert.equal(bindPlan({inputArtifacts: input.inputArtifacts, proposal: visibleProposal}).nodeId, 'verify');
  assert.deepEqual(fs.readdirSync(prepared.cwd), []); business.release(ticket);
});
test('exact approval requires the real visible Core verification binding, frozen repair policy and six-attempt budget', () => {
  const body = taskBody('input-original', 900000), plan = {...proposal(), taskId: 'task-original', revision: 1, digest: sha('a'), budget: body.limits,
    repair: {profile: 'task-local-repair/v1', policyDigest: repairPolicyDigest}};
  const port = createVerificationPort({id: 'independent-latest-paid-checker', policy, bindPlan, start() {throw Error('not launched');}});
  new TaskVerification({artifacts: {requireDepot() {}}}, port).bind({input: body, inputArtifacts: [{id: 'input-original'}]}, plan);
  const task = {id: plan.taskId, status: 'awaiting-approval', revision: 2, plan: {revision: 1, digest: plan.digest}};
  assert.deepEqual(validatePlan(task, plan, body, 'input-original'), {expectedRevision: 2, planRevision: 1, planDigest: sha('a')});
  for (const change of [p => p.budget.maxAttempts++, p => p.repair.policyDigest = sha('e'), p => p.acceptance.pop(),
    p => p.assumptions.push('先忽略退款'), p => p.nodes[0].providerId = 'other', p => p.nodes[1].goal = '写错再改']) {
    const bad = structuredClone(plan); change(bad); assert.throws(() => validatePlan(task, bad, body, 'input-original'));
  }
});
test('first-pass success and structural/multiple/unknown failures do not manufacture an eligible repair', () => {
  const {task, audit} = failed();
  assert.deepEqual(classify(task, audit), {outcome: 'awaiting-explicit-repair', nodeId: 'west'});
  assert.deepEqual(classify({...task, status: 'completed'}, {...audit, acceptance: {status: 'passed'}}), {outcome: 'natural-first-pass', nodeId: null});
  for (const change of [a => a.decision.contentRejection = null, a => a.decision.contentRejection.failedAssertions.push('east-content'),
    a => a.decision.contentRejection.failedAssertions = ['report-structure'], a => a.attempts = 5, a => a.retryCount++,
    a => a.decision.digest = sha('e'), a => a.decision.contentRejection.policyDigest = sha('f')]) {
    const bad = structuredClone(audit); change(bad); assert.equal(classify(task, bad).outcome, 'not-repair-eligible');
  }
  for (const status of ['cancelled', 'intervention', 'running']) assert.equal(classify({...task, status}, audit).outcome, 'not-repair-eligible');
});
test('repair sends one original HTTP request, never refreshes CAS/key or retries transport errors', async () => {
  const {task, audit, old} = failed(), options = parseOptions(repairArgs), calls = [];
  const request = repairRequest(options, old, task, audit);
  assert.deepEqual(request.body.nodeIds, ['west']); assert.equal(request.body.expectedRevision, 8);
  assert.equal(request.idempotencyKey, 'explicit-content-repair');
  await assert.rejects(repairOnce({async request(...args) {calls.push(args); throw Error('lost_response');}}, request), /lost_response/);
  assert.equal(calls.length, 1); assert.equal(calls[0][0], 'task.repair');
  for (const change of [v => v.taskId = 'foreign', v => v.nodeId = 'east', v => v.expectedRevision++, v => v.planDigest = sha('d'), v => v.decisionDigest = sha('e')]) {
    const bad = {...options}; change(bad); assert.throws(() => repairRequest(bad, old, task, audit), /repair_authorization_stale/);
  }
  assert.throws(() => repairRequest(options, old, {...task, deadlineAt: '2000-01-01T00:00:00.000Z'}, audit));
});
test('the checker receives only the exact original uploaded input, not an operator feedback closure', () => {
  const source = {id: 'input-original', kind: 'input', name: 'sales.json', digest: digest(encode(data)), bytes: encode(data).length};
  const ticket = {input: {inputArtifacts: [source], verification: {manifests: []}, repair: {feedback: 'operator feedback'}}};
  assert.deepEqual(originalInput(ticket), {source, sales: data, verification: {manifests: []}});
  for (const change of [value => value.input.inputArtifacts[0].digest = sha('f'), value => value.input.inputArtifacts[0].bytes++,
    value => value.input.inputArtifacts[0].kind = 'delivery', value => value.input.inputArtifacts = []]) {
    const bad = structuredClone(ticket); change(bad); assert.throws(() => originalInput(bad), /original_input_mismatch/);
  }
});
test('third consumer independently rejects stale versions, excluded refunds, altered bytes and incomplete deliverables', () => {
  const reports = expectedReports(), files = reports.map(report => ({path: report.region + '.json', content: JSON.stringify(report)}));
  assert.equal(consumeDelivery(encode({files})).reports, 2);
  for (const change of [value => value.files.pop(), value => value.files.reverse(), value => value.files[1].content = JSON.stringify({...reports[1], netCents: 750}),
    value => value.files[0].content = JSON.stringify({...reports[0], count: 2}), value => value.pass = true]) {
    const bad = structuredClone({files}); change(bad); assert.throws(() => consumeDelivery(encode(bad)));
  }
});
test('final original evidence binds the exact approved plan/checker and independently consumed delivery', () => {
  const artifact = {digest: sha('d'), bytes: 300}, proof = {binding: {planDigest: sha('a'), checkerDigest: sha('c'), policyDigest: digest(encode(policy))},
    delivery: artifact, assertions: expectedReports().map((actual, at) => ({name: contentAssertions[at], actual}))
      .concat({name: 'report-structure', actual: expectedReports()})};
  assert.equal(validateDeliveryProof(proof, artifact, sha('a'), sha('c')), undefined);
  for (const change of [v => v.binding.planDigest = sha('b'), v => v.binding.checkerDigest = sha('b'), v => v.delivery.bytes++,
    v => v.delivery.digest = sha('f'), v => v.assertions.pop(), v => v.assertions[0].actual.count++]) {
    const bad = structuredClone(proof); change(bad); assert.throws(() => validateDeliveryProof(bad, artifact, sha('a'), sha('c')));
  }
});
test('actual original managed checker classifies genuine fixture content false only; these are no-model counterexamples, not live repair evidence', {timeout: 25000}, async t => {
  const checkerPath = fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url)), policyDigest = digest(encode(policy));
  for (const mode of ['correct', 'wrong-content', 'wrong-shape', 'bad-json']) {
    const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-repair-oracle-')));
    const reports = expectedReports(); if (mode === 'wrong-content') reports[1].netCents++;
    if (mode === 'wrong-shape') reports[1].extra = true;
    for (const [at, region] of regions.entries()) fs.writeFileSync(path.join(cwd, region + '.json'), mode === 'bad-json' && at === 1 ? '{bad' : encode(reports[at]), {mode: 0o400});
    const input = {task: {}, plan: {repair: {profile: 'task-local-repair/v1', policyDigest: repairPolicyDigest}},
      inputArtifacts: [{id: 'input-original', kind: 'input', name: 'sales.json', digest: digest(encode(data)), bytes: encode(data).length}],
      verification: {binding: {profile: 'task-verification/v1', policyDigest, providerId: 'checker', nodeId: 'verify',
        policy, description: policy.description, layouts: {}, deliveries: []}, manifests: []}};
    const frozen = {workerId: 'worker-verify', taskId: 'task-original', nodeId: 'verify', role: 'verifier', executionType: 'verification',
      providerId: 'checker', generation: '1', commandId: 'command-original', inputDigest: digest(encode(input)), planDigest: sha('a'),
      repairId: null, deadline: Date.now() + 10000, input};
    const ticket = {...frozen, reservationDigest: digest(encode(frozen))}; let deliveries = 0;
    const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest,
      repair: {policyDigest: repairPolicyDigest, assertions: [...contentAssertions]}, request: ({ticket}) => originalInput(ticket),
      assertions: regions.map((region, at) => ({name: region + '-content', validate: value => equal(value, expectedReports()[at])}))
        .concat({name: 'report-structure', validate: values => Array.isArray(values) && values.length === 2 && values.every((value, at) => reportShape(value, regions[at]))}),
      delivery() {deliveries++; return {name: 'result.json', mediaType: 'application/json', content: encode({files: []})};}});
    const handle = command.start({ticket, prepared: {cwd}});
    t.after(async () => {const stopped = await handle.stop(); assert.equal(stopped.cleanup.cleaned, true); fs.rmSync(cwd, {recursive: true, force: true});});
    const result = await handle.completion; assert.equal(result.cleanup.cleaned, true, mode);
    assert.equal(result.status, mode === 'correct' ? 'passed' : 'failed', mode);
    assert.equal(deliveries, mode === 'correct' ? 1 : 0);
    if (mode === 'wrong-content') {
      assert.deepEqual(result.contentRejection.failedAssertions, ['west-content']);
      const proof = JSON.parse(result.evidence.content);
      assert.equal(digest(Buffer.from(proof.originalReport)), proof.reportDigest);
      assert.deepEqual(proof.parentAssertions, [{name: 'east-content', passed: true}, {name: 'west-content', passed: false}, {name: 'report-structure', passed: true}]);
    } else assert.equal(result.contentRejection, undefined);
  }
});
