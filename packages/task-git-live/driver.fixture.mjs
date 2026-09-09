// Explicit operator acceptance tool, not production inventory/default business.
// Real execution requires --execute-real and a clean exact source checkout.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {launchCommand} from '../agent-runtime/index.mjs';
import {createGitBusiness, gitDescription, PATCH} from '../task-git-business/index.mjs';
import {runGit} from '../task-git-business/git.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {LIMITS, NODES, MARKER, END, policy, proposal, taskBody, bindPlan, validatePlan, filePermission, check, equal, LiveError} from './business.fixture.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
const SOURCE = fs.realpathSync(here('../..')), CHECKER = here('./checker.fixture.mjs');
const GIT = '/usr/bin/git', NODE_VERSION = '24.15.0';
const git = async (cwd, args, extra) => (await runGit(GIT, cwd, args, extra)).toString();
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const safePath = value => text(value) && path.isAbsolute(value) && path.normalize(value) === value && value !== path.parse(value).root;
const ownKeys = (value, names) => value && typeof value === 'object' && Object.keys(value).every(key => names.includes(key));
function save(root, name, bytes) {
  check(bytes.length <= 1024 * 1024, 'evidence_limit');
  const fd = fs.openSync(path.join(root, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  const parent = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try {fs.fsyncSync(parent);} finally {fs.closeSync(parent);}
}
function environment(node) {
  const env = {PATH: path.dirname(node) + ':/usr/bin:/bin:/usr/sbin:/sbin'};
  for (const name of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (text(process.env[name])) env[name] = process.env[name];
  check(safePath(env.HOME), 'native_home_missing'); return env;
}
export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const values = {}; let real = false;
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i];
    if (key === '--execute-real' && !real) {real = true; continue;}
    check(['--run-dir', '--node', '--pi-entry', '--pi-sdk', '--qwen-entry', '--source-head'].includes(key) && !Object.hasOwn(values, key), 'invalid_arguments');
    const value = argv[++i]; check(text(value) && value && !value.startsWith('--'), 'invalid_arguments'); values[key] = value;
  }
  check(real, 'explicit_real_execution_required');
  for (const name of ['--run-dir', '--node', '--pi-entry', '--pi-sdk', '--qwen-entry']) check(safePath(values[name]), 'invalid_path');
  check(/^[a-f0-9]{40}$/.test(values['--source-head'] ?? ''), 'invalid_source');
  return {executeReal: true, runDir: values['--run-dir'], node: values['--node'], piEntry: values['--pi-entry'], sdkEntry: values['--pi-sdk'],
    qwenEntry: values['--qwen-entry'], sourceHead: values['--source-head']};
}
async function native(options) {
  check(options?.executeReal === true && ownKeys(options, ['executeReal', 'runDir', 'node', 'piEntry', 'sdkEntry', 'qwenEntry', 'sourceHead']), 'explicit_real_execution_required');
  check(process.versions.node === NODE_VERSION && safePath(options.node) && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  check(/^[a-f0-9]{40}$/.test(options.sourceHead ?? '') && (await git(SOURCE, ['rev-parse', 'HEAD'])).trim() === options.sourceHead &&
    (await git(SOURCE, ['status', '--porcelain', '--untracked-files=all'])).trim() === '', 'source_not_clean_exact');
  const piRoot = path.resolve(path.dirname(options.piEntry), '../..'), qwenRoot = path.dirname(options.qwenEntry);
  check(options.piEntry === path.join(piRoot, 'dist/bundle/cli.js') && options.sdkEntry === path.join(piRoot, 'dist/index.js') &&
    path.basename(options.qwenEntry) === 'cli-entry.js', 'native_identity');
  for (const entry of [options.piEntry, options.sdkEntry, options.qwenEntry]) check(fs.realpathSync(entry) === entry && fs.statSync(entry).isFile(), 'native_identity');
  // Public installed package metadata only. Authentication is the Agent's job.
  const pi = JSON.parse(fs.readFileSync(path.join(piRoot, 'package.json'))), qwen = JSON.parse(fs.readFileSync(path.join(qwenRoot, 'package.json')));
  check(pi.name === '@earendil-works/pi-coding-agent' && qwen.name === '@qwen-code/qwen-code' && text(pi.version) && text(qwen.version), 'native_identity');
  const env = environment(options.node);
  return {sourceHead: options.sourceHead, mode: 'real-native', versions: {pi: pi.version, qwen: qwen.version},
    entries: {pi: digest(fs.readFileSync(options.piEntry)), piSdk: digest(fs.readFileSync(options.sdkEntry)), qwen: digest(fs.readFileSync(options.qwenEntry))},
    providers: [createPiProvider({id: 'pi-rpc', executable: options.node, args: [options.piEntry, '--mode', 'rpc', '--no-session'], env, bridge: {sdkEntry: options.sdkEntry}}),
      createAcpProvider({id: 'qwen-acp', executable: options.node, args: [options.qwenEntry, '--acp'], env})]};
}
async function repositories(root) {
  const parent = path.join(root, 'repositories'); fs.mkdirSync(parent, {mode: 0o700});
  const roots = {}, nodes = [], untouched = {};
  for (const [nodeId, filename, declaration] of [['library', 'net.mjs', 'net(cents,discount)'], ['client', 'invoice.mjs', 'invoice(rows,net)']]) {
    const cwd = path.join(parent, nodeId); fs.mkdirSync(cwd, {mode: 0o700}); roots[nodeId] = cwd;
    await git(cwd, ['init', '-b', 'main']);
    fs.writeFileSync(path.join(cwd, filename), `export function ${declaration} { throw Error('not implemented'); }\n`, {flag: 'wx', mode: 0o644});
    const preserved = Buffer.from(nodeId + ' original unrelated content\n'); untouched[nodeId] = digest(preserved);
    fs.writeFileSync(path.join(cwd, 'untouched.txt'), preserved, {flag: 'wx', mode: 0o644});
    await git(cwd, ['add', '--', filename, 'untouched.txt']);
    await git(cwd, ['-c', 'user.name=Git mixed acceptance', '-c', 'user.email=acceptance@example.invalid', 'commit', '--no-gpg-sign', '-m', 'public synthetic locked base']);
    nodes.push({nodeId, repositoryId: nodeId, base: (await git(cwd, ['rev-parse', 'HEAD'])).trim(), writePaths: [filename]});
  }
  return {roots, untouched, description: {profile: 'task-git-input/v1', nodes}};
}
function expected(actual, repos, refs) {
  return equal(actual?.invoice, {lines: [{sku: 'desk', amount: 900}, {sku: 'lamp', amount: 450}], total: 1350}) &&
    actual.checks === 23 && actual.negativeChecks === 12 &&
    equal(actual.kept, NODES.map(nodeId => ({nodeId, digest: repos.untouched[nodeId]}))) &&
    equal(actual.patches, refs.map(ref => ({nodeId: ref.nodeId, digest: ref.patchDigest})));
}
export function overlap(facts) {
  const authors = facts.filter(item => item.role === 'author');
  check(authors.length === 2 && equal(authors.map(item => item.nodeId).sort(), ['client', 'library']) &&
    new Set(authors.map(item => item.executionId)).size === 2, 'execution_binding');
  const value = Math.min(...authors.map(item => Date.parse(item.agentExitedAt))) - Math.max(...authors.map(item => Date.parse(item.startedAt)));
  check(Number.isFinite(value) && value > 0, 'authors_did_not_overlap'); return value;
}
function fact(identity, result) {
  const cleanup = result?.cleanup;
  return {...identity, status: result?.status ?? 'unknown', stopReason: result?.stopReason ?? null, cleaned: cleanup?.cleaned === true,
    executionId: cleanup?.started?.executionId ?? null, startedAt: cleanup?.started?.startedAt ?? null,
    agentExitedAt: cleanup?.agentExit?.observed === true ? cleanup.agentExit.at : null};
}
async function run(runDir, node, configuration, signal) {
  check(safePath(runDir) && fs.realpathSync(path.dirname(runDir)) === path.dirname(runDir), 'invalid_run_directory');
  fs.mkdirSync(runDir, {mode: 0o700}); // No adoption, overwrite, removal or retry.
  const evidence = {profile: 'git-mixed-http-acceptance/v1', mode: configuration.mode, sourceHead: configuration.sourceHead,
    proofScope: 'operator observation, not a signed Core receipt or hostile-code isolation proof', passed: false,
    production: false, publisherSeparationProven: false, recoveryClaim: 'graceful-same-version-only',
    startedAt: new Date().toISOString(), nodeVersion: process.versions.node, versions: configuration.versions, entries: configuration.entries,
    driverDigest: digest(fs.readFileSync(here('./driver.fixture.mjs'))), businessDigest: digest(fs.readFileSync(here('./business.fixture.mjs'))),
    checkerDigest: digest(fs.readFileSync(CHECKER)), permissions: {pi: {allowed: 0, denied: 0}, qwen: {allowed: 0, denied: 0}}, executions: []};
  let service, consumer, stage = 'repository-init', refs = [], verifierStarts = 0, deadline, interrupted = false;
  const observations = [], verifiers = [], identities = new Map(), byWorker = new Map();
  const stop = () => {interrupted = true; if (service) void service.shutdown().catch(() => {});};
  process.once('SIGINT', stop); process.once('SIGTERM', stop); signal?.addEventListener('abort', stop, {once: true});
  const active = () => {check(!interrupted && !signal?.aborted, 'driver_interrupted'); check(!deadline || Date.now() < deadline, 'driver_deadline');};
  async function until(client, id, status) {
    for (;;) {active(); const value = await client.getTask(id); evidence.lastTaskStatus = value.status; if (value.status === status) return value;
      check(!['failed', 'cancelled', 'intervention', 'paused', 'awaiting-answer'].includes(value.status), 'task_not_' + status); await pause(100);}
  }
  try {
    const repos = await repositories(runDir); active();
    evidence.repositories = repos.description.nodes.map(item => ({...item, untouchedDigest: repos.untouched[item.nodeId]}));
    const verificationParent = path.join(runDir, 'verification'), consumerParent = path.join(runDir, 'consumer');
    fs.mkdirSync(verificationParent, {mode: 0o700}); fs.mkdirSync(consumerParent, {mode: 0o700});
    const providers = new Map(configuration.providers.map(original => [original.id, {id: original.id, start(input) {
      const identity = identities.get(input.cwd); check(identity && identity.providerId === original.id, 'execution_binding');
      const handle = original.start(input); observations.push({identity, handle}); return handle;
    }}]));
    const command = createVerificationCommand({executable: node, checkerPath: CHECKER, checkerDigest: evidence.checkerDigest, policyDigest: digest(encode(policy)),
      request: ({ticket}) => {
        refs = gitDescription(ticket.input.task.context.text).nodes.map(item => {
          const upstream = ticket.input.upstream.find(value => value.nodeId === item.nodeId), result = upstream?.result;
          const original = byWorker.get(upstream?.workerId);
          check(result?.files?.length === 2 && result.taskId === ticket.taskId && original?.taskId === ticket.taskId &&
            original.nodeId === item.nodeId && original.reservationDigest === result.reservationDigest && original.planDigest === result.planDigest, 'candidate_binding');
          return {...item, root: repos.roots[item.repositoryId], taskId: ticket.taskId, workerId: upstream.workerId,
            // Candidate.inputDigest hashes materialized files. Git context binds
            // the original execution ticket input, not that different digest.
            reservationDigest: result.reservationDigest, inputDigest: original.inputDigest, planDigest: result.planDigest,
            untouchedDigest: repos.untouched[item.nodeId], patchDigest: result.files.find(file => file.path === PATCH)?.digest};
        });
        return {repositories: refs, worktreeParent: verificationParent};
      }, assertions: [{name: 'git-combination', validate: actual => expected(actual, repos, refs)}],
      delivery: ({prepared}) => ({name: 'git-mixed-patches.json', mediaType: 'application/json', content: encode({profile: 'git-mixed-delivery/v1',
        files: NODES.map(nodeId => ({repositoryId: nodeId, base: repos.description.nodes.find(item => item.nodeId === nodeId).base,
          patch: fs.readFileSync(path.join(prepared.cwd, nodeId + '.patch'), 'utf8'),
          context: JSON.parse(fs.readFileSync(path.join(prepared.cwd, nodeId + '-context.json'), 'utf8'))}))})})});
    const startVerification = input => {verifierStarts++; const handle = command.start(input); verifiers.push({identity: {
      taskId: input.ticket.taskId, workerId: input.ticket.workerId, nodeId: input.ticket.nodeId, role: 'verifier', providerId: input.ticket.providerId}, handle}); return handle;};
    Object.defineProperty(startVerification, 'custodyProfile', {value: command.custodyProfile});
    const verification = createVerificationPort({id: 'git-mixed-independent-checker', policy, bindPlan, start: startVerification});
    const config = {root: path.join(runDir, 'data'), providers, verification, custody: {profile: 'node-execution-custody/v1'},
      applicationOptions: {execution: {maxWorkers: 2, defaultProvider: 'pi-rpc'}},
      businessFactory: ports => {
        const business = createGitBusiness({parent: ports.executionParent, depot: ports.depot, approvedLayout: ports.approvedLayout, observeExecution: ports.observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          repositoryFor: (_ticket, request) => repos.roots[request.repositoryId], authorize: (ticket, request) => {
            const answer = filePermission(byWorker.get(ticket.workerId), request), bucket = ticket.providerId === 'pi-rpc' ? 'pi' : 'qwen';
            const allowed = answer.outcome.outcome === 'selected' && request.options.some(option => option.kind === 'allow_once' && option.optionId === answer.outcome.optionId);
            evidence.permissions[bucket][allowed ? 'allowed' : 'denied']++; return answer;
          }});
        return {...business, async prepare(ticket, context) {
          const prepared = await business.prepare(ticket, context);
          if (ticket.role === 'planner') prepared.prompt = '本次仅支持下列完整固定业务计划；原样返回此 JSON，不改 goal/scope/providerId/边或预算，不写文件、自行批准或输出代码。\n' +
            MARKER + encode(proposal()) + END + prepared.prompt;
          check(Buffer.byteLength(prepared.prompt) <= 256 * 1024, 'prompt_limit');
          const identity = {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role, providerId: ticket.providerId};
          identities.set(prepared.cwd, identity); byWorker.set(ticket.workerId, {...identity, cwd: prepared.cwd,
            reservationDigest: ticket.reservationDigest, inputDigest: ticket.inputDigest, planDigest: ticket.planDigest}); return prepared;
        }};
      }, supervisorOptions: {intervalMs: 25}};
    const start = async mode => {service = await startTaskService({...config, mode}); const connection = JSON.parse(fs.readFileSync(service.connectionFile));
      return new TaskClient({baseURL: connection.url, token: connection.token});};
    stage = 'planning'; let client = await start('create'); const body = taskBody(repos.description);
    deadline = Date.now() + LIMITS.timeoutMs;
    const created = await client.createTask(body, 'git-mixed-create'); evidence.taskId = created.id;
    const task = await until(client, created.id, 'awaiting-approval'), plan = await client.request('task.plan', {path: {taskId: task.id}});
    const approval = validatePlan(task, plan, body); evidence.planDigest = plan.digest; evidence.deadlineAt = task.deadlineAt;
    stage = 'approved'; const operation = await client.approveTask(task.id, approval, 'git-mixed-approve');
    evidence.approvalOperationId = operation.id;
    const done = await until(client, task.id, 'completed'); stage = 'download';
    const audit = await client.request('task.audit', {path: {taskId: task.id}});
    check(audit.attempts === 4 && audit.acceptance.status === 'passed' && observations.length === 3 && verifierStarts === 1, 'independent_acceptance_missing');
    evidence.executions = await Promise.all(observations.map(async ({identity, handle}) => fact(identity, await handle.completion)));
    check(evidence.executions.every(item => item.cleaned && item.status === 'completed' && item.stopReason === 'end_turn'), 'original_cleanup_missing');
    evidence.overlapMs = overlap(evidence.executions);
    const workers = await client.request('task.workers', {path: {taskId: task.id}});
    check(workers.nextCursor === null && workers.items.length === 4 && workers.items.every(item => item.status === 'completed') &&
      evidence.executions.every(item => workers.items.some(worker => worker.id === item.workerId && worker.nodeId === item.nodeId && worker.providerId === item.providerId)), 'http_execution_mismatch');
    evidence.workers = workers.items.map(({id, nodeId, providerId, role, status, attempt}) => ({id, nodeId, providerId, role, status, attempt}));
    const downloads = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id))), deliveries = downloads.filter(item => item.artifact.kind === 'delivery');
    check(deliveries.length === 1, 'delivery_missing'); const delivered = deliveries[0], bytes = delivered.content;
    check(bytes.length <= 180000 && delivered.artifact.taskId === task.id && digest(bytes) === delivered.artifact.digest, 'delivery_binding');
    const bundle = JSON.parse(bytes); check(bundle.profile === 'git-mixed-delivery/v1' && equal(bundle.files.map(item => item.repositoryId), NODES), 'delivery_binding');
    for (const item of bundle.files) check(item.base === repos.description.nodes.find(value => value.repositoryId === item.repositoryId).base, 'delivery_binding');
    save(runDir, 'git-mixed-patches.json', bytes);
    stage = 'consumer'; const nonce = randomUUID(), input = Buffer.concat([encode({profile: 'git-mixed-consumer/v1', nonce,
      input: {repositories: refs, worktreeParent: consumerParent, delivery: bundle}}), Buffer.from('\n')]);
    check(input.length <= 256 * 1024, 'consumer_input_limit');
    consumer = await launchCommand({executable: node, args: [CHECKER], cwd: consumerParent, env: {}, deadline,
      input, limits: {inputBytes: 256 * 1024, outputBytes: 256 * 1024, stderrBytes: 65536}});
    const consumed = await consumer.completion, parsed = JSON.parse(consumed.stdout);
    check(consumed.cleanup?.cleaned && consumed.cleanup.agentExit.code === 0 && consumed.outputComplete &&
      encode(parsed).toString() + '\n' === consumed.stdout.toString() && parsed.profile === 'git-mixed-consumer/v1' && parsed.nonce === nonce &&
      expected(parsed.actual, repos, refs), 'download_consumer_failed');
    evidence.consumer = {checks: parsed.actual.checks, negativeChecks: parsed.actual.negativeChecks, total: parsed.actual.invoice.total,
      deliveryDigest: digest(bytes), deliveryBytes: bytes.length, patchDigests: parsed.actual.patches, cleanup: true};
    evidence.artifacts = downloads.map(({artifact}) => ({id: artifact.id, kind: artifact.kind, digest: artifact.digest, bytes: artifact.bytes}));
    for (const item of repos.description.nodes) check((await git(repos.roots[item.repositoryId], ['rev-parse', 'HEAD'])).trim() === item.base &&
      await git(repos.roots[item.repositoryId], ['status', '--porcelain']) === '' && await git(repos.roots[item.repositoryId], ['remote']) === '', 'original_repository_changed');
    evidence.originalRepositoriesUnchanged = true;
    check((await client.request('operation.get', {path: {operationId: operation.id}})).status === 'succeeded', 'approval_not_settled');
    stage = 'reopen'; check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null;
    client = await start('open');
    check(equal(await client.createTask(body, 'git-mixed-create'), created) && equal(await client.approveTask(task.id, approval, 'git-mixed-approve'), operation) &&
      equal(await client.getTask(task.id), done), 'original_receipt_changed');
    const reopened = await client.downloadArtifact(delivered.artifact.id);
    check(equal(reopened.artifact, delivered.artifact) && reopened.content.equals(bytes) && observations.length === 3 && verifierStarts === 1, 'reopen_changed');
    if (configuration.mode === 'real-native') check((await git(SOURCE, ['rev-parse', 'HEAD'])).trim() === configuration.sourceHead &&
      (await git(SOURCE, ['status', '--porcelain', '--untracked-files=all'])).trim() === '', 'source_changed_during_run');
    evidence.restart = {mode: 'graceful-same-version', originalReceipts: true, sameBytes: true, duplicateStarts: 0}; evidence.passed = true;
  } catch (error) {
    evidence.failure = {stage, code: error instanceof LiveError ? error.code : 'driver_dependency_failed'};
    evidence.inconclusive = error instanceof LiveError && error.code === 'authors_did_not_overlap';
  } finally {
    process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop); signal?.removeEventListener('abort', stop);
    if (service) try {check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed');} catch {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
    for (const {identity, handle} of observations) try {
      const result = await handle.stop(); if (!result?.cleanup?.cleaned) {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
      if (!evidence.executions.some(item => item.workerId === identity.workerId)) evidence.executions.push(fact(identity, result));
    } catch {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
    evidence.verifierStarts = verifierStarts; evidence.verifiers = [];
    for (const {identity, handle} of verifiers) try {
      const result = await handle.stop(); evidence.verifiers.push(fact(identity, result));
      if (!result.cleanup?.cleaned) {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
    } catch {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
    if (consumer) try {if (!(await consumer.stop()).cleanup?.cleaned) {evidence.passed = false; evidence.cleanupUnconfirmed = true;}} catch {evidence.passed = false; evidence.cleanupUnconfirmed = true;}
    evidence.finishedAt = new Date().toISOString(); save(runDir, 'evidence.json', encode(evidence));
  }
  return evidence;
}
export async function runLive(options) {return run(options.runDir, options.node, await native(options));}

/** Fixed no-model selftest entry, not available from the live CLI. Real Provider
 * implementations still own both protocol clients, native bridge and guards. */
export async function runFixture({runDir, scenario = 'good', signal} = {}) {
  check(['good', 'wrong', 'bad-plan'].includes(scenario), 'invalid_fixture');
  const peer = here('./agent.fixture.mjs'), sdkEntry = here('../agent-provider-pi/fixtures/sdk/index.mjs');
  return run(runDir, process.execPath, {mode: 'deterministic-fixture', sourceHead: (await git(SOURCE, ['rev-parse', 'HEAD'])).trim(),
    versions: {pi: 'fixture-not-Pi', qwen: 'fixture-not-Qwen'}, entries: {peer: digest(fs.readFileSync(peer)), sdk: digest(fs.readFileSync(sdkEntry))},
    providers: [createPiProvider({id: 'pi-rpc', executable: process.execPath, args: [peer, 'pi'], env: {GIT_MIXED_FIXTURE: scenario}, bridge: {sdkEntry}}),
      createAcpProvider({id: 'qwen-acp', executable: process.execPath, args: [peer, 'qwen'], env: {GIT_MIXED_FIXTURE: scenario}})]}, signal);
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('--execute-real --run-dir ABS_NEW_PRIVATE_DIR --node ABS_NODE_24_15 --pi-entry ABS_PI_CLI --pi-sdk ABS_PI_SDK --qwen-entry ABS_QWEN_ENTRY --source-head CLEAN_SHA\n固定4 Attempts/2 Workers/10分钟，无模型重试；只自建两库，保留成功/失败现场，不宣称production。\n');
    else {const result = await runLive(options); process.stdout.write(JSON.stringify({passed: result.passed, mode: result.mode, taskId: result.taskId ?? null,
      failure: result.failure ?? null, evidence: path.join(options.runDir, 'evidence.json')}) + '\n'); if (!result.passed) process.exitCode = 1;}
  } catch (error) {process.stderr.write(JSON.stringify({code: error instanceof LiveError ? error.code : 'driver_preflight_failed'}) + '\n'); process.exitCode = 1;}
}
