import {supportsNode} from '../task-store/runtime.mjs';
// Explicit, test-only Pi HTTP team driver; never included in default release.
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
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';
import {data, taskBody, validatePlan, consumeDelivery, executionFact, assertTeam} from '../task-qwen-live/driver.fixture.mjs';

const checkerPath = fileURLToPath(new URL('../task-team-integration/checker.fixture.mjs', import.meta.url));
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const keys = (value, required, optional = []) => object(value) && required.every(key => Object.hasOwn(value, key)) &&
  Object.keys(value).every(key => required.includes(key) || optional.includes(key));
const equal = (a, b) => { try { return encode(a).equals(encode(b)); } catch { return false; } };
const deny = () => ({outcome: {outcome: 'cancelled'}});
export class PiLiveError extends Error { constructor(code) { super(code); this.name = 'PiLiveError'; this.code = code; } }
const check = (condition, code) => { if (!condition) throw new PiLiveError(code); };
export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const values = {}; let executeReal = false;
  for (let at = 0; at < argv.length; at++) {
    const name = argv[at];
    if (name === '--execute-real' && !executeReal) { executeReal = true; continue; }
    check(['--run-dir', '--node', '--pi-entry', '--pi-sdk', '--timeout-ms'].includes(name) && !Object.hasOwn(values, name), 'invalid_arguments');
    const value = argv[++at]; check(text(value) && value && !value.startsWith('--'), 'invalid_arguments'); values[name] = value;
  }
  check(executeReal, 'explicit_real_execution_required');
  for (const name of ['--run-dir', '--node', '--pi-entry', '--pi-sdk']) check(text(values[name]) && path.isAbsolute(values[name]) &&
    path.normalize(values[name]) === values[name] && values[name] !== path.parse(values[name]).root, 'invalid_path');
  const timeoutMs = values['--timeout-ms'] === undefined ? 600000 : Number(values['--timeout-ms']);
  check(Number.isSafeInteger(timeoutMs) && timeoutMs >= 60000 && timeoutMs <= 900000, 'invalid_timeout');
  return {executeReal, runDir: values['--run-dir'], node: values['--node'], piEntry: values['--pi-entry'], sdkEntry: values['--pi-sdk'], timeoutMs};
}

/** Only this approved test business, not an Agent-wide tool restriction. Native
 * Pi supplies its final prepared parameters, never Qwen's file_path/options. */
export function filePermission({cwd, role, nodeId} = {}, request) {
  if (role !== 'author' || !['east', 'west'].includes(nodeId) || !text(cwd) || !path.isAbsolute(cwd) ||
    !object(request?.toolCall) || !Array.isArray(request.options) || request.options.some(option => !object(option))) return deny();
  const call = request.toolCall, input = call.rawInput, name = call._meta?.toolName;
  if (call._meta?.provider !== 'pi' || !text(input?.path)) return deny();
  let targetName, missingAllowed = false;
  if (name === 'read' && call.kind === 'read' && keys(input, ['path'], ['offset', 'limit']) &&
    ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 1 && input[key] <= 10000))
    targetName = ['sales.json', nodeId + '.json'].find(value => input.path === value || input.path === path.join(cwd, value));
  else if (name === 'write' && call.kind === 'edit' && keys(input, ['path', 'content']) && text(input.content) && Buffer.byteLength(input.content) <= 4096) {
    targetName = nodeId + '.json'; missingAllowed = true;
  } else if (name === 'edit' && call.kind === 'edit') {
    const edits = keys(input, ['path', 'edits']) ? input.edits : keys(input, ['path', 'oldText', 'newText']) ? [{oldText: input.oldText, newText: input.newText}] : null;
    if (!Array.isArray(edits) || !edits.length || edits.length > 8 || !edits.every(edit => keys(edit, ['oldText', 'newText']) &&
      [edit.oldText, edit.newText].every(value => text(value) && Buffer.byteLength(value) <= 4096)) || Buffer.byteLength(JSON.stringify(edits)) > 8192) return deny();
    targetName = nodeId + '.json';
  } else return deny();
  if (!targetName || input.path !== targetName && input.path !== path.join(cwd, targetName)) return deny();
  const target = path.join(cwd, targetName);
  try { if (fs.realpathSync(cwd) !== cwd || !fs.lstatSync(cwd).isDirectory()) return deny(); }
  catch { return deny(); }
  try {
    const stat = fs.lstatSync(target);
    if (!stat.isFile() || stat.nlink !== 1 || fs.realpathSync(target) !== target) return deny();
  } catch (error) { if (error.code !== 'ENOENT' || !missingAllowed) return deny(); }
  const allowed = request.options.filter(option => option.kind === 'allow_once' && option.optionId === 'allow-once');
  return allowed.length === 1 ? {outcome: {outcome: 'selected', optionId: 'allow-once'}} : deny();
}

