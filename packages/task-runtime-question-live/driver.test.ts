import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {parseOptions, validatePlan, answerOnce, runLive} from './driver.fixture.mjs';
import {data, choices, policy, questionPolicyDigest, questions, taskBody, bindPlan, expectedReports, validQuestion, answerFromRefs, verifyBusiness, consumeDelivery, verificationRequest} from './scenario.fixture.mjs';
import {TaskVerification, createVerificationPort} from '../task-application/verification.mjs';
import {encode, digest} from '../task-store/store.mjs';
const hash = value => digest(encode(value));
const args = ['--execute-real', '--answer', 'paid', '--run-dir', '/private/tmp/live-new', '--node', '/installed/node',
  '--pi-entry', '/installed/pi/dist/bundle/cli.js', '--pi-sdk', '/installed/pi/dist/index.js'];
function proof(answer = 'paid') {
  const q = {profile: 'task-runtime-question/v1', taskId: 'task-original', workerId: 'worker-east', nodeId: 'east',
    planDigest: 'sha256:' + 'a'.repeat(64), policyDigest: questionPolicyDigest, sequence: 1,
    request: {kind: 'select', prompt: 'east 需要采用哪种业务过滤状态？', options: [...choices]}};
  const questionDigest = hash(q), a = {taskId: q.taskId, questionDigest, answer};
  const ref = {question: q, questionDigest, answer: a, answerDigest: hash(a), dispatchDigest: 'sha256:' + 'b'.repeat(64), ackDigest: 'sha256:' + 'c'.repeat(64)};
  return {refs: [ref], verification: {manifests: [{nodeId: 'east', workerId: q.workerId}]}, planDigest: q.planDigest,
    source: {id: 'original-input', kind: 'input', name: 'sales.json', digest: digest(encode(data)), bytes: encode(data).length}};
}
const files = answer => expectedReports(answer).map(value => ({path: value.region + '.json', content: JSON.stringify(value)}));
test('explicit execution and predeclared closed business answer are mandatory; import does not call models', async () => {
  assert.deepEqual(parseOptions(['--help']), {help: true}); assert.equal(parseOptions(args).answer, 'paid');
  assert.equal(parseOptions(args.map(value => value === 'paid' ? 'cancelled' : value)).answer, 'cancelled');
  for (const value of [[], args.slice(1), args.filter((_, at) => ![1, 2].includes(at)), [...args, '--answer', 'paid'],
    args.map(value => value === 'paid' ? 'any' : value), [...args, '--retry', '2'], [...args, '--model', 'other'], [...args, '--token', 'secret']])
    assert.throws(() => parseOptions(value));
  await assert.rejects(runLive({}), /explicit_real_execution_required/);
});
test('selected answer never enters task/prompt; immutable policy permits question paraphrase but not extra permissions', () => {
  const first = taskBody('input', 600000), second = taskBody('input', 600000);
  assert.deepEqual(first, second); assert.equal(questions().policyDigest, questionPolicyDigest);
  assert.doesNotMatch(JSON.stringify(first), /1275|550|9000|answerDigest|predeclaredAnswer/);
  assert.equal(Object.hasOwn(first, 'answer'), false); assert.equal(Object.hasOwn(first.context, 'answer'), false);
  assert.equal(validQuestion({kind: 'select', prompt: '请选择 east 的状态', options: [...choices]}), true);
  assert.equal(validQuestion({kind: 'select', prompt: '不同措辞但相同业务槽', options: [...choices]}), true);
  for (const value of [{kind: 'input', prompt: '状态', options: []}, {kind: 'select', prompt: '状态', options: ['shell', 'paid']},
    {kind: 'select', prompt: '', options: [...choices]}]) assert.equal(validQuestion(value), false);
});
test('plan consumes exact Core policy/layout suffix, not model wording or mere policy ID', () => {
  const body = taskBody('input', 600000), plan = {summary: '业务计划', taskId: 'task', revision: 1, digest: 'sha256:' + 'a'.repeat(64),
    nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', providerId: null, goal: '本节点业务', scope: [id]})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: ['east.json', 'west.json'], acceptance: ['合理不同表述'], assumptions: [], budget: body.limits,
    interaction: {profile: 'task-runtime-question/v1', policyDigest: questionPolicyDigest, maxQuestions: 1, maxWaitMs: 120000}};
  const port = createVerificationPort({id: 'verify', policy, bindPlan, start() {throw Error('never launched');}});
  new TaskVerification({artifacts: {requireDepot() {}}}, port).bind({input: body, inputArtifacts: [{id: 'input'}]}, plan);
  const task = {id: 'task', status: 'awaiting-approval', revision: 2, plan: {revision: 1, digest: plan.digest}};
  assert.deepEqual(validatePlan(task, plan, body, 'input'), {expectedRevision: 2, planRevision: 1, planDigest: plan.digest});
  for (const change of [p => p.acceptance.pop(), p => p.assumptions.push('east defaults paid'), p => p.nodes[0].providerId = 'other',
    p => p.interaction.policyDigest = 'sha256:' + 'f'.repeat(64), p => p.budget.maxAttempts++]) {
    const bad = structuredClone(plan); change(bad); assert.throws(() => validatePlan(task, bad, body, 'input'));
  }
});
test('exact HTTP answer is sent once; CAS/transport errors do not refresh revision/key or autoapprove', async () => {
  const task = {id: 'task', revision: 8}, question = {id: 'question', taskId: 'task', workerId: 'worker', nodeId: 'east', kind: 'business', revision: 1,
    subject: 'sha256:' + 'a'.repeat(64), questionDigest: 'sha256:' + 'a'.repeat(64), status: 'open', deliveryStatus: null,
    options: choices.map(value => ({value, label: value})), deadlineAt: new Date(Date.now() + 10000).toISOString()};
  const calls = [], client = {async request(...input) {calls.push(input); throw Error('revision_conflict');}};
  await assert.rejects(answerOnce(client, task, question, 'paid'), /revision_conflict/); assert.equal(calls.length, 1);
  assert.equal(calls[0][0], 'task.answer'); assert.equal(calls[0][1].body.expectedRevision, 8); assert.equal(calls[0][1].idempotencyKey, 'runtime-business-answer');
  for (const changed of [{...question, taskId: 'other'}, {...question, nodeId: 'west'}, {...question, deliveryStatus: 'pending'},
    {...question, deadlineAt: '2000-01-01T00:00:00.000Z'}]) await assert.rejects(answerOnce(client, task, changed, 'paid'), /question_binding_mismatch/);
  assert.equal(calls.length, 1);
});
test('oracle recomputes both answer choices using original data; output pass labels and changed data are rejected', () => {
  for (const answer of choices) {
    const original = proof(answer), result = verifyBusiness({...original, sales: encode(data), files: files(answer)});
    assert.deepEqual(result.reports, expectedReports(answer));
    assert.equal(result.answer, answer);
    assert.throws(() => verifyBusiness({...original, sales: encode({rows: []}), files: files(answer)}));
    assert.throws(() => verifyBusiness({...original, sales: encode(data), files: [{path: 'east.json', content: '{"pass":true}'}, files(answer)[1]]}));
    assert.throws(() => verifyBusiness({...original, sales: encode(data), files: files(answer).slice(1)}));
  }
});
test('missing/foreign/tampered refs fail independently of business output; rehashed foreign Worker still refuses', () => {
  const original = proof(); assert.equal(answerFromRefs(original.refs, original.verification, original.planDigest), 'paid');
  for (const change of [p => p.refs = [], p => p.refs[0].answer.answer = 'cancelled', p => p.refs[0].ackDigest = null,
    p => p.refs[0].question.workerId = 'foreign', p => p.refs[0].question.policyDigest = 'sha256:' + 'f'.repeat(64),
    p => p.verification.manifests[0].workerId = 'other']) {
    const bad = structuredClone(original); change(bad);
    assert.throws(() => answerFromRefs(bad.refs, bad.verification, bad.planDigest));
  }
  const foreign = structuredClone(original); foreign.refs[0].question.workerId = 'foreign';
  foreign.refs[0].questionDigest = hash(foreign.refs[0].question); foreign.refs[0].answer.questionDigest = foreign.refs[0].questionDigest;
  foreign.refs[0].answerDigest = hash(foreign.refs[0].answer);
  assert.throws(() => answerFromRefs(foreign.refs, foreign.verification, foreign.planDigest));
});
test('checker command payload contains original input bytes only when exact uploaded receipt matches', () => {
  const p = proof(), ticket = {input: {inputArtifacts: [p.source], verification: p.verification, fileLayout: {}, interactionRefs: p.refs}};
  assert.deepEqual(verificationRequest({ticket}).sales, data); assert.deepEqual(verificationRequest({ticket}).interactionRefs, p.refs);
  for (const change of [value => value.input.inputArtifacts = [], value => value.input.inputArtifacts[0].digest = 'sha256:' + 'f'.repeat(64),
    value => value.input.inputArtifacts[0].bytes++, value => value.input.inputArtifacts[0].kind = 'delivery']) {
    const bad = structuredClone(ticket); change(bad); assert.throws(() => verificationRequest({ticket: bad}));
  }
});
test('download consumer requires exact explicit answer and question/answer hashes plus complete reports', () => {
  const p = proof(), value = {answer: 'paid', reports: expectedReports('paid'), questionDigest: p.refs[0].questionDigest, answerDigest: p.refs[0].answerDigest};
  assert.equal(consumeDelivery(encode(value), 'paid', value.questionDigest, value.answerDigest).reports, 2);
  for (const bad of [{...value, answer: 'cancelled'}, {...value, reports: []}, {...value, questionDigest: 'sha256:' + 'e'.repeat(64)}, {...value, extra: 'x'}])
    assert.throws(() => consumeDelivery(encode(bad), 'paid', value.questionDigest, value.answerDigest));
});
test('actual fixed Node checker preserves binding and rejects changed author bytes without models', t => {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-question-live-oracle-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true}));
  const p = proof('cancelled'), request = {profile: 'task-verification-command/v1', nonce: 'original-nonce', binding: {planDigest: p.planDigest},
    input: {interactionRefs: p.refs, verification: p.verification, sales: data, source: p.source}};
  fs.writeFileSync(path.join(cwd, 'sales.json'), encode(data)); for (const file of files('cancelled')) fs.writeFileSync(path.join(cwd, file.path), file.content);
  const checker = fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url));
  const run = () => spawnSync(process.execPath, [checker], {cwd, env: {}, input: encode(request).toString() + '\n', encoding: 'utf8', timeout: 5000, maxBuffer: 262144});
  const positive = run(); assert.equal(positive.status, 0, positive.stderr); const report = JSON.parse(positive.stdout);
  assert.equal(report.nonce, request.nonce); assert.deepEqual(report.binding, request.binding); assert.equal(report.assertions[0].actual.answer, 'cancelled');
  fs.writeFileSync(path.join(cwd, 'east.json'), JSON.stringify(expectedReports('paid')[0]));
  const failed = run(); assert.equal(failed.status, 1); assert.equal(failed.stdout, '');
});
