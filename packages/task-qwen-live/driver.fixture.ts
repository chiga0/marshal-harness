import {supportsNode} from '../task-store/runtime.mjs';
// Explicit, test-only real-model driver. Never a default service configuration.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';

const checkerPath = fileURLToPath(new URL('../task-team-integration/checker.fixture.mjs', import.meta.url));
const deny = () => ({outcome: {outcome: 'cancelled'}});
// HTTP parseJson returns null-prototype objects. Compare complete canonical
// data, not JS prototypes or only a status field.
const equal = (left, right) => { try { return encode(left).equals(encode(right)); } catch { return false; } };
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const check = (condition, code) => { if (!condition) throw new DriverError(code); };
const fields = (value, required, optional = []) => object(value) && required.every(key => Object.hasOwn(value, key)) &&
  Object.keys(value).every(key => required.includes(key) || optional.includes(key));
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
export class DriverError extends Error { constructor(code) { super(code); this.name = 'DriverError'; this.code = code; } }
export const data = Object.freeze({rows: Object.freeze([
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
].map(Object.freeze))});
// Independent expected values, never supplied to a model as its answer.
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
const acceptance = '只统计 paid，排除 cancelled；零额计入笔数，负数退款计入净额；整数 cents，不转换为浮点金额。';
export function taskBody(inputId, timeoutMs) {
  return {intent: '两个真实作者并行分析公开合成销售数据，输出两个地区报告，并经独立检查器集成为可下载报告。',
    context: {inputRefs: [inputId], text: '这是一项受限的实机验收业务，不是真实客户数据。请规划且仅规划三个节点，按 east、west、verify 顺序：' +
      'east 和 west 的 role=author，独立读取 sales.json，仅生成各自 east.json 或 west.json；verify 的 role=verifier，' +
      '由已配置的独立检查器执行。仅两条边 east→verify、west→verify，没有作者间依赖。每节点 providerId=null。' +
      '作者输出单个 JSON 对象，恰有 region、count、netCents 三字段。region 是自身地区；count 为有效笔数；netCents 为整数净额。' +
      '禁止 shell、外部发布和额外文件，仅用原生读写/编辑文件工具；未知权限或真实问题不可自动通过。' +
      '不预设计算结果。计划 deliverables 恰为 east.json、west.json，assumptions=[]，预算沿用任务限制。' + acceptance},
    requirements: {deliverables: ['east.json', 'west.json'], acceptance: [acceptance]},
    limits: {timeoutMs, maxAttempts: 4, maxWorkers: 2}};
}

export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const values = {}; let execute = false;
  for (let index = 0; index < argv.length; index++) {
    const name = argv[index];
    if (name === '--execute-real' && !execute) { execute = true; continue; }
    check(['--run-dir', '--node', '--qwen-entry', '--timeout-ms', '--scenario'].includes(name) && !Object.hasOwn(values, name), 'invalid_arguments');
    const value = argv[++index]; check(text(value) && value && !value.startsWith('--'), 'invalid_arguments'); values[name] = value;
  }
  check(execute, 'explicit_real_execution_required');
  const timeoutMs = values['--timeout-ms'] === undefined ? 600000 : Number(values['--timeout-ms']);
  check(Number.isSafeInteger(timeoutMs) && timeoutMs >= 60000 && timeoutMs <= 900000, 'invalid_timeout');
  const scenario = values['--scenario'] ?? 'team';
  check(['team', 'cancel'].includes(scenario), 'invalid_scenario');
  for (const key of ['--run-dir', '--node', '--qwen-entry']) check(text(values[key]) && path.isAbsolute(values[key]) &&
    path.normalize(values[key]) === values[key] && values[key] !== path.parse(values[key]).root, 'invalid_path');
  return {runDir: values['--run-dir'], node: values['--node'], qwenEntry: values['--qwen-entry'], timeoutMs, scenario};
}