export function expectedRegions() {
  return ['east', 'west'].map(region => { const rows = data.rows.filter(row => row.region === region && row.status === 'paid');
    return {region, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)}; });
}
function environment(node) {
  const env = {PATH: path.dirname(node) + ':' + (process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin')};
  for (const key of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (text(process.env[key])) env[key] = process.env[key];
  check(path.isAbsolute(env.HOME ?? ''), 'native_home_missing'); return env;
}
function save(directory, name, bytes) {
  const fd = fs.openSync(path.join(directory, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
  const parent = fs.openSync(directory, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try { fs.fsyncSync(parent); } finally { fs.closeSync(parent); }
}
async function until(client, id, expected, deadline) {
  for (;;) {
    const task = await client.getTask(id); if (task.status === expected) return task;
    check(!['failed', 'cancelled', 'intervention', 'awaiting-answer', 'paused'].includes(task.status), 'task_did_not_reach_' + expected);
    check(Date.now() < deadline, 'observation_deadline'); await pause(200);
  }
}

/** No model is launched before the explicit flag and native identity checks. */
export async function runLive(options) {
  check(options?.executeReal === true, 'explicit_real_execution_required');
  check(supportsNode() && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  const packageRoot = path.resolve(path.dirname(options.piEntry), '../..');
  check(options.piEntry === path.join(packageRoot, 'dist/bundle/cli.js') && options.sdkEntry === path.join(packageRoot, 'dist/index.js') &&
    fs.realpathSync(options.piEntry) === options.piEntry && fs.realpathSync(options.sdkEntry) === options.sdkEntry, 'pi_entry_identity');
  const metadata = JSON.parse(fs.readFileSync(path.join(packageRoot, 'package.json')));
  check(metadata.name === '@earendil-works/pi-coding-agent' && text(metadata.version), 'pi_entry_identity');
  check(fs.realpathSync(path.dirname(options.runDir)) === path.dirname(options.runDir), 'run_parent_identity');
  fs.mkdirSync(options.runDir, {mode: 0o700});
  const observations = [], byCwd = new Map(), byWorker = new Map(); let service, verifierStarts = 0, stage = 'starting';
  const evidence = {profile: 'pi-http-team-dogfood/v1', startedAt: new Date().toISOString(), passed: false, ordinaryUser: true,
    production: false, publisherSeparationProven: false, nodeVersion: process.versions.node, piVersion: metadata.version,
    piEntryDigest: digest(fs.readFileSync(options.piEntry)), sdkEntryDigest: digest(fs.readFileSync(options.sdkEntry)),
    checkerDigest: digest(fs.readFileSync(checkerPath)), permission: {allowed: 0, denied: 0}, executions: []};
  try {
    const native = createPiProvider({id: 'pi-rpc', executable: options.node, args: [options.piEntry, '--mode', 'rpc', '--no-session'],
      env: environment(options.node), bridge: {sdkEntry: options.sdkEntry}});
    const provider = {id: native.id, start(input) { const identity = byCwd.get(input.cwd); check(identity, 'missing_execution_binding');
      const handle = native.start(input); observations.push({identity, handle}); return handle; }};
    const command = createVerificationCommand({executable: options.node, checkerPath, checkerDigest: evidence.checkerDigest,
      policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: value => equal(value, expectedRegions())}],
      delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json', content: encode({files: ['east', 'west'].map(region =>
        ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
    const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan, start(input) { verifierStarts++; return command.start(input); }});
    const config = {root: path.join(options.runDir, 'data'), providers: new Map([[provider.id, provider]]), verification,
      businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
        const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          authorize: (ticket, request) => { const answer = filePermission(byWorker.get(ticket.workerId), request);
            evidence.permission[answer.outcome.outcome === 'selected' ? 'allowed' : 'denied']++; return answer; }});
        return {...business, prepare: async (ticket, context) => { const prepared = await business.prepare(ticket, context);
          const identity = {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role};
          byCwd.set(prepared.cwd, identity); byWorker.set(ticket.workerId, {...identity, cwd: prepared.cwd}); return prepared; }};
      }, supervisorOptions: {intervalMs: 50}};
    const start = async mode => { service = await startTaskService({...config, mode}); const connection = JSON.parse(fs.readFileSync(service.connectionFile));
      return new TaskClient({baseURL: connection.url, token: connection.token}); };
    let client = await start('create'); stage = 'planning';
    const input = await client.request('input.create', {idempotencyKey: 'pi-sales-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const body = taskBody(input.id, options.timeoutMs), deadline = Date.now() + options.timeoutMs;
    const created = await client.createTask(body, 'pi-create-task'); evidence.taskId = created.id;
    const task = await until(client, created.id, 'awaiting-approval', deadline), plan = await client.request('task.plan', {path: {taskId: task.id}});
    const request = validatePlan(task, plan, body.limits, input.id); evidence.planDigest = plan.digest; stage = 'executing';
    const operation = await client.approveTask(task.id, request, 'pi-approve-task'); evidence.approvalOperationId = operation.id;
    const done = await until(client, task.id, 'completed', deadline); stage = 'downloading';
    const audit = await client.request('task.audit', {path: {taskId: task.id}});
    check(audit.acceptance.status === 'passed' && audit.attempts === 4 && verifierStarts === 1, 'independent_acceptance_missing');
    evidence.executions = await Promise.all(observations.map(async ({identity, handle}) => executionFact(identity, await handle.completion)));
    evidence.overlapMs = assertTeam(evidence.executions); evidence.verifierStarts = verifierStarts;
    const workers = await client.request('task.workers', {path: {taskId: task.id}});
    check(workers.nextCursor === null && workers.items.length === 4 && evidence.executions.every(item => workers.items.some(worker =>
      worker.id === item.workerId && worker.nodeId === item.nodeId && worker.role === item.role && worker.status === 'completed')), 'http_worker_evidence_mismatch');
    evidence.workers = workers.items.map(({id, nodeId, role, status}) => ({id, nodeId, role, status}));
    const artifacts = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id)));
    const deliveries = artifacts.filter(value => value.artifact.kind === 'delivery'); check(deliveries.length === 1, 'delivery_missing');
    const delivery = deliveries[0]; check(delivery.artifact.taskId === task.id, 'delivery_task_mismatch');
    evidence.consumer = consumeDelivery(delivery.content); save(options.runDir, 'regional-report.json', delivery.content);
    evidence.artifacts = artifacts.map(({artifact}) => ({id: artifact.id, kind: artifact.kind, digest: artifact.digest, bytes: artifact.bytes}));
    check((await client.request('operation.get', {path: {operationId: operation.id}})).status === 'succeeded', 'approval_not_reconciled');
    stage = 'restarting'; check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
    client = await start('open');
    check(equal(await client.createTask(body, 'pi-create-task'), created) && equal(await client.approveTask(task.id, request, 'pi-approve-task'), operation), 'original_receipt_changed');
    check(equal(await client.getTask(task.id), done), 'task_changed_after_restart');
    const recovered = await client.downloadArtifact(delivery.artifact.id);
    check(equal(recovered.artifact, delivery.artifact) && recovered.content.equals(delivery.content), 'delivery_changed_after_restart');
    check(observations.length === 3 && verifierStarts === 1, 'restart_started_duplicate_execution');
    evidence.restart = {mode: 'graceful-same-version', originalReceipts: true, sameArtifact: true, duplicateStarts: 0}; evidence.passed = true; stage = 'complete';
  } catch (error) { evidence.failure = {stage, code: error instanceof PiLiveError ? error.code : 'pi_driver_dependency_failed'}; }
  finally {
    if (service) { try { check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); }
      catch { evidence.passed = false; evidence.failure = {stage: 'shutdown', code: 'shutdown_unconfirmed'}; } }
    for (const {identity, handle} of observations) {
      try { const result = await handle.stop(); if (!result?.cleanup?.cleaned) evidence.passed = false;
        if (!evidence.executions.some(item => item.workerId === identity.workerId)) { const cleanup = result?.cleanup;
          evidence.executions.push({...identity, status: result?.status ?? 'unknown', cleanup: cleanup?.cleaned === true,
            executionId: cleanup?.started?.executionId ?? null, startedAt: cleanup?.started?.startedAt ?? null, agentExitedAt: cleanup?.agentExit?.at ?? null}); }
      } catch { evidence.passed = false; }
    }
    evidence.finishedAt = new Date().toISOString(); save(options.runDir, 'evidence.json', encode(evidence));
  }
  return evidence;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('--execute-real --run-dir ABS_NEW_PRIVATE_DIR --node ABS_NODE_24_15 --pi-entry ABS_dist/bundle/cli.js --pi-sdk ABS_dist/index.js [--timeout-ms 600000]\n仅显式普通用户实机验收；无重试、fallback 或生产声明。\n');
    else { const evidence = await runLive(options); process.stdout.write(JSON.stringify({passed: evidence.passed, taskId: evidence.taskId ?? null,
      failure: evidence.failure ?? null, evidence: path.join(options.runDir, 'evidence.json')}) + '\n'); if (!evidence.passed) process.exitCode = 1; }
  } catch (error) { process.stderr.write(JSON.stringify({error: error instanceof PiLiveError ? error.code : 'pi_driver_preflight_failed'}) + '\n'); process.exitCode = 1; }
}
