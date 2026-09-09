// Explicit real-model acceptance tool. Not distributed and never run by default CI.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {parseOptions as parsePiOptions, filePermission} from '../task-pi-live/driver.fixture.mjs';
import {executionFact, assertTeam, trackExecution, cancelActiveTeam, DriverError} from '../task-qwen-live/driver.fixture.mjs';
import {data, choices, policy, questionPolicyDigest, questions, taskBody, bindPlan, expectedReports, answerFromRefs, consumeDelivery, equal, verificationRequest} from './scenario.fixture.mjs';
import {cancelPiWorker, workerCancellation, readRetainedResult} from './worker-cancel.fixture.mjs';

const checkerPath = fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url));
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
export class LiveError extends Error {constructor(code) {super(code); this.code = code;}}
const check = (value, code) => {if (!value) throw new LiveError(code);};
export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const scenarios = argv.flatMap((value, index) => value === '--scenario' ? [index] : []);
  check(scenarios.length <= 1, 'invalid_scenario');
  const scenario = scenarios.length ? argv[scenarios[0] + 1] : 'question';
  check(['question', 'cancel', 'worker-cancel'].includes(scenario), 'invalid_scenario');
  if (scenarios.length) argv = [...argv.slice(0, scenarios[0]), ...argv.slice(scenarios[0] + 2)];
  const indexes = argv.flatMap((value, index) => value === '--answer' ? [index] : []);
  if (scenario !== 'question') {
    check(indexes.length === 0, scenario === 'cancel' ? 'task_cancel_does_not_take_business_answer' : 'worker_cancel_does_not_take_business_answer');
    const parsed = parsePiOptions(argv);
    check(scenario !== 'worker-cancel' || parsed.timeoutMs <= 600000, 'worker_cancel_budget_limit');
    return {...parsed, scenario};
  }
  check(indexes.length === 1 && choices.includes(argv[indexes[0] + 1]), 'explicit_business_answer_required');
  const at = indexes[0], answer = argv[at + 1];
  return {...parsePiOptions([...argv.slice(0, at), ...argv.slice(at + 2)]), answer, scenario};
}
export async function cancelPiActiveTeam(options) {
  try {return await cancelActiveTeam({...options, expectedStopReason: 'pi_provider_stopped', idempotencyKey: 'pi-runtime-cancel-task'});}
  catch (error) {if (error instanceof DriverError) throw new LiveError(error.code); throw error;}
}
export function validatePlan(task, plan, body, inputId) {
  check(task.status === 'awaiting-approval' && plan.taskId === task.id && plan.revision === task.plan?.revision && plan.digest === task.plan.digest, 'plan_identity_mismatch');
  check(plan.nodes?.length === 3 && plan.nodes.every((node, at) => node.id === ['east', 'west', 'verify'][at] &&
    node.role === (at === 2 ? 'verifier' : 'author') && node.providerId === null && text(node.goal) && node.goal.trim()), 'plan_nodes_mismatch');
  check(equal(plan.edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]) && equal(plan.deliverables, ['east.json', 'west.json']) &&
    equal(plan.budget, body.limits) && equal(plan.assumptions, []) && equal(plan.interaction,
      {profile: 'task-runtime-question/v1', policyDigest: questionPolicyDigest, maxQuestions: 1, maxWaitMs: 120000}), 'plan_scope_mismatch');
  const binding = bindPlan({inputArtifacts: [{id: inputId}], proposal: plan});
  const visible = [{policy, description: binding.description}, ...binding.layouts.sort((a, b) => a.nodeId.localeCompare(b.nodeId)).map(layout => ({layout})),
    ...binding.deliveries.sort((a, b) => a.targetPath.localeCompare(b.targetPath)).map(delivery => ({delivery}))];
  let actual; try {actual = plan.acceptance.slice(-visible.length).map(value => parseJson(Buffer.from(value)));} catch {throw new LiveError('plan_acceptance_missing');}
  check(equal(actual, visible), 'plan_acceptance_missing');
  return {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
}
export async function answerOnce(client, task, question, answer) {
  check(choices.includes(answer) && question.taskId === task.id && question.kind === 'business' && question.nodeId === 'east' &&
    question.revision === 1 && question.subject === question.questionDigest && question.status === 'open' && question.deliveryStatus === null &&
    equal(question.options?.map(option => option.value), choices) && Date.now() < Date.parse(question.deadlineAt), 'question_binding_mismatch');
  const request = {path: {taskId: task.id, questionId: question.id}, idempotencyKey: 'runtime-business-answer',
    body: {expectedRevision: task.revision, questionDigest: question.questionDigest, questionRevision: 1, answer}};
  // Exactly one write; CAS conflict or a lost response is evidence, not a retry.
  return {request, receipt: await client.request('task.answer', request)};
}
function save(root, name, bytes) {
  const fd = fs.openSync(path.join(root, name), fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  const directory = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try {fs.fsyncSync(directory);} finally {fs.closeSync(directory);}
}
async function until(read, accept, deadline) {
  for (;;) {const value = await read(); if (accept(value)) return value; check(Date.now() < deadline, 'observation_deadline'); await pause(100);}
}
function environment(node) {
  const env = {PATH: path.dirname(node) + ':' + (process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin')};
  for (const key of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (text(process.env[key])) env[key] = process.env[key];
  check(path.isAbsolute(env.HOME ?? ''), 'native_home_missing'); return env;
}
export async function runLive(options) {
  const scenario = options?.scenario ?? 'question';
  check(options?.executeReal === true && ['question', 'cancel', 'worker-cancel'].includes(scenario) &&
    (scenario !== 'question' ? options.answer === undefined : choices.includes(options.answer)), 'explicit_real_execution_required');
  check(scenario !== 'worker-cancel' || Number.isSafeInteger(options.timeoutMs) && options.timeoutMs > 0 && options.timeoutMs <= 600000, 'worker_cancel_budget_limit');
  check(process.versions.node === '24.15.0' && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  const packageRoot = path.resolve(path.dirname(options.piEntry), '../..');
  check(options.piEntry === path.join(packageRoot, 'dist/bundle/cli.js') && options.sdkEntry === path.join(packageRoot, 'dist/index.js') &&
    fs.realpathSync(options.piEntry) === options.piEntry && fs.realpathSync(options.sdkEntry) === options.sdkEntry, 'pi_entry_identity');
  const metadata = JSON.parse(fs.readFileSync(path.join(packageRoot, 'package.json')));
  check(metadata.name === '@earendil-works/pi-coding-agent' && text(metadata.version), 'pi_entry_identity');
  check(fs.realpathSync(path.dirname(options.runDir)) === path.dirname(options.runDir), 'run_parent_identity');
  fs.mkdirSync(options.runDir, {mode: 0o700});
  const observed = [], byCwd = new Map(), byWorker = new Map(); let service, verifierStarts = 0, stage = 'starting';
  const evidence = {profile: 'pi-runtime-question-live/v1', passed: false, ordinaryUser: true, production: false, publisherSeparationProven: false,
    ...(scenario !== 'question' ? {scenario, custodyProfile: 'node-execution-custody/v1', providerCustodyProfile: 'pi-native-file-question-v1'} : {}),
    ...(scenario === 'worker-cancel' ? {workerCancellationProfile: workerCancellation.profile, layout: 6, unpermitted: false} : {}),
    startedAt: new Date().toISOString(), piVersion: metadata.version, nodeVersion: process.versions.node,
    piEntryDigest: digest(fs.readFileSync(options.piEntry)), sdkEntryDigest: digest(fs.readFileSync(options.sdkEntry)),
    checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: questionPolicyDigest, answer: options.answer ?? null, permission: {allowed: 0, denied: 0}, executions: []};
  try {
    const native = createPiProvider({id: 'pi', executable: options.node, args: [options.piEntry, '--mode', 'rpc', '--no-session'], env: environment(options.node),
      bridge: {sdkEntry: options.sdkEntry}, custodyProfile: {id: 'pi-native-file-question-v1', scope: 'inherited-process-group', eligible: true}});
    const provider = {...native, start(input) {const identity = byCwd.get(input.cwd); check(identity, 'missing_original_identity');
      const handle = native.start(input); observed.push(trackExecution(identity, handle)); return handle;}};
    const command = createVerificationCommand({executable: options.node, checkerPath, checkerDigest: evidence.checkerDigest, policyDigest: digest(encode(policy)), request: verificationRequest,
      assertions: [{name: 'answered-regions', validate: (actual, {ticket}) => {
        const refs = ticket.input.interactionRefs, answer = answerFromRefs(refs, ticket.input.verification, ticket.planDigest);
        return equal(actual, {answer, reports: expectedReports(answer), questionDigest: refs[0].questionDigest, answerDigest: refs[0].answerDigest});
      }}], delivery: ({report}) => ({name: 'answered-regions.json', mediaType: 'application/json', content: encode(report.assertions[0].actual)})});
    const startChecker = input => {verifierStarts++; return command.start(input);};
    Object.defineProperty(startChecker, 'custodyProfile', {value: command.custodyProfile});
    const verification = createVerificationPort({id: 'independent-regional-checker', policy, bindPlan,
      interactionPolicyDigests: [questionPolicyDigest], start: startChecker});
    const config = {root: path.join(options.runDir, 'data'), providers: new Map([[provider.id, provider]]), verification, runtimeQuestions: questions(),
      custody: {profile: 'node-execution-custody/v1'}, supervisorOptions: {intervalMs: 50},
      ...(scenario === 'worker-cancel' ? {workerCancellation} : {}),
      businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
        const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          authorize: (ticket, request) => {const answer = filePermission(byWorker.get(ticket.workerId), request);
            evidence.permission[answer.outcome.outcome === 'selected' ? 'allowed' : 'denied']++; return answer;}});
        return {...business, async prepare(ticket, context) {const prepared = await business.prepare(ticket, context);
          const identity = {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role};
          byCwd.set(prepared.cwd, identity); byWorker.set(ticket.workerId, {...identity, cwd: prepared.cwd}); return prepared;}};
      }};
    const start = async mode => {service = await startTaskService({...config, mode});
      if (scenario === 'worker-cancel') check(JSON.parse(fs.readFileSync(path.join(config.root, 'profile.json'))).layout === 6, 'worker_cancel_profile_unavailable');
      const connection = JSON.parse(fs.readFileSync(service.connectionFile));
      return new TaskClient({baseURL: connection.url, token: connection.token});};
    let client = await start('create'); stage = 'planning';
    const input = await client.request('input.create', {idempotencyKey: 'runtime-sales', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const body = taskBody(input.id, options.timeoutMs), created = await client.createTask(body, 'runtime-create'); evidence.taskId = created.id;
    const deadline = Date.parse(created.deadlineAt);
    const task = await until(() => client.getTask(created.id), value => {
      check(!['failed', 'cancelled', 'intervention'].includes(value.status), 'planning_failed'); return value.status === 'awaiting-approval';}, deadline);
    const plan = await client.request('task.plan', {path: {taskId: task.id}}), approval = validatePlan(task, plan, body, input.id);
    evidence.planDigest = plan.digest; const operation = await client.approveTask(task.id, approval, 'runtime-approve'); stage = 'waiting-question';
    if (scenario === 'worker-cancel') {
      stage = 'cancelling-worker';
      const cancelled = await cancelPiWorker({client, taskId: task.id, observations: observed, getVerifierStarts: () => verifierStarts, end: deadline});
      evidence.executions = cancelled.executions; evidence.overlapMs = cancelled.overlapMs; evidence.verifierStarts = verifierStarts;
      evidence.workers = cancelled.workers.map(worker => ({id: worker.id, nodeId: worker.nodeId, role: worker.role, status: worker.status}));
      evidence.workerCancel = {requestedAt: cancelled.requestedAt, confirmedAt: new Date().toISOString(), operationId: cancelled.operation.id,
        workerId: cancelled.operation.workerId, taskId: task.id, operationStatus: cancelled.terminalOperation.status,
        taskStatus: cancelled.done.status, taskCode: cancelled.done.code, noVerifier: true, noDelivery: true,
        modelConsumptionProven: false, toolExecutionProven: false};
      evidence.artifacts = [];
      check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
      const expected = {taskId: task.id, workerId: cancelled.siblingWorkerId, planDigest: plan.digest,
        executionId: cancelled.executions.find(entry => entry.workerId === cancelled.siblingWorkerId).executionId};
      const retained = readRetainedResult(config.root, expected);
      // This is an unaccepted sibling candidate, NOT a Task delivery or Decision.
      evidence.retainedSibling = {...retained, independentAcceptance: false, taskDelivery: false};
      stage = 'reopening'; client = await start('open');
      check(equal(await client.createTask(body, 'runtime-create'), created) && equal(await client.approveTask(task.id, approval, 'runtime-approve'), operation), 'original_receipt_changed');
      check(equal(await client.request('worker.cancel', cancelled.request), cancelled.operation), 'worker_cancel_receipt_changed');
      check(equal(await client.getTask(task.id), cancelled.done) && equal(await client.request('operation.get',
        {path: {operationId: cancelled.operation.id}}), cancelled.terminalOperation), 'worker_cancel_changed_after_restart');
      check(equal((await client.request('task.workers', {path: {taskId: task.id}})).items, cancelled.workers) &&
        equal(await client.request('task.graph', {path: {taskId: task.id}}), cancelled.graph) && observed.length === 3 && verifierStarts === 0 &&
        (await client.request('supervisor.get')).activeWorkers === 0, 'reopen_drift_or_duplicate');
      check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
      check(equal(readRetainedResult(config.root, expected), retained), 'worker_cancel_retained_result_changed');
      evidence.restart = {originalReceipts: true, sameFailedTask: true, sameRetainedResult: true, duplicateStarts: 0};
      evidence.passed = true; stage = 'complete'; return evidence;
    }
    if (scenario === 'cancel') {
      stage = 'cancelling';
      const cancelled = await cancelPiActiveTeam({client, taskId: task.id, observations: observed, getVerifierStarts: () => verifierStarts, end: deadline});
      evidence.executions = cancelled.executions; evidence.overlapMs = assertTeam(cancelled.executions); evidence.verifierStarts = verifierStarts;
      evidence.workers = cancelled.workers.map(worker => ({id: worker.id, nodeId: worker.nodeId, role: worker.role, status: worker.status}));
      const audit = await client.getAudit(task.id);
      check(audit.attempts === 3 && audit.retryCount === 0 && audit.reworkCount === 0 && audit.acceptance.status !== 'passed', 'cancel_unexpected_acceptance');
      check((await client.request('supervisor.get')).activeWorkers === 0, 'cancel_capacity_unsettled');
      evidence.cancel = {requestedAt: cancelled.requestedAt, confirmedAt: new Date().toISOString(), operationId: cancelled.operation.id,
        originalAuthorsStopped: true, noVerifier: true, noDelivery: true, modelConsumptionProven: false, toolExecutionProven: false};
      evidence.artifacts = [];
      check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null; stage = 'reopening'; client = await start('open');
      check(equal(await client.createTask(body, 'runtime-create'), created) && equal(await client.approveTask(task.id, approval, 'runtime-approve'), operation), 'original_receipt_changed');
      // Exact old-key receipt replay is readback of the accepted command, not
      // another cancellation attempt or a new revision/key after a conflict.
      check(equal(await client.request('task.cancel', cancelled.request), cancelled.operation), 'cancel_receipt_changed');
      check(equal(await client.getTask(task.id), cancelled.done), 'task_changed_after_restart');
      check((await client.request('operation.get', {path: {operationId: cancelled.operation.id}})).status === 'succeeded', 'cancel_not_reconciled');
      check(equal((await client.request('task.workers', {path: {taskId: task.id}})).items, cancelled.workers) &&
        observed.length === 3 && verifierStarts === 0 && (await client.request('supervisor.get')).activeWorkers === 0, 'reopen_drift_or_duplicate');
      evidence.restart = {originalReceipts: true, sameCancelledTask: true, duplicateStarts: 0}; evidence.passed = true; stage = 'complete';
      return evidence;
    }
    const list = await until(async () => {
      const current = await client.getTask(task.id); check(!['failed', 'cancelled', 'completed', 'intervention'].includes(current.status), 'missing_business_question');
      return client.request('task.questions', {path: {taskId: task.id}});
    }, value => value.items.length > 0, deadline);
    check(list.items.length === 1 && list.nextCursor === null, 'unexpected_question_count'); const q = list.items[0];
    const waiting = await client.request('worker.get', {path: {workerId: q.workerId}}); check(waiting.status === 'awaiting-answer', 'worker_not_waiting');
    await until(async () => {const workers = await client.request('task.workers', {path: {taskId: task.id}});
      check(!workers.items.some(worker => ['failed', 'unknown', 'cancelled'].includes(worker.status)), 'parallel_worker_failed'); return workers;
    }, value => value.items.some(worker => worker.nodeId === 'west' && worker.status === 'completed'), Math.min(deadline, Date.parse(q.deadlineAt)));
    stage = 'answering'; const current = await client.getTask(task.id), answered = await answerOnce(client, current, q, options.answer);
    evidence.question = {id: q.id, workerId: q.workerId, questionDigest: q.questionDigest, waitingStartedAt: waiting.startedAt, attempt: waiting.attempt,
      independentAuthorCompletedBeforeAnswer: true}; stage = 'verifying';
    const done = await until(() => client.getTask(task.id), value => {
      check(!['failed', 'cancelled', 'intervention'].includes(value.status), 'team_failed'); return value.status === 'completed';}, deadline);
    const finalQ = (await client.request('task.questions', {path: {taskId: task.id}})).items[0];
    const finalWorker = await client.request('worker.get', {path: {workerId: q.workerId}});
    check(finalQ.deliveryStatus === 'acknowledged' && finalQ.answer === options.answer && finalWorker.startedAt === waiting.startedAt && finalWorker.attempt === waiting.attempt,
      'same_execution_answer_missing');
    check((await client.request('operation.get', {path: {operationId: answered.receipt.operation.id}})).status === 'succeeded', 'answer_not_acknowledged');
    const audit = await client.request('task.audit', {path: {taskId: task.id}});
    check(audit.acceptance.status === 'passed' && audit.attempts === 4 && verifierStarts === 1, 'independent_acceptance_missing');
    evidence.executions = await Promise.all(observed.map(async ({identity, handle}) => executionFact(identity, await handle.completion)));
    evidence.overlapMs = assertTeam(evidence.executions); evidence.verifierStarts = verifierStarts;
    const artifacts = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id))), delivery = artifacts.filter(item => item.artifact.kind === 'delivery');
    check(delivery.length === 1 && delivery[0].artifact.taskId === task.id, 'delivery_missing');
    const proofArtifacts = artifacts.filter(item => item.artifact.kind === 'evidence'); check(proofArtifacts.length === 1, 'verification_evidence_missing');
    const proof = parseJson(proofArtifacts[0].content), assertion = proof.assertions?.find(item => item.name === 'answered-regions');
    check(proof.binding?.planDigest === plan.digest && proof.delivery?.digest === delivery[0].artifact.digest &&
      proof.delivery?.bytes === delivery[0].artifact.bytes && assertion?.actual?.questionDigest === q.questionDigest &&
      /^sha256:[a-f0-9]{64}$/.test(assertion.actual.answerDigest ?? ''), 'verification_delivery_binding');
    evidence.consumer = consumeDelivery(delivery[0].content, options.answer, q.questionDigest, assertion.actual.answerDigest);
    save(options.runDir, 'answered-regions.json', delivery[0].content);
    evidence.artifacts = artifacts.map(({artifact}) => ({id: artifact.id, kind: artifact.kind, digest: artifact.digest, bytes: artifact.bytes}));
    check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null; stage = 'reopening'; client = await start('open');
    check(equal(await client.createTask(body, 'runtime-create'), created) && equal(await client.approveTask(task.id, approval, 'runtime-approve'), operation), 'original_receipt_changed');
    const replay = await client.request('task.answer', answered.request);
    check(replay.replayed === true && replay.deliveryStatus === 'pending' && equal(replay.operation, answered.receipt.operation) &&
      replay.questionDigest === q.questionDigest && equal(await client.getTask(task.id), done), 'answer_receipt_changed');
    const reopened = await client.downloadArtifact(delivery[0].artifact.id);
    check(equal(reopened.artifact, delivery[0].artifact) && reopened.content.equals(delivery[0].content) && observed.length === 3 && verifierStarts === 1, 'reopen_drift_or_duplicate');
    evidence.restart = {originalReceipts: true, sameArtifact: true, duplicateStarts: 0}; evidence.passed = true; stage = 'complete';
  } catch (error) {evidence.failure = {stage, code: error instanceof LiveError || error instanceof DriverError ? error.code : 'runtime_live_dependency_failed'};}
  finally {
    if (service) try {check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed');} catch {evidence.passed = false; evidence.failure = {stage: 'shutdown', code: 'shutdown_unconfirmed'};}
    for (const {identity, handle} of observed) try {const result = await handle.stop(); if (!result?.cleanup?.cleaned) evidence.passed = false;
      if (!evidence.executions.some(value => value.workerId === identity.workerId)) evidence.executions.push({...identity, status: result?.status ?? 'unknown',
        cleanup: result?.cleanup?.cleaned === true, executionId: result?.cleanup?.started?.executionId ?? null});
    } catch {evidence.passed = false;}
    evidence.finishedAt = new Date().toISOString(); save(options.runDir, 'evidence.json', encode(evidence));
  }
  return evidence;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('--execute-real [--scenario question --answer paid|cancelled | --scenario cancel | --scenario worker-cancel] --run-dir ABS_NEW_PRIVATE_DIR --node ABS_NODE_24_15 --pi-entry ABS_dist/bundle/cli.js --pi-sdk ABS_dist/index.js [--timeout-ms 600000]\n默认 question；两种 cancel 不接受 --answer。一次 Task/批准/答案或取消，无重试；默认不启动模型。\n');
    else {const result = await runLive(options); process.stdout.write(JSON.stringify({passed: result.passed, taskId: result.taskId ?? null,
      failure: result.failure ?? null, evidence: path.join(options.runDir, 'evidence.json')}) + '\n'); if (!result.passed) process.exitCode = 1;}
  } catch (error) {process.stderr.write(JSON.stringify({code: error instanceof LiveError ? error.code : 'runtime_live_preflight_failed'}) + '\n'); process.exitCode = 1;}
}