export function validatePlan(task, plan, limits, inputId) {
  check(task.status === 'awaiting-approval' && plan.taskId === task.id && plan.revision === task.plan?.revision &&
    plan.digest === task.plan.digest, 'plan_identity_mismatch');
  check(Array.isArray(plan.nodes) && plan.nodes.length === 3 && plan.nodes.every((node, index) =>
    node.id === ['east', 'west', 'verify'][index] && node.role === (index === 2 ? 'verifier' : 'author') &&
    node.providerId === null && text(node.goal) && node.goal.trim()), 'unexpected_plan_nodes');
  check(equal(plan.edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]) &&
    equal(plan.deliverables, ['east.json', 'west.json']) && equal(plan.budget, limits) && equal(plan.assumptions, []), 'unexpected_plan_contract');
  check(typeof inputId === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(inputId), 'missing_business_input');
  // Core appends these deterministic policy/layout/delivery projections AFTER
  // the planner's prose. Match the complete suffix, including the actual input
  // reference, rather than asking an LLM to reproduce a particular sentence or
  // accepting a policy ID mentioned somewhere in untrusted prose.
  const binding = bindPlan({inputArtifacts: [{id: inputId}], proposal: plan});
  const visible = [{policy, description: binding.description},
    ...binding.layouts.sort((a, b) => a.nodeId < b.nodeId ? -1 : 1).map(layout => ({layout})),
    ...binding.deliveries.sort((a, b) => a.targetPath < b.targetPath ? -1 : 1).map(delivery => ({delivery}))];
  let projected;
  try { projected = plan.acceptance.slice(-visible.length).map(value => parseJson(Buffer.from(value))); }
  catch { throw new DriverError('missing_business_acceptance'); }
  // Full JSON values, rejecting duplicate keys; insignificant member order is
  // not a contract, while every policy/layout/delivery field is.
  check(equal(projected, visible), 'missing_business_acceptance');
  return {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
}

/** This authorizes only an offered, one-time bounded file operation. It is not
 * an OS sandbox and cannot revoke permissions already granted in native config. */
export function filePermission({cwd, role, nodeId}, request) {
  if (role !== 'author' || !['east', 'west'].includes(nodeId) || !path.isAbsolute(cwd ?? '') ||
    !object(request?.toolCall) || !Array.isArray(request.options)) return deny();
  const {kind, rawInput: input} = request.toolCall;
  let filename;
  if (kind === 'read' && fields(input, ['file_path'], ['offset', 'limit']) &&
    ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 0)) {
    filename = ['sales.json', nodeId + '.json'].find(name => input.file_path === name || input.file_path === path.join(cwd, name));
    if (!filename) return deny();
  }
  else if (kind === 'edit' && fields(input, ['file_path', 'content']) && text(input.content) && Buffer.byteLength(input.content) <= 4096) filename = nodeId + '.json';
  else if (kind === 'edit' && fields(input, ['file_path', 'old_string', 'new_string'], ['replace_all']) &&
    [input.old_string, input.new_string].every(value => text(value) && Buffer.byteLength(value) <= 4096) &&
    (input.replace_all === undefined || typeof input.replace_all === 'boolean')) filename = nodeId + '.json';
  else return deny();
  if (!text(input.file_path) || input.file_path !== filename && input.file_path !== path.join(cwd, filename)) return deny();
  // Reject an existing link or unexpected target; task-files independently
  // rechecks the immutable inputs and exact result set after original cleanup.
  const target = path.join(cwd, filename);
  try { const stat = fs.lstatSync(target); if (!stat.isFile() || stat.nlink !== 1 || fs.realpathSync(target) !== target) return deny(); }
  catch (error) { if (error.code !== 'ENOENT' || kind === 'read') return deny(); }
  const options = request.options.filter(option => option.kind === 'allow_once' && option.optionId === 'proceed_once');
  return options.length === 1 ? {outcome: {outcome: 'selected', optionId: 'proceed_once'}} : deny();
}

export function consumeDelivery(content) {
  check(content instanceof Uint8Array && content.byteLength > 0 && content.byteLength <= 16384, 'delivery_limit');
  let value; try { value = parseJson(Buffer.from(content)); } catch { throw new DriverError('delivery_invalid_json'); }
  check(fields(value, ['files']) && Array.isArray(value.files) && value.files.length === 2, 'delivery_shape');
  const actual = value.files.map((file, index) => {
    check(fields(file, ['path', 'content']) && file.path === ['east.json', 'west.json'][index] && text(file.content) &&
      Buffer.byteLength(file.content) <= 4096, 'delivery_file_shape');
    try { return parseJson(Buffer.from(file.content)); } catch { throw new DriverError('delivery_invalid_json'); }
  });
  check(equal(actual, expected), 'delivery_business_failed');
  return {regions: 2, count: 4, netCents: 1825, digest: digest(content)};
}

