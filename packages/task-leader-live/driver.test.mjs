import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {encode, digest} from '../task-store/store.mjs';
import {contract, leaderRequestDigest, leaderReplyDigest} from '../task-api/contract.mjs';
import {parseOptions, runLive, workPackage, reviewPolicy, liveApplicationOptions, managedPrompt, createObservedBusiness, saveFailureDiagnostics} from './driver.fixture.mjs';
import {trackExecution} from '../task-qwen-live/driver.fixture.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {createFileBusiness, isManagedFileBusiness, fileLayoutDigest} from '../task-business/index.mjs';
import {filePermission} from '../task-pi-live/driver.fixture.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, parseManagedOutput, renderLeaderPrompt, renderReviewPrompt} from '../task-application/application.mjs';
import {startTaskService} from '../task-service/composition.mjs';
import {data, choices, policy, taskBody, bindPlan, reportFor} from './scenario.fixture.mjs';
import {equal, businessReply, verificationRequest, validatePlan, replyOnce, assertReplyReplay, verifyAcceptance, authorizeReport, authorOverlap} from './proof.fixture.mjs';
import {checkRequest} from './checker.fixture.mjs';
import {startReportServer, consumePublished} from './report-server.fixture.mjs';

const hash = value => digest(encode(value)), sha = 'sha256:' + 'a'.repeat(64), taskId = 'task-example';
const fresh = name => structuredClone(contract.components.schemas[name].examples[0]);
const deadline = () => new Date(Date.now() + 60000).toISOString();
function reply(answer = 'paid') {
  const ref = {requestId: 'request-example', requestDigest: sha};
  return {...ref, answer, replyDigest: leaderReplyDigest(taskId, ref.requestId, {...ref, answer})};
}
function ticket(answer = 'paid') {
  const {answer: _answer, ...ref} = reply(answer);
  return {taskId, input: {inputArtifacts: [{id: 'input-sales', kind: 'input', name: 'sales.json', digest: hash(data), bytes: encode(data).length}],
    leaderReplyRefs: [ref], leaderReplies: [reply(answer)], verification: {binding: {profile: 'task-verification/v1'}}, fileLayout: {inputs: [], allowedPaths: []}}};
}

