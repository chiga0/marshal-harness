import {supportsNode} from '../task-store/runtime.mjs';
// Explicit ordinary-user real-model acceptance tool, never a CI model job or
// production entry point. Two invocations, no invented error or automatic retry.
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
import {parseOptions as piOptions, filePermission} from '../task-pi-live/driver.fixture.mjs';
import {executionFact, assertTeam} from '../task-qwen-live/driver.fixture.mjs';
import {data, regions, policy, repairPort, repairPolicyDigest, proposal, taskBody, bindPlan, originalInput,
  expectedReports, reportShape, consumeDelivery, equal, contentAssertions} from './scenario.fixture.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
const checkerPath = here('./checker.fixture.mjs');
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const check = (value, code) => {if (!value) throw new LiveError(code);};
export class LiveError extends Error {constructor(code) {super(code); this.code = code;}}
export function parseOptions(argv) {
  if (equal(argv, ['--help'])) return {help: true};
  const own = new Map(), rest = [], names = ['--phase', '--task-id', '--node-id', '--expected-revision', '--plan-digest', '--decision-digest', '--feedback'];
  for (let at = 0; at < argv.length; at++) {
    if (!names.includes(argv[at])) {rest.push(argv[at]); continue;}
    const name = argv[at]; check(!own.has(name) && text(argv[at + 1]) && !argv[at + 1].startsWith('--'), 'invalid_arguments');
    own.set(name, argv[++at]);
  }
  const base = piOptions(rest), phase = own.get('--phase'); check(['initial', 'repair'].includes(phase), 'explicit_phase_required');
  if (phase === 'initial') {check(own.size === 1, 'initial_cannot_preapprove_repair'); return {...base, phase};}
  check(own.size === names.length && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(own.get('--task-id') ?? '') &&
    regions.includes(own.get('--node-id')) && /^[1-9][0-9]*$/.test(own.get('--expected-revision') ?? '') &&
    Number.isSafeInteger(Number(own.get('--expected-revision'))) && sha(own.get('--plan-digest')) && sha(own.get('--decision-digest')) &&
    own.get('--feedback').trim() && Buffer.byteLength(own.get('--feedback')) <= 4096, 'exact_repair_authorization_required');
  return {...base, phase, taskId: own.get('--task-id'), nodeId: own.get('--node-id'), expectedRevision: Number(own.get('--expected-revision')),
    planDigest: own.get('--plan-digest'), decisionDigest: own.get('--decision-digest'), feedback: own.get('--feedback')};
}
export function validatePlan(task, plan, body, inputId) {
  check(task.status === 'awaiting-approval' && task.id === plan.taskId && task.plan?.digest === plan.digest && task.plan.revision === plan.revision,
    'plan_identity_mismatch');
  const declared = proposal();
  check(equal(plan.nodes, declared.nodes) && equal(plan.edges, declared.edges) && equal(plan.deliverables, declared.deliverables) &&
    equal(plan.assumptions, []) && equal(plan.budget, body.limits) && equal(plan.repair, {profile: 'task-local-repair/v1', policyDigest: repairPolicyDigest}) &&
    plan.interaction === undefined, 'plan_scope_mismatch');
  const binding = bindPlan({inputArtifacts: [{id: inputId}], proposal: plan});
  const visible = [{policy, description: binding.description}, ...binding.layouts.sort((a, b) => a.nodeId.localeCompare(b.nodeId)).map(layout => ({layout})),
    ...binding.deliveries.sort((a, b) => a.targetPath.localeCompare(b.targetPath)).map(delivery => ({delivery}))];
  let actual; try {actual = plan.acceptance.slice(-visible.length).map(value => parseJson(Buffer.from(value)));} catch {throw new LiveError('plan_acceptance_missing');}
  check(equal(actual, visible) && plan.acceptance.includes(declared.acceptance[0]), 'plan_acceptance_missing');
  return {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
}
export function classify(task, audit) {
  if (task.status === 'completed' && audit.acceptance.status === 'passed') return {outcome: 'natural-first-pass', nodeId: null};
  const rejection = audit.decision?.contentRejection;
  if (task.status !== 'failed' || audit.acceptance.status !== 'failed' || audit.decision?.status !== 'rejected' ||
      audit.decision.digest !== audit.acceptance.digest || rejection?.policyDigest !== repairPolicyDigest ||
      rejection.failedAssertions?.length !== 1 || !contentAssertions.includes(rejection.failedAssertions[0]) ||
      !task.allowedActions.includes('repair') || audit.attempts !== 4 || audit.retryCount !== 0 || audit.reworkCount !== 0)
    return {outcome: 'not-repair-eligible', nodeId: null};
  return {outcome: 'awaiting-explicit-repair', nodeId: rejection.failedAssertions[0].slice(0, -'-content'.length)};
}
export function repairRequest(options, old, current, audit) {
  const eligible = classify(current, audit);
  check(eligible.outcome === 'awaiting-explicit-repair' && options.nodeId === eligible.nodeId && options.taskId === old.created.id &&
    current.id === old.created.id && equal(current, old.failed) && equal(audit, old.audit) &&
    options.expectedRevision === current.revision && options.planDigest === old.plan.digest &&
    options.decisionDigest === audit.decision.digest && Date.now() < Date.parse(current.deadlineAt), 'repair_authorization_stale');
  return {path: {taskId: current.id}, idempotencyKey: 'explicit-content-repair', body: {expectedRevision: options.expectedRevision,
    planDigest: options.planDigest, decisionDigest: options.decisionDigest, nodeIds: [options.nodeId], feedback: options.feedback}};
}
export async function repairOnce(client, request) {return client.request('task.repair', request);}
export function validateDeliveryProof(proof, artifact, planDigest, checkerDigest) {
  check(proof?.binding?.planDigest === planDigest && proof.binding.checkerDigest === checkerDigest &&
    proof.binding.policyDigest === digest(encode(policy)) && proof.delivery?.digest === artifact.digest && proof.delivery.bytes === artifact.bytes &&
    equal(proof.assertions, expectedReports().map((actual, at) => ({name: contentAssertions[at], actual}))
      .concat({name: 'report-structure', actual: expectedReports()})), 'final_evidence_delivery_binding');
}
function read(file, limit = 1048576) {
  const stat = fs.lstatSync(file); check(stat.isFile() && stat.nlink === 1 && stat.size > 0 && stat.size <= limit, 'evidence_file_boundary');
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {check(fs.fstatSync(fd).ino === stat.ino, 'evidence_file_boundary'); return fs.readFileSync(fd);} finally {fs.closeSync(fd);}
}
function save(root, name, bytes) {
  const fd = fs.openSync(path.join(root, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  const directory = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); try {fs.fsyncSync(directory);} finally {fs.closeSync(directory);}
}
const plain = value => structuredClone(value);
const request = (client, name, options) => client.request(name, options).then(plain);
const get = (client, name, taskId) => request(client, name, {path: {taskId}});
async function terminal(client, taskId, deadline) {
  for (;;) {const task = plain(await client.getTask(taskId));
    if (['completed', 'failed', 'cancelled', 'intervention'].includes(task.status)) return task;
    check(Date.now() < deadline, 'observation_deadline'); await pause(100);}
}
async function negativeProof(client, audit, planDigest) {
  check(audit.acceptance.evidenceIds.length === 1, 'negative_evidence_missing');
  const artifact = await client.downloadArtifact(audit.acceptance.evidenceIds[0]), proof = parseJson(artifact.content);
  check(artifact.artifact.kind === 'evidence' && proof.binding?.planDigest === planDigest && typeof proof.originalReport === 'string' &&
    proof.originalReport.endsWith('\n') && digest(Buffer.from(proof.originalReport)) === proof.reportDigest &&
    proof.reportDigest === audit.decision.contentRejection.reportDigest && sha(proof.requestDigest), 'negative_evidence_binding');
  const original = parseJson(Buffer.from(proof.originalReport));
  check(encode(original).toString() + '\n' === proof.originalReport && equal(original.binding, proof.binding) && original.nonce === proof.nonce &&
    equal(proof.parentAssertions, contentAssertions.map(name => ({name, passed: !audit.decision.contentRejection.failedAssertions.includes(name)}))
      .concat({name: 'report-structure', passed: true})), 'negative_parent_facts_missing');
  return {artifact: plain(artifact.artifact), content: artifact.content.toString('utf8')};
}
function nativeIdentity(options) {
  check(options?.executeReal === true, 'explicit_real_execution_required');
  check(supportsNode() && fs.realpathSync(options.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  const root = path.resolve(path.dirname(options.piEntry), '../..');
  check(options.piEntry === path.join(root, 'dist/bundle/cli.js') && options.sdkEntry === path.join(root, 'dist/index.js') &&
    fs.realpathSync(options.piEntry) === options.piEntry && fs.realpathSync(options.sdkEntry) === options.sdkEntry, 'pi_entry_identity');
  const metadata = parseJson(read(path.join(root, 'package.json')));
  check(metadata.name === '@earendil-works/pi-coding-agent' && text(metadata.version), 'pi_entry_identity');
  return {nodeVersion: process.versions.node, piVersion: metadata.version,
    piEntryDigest: digest(read(options.piEntry, 33554432)), sdkEntryDigest: digest(read(options.sdkEntry, 33554432)),
    checkerDigest: digest(read(checkerPath)), toolDigest: digest(encode(['driver.fixture.mjs', 'scenario.fixture.mjs', 'checker.fixture.mjs'].map(name => digest(read(here('./' + name))))))};
}
/** Only explicit invocation starts models. A first-pass result is not repair evidence. */
export async function runLive(options) {
  const identity = nativeIdentity(options), initial = options.phase === 'initial';
  check(['initial', 'repair'].includes(options.phase) && fs.realpathSync(path.dirname(options.runDir)) === path.dirname(options.runDir), 'run_parent_identity');
  if (initial) fs.mkdirSync(options.runDir, {mode: 0o700});
  const rootStat = fs.lstatSync(options.runDir);
  check(rootStat.isDirectory() && rootStat.uid === process.getuid() && (rootStat.mode & 0o777) === 0o700 && fs.realpathSync(options.runDir) === options.runDir, 'private_run_root_required');
  const evidence = {profile: 'pi-same-plan-repair-live/v1', phase: options.phase, outcome: 'unconfirmed', ...identity,
    startedAt: new Date().toISOString(), ordinaryUser: true, production: false, publisherSeparationProven: false,
    modelErrorInjected: false, usage: {status: 'unavailable', tokens: null, cost: null}, executions: [], verifications: [], permission: {allowed: 0, denied: 0}};
  const observations = [], tickets = new Map(), byWorker = new Map(); let service, checkpoint, stage = 'starting', valid = false;
  try {
    const old = initial ? null : parseJson(read(path.join(options.runDir, 'initial.json')));
    if (old) check(equal(old.identity, identity), 'frozen_tool_or_native_identity_drift');
    const env = {PATH: path.dirname(options.node) + ':' + (process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin')};
    for (const name of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (text(process.env[name])) env[name] = process.env[name];
    check(path.isAbsolute(env.HOME ?? ''), 'native_home_missing');
    const native = createPiProvider({id: 'pi', executable: options.node, args: [options.piEntry, '--mode', 'rpc', '--no-session'], env,
      bridge: {sdkEntry: options.sdkEntry}, custodyProfile: {id: 'pi-native-file-repair-v1', scope: 'inherited-process-group', eligible: true}});
    const provider = {...native, start(input) {const ticket = tickets.get(input.cwd); check(ticket, 'missing_original_ticket');
      const handle = native.start(input); observations.push({identity: {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId,
        role: ticket.role, repairId: ticket.repairId}, handle}); return handle;}};
    const repair = repairPort(), command = createVerificationCommand({executable: options.node, checkerPath,
      checkerDigest: identity.checkerDigest, policyDigest: digest(encode(policy)), request: ({ticket}) => originalInput(ticket),
      repair: {policyDigest: repair.policyDigest, assertions: [...contentAssertions]},
      assertions: regions.map((region, at) => ({name: region + '-content', validate: value => equal(value, expectedReports()[at])}))
        .concat({name: 'report-structure', validate: values => Array.isArray(values) && values.length === 2 && values.every((value, at) => reportShape(value, regions[at]))}),
      delivery({prepared}) {
        const files = regions.map(region => ({path: region + '.json', content: read(path.join(prepared.cwd, region + '.json'), 4096).toString('utf8')}));
        return {name: 'latest-paid-regions.json', mediaType: 'application/json', content: encode({files})};
      }});
    function startChecker(input) {
      evidence.verifications.push({workerId: input.ticket.workerId, repairId: input.ticket.repairId,
        manifests: plain(input.ticket.input.verification.manifests), upstream: plain(input.ticket.input.upstream),
        files: regions.map(region => {const bytes = read(path.join(input.prepared.cwd, region + '.json'), 4096);
          return {path: region + '.json', digest: digest(bytes), bytes: bytes.length};})});
      return command.start(input);
    }
    startChecker.custodyProfile = command.start.custodyProfile; startChecker.repairBinding = command.start.repairBinding;
    const verification = createVerificationPort({id: 'independent-latest-paid-checker', policy, bindPlan,
      repairPolicyDigests: [repair.policyDigest], start: startChecker});
    const config = {root: path.join(options.runDir, 'data'), custody: {profile: 'node-execution-custody/v1'}, repair,
      providers: new Map([[provider.id, provider]]), verification, supervisorOptions: {intervalMs: 50},
      businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
        const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          authorize: (ticket, call) => {const decision = filePermission(byWorker.get(ticket.workerId), call);
            evidence.permission[decision.outcome.outcome === 'selected' ? 'allowed' : 'denied']++; return decision;}});
        return {...business, async prepare(ticket, context) {const prepared = await business.prepare(ticket, context);
          tickets.set(prepared.cwd, ticket); byWorker.set(ticket.workerId, {...ticket, cwd: prepared.cwd}); return prepared;}};
      }};
    const open = async mode => {service = await startTaskService({...config, mode});
      const connection = parseJson(read(service.connectionFile)); return new TaskClient({baseURL: connection.url, token: connection.token});};
    let client = await open(initial ? 'create' : 'open'), saved = old;
    if (initial) {
      const input = await request(client, 'input.create', {idempotencyKey: 'repair-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
      const body = taskBody(input.id, options.timeoutMs), created = await request(client, 'task.create', {idempotencyKey: 'repair-create', body});
      evidence.taskId = created.id; stage = 'planning'; let ready;
      for (;;) {ready = plain(await client.getTask(created.id)); if (ready.status === 'awaiting-approval') break;
        check(!['failed', 'cancelled', 'intervention'].includes(ready.status) && Date.now() < Date.parse(created.deadlineAt), 'planning_failed'); await pause(100);}
      const plan = await get(client, 'task.plan', created.id), approval = validatePlan(ready, plan, body, input.id);
      const operation = await request(client, 'task.approve', {path: {taskId: created.id}, idempotencyKey: 'repair-approve', body: approval}); stage = 'initial-authors';
      const done = await terminal(client, created.id, Date.parse(created.deadlineAt)), audit = await get(client, 'task.audit', created.id);
      const workers = (await get(client, 'task.workers', created.id)).items;
      evidence.executions = await Promise.all(observations.map(async ({identity: id, handle}) => executionFact(id, await handle.completion)));
      evidence.overlapMs = assertTeam(evidence.executions); evidence.attempts = audit.attempts;
      check(audit.attempts === 4 && evidence.verifications.length === 1 && workers.length === 4, 'initial_execution_set_mismatch');
      const classification = classify(done, audit); evidence.outcome = classification.outcome;
      saved = {identity, body, created, plan, approval, operation, workers, failed: done, audit, verification: evidence.verifications[0]};
      if (classification.outcome === 'awaiting-explicit-repair') {
        saved.negative = await negativeProof(client, audit, plan.digest); checkpoint = saved;
        save(options.runDir, 'negative-evidence.json', Buffer.from(saved.negative.content));
        evidence.authorizationRequired = {taskId: created.id, nodeId: classification.nodeId, expectedRevision: done.revision,
          planDigest: plan.digest, decisionDigest: audit.decision.digest, deadlineAt: done.deadlineAt, remainingAttempts: 2,
          feedbackRequired: true, key: 'explicit-content-repair'};
      } else if (classification.outcome !== 'natural-first-pass') throw new LiveError('initial_not_content_repair_eligible');
    } else {
      evidence.taskId = old.created.id; stage = 'repair-authorization';
      check(equal(await get(client, 'task.plan', old.created.id), old.plan) && equal((await get(client, 'task.workers', old.created.id)).items, old.workers) &&
        equal(await negativeProof(client, old.audit, old.plan.digest), old.negative), 'original_failure_drift');
      const current = plain(await client.getTask(old.created.id)), audit = await get(client, 'task.audit', old.created.id);
      const mutation = repairRequest(options, old, current, audit); evidence.requestDigest = digest(encode(mutation));
      // A durable one-shot marker is diagnostic only, not an authority receipt.
      // If the response is lost, do not silently repeat this paid operation.
      save(options.runDir, 'repair-request.json', encode(mutation)); evidence.repairSubmission = 'unknown';
      const receipt = plain(await repairOnce(client, mutation)); evidence.repairSubmission = 'accepted'; evidence.repairId = receipt.repairId;
      check(receipt.replayed === false && receipt.operation.status === 'accepted' && receipt.acceptedRevision === current.revision + 1 &&
        equal(receipt.affectedNodes, [options.nodeId, 'verify']), 'repair_receipt_mismatch');
      saved = {...old, mutation, receipt}; stage = 'repairing';
      const done = await terminal(client, old.created.id, Date.parse(old.created.deadlineAt)), finalAudit = await get(client, 'task.audit', old.created.id);
      evidence.attempts = finalAudit.attempts; evidence.executions = await Promise.all(observations.map(async ({identity: id, handle}) => executionFact(id, await handle.completion)));
      check(done.status === 'completed' && finalAudit.acceptance.status === 'passed' && finalAudit.decision?.status === 'accepted' &&
        finalAudit.decision.digest === finalAudit.acceptance.digest && finalAudit.decision.digest !== old.audit.decision.digest &&
        finalAudit.decision.contentRejection === null && finalAudit.attempts === 6 && finalAudit.reworkCount === 1 &&
        finalAudit.retryCount === 0 && done.deadlineAt === old.created.deadlineAt && evidence.executions.length === 1 && evidence.verifications.length === 1 &&
        evidence.executions[0].nodeId === options.nodeId && evidence.executions[0].repairId === receipt.repairId, 'repair_not_completed');
      check(equal(await get(client, 'task.plan', old.created.id), old.plan) && equal(await negativeProof(client, old.audit, old.plan.digest), old.negative), 'original_plan_or_negative_changed');
      const workers = (await get(client, 'task.workers', old.created.id)).items;
      check(equal(workers.filter(worker => old.workers.some(original => original.id === worker.id)), old.workers) && workers.length === 6, 'old_worker_changed');
      const retained = regions.find(region => region !== options.nodeId), latest = evidence.verifications[0];
      check(latest.repairId === receipt.repairId && equal(latest.manifests.find(item => item.nodeId === retained), old.verification.manifests.find(item => item.nodeId === retained)) &&
        equal(latest.files.find(item => item.path === retained + '.json'), old.verification.files.find(item => item.path === retained + '.json')) &&
        latest.manifests.length === 2 && latest.upstream.length === 2 &&
        equal(latest.upstream.map(item => item.workerId).sort(), latest.manifests.map(item => item.workerId).sort()) &&
        latest.manifests.find(item => item.nodeId === options.nodeId)?.workerId === evidence.executions[0].workerId &&
        (await client.request('operation.get', {path: {operationId: receipt.operation.id}})).status === 'succeeded', 'selected_results_mismatch');
      evidence.retained = {nodeId: retained, manifest: latest.manifests.find(item => item.nodeId === retained), file: latest.files.find(item => item.path === retained + '.json')};
      evidence.outcome = 'natural-content-repair-completed';
    }
    if (evidence.outcome !== 'awaiting-explicit-repair') {
      stage = 'download'; const task = plain(await client.getTask(saved.created.id));
      const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id))), deliveries = artifacts.filter(item => item.artifact.kind === 'delivery');
      check(deliveries.length === 1, 'delivery_missing'); const delivery = deliveries[0];
      const proofs = artifacts.filter(item => item.artifact.kind === 'evidence'); check(proofs.length === 1, 'final_evidence_missing');
      validateDeliveryProof(parseJson(proofs[0].content), delivery.artifact, saved.plan.digest, identity.checkerDigest);
      evidence.acceptance = plain(await get(client, 'task.audit', saved.created.id)).acceptance;
      evidence.consumer = consumeDelivery(delivery.content); save(options.runDir, initial ? 'first-pass-delivery.json' : 'repaired-delivery.json', delivery.content);
      evidence.delivery = plain(delivery.artifact);
      check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed'); service = null; client = await open('open');
      check(equal(await request(client, 'task.create', {idempotencyKey: 'repair-create', body: saved.body}), saved.created) &&
        equal(await request(client, 'task.approve', {path: {taskId: saved.created.id}, idempotencyKey: 'repair-approve', body: saved.approval}), saved.operation), 'original_receipt_drift');
      if (!initial) {const replay = await request(client, 'task.repair', saved.mutation);
        check(replay.replayed === true && equal({...replay, replayed: false, currentTask: null}, {...saved.receipt, currentTask: null}) &&
          equal(replay.currentTask, task), 'repair_receipt_drift');}
      const reopened = await client.downloadArtifact(delivery.artifact.id);
      check(equal(reopened.artifact, delivery.artifact) && reopened.content.equals(delivery.content) &&
        observations.length === (initial ? 3 : 1) && evidence.verifications.length === 1, 'cold_delivery_or_duplicate');
      evidence.restart = {sameArtifact: true, originalReceipts: true, duplicateStarts: 0};
    }
    valid = true;
  } catch (error) {evidence.failure = {stage, code: error instanceof LiveError ? error.code : 'repair_live_dependency_failed'};}
  finally {
    let clean = true;
    if (service) try {check((await service.shutdown()).shutdownClean === true, 'shutdown_unconfirmed');} catch {clean = false; evidence.failure = {stage: 'shutdown', code: 'shutdown_unconfirmed'};}
    for (const {identity: original, handle} of observations) try {
      const result = await handle.stop(); if (result?.cleanup?.cleaned !== true) clean = false;
      if (!evidence.executions.some(item => item.workerId === original.workerId)) evidence.executions.push({...original,
        status: ['completed', 'failed', 'cancelled'].includes(result?.status) ? result.status : 'unknown',
        cleanup: result?.cleanup?.cleaned === true, executionId: result?.cleanup?.started?.executionId ?? null});
    } catch {clean = false;}
    valid = valid && clean;
    evidence.cleanupConfirmed = clean; evidence.validObservation = valid; evidence.finishedAt = new Date().toISOString();
    if (valid && checkpoint) save(options.runDir, 'initial.json', encode(checkpoint));
    save(options.runDir, initial ? 'initial-evidence.json' : 'repair-evidence.json', encode(evidence));
  }
  return evidence;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const options = parseOptions(process.argv.slice(2));
    if (options.help) process.stdout.write('显式固定业务实机工具；只用公开合成数据，不保证自然错误。\n' +
      '--execute-real --phase initial|repair --run-dir ABS --node ABS_NODE_24_15 --pi-entry ABS_dist/bundle/cli.js --pi-sdk ABS_dist/index.js [--timeout-ms 900000]\n' +
      'repair 另需 --task-id ID --node-id east|west --expected-revision N --plan-digest SHA --decision-digest SHA --feedback TEXT；先阅读 initial-evidence/negative-evidence。\n');
    else {const evidence = await runLive(options); process.stdout.write(JSON.stringify({outcome: evidence.outcome, validObservation: evidence.validObservation,
      taskId: evidence.taskId ?? null, failure: evidence.failure ?? null, authorizationRequired: evidence.authorizationRequired ?? null,
      evidence: path.join(options.runDir, options.phase === 'initial' ? 'initial-evidence.json' : 'repair-evidence.json')}) + '\n');
      if (!evidence.validObservation) process.exitCode = 1;}
  } catch (error) {process.stderr.write(JSON.stringify({code: error instanceof LiveError ? error.code : 'repair_live_preflight_failed'}) + '\n'); process.exitCode = 1;}
}