export function executionFact(identity, result) {
  const cleanup = result?.cleanup;
  check(cleanup?.cleaned === true && cleanup.agentExit?.observed === true && text(cleanup.started?.executionId) &&
    Number.isFinite(Date.parse(cleanup.started.startedAt)) && Number.isFinite(Date.parse(cleanup.agentExit.at)), 'execution_cleanup_unproven');
  check(result.status === 'completed' && result.stopReason === 'end_turn', 'agent_not_completed');
  return {...identity, executionId: cleanup.started.executionId, startedAt: cleanup.started.startedAt,
    agentExitedAt: cleanup.agentExit.at, status: result.status, cleanup: true};
}
export function assertTeam(facts) {
  check(facts.length === 3 && facts.filter(value => value.role === 'planner').length === 1 &&
    new Set(facts.map(value => value.executionId)).size === 3 && new Set(facts.map(value => value.workerId)).size === 3, 'unexpected_execution_count');
  const authors = facts.filter(value => value.role === 'author');
  check(authors.length === 2 && equal(authors.map(value => value.nodeId).sort(), ['east', 'west']), 'unexpected_authors');
  const start = Math.max(...authors.map(value => Date.parse(value.startedAt))), end = Math.min(...authors.map(value => Date.parse(value.agentExitedAt)));
  check(Number.isFinite(start) && end > start, 'authors_did_not_overlap');
  return end - start;
}

/** Observe only the original in-memory handle; never reconstruct process control from evidence. */
export function trackExecution(identity, handle) {
  const entry = {identity, handle, started: null, settled: false, result: null, failed: false};
  handle.started.then(value => { entry.started = value; }, () => { entry.failed = true; });
  handle.completion.then(value => { entry.result = value; entry.settled = true; }, () => { entry.failed = true; entry.settled = true; });
  return entry;
}
export function cancelledExecutionFact(identity, result, requestedAt, started, expectedStopReason = 'provider_stopped') {
  const cleanup = result?.cleanup;
  check(['provider_stopped', 'pi_provider_stopped'].includes(expectedStopReason) && result?.status === 'cancelled' && result.reason === expectedStopReason && cleanup?.cleaned === true &&
    cleanup.agentExit?.observed === true && text(started?.executionId) && started.executionId.length > 0 &&
    equal(cleanup.started, started) && Number.isFinite(Date.parse(started.startedAt)) &&
    Date.parse(started.startedAt) <= Date.parse(requestedAt) && Date.parse(cleanup.agentExit.at) > Date.parse(requestedAt),
  'cancel_execution_unproven');
  return {...identity, executionId: started.executionId, startedAt: started.startedAt,
    agentExitedAt: cleanup.agentExit.at, status: result.status, cleanup: true};
}