test('private failed-decision forensics preserve original Provider output bytes without changing the opaque result or exposing incidental fields', async t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-diagnostics-')));
  t.after(() => fs.rmSync(root, {recursive: true}));
  const privateMarker = 'SYNTHETIC_PRIVATE_CANARY', incidental = 'SYNTHETIC_ENV_STDERR_CANARY';
  // Deliberately invalid original output: do not normalize BOM, CRLF, duplicate
  // keys or non-JSON prose when preserving private failure evidence.
  const outputText = '\uFEFF```json\r\n{"summary":"' + privateMarker + '","summary":"重复"}\r\n```';
  const started = {executionId: 'original-parser-fixture', startedAt: new Date().toISOString()};
  const raw = {providerId: 'pi', status: 'completed', stopReason: 'end_turn', reason: 'pi_agent_stop', outputText,
    env: {privateValue: incidental}, stderr: incidental, sessionId: incidental,
    cleanup: {executionId: started.executionId, started, scope: 'inherited-process-group', cleaned: true, reason: 'owner_stop',
      agentExit: {observed: true, code: 143, signal: null, at: new Date().toISOString()},
      guardExit: {observed: true, code: null, signal: 'SIGKILL', at: new Date().toISOString()}, unknown: incidental}};
  const original = structuredClone(raw), entries = [];
  const provider = {id: 'pi', start() {const handle = {started: Promise.resolve(started), completion: Promise.resolve(raw), stop: async () => raw};
    entries.push(trackExecution({taskId, workerId: 'worker-leader', executionType: 'leader'}, handle)); return handle;}};
  const port = createLeaderPort({id: 'diagnostic-fixture', providerId: 'pi', policy: planCase().leader,
    prepare: () => ({prompt: 'controlled'}), parseDecision: parseManagedOutput});
  const result = await port.start({ticket: {executionType: 'leader', providerId: 'pi', input: {leader: {callId: 'call-original', inputDigest: sha}}},
    provider, prepared: {prompt: 'controlled'}}).completion;
  assert.equal(result.status, 'failed'); assert.equal(result.cleanup.cleaned, true);
  const receipt = result.receipt, summary = saveFailureDiagnostics(root, entries);
  assert.equal(summary.authority, false); assert.equal(result.receipt, receipt); assert.deepEqual(raw, original);
  const directory = path.join(root, 'failure-diagnostics'), text = fs.readFileSync(path.join(root, summary.path), 'utf8');
  const metadata = JSON.parse(text), item = metadata.items[0], saved = fs.readFileSync(path.join(directory, item.output.file));
  assert.deepEqual(saved, Buffer.from(outputText)); assert.equal(item.output.bytes, saved.length); assert.equal(item.output.digest, digest(saved));
  assert.equal(item.status, 'completed'); assert.equal(item.stopReason, 'end_turn'); assert.equal(item.reason, 'pi_agent_stop');
  assert.equal(item.cleanup.executionId, started.executionId); assert.equal(item.cleanup.agentExit.code, 143);
  assert.equal(metadata.authority, false); assert.equal(metadata.mayContainSensitiveOutput, true);
  assert.equal(fs.statSync(directory).mode & 0o777, 0o700);
  for (const file of fs.readdirSync(directory)) assert.equal(fs.statSync(path.join(directory, file)).mode & 0o777, 0o600);
  assert.ok(!text.includes(privateMarker) && !text.includes(incidental));
  assert.ok(!JSON.stringify(summary).includes(privateMarker));
  assert.throws(() => saveFailureDiagnostics(root, entries), {code: 'EEXIST'}); assert.deepEqual(fs.readFileSync(path.join(directory, item.output.file)), saved);
});
test('private failure forensics are bounded and closed; oversized/missing/unsettled output and unrecognized strings never become raw public metadata', t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-diagnostics-bounds-')));
  t.after(() => fs.rmSync(root, {recursive: true}));
  const entry = outputText => ({settled: true, failed: false, identity: {taskId}, result: {outputText}});
  const exact = 'a'.repeat(65536), unicode = '中'.repeat(22000), marker = 'PRIVATE VALUE MUST NOT ENTER METADATA';
  const values = [entry(exact), entry(unicode), entry('a'.repeat(65537)), {settled: true, failed: true}, {settled: false},
    {...entry(undefined), identity: {taskId: marker}, result: {status: marker, reason: marker, stopReason: marker,
      cleanup: {reason: marker, scope: marker, executionId: marker, started: {startedAt: marker}, guardExit: {signal: marker, at: marker}}}}];
  const summary = saveFailureDiagnostics(root, values), bytes = fs.readFileSync(path.join(root, summary.path));
  const metadata = JSON.parse(bytes), codes = metadata.items.map(item => item.code);
  assert.deepEqual(codes, ['received_output_saved', 'output_over_limit', 'output_over_limit', 'completion_rejected', 'completion_unsettled', 'output_unavailable']);
  assert.equal(metadata.items[0].output.bytes, 65536); assert.equal(metadata.items[1].output.bytes, 66000);
  assert.equal(fs.readdirSync(path.join(root, 'failure-diagnostics')).length, 2); // One original output + closed metadata.
  assert.ok(!bytes.includes(marker));
  assert.throws(() => saveFailureDiagnostics(root, Array(35).fill(entry(''))), {code: 'failure_diagnostics_limit'});
});
test('failure evidence never follows a diagnostic directory symlink or accepts a public root', t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-diagnostics-path-')));
  t.after(() => fs.rmSync(parent, {recursive: true}));
  const root = path.join(parent, 'run'), outside = path.join(parent, 'outside');
  fs.mkdirSync(root, {mode: 0o700}); fs.mkdirSync(outside, {mode: 0o700});
  fs.symlinkSync(outside, path.join(root, 'failure-diagnostics'));
  assert.throws(() => saveFailureDiagnostics(root, []), {code: 'EEXIST'}); assert.deepEqual(fs.readdirSync(outside), []);
  fs.chmodSync(root, 0o755); assert.throws(() => saveFailureDiagnostics(root, []), {code: 'failure_diagnostics_root'});
});
function question() {
  const task = {...fresh('Task'), id: taskId, status: 'awaiting-answer', plan: null, revision: 2, deadlineAt: deadline()};
  const view = fresh('LeaderView'); view.taskId = taskId; view.taskRevision = task.revision;
  view.pendingRequest = {...view.pendingRequest, kind: 'business', nodeIds: [], authorization: null, subject: sha,
    prompt: 'east 需要 paid 还是 cancelled？', options: choices.map(value => ({value, label: value})),
    deadlineAt: task.deadlineAt, status: 'pending', replyDigest: null};
  view.pendingRequest.requestDigest = leaderRequestDigest(taskId, view.pendingRequest); return {task, view};
}
function publication() {
  const {task, view} = question(); task.status = 'awaiting-confirmation';
  const content = encode(reportFor('paid')), artifact = {...fresh('Artifact'), taskId, kind: 'delivery', status: 'ready',
    mediaType: 'application/json', digest: digest(content), bytes: content.length};
  const plan = {digest: sha}, audit = {acceptance: {status: 'passed', digest: sha}}, target = {id: 'local-report', policyDigest: sha};
  const nameFor = ({taskId, artifactDigest}) => `${taskId}-${artifactDigest.slice(7)}.json`;
  view.review = {digest: sha, verdict: 'accept', selectionDigest: sha, policyDigest: hash(reviewPolicy), workerId: 'worker-review', evidenceIds: ['artifact-review']};
  view.pendingRequest.kind = 'publication'; view.pendingRequest.options = ['allow', 'deny'].map(value => ({value, label: value}));
  view.pendingRequest.authorization = {taskId, planDigest: plan.digest, artifactId: artifact.id, artifactDigest: artifact.digest, bytes: artifact.bytes,
    acceptanceDigest: sha, reviewDigest: sha, targetId: target.id, targetPolicyDigest: target.policyDigest,
    name: nameFor({taskId, artifactDigest: artifact.digest}), operation: 'create-if-absent', expiresAt: task.deadlineAt};
  rebind(view);
  return {task, view, artifact, content, answer: 'paid', publication: target, nameFor, plan, audit};
}
function rebind(view) {
  if (view.pendingRequest.authorization) view.pendingRequest.subject = hash(view.pendingRequest.authorization);
  view.pendingRequest.requestDigest = leaderRequestDigest(view.taskId, view.pendingRequest);
}
function planCase() {
  const body = taskBody('input-sales', 60000), task = {...fresh('Task'), id: taskId, status: 'awaiting-approval', revision: 5,
    plan: {revision: 1, digest: sha}};
  const leader = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 1, maxRequests: 4,
    repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: 'pi', policyDigest: sha}, publication: {targetId: 'local-report', policyDigest: sha}};
  const plan = {taskId, revision: 1, digest: sha, nodes: ['east', 'west', 'verify'].map((id, i) =>
    ({id, role: i === 2 ? 'verifier' : 'author', providerId: null, goal: '真实业务目标', scope: []})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], budget: body.limits, deliverables: body.requirements.deliverables, assumptions: []};
  const binding = bindPlan({inputArtifacts: [{id: 'input-sales'}], proposal: plan});
  plan.acceptance = ['合理改述而非固定中文复述', ...[{policy, description: binding.description}, ...binding.layouts.map(layout => ({layout})),
    ...binding.deliveries.map(delivery => ({delivery})), {profile: leader.profile, policyDigest: hash(leader), repair: leader.repair,
      review: leader.review, publication: leader.publication, completion: 'leader-delivery'}].map(JSON.stringify)];
  return {task, plan, body, leader};
}

