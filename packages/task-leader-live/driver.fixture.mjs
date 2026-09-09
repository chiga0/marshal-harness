// Manual opt-in only: real Pi, original Core and explicit local report target.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {parseOptions as parsePiOptions, filePermission} from '../task-pi-live/driver.fixture.mjs';
import {trackExecution} from '../task-qwen-live/driver.fixture.mjs';
import {data, choices, policy, taskBody, bindPlan, reportFor, consumeDelivery} from './scenario.fixture.mjs';
import {LiveError, check, equal, businessReply, verificationRequest, validatePlan, replyOnce, assertReplyReplay, verifyAcceptance, authorizeReport, authorOverlap} from './proof.fixture.mjs';
import {startReportServer, consumePublished} from './report-server.fixture.mjs';

const checkerPath = fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url));
export const reviewPolicy = Object.freeze({id: 'regional-requirements-review', version: '1',
  description: '独立检查原需求、完整原销售数据、用户真实回复与两份地区报告；检查零额计数、负数净额及逐项需求覆盖。不要求无意义润色，首轮正确即接受。'});
export const publicationPolicy = Object.freeze({profile: 'task-local-json-report/v1', id: 'regional-report', version: '1'});
export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const rest = [], own = {};
  for (let at = 0; at < argv.length; at++) {
    const key = argv[at];
    if (key === '--allow-local-publication') {check(!own.allow, 'invalid_arguments'); own.allow = true;}
    else if (['--answers', '--checkpoint'].includes(key)) {
      check(!Object.hasOwn(own, key), 'invalid_arguments'); own[key] = argv[++at];
    } else rest.push(key);
  }
  const answers = own['--answers']?.split(','), checkpoint = own['--checkpoint'] ?? 'complete';
  check(equal(answers, choices) && ['complete', 'first-workers'].includes(checkpoint), 'explicit_two_requirements_required');
  check(own.allow === true, 'explicit_local_publication_required');
  return {...parsePiOptions(rest), answers, checkpoint, allowLocalPublication: true};
}
function environment(node) {
  const env = {PATH: path.dirname(node) + ':' + (process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin')};
  for (const name of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (typeof process.env[name] === 'string' && !process.env[name].includes('\0')) env[name] = process.env[name];
  check(path.isAbsolute(env.HOME ?? ''), 'native_home_missing'); return env;
}
function save(root, name, bytes) {
  const fd = fs.openSync(path.join(root, name), fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  const dir = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try {fs.fsyncSync(dir);} finally {fs.closeSync(dir);}
}
const diagnosticEnum = (value, allowed) => allowed.includes(value) ? value : null;
const diagnosticId = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value) ? value : null;
const diagnosticTime = value => typeof value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(value) && Number.isFinite(Date.parse(value)) ? value : null;
function diagnosticCleanup(value) {
  if (!value) return null;
  const exit = original => !original ? null : {observed: original.observed === true, at: diagnosticTime(original.at),
    code: Number.isSafeInteger(original.code) && original.code >= 0 && original.code <= 255 ? original.code : null,
    signal: diagnosticEnum(original.signal, ['SIGTERM', 'SIGKILL', 'SIGINT', 'SIGABRT', 'SIGSEGV', 'SIGPIPE'])};
  return {cleaned: value.cleaned === true, executionId: diagnosticId(value.executionId ?? value.started?.executionId),
    startedAt: diagnosticTime(value.started?.startedAt), scope: diagnosticEnum(value.scope, ['inherited-process-group', 'unconfirmed']),
    reason: diagnosticEnum(value.reason, ['owner_stop', 'owner_lost', 'deadline', 'cleanup_unconfirmed', 'launch_failed']),
    agentExit: exit(value.agentExit), guardExit: exit(value.guardExit)};
}
/** Local failure forensics only, never Store input/cleanup authority or public
 * logs. Original received outputText may contain sensitive material: no
 * redaction/normalization is passed off as the original, no env/stderr is read. */
export function saveFailureDiagnostics(root, entries) {
  check(Array.isArray(entries) && entries.length <= 34, 'failure_diagnostics_limit'); // Two original 17-Attempt budgets.
  const stat = fs.lstatSync(root);
  check(stat.isDirectory() && !stat.isSymbolicLink() && (stat.mode & 0o077) === 0 &&
    (typeof process.getuid !== 'function' || stat.uid === process.getuid()) && fs.realpathSync(root) === root, 'failure_diagnostics_root');
  const directory = path.join(root, 'failure-diagnostics'); fs.mkdirSync(directory, {mode: 0o700});
  const reasons = ['pi_agent_stop', 'pi_agent_error', 'pi_agent_aborted', 'pi_agent_length', 'pi_agent_toolUse', 'pi_agent_deferred',
    'pi_provider_stopped', 'pi_provider_deadline', 'pi_provider_failed', 'pi_execution_scope_unproven', 'cleanup_unconfirmed',
    'pi_session_not_fresh', 'pi_bridge_not_ready', 'pi_invalid_terminal', 'pi_missing_terminal', 'pi_invalid_progress',
    'pi_progress_timeout', 'pi_progress_failed',
    'pi_bridge_binding_mismatch', 'pi_bridge_invalid_arguments', 'pi_bridge_invalid_call', 'pi_bridge_invalid_configuration',
    'pi_bridge_invalid_permission', 'pi_bridge_invalid_questions', 'pi_bridge_invalid_ready', 'pi_bridge_invalid_sdk',
    'pi_bridge_invalid_session', 'pi_bridge_invalid_shell', 'pi_bridge_invalid_tools', 'pi_bridge_invalid_truncated_calls',
    'pi_bridge_missing_configuration', 'pi_bridge_permission_denied', 'pi_bridge_question_tool_collision', 'pi_bridge_sdk_incompatible',
    'pi_bridge_shell_callback_failed', 'pi_bridge_shell_denied', 'pi_bridge_shell_input_failed', 'pi_bridge_shell_output_limit',
    'pi_bridge_shell_spawn_failed', 'pi_bridge_shell_stream_failed', 'pi_bridge_tool_capability_unavailable',
    'pi_bridge_unmatched_call', 'pi_bridge_unmatched_end', 'pi_bridge_unmatched_preparation', 'pi_bridge_user_shell_not_authorized',
    'pi_business_answer_ack_invalid', 'pi_business_answer_invalid', 'pi_business_answer_missing', 'pi_business_answer_unacknowledged',
    'pi_business_question_binding', 'pi_business_question_invalid', 'pi_permission_bridge_unavailable', 'pi_question_bridge_unavailable',
    'pi_invalid_configuration', 'pi_invalid_input', 'pi_tool_scope_unproven', 'pi_interaction_required', 'pi_shell_extra_scope',
    'pi_busy', 'pi_byte_stream_required', 'pi_callback_failed', 'pi_callback_timeout', 'pi_closed', 'pi_disconnected',
    'pi_extension_failed', 'pi_frame_limit', 'pi_interaction_failed', 'pi_interaction_limit', 'pi_invalid_content', 'pi_invalid_frame',
    'pi_invalid_interaction', 'pi_invalid_message', 'pi_invalid_options', 'pi_invalid_prompt', 'pi_invalid_state', 'pi_output_limit',
    'pi_pending_interaction', 'pi_prompt_timeout', 'pi_read_limit', 'pi_remote_rejected', 'pi_request_limit', 'pi_request_timeout',
    'pi_stream_error', 'pi_truncated_frame', 'pi_unapproved_queue', 'pi_unexpected_event', 'pi_unmatched_response',
    'pi_unsolicited_interaction', 'pi_write_failed', 'pi_write_limit',
    'agent_end_turn', 'agent_cancelled', 'agent_max_tokens', 'agent_max_turn_requests', 'agent_refusal', 'agent_spawn_failed',
    'provider_stopped', 'provider_deadline', 'provider_failed', 'provider_output_limit', 'provider_progress_failed',
    'provider_progress_limit', 'provider_progress_timeout', 'provider_invalid_configuration', 'provider_invalid_input',
    'provider_invalid_progress',
    'acp_already_initialized', 'acp_closed', 'acp_disconnected', 'acp_duplicate_peer_request', 'acp_frame_limit', 'acp_invalid_error',
    'acp_invalid_initialize', 'acp_invalid_initialize_result', 'acp_invalid_message', 'acp_invalid_options', 'acp_invalid_outbound',
    'acp_invalid_permission', 'acp_invalid_prompt', 'acp_invalid_prompt_result', 'acp_invalid_session', 'acp_invalid_session_result',
    'acp_invalid_update', 'acp_not_initialized', 'acp_peer_request_limit', 'acp_pending_limit', 'acp_read_limit', 'acp_request_limit',
    'acp_request_timeout', 'acp_session_busy', 'acp_session_creation_busy', 'acp_session_limit', 'acp_stream_error',
    'acp_unknown_response', 'acp_unknown_session', 'acp_unsupported_notification', 'acp_tool_scope_unproven',
    'acp_byte_stream_required', 'acp_callback_failed', 'acp_initialize_failed', 'acp_invalid_frame', 'acp_remote_error',
    'acp_truncated_frame', 'acp_write_failed', 'acp_write_limit',
    'runtime_byte_limit', 'runtime_client_factory_failed', 'runtime_config_limit', 'runtime_custody_failed',
    'runtime_guard_spawn_failed', 'runtime_invalid_callbacks', 'runtime_invalid_client_factory', 'runtime_invalid_context',
    'runtime_invalid_identity', 'runtime_invalid_input', 'runtime_invalid_options', 'runtime_launch_failed',
    'runtime_platform_unsupported',
    'custody_unavailable', 'custody_invalid_permit', 'custody_launch_denied', 'custody_client_invalid', 'custody_launch_failed',
    'custody_prepare_failed', 'custody_invalid_descriptor', 'custody_ack_conflict', 'custody_invalid_value', 'custody_limit',
    'custody_not_launched', 'custody_storage_limit', 'custody_storage_unavailable'];
  const items = entries.map((entry, index) => {
    const value = entry.result, output = value?.outputText;
    let code = entry.failed ? 'completion_rejected' : !entry.settled ? 'completion_unsettled' : 'output_unavailable', file = null, bytes = null, outputDigest = null;
    if (entry.settled && typeof output === 'string') {
      code = 'output_over_limit';
      if (output.length <= 65536) {
        bytes = Buffer.byteLength(output);
        if (bytes <= 65536) {
          file = `output-${String(index).padStart(2, '0')}.txt`; const original = Buffer.from(output);
          outputDigest = digest(original); save(directory, file, original); code = 'received_output_saved';
        }
      }
    }
    return {taskId: diagnosticId(entry.identity?.taskId), workerId: diagnosticId(entry.identity?.workerId),
      executionType: diagnosticEnum(entry.identity?.executionType, ['leader', 'review', 'agent', 'verification', 'publication', 'postverify']),
      executionId: diagnosticId(entry.started?.executionId), status: diagnosticEnum(value?.status, ['completed', 'failed', 'cancelled', 'unknown', 'passed', 'created', 'matched']),
      stopReason: diagnosticEnum(value?.stopReason, ['end_turn', 'cancelled', 'error', 'length', 'toolUse', 'deferred', 'max_tokens', 'max_turn_requests', 'refusal']),
      reason: diagnosticEnum(value?.reason, reasons), code, output: {file, bytes, digest: outputDigest}, cleanup: diagnosticCleanup(value?.cleanup)};
  });
  const metadata = {profile: 'leader-live-private-failure/v1', authority: false, mayContainSensitiveOutput: true, items};
  save(directory, 'metadata.json', encode(metadata));
  const fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try {fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  return {status: 'saved', path: 'failure-diagnostics/metadata.json', count: items.length, authority: false};
}
async function until(read, accept, deadline) {
  for (;;) {check(Date.now() < deadline, 'observation_deadline'); const result = await read(); if (accept(result)) return result; await pause(100);}
}
export function workPackage(ticket, prepared) {
  check(typeof prepared?.prompt === 'string' && Buffer.byteLength(prepared.prompt) <= 262144 &&
    prepared.prompt.includes(JSON.stringify(ticket.input.task)), 'work_package_task_missing');
  const required = ticket.executionType === 'leader' ? [ticket.input.leader] : ticket.executionType === 'review' ? [ticket.input.review] :
    [ticket.input.plan, ticket.input.node, ticket.input.leaderReplies];
  check(required.every(value => value !== undefined && prepared.prompt.includes(JSON.stringify(value))), 'work_package_context_missing');
  return {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role,
    executionType: ticket.executionType, promptDigest: digest(Buffer.from(prepared.prompt)), promptBytes: Buffer.byteLength(prepared.prompt),
    taskDigest: digest(encode(ticket.input.task)), inputDigest: ticket.inputDigest, reservationDigest: ticket.reservationDigest,
    originalTaskPresent: true, requiredContextPresent: true, observed: 'handed-off', agentConsumptionProven: false};
}
export function liveApplicationOptions(timeoutMs) {
  return {defaultLimits: {timeoutMs, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}};
}
export function managedPrompt(capture, render) {
  return ({ticket, input, prepared}) => {capture(ticket, prepared.cwd); return {prompt: render(input)};};
}
export function createObservedBusiness(createFileBusiness, context, {capture, authorize}) {
  const business = createFileBusiness({parent: context.executionParent, depot: context.depot, approvedLayout: context.approvedLayout,
    observeExecution: context.observeExecution, layoutFor: ticket => {
      // The original task-files allocation fixes this path. Observation does
      // not claim preparation/start succeeded; workPackage is checked at handoff.
      capture(ticket, path.join(context.executionParent, ticket.workerId));
      return ticket.input.fileLayout ?? {inputs: [], allowedPaths: []};
    }, authorize});
  check(typeof business.prepareManaged === 'function', 'managed_business_unavailable');
  // Core's private factory identity belongs to this exact object, not a spread.
  return business;
}
function terminalFact(entry) {
  const result = entry.result, cleanup = result?.cleanup, started = entry.started;
  check(entry.settled && !entry.failed && cleanup?.cleaned === true && cleanup.agentExit?.observed === true &&
    started?.executionId === cleanup.started?.executionId && cleanup.executionId === started.executionId &&
    Number.isFinite(Date.parse(started.startedAt)) && Number.isFinite(Date.parse(cleanup.agentExit.at)), 'original_cleanup_missing');
  return {...entry.identity, executionId: started.executionId, startedAt: started.startedAt, agentExitedAt: cleanup.agentExit.at,
    status: result.status, cleanup: true};
}
const healthy = task => {check(!['failed', 'cancelled', 'intervention', 'cancelling', 'paused'].includes(task.status), 'task_not_progressing'); return task;};

/** Real model entry. No injected fake Core/service path exists here. */
export async function runLive(options) {
  check(options?.executeReal === true && options.allowLocalPublication === true && equal(options.answers, choices) &&
    ['complete', 'first-workers'].includes(options.checkpoint), 'explicit_real_execution_required');
  check(process.versions.node === '24.15.0' && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  const packageRoot = path.resolve(path.dirname(options.piEntry), '../..');
  check(options.piEntry === path.join(packageRoot, 'dist/bundle/cli.js') && options.sdkEntry === path.join(packageRoot, 'dist/index.js') &&
    fs.realpathSync(options.piEntry) === options.piEntry && fs.realpathSync(options.sdkEntry) === options.sdkEntry, 'pi_entry_identity');
  const metadata = parseJson(fs.readFileSync(path.join(packageRoot, 'package.json')));
  check(metadata.name === '@earendil-works/pi-coding-agent' && typeof metadata.version === 'string', 'pi_entry_identity');
  check(fs.realpathSync(path.dirname(options.runDir)) === path.dirname(options.runDir), 'run_parent_identity');
  fs.mkdirSync(options.runDir, {mode: 0o700});
  const observed = [], byCwd = new Map(), byWorker = new Map(); let service, reports, publication, depot, stage = 'loading';
  const evidence = {profile: 'pi-managed-leader-live/v1', passed: false, fullDelivery: false, ordinaryUser: true, production: false,
    publisherSeparationProven: false, nodeVersion: process.versions.node, piVersion: metadata.version, tasks: [], workPackages: [],
    permission: {allowed: 0, denied: 0}, startedAt: new Date().toISOString(), checkerDigest: digest(fs.readFileSync(checkerPath)),
    piEntryDigest: digest(fs.readFileSync(options.piEntry)), sdkEntryDigest: digest(fs.readFileSync(options.sdkEntry))};
  try {
    // These imports deliberately require the real integrated candidate. Importing
    // this driver or running its pure tests never starts a service/model.
    const [{startTaskService}, {createPiProvider}, {createFileBusiness}, core, {createVerificationCommand}, pub] = await Promise.all([
      import('../task-service/composition.mjs'), import('../agent-provider-pi/index.mjs'), import('../task-business/index.mjs'),
      import('../task-application/application.mjs'), import('../task-verification-command/index.mjs'), import('../task-publication-report/index.mjs')]);
    check(['createLeaderPort', 'createReviewPort', 'renderLeaderPrompt', 'renderReviewPrompt', 'parseManagedOutput'].every(name =>
      typeof core[name] === 'function'), 'managed_core_unavailable');
    const root = path.join(options.runDir, 'data'), reportRoot = path.join(options.runDir, 'reports');
    fs.mkdirSync(reportRoot, {mode: 0o700}); reports = await startReportServer(reportRoot);
    const record = (identity, handle) => {observed.push(trackExecution(identity, handle)); return handle;};
    const identity = ticket => ({taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role,
      executionType: ticket.executionType, inputDigest: ticket.inputDigest, reservationDigest: ticket.reservationDigest, planDigest: ticket.planDigest});
    const capture = (ticket, cwd) => {byCwd.set(cwd, {ticket}); byWorker.set(ticket.workerId, {...identity(ticket), cwd});};
    const native = createPiProvider({id: 'pi', executable: options.node, args: [options.piEntry, '--mode', 'rpc', '--no-session'], env: environment(options.node),
      bridge: {sdkEntry: options.sdkEntry}, custodyProfile: {id: 'pi-native-file-v1', scope: 'inherited-process-group', eligible: true}});
    const provider = {...native, start(input) {
      const original = byCwd.get(input.cwd); check(original, 'missing_original_work_package');
      evidence.workPackages.push(workPackage(original.ticket, input)); return record(identity(original.ticket), native.start(input));
    }};
    const command = createVerificationCommand({executable: options.node, checkerPath, checkerDigest: evidence.checkerDigest,
      policyDigest: digest(encode(policy)), request: ({ticket}) => verificationRequest(ticket, source => depot.get({digest: source.digest, bytes: source.bytes})),
      assertions: [{name: 'leader-regions', validate: (actual, {ticket}) => {
        const reply = businessReply(ticket.taskId, ticket.input); return equal(actual, {report: reportFor(reply.answer), reply});
      }}], delivery: ({report}) => ({name: 'leader-regions.json', mediaType: 'application/json', content: encode(report.assertions[0].actual.report)})});
    const startChecker = input => record(identity(input.ticket), command.start(input));
    Object.defineProperty(startChecker, 'custodyProfile', {value: command.custodyProfile});
    const verification = core.createVerificationPort({id: 'leader-regions-checker', policy, bindPlan, start: startChecker,
      publicationExpected: ({ticket}) => reportFor(businessReply(ticket.taskId, ticket.input).answer)});
    const createPublication = () => {
      const original = pub.createLocalReportPublication({id: 'local-report', root: reportRoot, readBaseURL: reports.url, policy: publicationPolicy});
      return {...original, start: input => record(identity(input.ticket), original.start(input)),
        postverify: {...original.postverify, start: input => record(identity(input.ticket), original.postverify.start(input))}};
    };
    publication = createPublication();
    const leaderPolicy = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 1, maxRequests: 4,
      repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: provider.id, policyDigest: digest(encode(reviewPolicy))},
      publication: {targetId: publication.id, policyDigest: publication.policyDigest}};
    const leader = core.createLeaderPort({id: 'regional-leader', providerId: provider.id, policy: leaderPolicy,
      prepare: managedPrompt(capture, core.renderLeaderPrompt), parseDecision: core.parseManagedOutput});
    const review = core.createReviewPort({id: 'regional-review', providerId: provider.id, policy: reviewPolicy,
      prepare: managedPrompt(capture, core.renderReviewPrompt), parseReport: core.parseManagedOutput});
    const config = {root, providers: new Map([[provider.id, provider]]), leader, review, publication, verification,
      custody: {profile: 'node-execution-custody/v1'}, applicationOptions: liveApplicationOptions(options.timeoutMs), supervisorOptions: {intervalMs: 50},
      businessFactory: context => {
        depot = context.depot;
        return createObservedBusiness(createFileBusiness, context, {capture,
          authorize: (ticket, request) => {const result = filePermission(byWorker.get(ticket.workerId), request);
            evidence.permission[result.outcome.outcome === 'selected' ? 'allowed' : 'denied']++; return result;}});
      }};
    evidence.configuration = {leaderPolicyDigest: digest(encode(leaderPolicy)), reviewPolicyDigest: review.policyDigest,
      verificationPolicyDigest: digest(encode(policy)), publicationPolicyDigest: publication.policyDigest,
      publicationConfigurationDigest: publication.configurationDigest};
    const start = async mode => {
      if (mode === 'open') {
        publication.close(); publication = createPublication();
        check(publication.configurationDigest === evidence.configuration.publicationConfigurationDigest, 'publication_target_changed_on_open');
        config.publication = publication;
      }
      service = await startTaskService({...config, mode});
      check(parseJson(fs.readFileSync(path.join(root, 'profile.json'))).layout === 7, 'managed_profile_unavailable');
      const connection = parseJson(fs.readFileSync(service.connectionFile)); return new TaskClient({baseURL: connection.url, token: connection.token});};
    let client = await start('create');
    for (const [index, answer] of options.answers.entries()) {
      const prefix = 'leader-' + index, taskEvidence = {answer, passed: false}; evidence.tasks.push(taskEvidence); stage = prefix + '-intake';
      const input = await client.request('input.create', {idempotencyKey: prefix + '-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
      const body = taskBody(input.id, options.timeoutMs), created = await client.createTask(body, prefix + '-create'); taskEvidence.taskId = created.id;
      const end = Date.parse(created.deadlineAt), read = async () => ({task: healthy(await client.getTask(created.id)), view: await client.getLeader(created.id)});
      const question = await until(read, item => {
        check(item.task.plan === null && !['awaiting-approval', 'completed'].includes(item.task.status), 'leader_skipped_missing_requirement');
        return item.view.pendingRequest?.kind === 'business' && item.view.pendingRequest.status === 'pending';
      }, end);
      const answered = await replyOnce(client, question.task, question.view, answer, prefix + '-answer');
      taskEvidence.reply = answered.receipt; stage = prefix + '-approval';
      const ready = await until(read, item => item.task.status === 'awaiting-approval', end);
      const plan = await client.request('task.plan', {path: {taskId: created.id}}), approval = validatePlan(ready.task, plan, body, input.id, leaderPolicy);
      const operation = await client.approveTask(created.id, approval, prefix + '-approve'); taskEvidence.planDigest = plan.digest;
      if (options.checkpoint === 'first-workers') {
        stage = 'first-workers';
        const workers = await until(() => client.request('task.workers', {path: {taskId: created.id}}), page => page.items.filter(worker =>
          worker.role === 'author' && worker.status === 'running').length === 2, end);
        check(workers.nextCursor === null && evidence.workPackages.filter(item => item.taskId === created.id && item.role === 'author').length === 2, 'first_work_packages_missing');
        const current = await client.getTask(created.id), stop = await client.request('task.cancel', {path: {taskId: created.id}, idempotencyKey: prefix + '-checkpoint-stop', body: {expectedRevision: current.revision}});
        await until(() => client.getTask(created.id), task => task.status === 'cancelled', end);
        check((await client.request('operation.get', {path: {operationId: stop.id}})).status === 'succeeded', 'checkpoint_stop_unsettled');
        const entries = observed.filter(entry => entry.identity.taskId === created.id);
        await Promise.all(entries.map(entry => entry.handle.completion)); taskEvidence.executions = entries.map(terminalFact);
        taskEvidence.overlapMs = authorOverlap(taskEvidence.executions);
        check(taskEvidence.executions.filter(fact => fact.executionType === 'leader').length >= 2, 'leader_answer_not_revisited');
        evidence.checkpoint = {name: 'first-workers', passed: true, fullDelivery: false, taskId: created.id,
          originalCancelOperationId: stop.id, overlapMs: taskEvidence.overlapMs}; break;
      }
      stage = prefix + '-review-verification';
      const waiting = await until(read, item => {
        check(item.task.status !== 'completed', 'completed_without_publication');
        return item.view.pendingRequest?.kind === 'publication' && item.view.pendingRequest.status === 'pending';
      }, end);
      const auditBefore = await client.getAudit(created.id), a = waiting.view.pendingRequest.authorization;
      const delivery = await client.downloadArtifact(a.artifactId);
      const proofs = await Promise.all(auditBefore.acceptance.evidenceIds.map(id => client.downloadArtifact(id)));
      taskEvidence.verification = verifyAcceptance({taskId: created.id, planDigest: plan.digest, delivery, proofs, answered, answer});
      const authorization = authorizeReport({task: waiting.task, plan, view: waiting.view, audit: auditBefore,
        artifact: delivery.artifact, content: delivery.content, answer, publication, nameFor: pub.nameFor});
      // Persist the exact human-readable authorization before this one explicit
      // scenario-scoped allow. It does not authorize other tasks/targets/bytes.
      save(options.runDir, prefix + '-authorization.json', encode(authorization));
      const allowed = await replyOnce(client, waiting.task, waiting.view, 'allow', prefix + '-allow');
      taskEvidence.authorizationDigest = digest(encode(authorization)); taskEvidence.allow = allowed.receipt; stage = prefix + '-publication';
      const finished = await until(read, item => item.task.status === 'completed', end);
      const final = finished.view, audit = await client.getAudit(created.id);
      check(final.stage === 'terminal' && final.review?.verdict === 'accept' && final.review.digest === authorization.reviewDigest &&
        final.publication?.status === 'succeeded' && final.publication.authorizationDigest === taskEvidence.authorizationDigest &&
        final.postverify?.status === 'succeeded' && final.summaryArtifactId && final.lastDecision && audit.acceptance.status === 'passed', 'leader_closure_missing');
      const reviewReports = await Promise.all(final.review.evidenceIds.map(id => client.downloadArtifact(id)));
      check(reviewReports.length === 1 && reviewReports[0].artifact.taskId === created.id &&
        parseJson(reviewReports[0].content).report?.selectionDigest === final.review.selectionDigest &&
        parseJson(reviewReports[0].content).report?.verdict === 'accept', 'independent_review_report_missing');
      const summary = await client.downloadArtifact(final.summaryArtifactId), conclusion = parseJson(summary.content).report;
      check(summary.artifact.taskId === created.id && final.lastDecision.evidenceId === summary.artifact.id &&
        digest(encode(conclusion)) === final.lastDecision.digest && conclusion.actions?.some(action => action.type === 'conclude' && action.outcome === 'succeeded'), 'leader_summary_missing');
      const publicationReceipt = await client.downloadArtifact(final.publication.receiptArtifactId), postverifyEvidence = await client.downloadArtifact(final.postverify.evidenceArtifactId);
      check(publicationReceipt.artifact.taskId === created.id && postverifyEvidence.artifact.taskId === created.id, 'publication_receipt_missing');
      const currentDelivery = await client.downloadArtifact(delivery.artifact.id), published = await consumePublished(reports.url, authorization.name, end);
      check(equal(currentDelivery.artifact, delivery.artifact) && currentDelivery.content.equals(delivery.content) && published.equals(delivery.content), 'published_bytes_changed');
      taskEvidence.consumer = consumeDelivery(published, answer); save(options.runDir, prefix + '-report.json', published);
      const entries = observed.filter(entry => entry.identity.taskId === created.id);
      await Promise.all(entries.map(entry => entry.handle.completion)); const facts = entries.map(terminalFact);
      const workers = await client.request('task.workers', {path: {taskId: created.id}});
      check(workers.nextCursor === null && workers.items.length === audit.attempts && audit.attempts <= 17 && facts.every(fact =>
        workers.items.some(worker => worker.id === fact.workerId && worker.nodeId === fact.nodeId && worker.role === fact.role)) &&
        facts.filter(fact => fact.executionType === 'leader').length <= 9 && facts.some(fact => fact.workerId === final.review.workerId && fact.executionType === 'review') &&
        facts.some(fact => fact.executionType === 'verification') && facts.some(fact => fact.executionType === 'publication') &&
        facts.some(fact => fact.executionType === 'postverify'), 'execution_coverage_missing');
      check(facts.some(fact => fact.executionType === 'verification' && fact.executionId === taskEvidence.verification.executionId &&
        fact.inputDigest === taskEvidence.verification.inputDigest && fact.reservationDigest === taskEvidence.verification.reservationDigest), 'verification_original_execution_missing');
      taskEvidence.executions = facts; taskEvidence.overlapMs = authorOverlap(facts); taskEvidence.attempts = audit.attempts;
      taskEvidence.leaderCalls = facts.filter(fact => fact.executionType === 'leader').length;
      taskEvidence.reworkCount = audit.reworkCount; taskEvidence.firstpass = audit.reworkCount === 0;
      taskEvidence.review = final.review; taskEvidence.publication = final.publication; taskEvidence.postverify = final.postverify;
      taskEvidence.artifact = delivery.artifact; taskEvidence.usage = null;
      check((await client.request('supervisor.get')).activeWorkers === 0, 'capacity_not_released');
      const starts = observed.length; stage = prefix + '-cold-open';
      check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null; client = await start('open');
      check(equal(await client.createTask(body, prefix + '-create'), created) &&
        equal(await client.approveTask(created.id, approval, prefix + '-approve'), operation), 'original_receipt_changed');
      assertReplyReplay(answered, await client.request('task.leader.reply', answered.request));
      assertReplyReplay(allowed, await client.request('task.leader.reply', allowed.request));
      check(equal(await client.getTask(created.id), finished.task) && equal(await client.getLeader(created.id), final) &&
        equal((await client.request('task.workers', {path: {taskId: created.id}})).items, workers.items) &&
        (await client.downloadArtifact(delivery.artifact.id)).content.equals(delivery.content) && observed.length === starts &&
        (await client.request('supervisor.get')).activeWorkers === 0, 'cold_open_changed_or_redispatched');
      taskEvidence.restart = {sameReceipts: true, sameDelivery: true, sameLeader: true, duplicateStarts: 0}; taskEvidence.passed = true;
    }
    if (options.checkpoint === 'complete') {
      check(evidence.tasks.length === 2 && evidence.tasks.every(task => task.passed) && evidence.tasks[0].taskId !== evidence.tasks[1].taskId &&
        evidence.tasks[0].consumer.digest !== evidence.tasks[1].consumer.digest, 'two_requirements_not_proven');
      evidence.sameConfiguration = true; evidence.passed = true; evidence.fullDelivery = true;
    }
    stage = 'complete';
  } catch (error) {evidence.failure = {stage, code: error instanceof LiveError ? error.code : 'leader_live_dependency_failed'};}
  finally {
    if (service) try {check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed');}
    catch {evidence.passed = false; evidence.checkpoint = null; evidence.failure = {stage: 'shutdown', code: 'shutdown_unconfirmed'};}
    for (const entry of observed) try {const result = await entry.handle.stop(); check(result?.cleanup?.cleaned === true, 'shutdown_unconfirmed');}
    catch {evidence.passed = false; evidence.checkpoint = null; evidence.failure = {stage: 'cleanup', code: 'shutdown_unconfirmed'};}
    try {publication?.close();} catch {evidence.passed = false; evidence.checkpoint = null;}
    try {await reports?.close();} catch {evidence.passed = false; evidence.checkpoint = null;}
    if (!evidence.passed && !evidence.checkpoint?.passed) {
      try {evidence.failureDiagnostics = saveFailureDiagnostics(options.runDir, observed);}
      catch {evidence.failureDiagnostics = {status: 'unavailable', code: 'failure_diagnostics_write_failed', authority: false};}
    }
    if (!evidence.passed) evidence.fullDelivery = false;
    evidence.finishedAt = new Date().toISOString(); save(options.runDir, 'evidence.json', encode(evidence));
  }
  return evidence;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('--execute-real --answers paid,cancelled --allow-local-publication --run-dir ABS_NEW_PRIVATE_DIR --node ABS_NODE_24_15 --pi-entry ABS_dist/bundle/cli.js --pi-sdk ABS_dist/index.js [--checkpoint complete|first-workers] [--timeout-ms 600000]\n两个不同需求、同配置；显式 first-workers 仅首Task首次执行后原HTTP取消，不是整链。默认不调用模型。\n');
    else {const result = await runLive(options); process.stdout.write(JSON.stringify({passed: result.passed, fullDelivery: result.fullDelivery,
      checkpoint: result.checkpoint ?? null, failure: result.failure ?? null, evidence: path.join(options.runDir, 'evidence.json')}) + '\n');
      if (!result.passed && !result.checkpoint?.passed) process.exitCode = 1;}
  } catch (error) {process.stderr.write(JSON.stringify({code: error instanceof LiveError ? error.code : 'leader_live_preflight_failed'}) + '\n'); process.exitCode = 1;}
}