/** One explicit HTTP cancellation, with no model delay, mutation retry or substitute execution. */
export async function cancelActiveTeam({client, taskId, observations, getVerifierStarts, end,
  expectedStopReason = 'provider_stopped', idempotencyKey = 'qwen-cancel-task'}) {
  check(['provider_stopped', 'pi_provider_stopped'].includes(expectedStopReason), 'cancel_execution_unproven');
  const authors = () => observations.filter(entry => entry.identity.role === 'author');
  const live = () => {
    check(getVerifierStarts() === 0 && observations.length <= 3 && authors().every(entry => !entry.failed && !entry.settled &&
      !['stopping', 'terminal'].includes(entry.handle.snapshot().phase)), 'cancel_window_missed');
  };
  let task, workers;
  for (;;) {
    check(Date.now() < end, 'cancel_window_timeout'); live();
    workers = await client.request('task.workers', {path: {taskId}});
    task = await client.getTask(taskId); live();
    check(!['completed', 'failed', 'cancelled', 'cancelling', 'intervention', 'paused', 'awaiting-answer'].includes(task.status), 'cancel_window_missed');
    const current = authors();
    if (task.status === 'running' && current.length === 2 && current.every(entry => entry.started)) {
      check(observations.length === 3 && workers.nextCursor === null && workers.items.length <= 3 &&
        equal(current.map(entry => entry.identity.nodeId).sort(), ['east', 'west']) &&
        current.every(entry => entry.identity.taskId === taskId), 'cancel_worker_binding_mismatch');
      // A read begun before the second started callback may still show starting.
      // Wait for that original HTTP projection; never replace its identity.
      if (workers.items.length === 3 && current.every(entry => workers.items.some(worker => worker.id === entry.identity.workerId &&
        worker.nodeId === entry.identity.nodeId && worker.role === 'author' && worker.status === 'running' && worker.startedAt === entry.started.startedAt))) break;
    }
    await pause(20); // Observation polling only; never delay either model or its result.
  }
  live(); check(Date.now() < end, 'cancel_window_timeout');
  const active = authors().map(entry => ({entry, started: structuredClone(entry.started)}));
  const requestedAt = new Date().toISOString();
  const request = {path: {taskId}, idempotencyKey, body: {expectedRevision: task.revision}};
  let operation;
  try { operation = await client.request('task.cancel', request); }
  catch (error) { if (error.status === 409) throw new DriverError('cancel_window_missed'); throw error; }
  const done = await until(client, taskId, ['cancelled'], end);
  check(getVerifierStarts() === 0 && done.artifactIds.length === 0 && observations.length === 3, 'cancel_started_verifier_or_delivery');
  let terminalOperation;
  for (;;) {
    check(Date.now() < end, 'cancel_observation_timeout');
    terminalOperation = await client.request('operation.get', {path: {operationId: operation.id}});
    if (!['accepted', 'running'].includes(terminalOperation.status)) break;
    await pause(20);
  }
  check(terminalOperation.status === 'succeeded' && terminalOperation.taskId === taskId && terminalOperation.kind === 'task.cancel', 'cancel_not_reconciled');
  const executions = await Promise.all(observations.map(async entry => {
    const result = await entry.handle.completion;
    return entry.identity.role === 'planner' ? executionFact(entry.identity, result) :
      cancelledExecutionFact(entry.identity, result, requestedAt, active.find(item => item.entry === entry)?.started, expectedStopReason);
  }));
  workers = await client.request('task.workers', {path: {taskId}});
  check(workers.nextCursor === null && workers.items.length === 3 && executions.every(item => workers.items.some(worker =>
    worker.id === item.workerId && worker.nodeId === item.nodeId && worker.role === item.role && worker.status === item.status)), 'cancel_worker_evidence_mismatch');
  const audit = await client.request('task.audit', {path: {taskId}});
  check(audit.attempts === 3 && audit.acceptance.status !== 'passed', 'cancel_unexpected_acceptance');
  assertTeam(executions);
  return {done, operation, request, requestedAt, executions, workers: workers.items};
}