test('opt-in is explicit, two requirements share one configuration, first-workers is not full delivery', async () => {
  const args = ['--execute-real', '--answers', 'paid,cancelled', '--allow-local-publication', '--run-dir', '/private/tmp/new-leader',
    '--node', '/fixed/node', '--pi-entry', '/fixed/dist/bundle/cli.js', '--pi-sdk', '/fixed/dist/index.js'];
  assert.deepEqual(parseOptions(args).answers, choices); assert.equal(parseOptions(args).checkpoint, 'complete');
  assert.equal(parseOptions([...args, '--checkpoint', 'first-workers']).checkpoint, 'first-workers');
  for (const invalid of [args.filter(x => x !== '--execute-real'), args.filter(x => x !== '--allow-local-publication'),
    args.map(x => x === 'paid,cancelled' ? 'paid,paid' : x), [...args, '--answer', 'paid'], [...args, '--checkpoint', 'publish-anything']])
    assert.throws(() => parseOptions(invalid));
  await assert.rejects(runLive({}), {code: 'explicit_real_execution_required'});
});
test('Core reply and original Depot input are required; caller answer, stale/foreign refs and byte drift cannot substitute', () => {
  for (const answer of choices) {
    const original = ticket(answer), request = verificationRequest(original, () => encode(data));
    assert.equal(businessReply(taskId, original.input).answer, answer); assert.equal(request.reply.answer, answer);
    assert.deepEqual(reportFor(answer), reportFor(request.reply.answer));
  }
  for (const mutate of [v => {v.input.leaderReplyRefs = [];}, v => {v.input.leaderReplies[0].answer = 'cancelled';},
    v => {v.input.leaderReplyRefs[0].requestId = 'foreign';}, v => {v.taskId = 'task-foreign';},
    v => {v.input.inputArtifacts[0].digest = sha;}, v => {v.input.leaderReplies.push(reply());}]) {
    const original = ticket(); mutate(original); assert.throws(() => verificationRequest(original, () => encode(data)));
  }
  assert.throws(() => verificationRequest(ticket(), () => Buffer.from('wrong')));
});
test('plan keeps Core policy/layout and original limits but tolerates natural-language paraphrase', () => {
  const {task, plan, body, leader} = planCase(); assert.equal(validatePlan(task, plan, body, 'input-sales', leader).expectedRevision, 5);
  for (const mutate of [v => {v.acceptance.pop();}, v => {v.acceptance.push(v.acceptance.at(-1));}, v => {v.nodes[0].role = 'reviewer';},
    v => {v.nodes[0].scope = {write: ['east.json']};}, v => {v.edges.push({from: 'east', to: 'west'});}, v => {v.budget.maxAttempts++;}]) {
    const value = structuredClone(plan); mutate(value); assert.throws(() => validatePlan(task, value, body, 'input-sales', leader));
  }
});
test('reply exact CAS/key/body is one write; lost response never retries; replay cannot become fresh authorization', async () => {
  const {task, view} = question(); let count = 0;
  const client = {request: async (operation, request) => {count++; assert.equal(operation, 'task.leader.reply');
    return {taskId, requestId: request.path.requestId, receiptId: 'receipt-original', requestDigest: request.body.requestDigest,
      replyDigest: leaderReplyDigest(taskId, request.path.requestId, request.body), acceptedRevision: task.revision + 1, replayed: false};}};
  const original = await replyOnce(client, task, view, 'paid', 'original-key'); assert.equal(count, 1);
  assert.equal(original.request.idempotencyKey, 'original-key'); assert.equal(original.request.body.expectedRevision, task.revision);
  assertReplyReplay(original, {...original.receipt, replayed: true});
  assert.throws(() => assertReplyReplay(original, {...original.receipt, acceptedRevision: 999, replayed: true}));
  await assert.rejects(replyOnce({request: async () => {count++; throw Error('response_lost');}}, task, view, 'paid', 'original-key'));
  assert.equal(count, 2);
  await assert.rejects(replyOnce(client, {...task, revision: 999}, view, 'paid', 'new-key'));
  assert.equal(count, 2);
});
test('publication allow checks full original evidence and business bytes, not a model description or mere checksum', () => {
  const original = publication(); assert.equal(authorizeReport(original).targetId, 'local-report');
  for (const mutate of [v => {v.view.pendingRequest.authorization.targetId = 'foreign'; rebind(v.view);},
    v => {v.view.pendingRequest.authorization.artifactId = 'other'; rebind(v.view);},
    v => {v.view.pendingRequest.authorization.acceptanceDigest = 'sha256:' + 'b'.repeat(64); rebind(v.view);},
    v => {v.view.review.verdict = 'rework';}, v => {v.answer = 'cancelled';}, v => {v.content = encode({reports: []});},
    v => {v.view.pendingRequest.authorization.operation = 'overwrite'; rebind(v.view);}]) {
    const value = publication(); mutate(value); assert.throws(() => authorizeReport(value));
  }
});
test('verification evidence must bind the original HTTP reply receipt, task, policy and downloaded delivery', () => {
  const pub = publication(), originalReply = reply();
  const answered = {receipt: {requestId: originalReply.requestId, requestDigest: originalReply.requestDigest, replyDigest: originalReply.replyDigest}};
  const value = {profile: 'task-verification-command/v1', binding: {planDigest: sha, policyDigest: hash(policy), inputDigest: sha, reservationDigest: sha},
    executionId: 'original-verifier', delivery: {digest: pub.artifact.digest, bytes: pub.artifact.bytes},
    assertions: [{name: 'leader-regions', actual: {report: reportFor('paid'), reply: originalReply}}]};
  const checkValue = (value, answer = 'paid') => verifyAcceptance({taskId, planDigest: sha, delivery: {artifact: pub.artifact, content: pub.content},
    proofs: [{artifact: {id: 'original-evidence', taskId, kind: 'evidence', digest: hash(value)}, content: encode(value)}], answered, answer});
  assert.equal(checkValue(value).executionId, 'original-verifier');
  for (const mutate of [v => {v.assertions[0].actual.reply.requestId = 'foreign';}, v => {v.binding.policyDigest = sha;},
    v => {v.delivery.digest = sha;}, v => {v.assertions[0].actual.report = reportFor('cancelled');}]) {
    const next = structuredClone(value); mutate(next); assert.throws(() => checkValue(next));
  }
});
test('actual prepared work package includes full Task, shared plan, node and replies without asserting Agent consumption', () => {
  const original = {...ticket(), executionType: 'agent', inputDigest: sha, reservationDigest: sha, workerId: 'worker-a', nodeId: 'east', role: 'author'};
  Object.assign(original.input, {task: taskBody('input-sales', 60000), plan: planCase().plan, node: {id: 'east', goal: '原需求'}});
  const prompt = JSON.stringify(original.input), observed = workPackage(original, {prompt});
  assert.equal(observed.agentConsumptionProven, false); assert.equal(observed.promptDigest, digest(Buffer.from(prompt)));
  for (const field of ['task', 'plan', 'node', 'leaderReplies']) {
    const value = {...original.input}; delete value[field]; assert.throws(() => workPackage(original, {prompt: JSON.stringify(value)}));
  }
  assert.throws(() => authorOverlap([{role: 'author', nodeId: 'east', workerId: 'a', executionId: 'a', startedAt: '2026-01-01T00:00:01Z', agentExitedAt: '2026-01-01T00:00:02Z'},
    {role: 'author', nodeId: 'west', workerId: 'b', executionId: 'b', startedAt: '2026-01-01T00:00:03Z', agentExitedAt: '2026-01-01T00:00:04Z'}]));
});
test('observed FileBusiness retains original private identity, real allocation and scoped permission; managed callbacks capture original cwd', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-identity-')));
  const executionParent = path.join(parent, 'executions'); fs.mkdirSync(executionParent, {mode: 0o700});
  const depot = ArtifactDepot.create(path.join(parent, 'depot')), byCwd = new Map(), byWorker = new Map();
  const capture = (ticket, cwd) => {byCwd.set(cwd, ticket); byWorker.set(ticket.workerId, {...ticket, cwd});};
  const layout = {inputs: [{path: 'sales.json', source: {kind: 'input', id: 'input-sales'}}], allowedPaths: ['east.json']};
  const started = {executionId: 'controlled-author', startedAt: new Date().toISOString()};
  const business = createObservedBusiness(createFileBusiness, {executionParent, depot,
    approvedLayout: ticket => ({planDigest: ticket.planDigest, nodeId: ticket.nodeId, layoutDigest: fileLayoutDigest(layout)}),
    observeExecution: () => started}, {capture, authorize: (ticket, request) => filePermission(byWorker.get(ticket.workerId), request)});
  t.after(() => {business.close(); depot.close(); fs.rmSync(parent, {recursive: true});});
  assert.equal(isManagedFileBusiness(business), true); assert.equal(isManagedFileBusiness({...business}), false);
  const {plan, body} = planCase(), originalInput = ticket().input;
  const seal = ({reservationDigest: _previous, ...value}) => {
    const original = {...value, inputDigest: hash(value.input)}; return {...original, reservationDigest: hash(original)};
  };
  const author = seal({taskId, workerId: 'worker-author', nodeId: 'east', role: 'author', providerId: 'pi', commandId: 'command-author',
    executionType: 'agent', generation: '1', planDigest: plan.digest, deadline: Date.now() + 60000,
    input: {...originalInput, task: body, plan, node: plan.nodes[0], upstream: [], fileLayout: layout,
      inputArtifacts: [{id: 'input-sales', kind: 'input', taskId: null, status: 'ready', name: 'sales.json', ...depot.put(encode(data))}]}});
  const context = value => ({signal: new AbortController().signal, deadline: value.deadline});
  const prepared = await business.prepare(author, context(author));
  assert.equal(prepared.cwd, path.join(executionParent, author.workerId));
  assert.deepEqual(encode(byCwd.get(prepared.cwd)), encode(author));
  assert.deepEqual(fs.readFileSync(path.join(prepared.cwd, 'sales.json')), encode(data));
  assert.equal(workPackage(byCwd.get(prepared.cwd), prepared).requiredContextPresent, true);
  const permission = name => ({toolCall: {kind: 'edit', rawInput: {path: name, content: '{}'}, _meta: {provider: 'pi', toolName: 'write'}},
    options: [{kind: 'allow_once', optionId: 'allow-once'}]});
  assert.equal((await prepared.onPermission(permission('east.json'))).outcome.outcome, 'selected');
  assert.equal((await prepared.onPermission(permission('west.json'))).outcome.outcome, 'cancelled');
  fs.writeFileSync(path.join(prepared.cwd, 'east.json'), encode(reportFor('paid').reports[0]), {mode: 0o600});
  const collected = await business.collect(author, {providerId: 'pi', status: 'completed', stopReason: 'end_turn', outputText: '受控文件候选',
    cleanup: {started, cleaned: true}}, context(author));
  assert.equal(collected.result.files[0].path, 'east.json');
  for (const [type, create, render] of [['leader', createLeaderPort, renderLeaderPrompt], ['review', createReviewPort, renderReviewPrompt]]) {
    const input = {...author.input, upstream: [], node: {id: 'managed-' + type, role: type === 'leader' ? 'planner' : 'reviewer'},
      [type]: {taskId, callId: 'call-example', inputDigest: sha, selectionDigest: sha, snapshot: {task: {input: body}}, materials: []}};
    const value = seal({...author, workerId: 'worker-' + type, nodeId: input.node.id, role: input.node.role, executionType: type, input});
    const port = create({id: type, providerId: 'pi', policy: type === 'leader' ? planCase().leader : reviewPolicy,
      prepare: managedPrompt(capture, render), ...(type === 'leader' ? {parseDecision: parseManagedOutput} : {parseReport: parseManagedOutput})});
    const staged = await business.prepareManaged(value, context(value));
    assert.equal(byCwd.has(staged.cwd), false);
    const prompt = await port.prepare(value, staged, context(value));
    assert.deepEqual(encode(byCwd.get(staged.cwd)), encode(value));
    assert.equal(workPackage(byCwd.get(staged.cwd), prompt).requiredContextPresent, true);
    assert.equal((await prompt.onPermission(permission('east.json'))).outcome.outcome, 'cancelled');
    business.validateManaged(value); business.release(value);
  }
});
test('live composition preflight creates and reopens v7 with original business identity and explicit three-slot defaults, without a Task/model', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-preflight-'))), root = path.join(parent, 'service');
  let service, starts = 0, factories = 0;
  t.after(async () => {if (service) await service.shutdown(); fs.rmSync(parent, {recursive: true});});
  const native = {id: 'pi', start() {starts++; throw Error('model_start_forbidden');}};
  const leaderPolicy = {...planCase().leader, review: {providerId: native.id, policyDigest: hash(reviewPolicy)}, publication: null};
  const leader = createLeaderPort({id: 'leader', providerId: native.id, policy: leaderPolicy,
    prepare: managedPrompt(() => {}, renderLeaderPrompt), parseDecision: parseManagedOutput});
  const review = createReviewPort({id: 'review', providerId: native.id, policy: reviewPolicy,
    prepare: managedPrompt(() => {}, renderReviewPrompt), parseReport: parseManagedOutput});
  const verification = createVerificationPort({id: 'checker', policy, bindPlan, start() {throw Error('checker_start_forbidden');}});
  const config = {root, providers: new Map([[native.id, native]]), leader, review, verification,
    custody: {profile: 'node-execution-custody/v1'}, applicationOptions: liveApplicationOptions(60000),
    businessFactory: context => {factories++; return createObservedBusiness(createFileBusiness, context, {capture: () => {}, authorize: () => {throw Error('permission_without_task');}});}};
  assert.deepEqual(config.applicationOptions.defaultLimits, taskBody('input-preflight', 60000).limits);
  for (const mode of ['create', 'open']) {
    service = await startTaskService({...config, mode});
    assert.equal(JSON.parse(fs.readFileSync(path.join(root, 'profile.json'))).layout, 7);
    assert.equal((await service.shutdown()).shutdownClean, true); service = null;
  }
  assert.equal(factories, 2); assert.equal(starts, 0);
});
test('fixed Node checker reads real bounded files and rejects plausible wrong total and wrong source', async t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-checker-')); t.after(() => fs.rmSync(root, {recursive: true}));
  const requested = verificationRequest(ticket(), () => encode(data));
  const frame = {profile: 'task-verification-command/v1', nonce: 'original-nonce', binding: {inputDigest: sha}, input: requested};
  const reports = reportFor('paid').reports;
  for (const report of reports) fs.writeFileSync(path.join(root, report.region + '.json'), encode(report), {mode: 0o600});
  const run = async () => {
    const child = spawn(process.execPath, [fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url))], {cwd: root, env: {}, timeout: 5000, stdio: ['pipe', 'pipe', 'pipe']});
    const chunks = []; child.stdout.on('data', chunk => chunks.push(chunk)); child.stderr.resume();
    const done = new Promise((resolve, reject) => {child.on('error', reject); child.on('close', (code, signal) => resolve({code, signal, bytes: Buffer.concat(chunks)}));});
    child.stdin.end(Buffer.concat([encode(frame), Buffer.from('\n')])); return done;
  };
  const first = await run(); assert.equal(first.code, 0); assert.equal(first.signal, null);
  assert.deepEqual(JSON.parse(first.bytes).assertions[0].actual, {report: reportFor('paid'), reply: reply()});
  fs.writeFileSync(path.join(root, 'west.json'), encode({...reports[1], netCents: 800})); assert.notEqual((await run()).code, 0);
  assert.throws(() => checkRequest({...frame, input: {...requested, source: {...requested.source, digest: sha}}}, name => fs.readFileSync(path.join(root, name))));
});
test('read-only report target serves exact bytes and rejects unknown names, symlinks and foreign origin', async t => {
  const parent = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'marshal-leader-report-')), root = path.join(parent, 'reports');
  fs.mkdirSync(root, {mode: 0o700}); t.after(() => fs.rmSync(parent, {recursive: true}));
  const bytes = encode(reportFor('paid')), name = `${taskId}-${digest(bytes).slice(7)}.json`;
  fs.writeFileSync(path.join(root, name), bytes, {mode: 0o600});
  const server = await startReportServer(root); t.after(() => server.close());
  assert.deepEqual(await consumePublished(server.url, name, Date.now() + 5000), bytes);
  for (const [url, options] of [[server.url + name, {method: 'POST'}], [server.url + '../private', {}],
    [server.url + name, {headers: {Origin: 'http://foreign.invalid'}}]]) assert.equal((await fetch(url, options)).status, 404);
  fs.unlinkSync(path.join(root, name)); fs.symlinkSync(path.join(parent, 'missing'), path.join(root, name));
  await assert.rejects(consumePublished(server.url, name, Date.now() + 5000));
});