function save(runDir, name, bytes) {
  const fd = fs.openSync(path.join(runDir, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
  const parent = fs.openSync(runDir, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try { fs.fsyncSync(parent); } finally { fs.closeSync(parent); }
}
async function until(client, id, desired, end) {
  for (;;) {
    const task = await client.getTask(id);
    if (desired.includes(task.status)) return task;
    check(!['failed', 'cancelled', 'intervention', 'awaiting-answer', 'paused'].includes(task.status), 'task_did_not_reach_' + desired[0]);
    check(Date.now() < end, 'observation_deadline'); await pause(200);
  }
}
function nativeEnvironment(node) {
  const result = {PATH: path.dirname(node) + ':/usr/bin:/bin:/usr/sbin:/sbin'};
  for (const key of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (text(process.env[key])) result[key] = process.env[key];
  check(path.isAbsolute(result.HOME ?? ''), 'native_home_missing');
  return result;
}

/** Called only after explicit --execute-real. No retry, substitute provider,
 * model output fixture, arbitrary shell or direct Application mutation. */
export async function runLive(options) {
  const scenario = options.scenario ?? 'team'; check(['team', 'cancel'].includes(scenario), 'invalid_scenario');
  check(supportsNode() && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  check(fs.realpathSync(options.qwenEntry) === options.qwenEntry && path.basename(options.qwenEntry) === 'cli-entry.js', 'qwen_entry_identity');
  const qwenPackage = JSON.parse(fs.readFileSync(path.join(path.dirname(options.qwenEntry), 'package.json')));
  check(qwenPackage.name === '@qwen-code/qwen-code', 'qwen_entry_identity');
  check(fs.realpathSync(path.dirname(options.runDir)) === path.dirname(options.runDir), 'run_parent_identity');
  fs.mkdirSync(options.runDir, {mode: 0o700}); // Existing roots are never adopted or deleted.
  const observations = [], byCwd = new Map(), byWorker = new Map(); let service = null, verifierStarts = 0, stage = 'starting';
  const evidence = {profile: 'qwen-http-team-dogfood/v1', scenario, startedAt: new Date().toISOString(), passed: false,
    ordinaryUser: true, production: false, publisherSeparationProven: false, nodeVersion: process.versions.node,
    qwenVersion: qwenPackage.version, qwenEntryDigest: digest(fs.readFileSync(options.qwenEntry)), checkerDigest: digest(fs.readFileSync(checkerPath)),
    permission: {allowed: 0, denied: 0}, executions: []};
  try {
    const native = createAcpProvider({id: 'qwen-acp', executable: options.node, args: [options.qwenEntry, '--acp'], env: nativeEnvironment(options.node)});
    const provider = {id: native.id, start(input) {
      const identity = byCwd.get(input.cwd); check(identity, 'missing_execution_binding');
      const handle = native.start(input); observations.push(trackExecution(identity, handle)); return handle;
    }};
    const command = createVerificationCommand({executable: options.node, checkerPath, checkerDigest: evidence.checkerDigest,
      policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: value => equal(value, expected)}],
      delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json', content: encode({files: ['east', 'west'].map(region =>
        ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
    const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan, start(input) { verifierStarts++; return command.start(input); }});
    const config = {root: path.join(options.runDir, 'data'), providers: new Map([[provider.id, provider]]), verification,
      businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
        const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          authorize: (ticket, request) => { const answer = filePermission(byWorker.get(ticket.workerId) ?? {}, request);
            evidence.permission[answer.outcome.outcome === 'selected' ? 'allowed' : 'denied']++; return answer; }});
        return {...business, prepare: async (ticket, context) => {
          const prepared = await business.prepare(ticket, context);
          const identity = {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role};
          byCwd.set(prepared.cwd, identity); byWorker.set(ticket.workerId, {...identity, cwd: prepared.cwd}); return prepared;
        }};
      }, supervisorOptions: {intervalMs: 50}};
    const start = async mode => { service = await startTaskService({...config, mode});
      const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return new TaskClient({baseURL: connection.url, token: connection.token}); };
    let client = await start('create'); stage = 'planning';
    const input = await client.request('input.create', {idempotencyKey: 'qwen-sales-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const body = taskBody(input.id, options.timeoutMs), end = Date.now() + options.timeoutMs;
    const created = await client.createTask(body, 'qwen-create-task'); evidence.taskId = created.id;
    const task = await until(client, created.id, ['awaiting-approval'], end);
    const plan = await client.request('task.plan', {path: {taskId: task.id}}), request = validatePlan(task, plan, body.limits, input.id);
    evidence.planDigest = plan.digest; stage = 'executing';
    const operation = await client.approveTask(task.id, request, 'qwen-approve-task'); // Exactly one new approval, no mutation retry.
    evidence.approvalOperationId = operation.id;
    if (scenario === 'cancel') {
      stage = 'cancelling';
      const cancelled = await cancelActiveTeam({client, taskId: task.id, observations, getVerifierStarts: () => verifierStarts, end});
      evidence.executions = cancelled.executions; evidence.overlapMs = assertTeam(cancelled.executions); evidence.verifierStarts = verifierStarts;
      evidence.workers = cancelled.workers.map(worker => ({id: worker.id, nodeId: worker.nodeId, role: worker.role, status: worker.status}));
      evidence.artifacts = [];
      evidence.cancel = {requestedAt: cancelled.requestedAt, operationId: cancelled.operation.id, originalAuthorsStopped: true, noVerifier: true, noDelivery: true};
      stage = 'restarting'; check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
      client = await start('open');
      check(equal(await client.createTask(body, 'qwen-create-task'), created), 'create_receipt_changed');
      check(equal(await client.approveTask(task.id, request, 'qwen-approve-task'), operation), 'approval_receipt_changed');
      check(equal(await client.request('task.cancel', cancelled.request), cancelled.operation), 'cancel_receipt_changed');
      check(equal(await client.getTask(task.id), cancelled.done), 'task_changed_after_restart');
      check((await client.request('operation.get', {path: {operationId: cancelled.operation.id}})).status === 'succeeded', 'cancel_not_reconciled');
      check(observations.length === 3 && verifierStarts === 0, 'restart_started_duplicate_execution');
      evidence.restart = {mode: 'graceful-same-version', originalReceipts: true, sameTask: true, duplicateStarts: 0, noDelivery: true};
      evidence.passed = true; stage = 'complete'; return evidence;
    }
    const done = await until(client, task.id, ['completed'], end); stage = 'downloading';
    const audit = await client.request('task.audit', {path: {taskId: task.id}});
    check(audit.acceptance.status === 'passed' && audit.attempts === 4 && verifierStarts === 1, 'independent_acceptance_missing');
    evidence.executions = await Promise.all(observations.map(async ({identity, handle}) => executionFact(identity, await handle.completion)));
    evidence.overlapMs = assertTeam(evidence.executions); evidence.verifierStarts = verifierStarts;
    const workers = await client.request('task.workers', {path: {taskId: task.id}});
    check(workers.nextCursor === null && workers.items.length === 4 && evidence.executions.every(item =>
      workers.items.some(worker => worker.id === item.workerId && worker.nodeId === item.nodeId && worker.role === item.role && worker.status === 'completed')),
    'http_worker_evidence_mismatch');
    evidence.workers = workers.items.map(worker => ({id: worker.id, nodeId: worker.nodeId, role: worker.role, status: worker.status}));
    const artifacts = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id)));
    const deliveries = artifacts.filter(item => item.artifact.kind === 'delivery'); check(deliveries.length === 1, 'delivery_missing');
    const delivery = deliveries[0]; check(delivery.artifact.taskId === task.id, 'delivery_task_mismatch');
    evidence.consumer = consumeDelivery(delivery.content);
    save(options.runDir, 'regional-report.json', delivery.content);
    evidence.artifacts = artifacts.map(({artifact}) => ({id: artifact.id, kind: artifact.kind, digest: artifact.digest, bytes: artifact.bytes}));
    check((await client.request('operation.get', {path: {operationId: operation.id}})).status === 'succeeded', 'approval_not_reconciled');
    stage = 'restarting'; check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
    client = await start('open');
    check(equal(await client.createTask(body, 'qwen-create-task'), created), 'create_receipt_changed');
    check(equal(await client.approveTask(task.id, request, 'qwen-approve-task'), operation), 'approval_receipt_changed');
    check(equal(await client.getTask(task.id), done), 'task_changed_after_restart');
    const reloaded = await client.downloadArtifact(delivery.artifact.id);
    check(equal(reloaded.artifact, delivery.artifact) && reloaded.content.equals(delivery.content), 'delivery_changed_after_restart');
    check(observations.length === 3 && verifierStarts === 1, 'restart_started_duplicate_execution');
    evidence.restart = {mode: 'graceful-same-version', originalReceipts: true, sameArtifact: true, duplicateStarts: 0};
    evidence.passed = true; stage = 'complete';
  } catch (error) {
    evidence.failure = {stage, code: error instanceof DriverError ? error.code : 'driver_dependency_failed'};
  } finally {
    if (service) { try { check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); }
      catch { evidence.passed = false; evidence.failure = {stage: 'shutdown', code: 'shutdown_unconfirmed'}; } }
    // Only original handles, never a PID from disk. Keep raw outputs private in
    // the original service store; the evidence file contains no model text.
    for (const {identity, handle} of observations) {
      try { const result = await handle.stop(); if (!result?.cleanup?.cleaned) evidence.passed = false;
        if (!evidence.executions.some(item => item.workerId === identity.workerId)) {
          const cleanup = result?.cleanup;
          evidence.executions.push({...identity, status: result?.status ?? 'unknown', cleanup: cleanup?.cleaned === true,
            executionId: cleanup?.started?.executionId ?? null, startedAt: cleanup?.started?.startedAt ?? null, agentExitedAt: cleanup?.agentExit?.at ?? null});
        }
      } catch { evidence.passed = false; }
    }
    evidence.finishedAt = new Date().toISOString(); save(options.runDir, 'evidence.json', encode(evidence));
  }
  return evidence;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('显式实机：--execute-real --run-dir ABS_NEW_PRIVATE_DIR --node ABS_NODE_24_15 --qwen-entry ABS_cli-entry.js [--timeout-ms 600000] [--scenario team|cancel]\n不会自动重试；仅普通用户 dogfood；不随发行包或 CI 调用。\n');
    else { const evidence = await runLive(options); process.stdout.write(JSON.stringify({passed: evidence.passed, taskId: evidence.taskId ?? null,
      failure: evidence.failure ?? null, evidence: path.join(options.runDir, 'evidence.json')}) + '\n'); if (!evidence.passed) process.exitCode = 1; }
  } catch (error) { process.stderr.write(JSON.stringify({error: error instanceof DriverError ? error.code : 'driver_preflight_failed'}) + '\n'); process.exitCode = 1; }
}
