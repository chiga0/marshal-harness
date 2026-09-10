import {supportsNode} from '../task-store/runtime.mjs';
// External, explicitly authorized real-Pi consumer. Not a distribution asset.
// The .fixture suffix only excludes this developer tool from package inventory;
// this consumer does not inject a fake Provider or fixture configuration.
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {verify} from '../task-distribution/index.mjs';

const check = (ok, code) => {if (!ok) throw new Error(code);};
// HTTP JSON has null-prototype records. Compare JSON values, not prototypes;
// never erase extra fields, sort arrays, invoke toJSON, or coerce scalar types.
export function sameJSON(a, b, code = 'evidence_mismatch') {
  let nodes = 0;
  function canonical(value, depth = 0) {
    check(++nodes <= 200000 && depth <= 64, 'comparison_limit');
    if (value === null || typeof value === 'boolean' || typeof value === 'string') return JSON.stringify(value);
    if (typeof value === 'number') {check(Number.isFinite(value), 'comparison_not_json'); return JSON.stringify(value);}
    check(value && typeof value === 'object' && (Array.isArray(value) || [null, Object.prototype].includes(Object.getPrototypeOf(value))), 'comparison_not_json');
    check(Object.getOwnPropertySymbols(value).length === 0, 'comparison_not_json');
    const names = Object.getOwnPropertyNames(value);
    if (Array.isArray(value)) {
      check(names.length === value.length + 1 && names.includes('length'), 'comparison_not_json');
      return '[' + Array.from({length: value.length}, (_, index) => {
        const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
        check(descriptor && Object.hasOwn(descriptor, 'value') && descriptor.enumerable, 'comparison_not_json');
        return canonical(descriptor.value, depth + 1);
      }).join(',') + ']';
    }
    return '{' + names.sort().map(name => {
      const descriptor = Object.getOwnPropertyDescriptor(value, name);
      check(Object.hasOwn(descriptor, 'value') && descriptor.enumerable, 'comparison_not_json');
      return JSON.stringify(name) + ':' + canonical(descriptor.value, depth + 1);
    }).join(',') + '}';
  }
  check(canonical(a) === canonical(b), code);
}
const same = (a, b, code = 'evidence_mismatch') => {
  if (Buffer.isBuffer(a) || Buffer.isBuffer(b)) check(Buffer.isBuffer(a) && Buffer.isBuffer(b) && a.equals(b), code);
  else sameJSON(a, b, code);
};
export function checkPlan(plan, limits) {
  check(Array.isArray(plan.nodes) && plan.nodes.length === 3 && Array.isArray(plan.edges) && plan.edges.length === 2, 'plan_members_mismatch');
  const nodes = plan.nodes.map(node => [node.id, node.role]).sort((a, b) => a[0].localeCompare(b[0]));
  same(nodes, [['east', 'author'], ['verify', 'verifier'], ['west', 'author']], 'plan_nodes_mismatch');
  // Sort copies of the original edge records, retaining every field. Exact
  // membership rejects duplicates/missing/extra edges; original Plan is intact.
  const edges = [...plan.edges].sort((a, b) => a.from.localeCompare(b.from) || a.to.localeCompare(b.to));
  same(edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], 'plan_edges_mismatch');
  same(plan.budget, limits, 'plan_budget_mismatch');
}
const digest = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const jsonDigest = value => digest(Buffer.from(JSON.stringify((function sorted(item) {
  if (Array.isArray(item)) return item.map(sorted);
  if (item !== null && typeof item === 'object') return Object.fromEntries(Object.keys(item).sort().map(key => [key, sorted(item[key])]));
  return item;
})(value))));
const observationId = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
// Optional observations do not authorize repair, retry reads, gate success, or
// claim atomic HTTP snapshots / node-level selected-result preservation.
export function repairObserver(taskId, {clock = Date.now, maxSamples = 128, maxSnapshots = 64, maxArtifacts = 16, intervalMs = 750} = {}) {
  const evidence = {taskId, authority: false, atomic: false, repairObserved: null, reworkCount: null,
    unaffectedBranchPreserved: null, independentTerminalSQLiteRequired: true, samples: 0, truncated: false,
    observationError: false, snapshots: [], negativeReviews: [], repairs: [], finalSelectionDigest: null};
  const seen = new Set(); let nextAt = 0, disabled = false, previous = null;
  const workersView = workers => workers.items.slice(0, 17).map(worker => ({id: worker.id, nodeId: worker.nodeId,
    role: worker.role, attempt: worker.attempt, status: worker.status})).sort((a, b) => a.id.localeCompare(b.id));
  async function artifact(client, id, timeoutMs, signal) {
    if (seen.has(id)) return null;
    if (seen.size >= maxArtifacts) {evidence.truncated = true; return null;}
    seen.add(id);
    const file = await client.downloadArtifact(id, {timeoutMs, signal});
    check(file.artifact.taskId === taskId && file.artifact.id === id && file.content.length <= 65536 && digest(file.content) === file.artifact.digest, 'observation_artifact_binding');
    return {artifactId: id, artifactDigest: file.artifact.digest, value: JSON.parse(file.content)};
  }
  return {evidence,
    async sample(client, task, deadline) {
      if (disabled || clock() < nextAt || deadline - clock() < 1500) return;
      if (evidence.samples >= maxSamples) {evidence.truncated = true; return;}
      nextAt = clock() + intervalMs; evidence.samples++;
      try {
        const timeoutMs = Math.min(1000, deadline - clock());
        const signal = AbortSignal.timeout(timeoutMs);
        const [leader, workers] = await Promise.all([client.getLeader(taskId, {timeoutMs, signal}),
          client.request('task.workers', {path: {taskId}, query: {limit: 100}, timeoutMs, signal})]);
        check(task.id === taskId && leader.taskId === taskId && workers.taskId === taskId, 'observation_identity');
        if (workers.nextCursor !== null || workers.items.length > 17) evidence.truncated = true;
        const snapshot = {taskRevision: task.revision, leaderTaskRevision: leader.taskRevision, planDigest: task.plan?.digest ?? null, stage: leader.stage,
          decisionDigest: leader.lastDecision?.digest ?? null, reviewDigest: leader.review?.digest ?? null,
          reviewVerdict: leader.review?.verdict ?? null, selectionDigest: leader.review?.selectionDigest ?? null, workers: workersView(workers)};
        const key = JSON.stringify(snapshot);
        if (previous !== key) {
          previous = key;
          if (evidence.snapshots.length < maxSnapshots) evidence.snapshots.push(snapshot); else evidence.truncated = true;
        }
        const review = leader.review;
        if (review?.verdict === 'rework') for (const id of review.evidenceIds) {
          const original = await artifact(client, id, timeoutMs, signal); if (!original) continue;
          const report = original.value.report;
          check(report?.profile === 'task-independent-review/v1' && report.verdict === 'rework' && report.selectionDigest === review.selectionDigest &&
            Array.isArray(report.findings) && report.findings.length <= 16 && report.findings.every(item => Array.isArray(item.nodeIds) && item.nodeIds.every(observationId)), 'observation_review_binding');
          check(jsonDigest({verdict: review.verdict, selectionDigest: review.selectionDigest, policyDigest: review.policyDigest,
            workerId: review.workerId, evidenceIds: review.evidenceIds}) === review.digest, 'observation_review_digest');
          evidence.negativeReviews.push({reviewDigest: review.digest, selectionDigest: review.selectionDigest, reviewerWorkerId: review.workerId,
            artifactId: original.artifactId, artifactDigest: original.artifactDigest,
            affectedAuthorNodeIds: [...new Set(report.findings.flatMap(item => item.nodeIds))].sort()});
        }
        if (leader.lastDecision) {
          const original = await artifact(client, leader.lastDecision.evidenceId, timeoutMs, signal);
          if (original) {
            const report = original.value.report;
            check(report?.profile === 'task-managed-leader/v1' && report.callId === leader.lastDecision.callId &&
              jsonDigest(report) === leader.lastDecision.digest && Array.isArray(report.actions) && report.actions.length <= 4, 'observation_decision_binding');
            for (const action of report.actions.filter(item => item.type === 'repair')) {
              check(Array.isArray(action.nodeIds) && action.nodeIds.length <= 64 && action.nodeIds.every(observationId) &&
                ['review', 'content-rejection', 'execution-failure'].includes(action.basis?.kind) && /^sha256:[a-f0-9]{64}$/.test(action.basis.digest), 'observation_repair_binding');
              evidence.repairs.push({decisionDigest: leader.lastDecision.digest, callId: report.callId, planDigest: snapshot.planDigest, artifactId: original.artifactId,
                artifactDigest: original.artifactDigest, basisKind: action.basis.kind, basisDigest: action.basis.digest,
                requestedNodeIds: [...action.nodeIds], affectedNodes: null,
                beforeSelectionDigest: evidence.negativeReviews.find(item => item.reviewDigest === action.basis.digest)?.selectionDigest ?? null,
                afterSelectionDigest: null, selectedWorkerBinding: 'unknown-requires-independent-terminal-sqlite'});
              evidence.repairObserved = true;
            }
          }
        }
      } catch {evidence.observationError = true; disabled = true;}
    },
    finish(audit, leader, workers) {
      evidence.reworkCount = audit.reworkCount;
      evidence.finalSelectionDigest = leader.review?.selectionDigest ?? null;
      evidence.finalWorkers = workersView(workers);
      if (evidence.repairs.length) evidence.repairObserved = true;
      else evidence.repairObserved = audit.reworkCount === 0 ? false : null;
      for (const repair of evidence.repairs) repair.afterSelectionDigest = evidence.finalSelectionDigest;
    }};
}
export function parseOptions(argv) {
  const names = ['package', 'manifest-digest', 'source-head', 'node', 'pi-entry', 'pi-sdk', 'run-dir'];
  const options = {};
  for (let i = 0; i < argv.length; i++) {
    const name = argv[i].slice(2);
    check(argv[i].startsWith('--') && !Object.hasOwn(options, name), 'invalid_arguments');
    if (['execute-real', 'allow-local-publication'].includes(name)) options[name] = true;
    else {check(names.includes(name) && argv[i + 1] && !argv[i + 1].startsWith('--'), 'invalid_arguments'); options[name] = argv[++i];}
  }
  check(options['execute-real'] === true && options['allow-local-publication'] === true && names.every(name => typeof options[name] === 'string'), 'explicit_authorization_required');
  check(/^[a-f0-9]{40}$/.test(options['source-head']) && /^sha256:[a-f0-9]{64}$/.test(options['manifest-digest']), 'invalid_pin');
  check(names.filter(name => !['source-head', 'manifest-digest'].includes(name)).every(name => path.isAbsolute(options[name])), 'absolute_paths_required');
  return options;
}
export function expectedReport(data, window, sourceDigest) {
  return {profile: 'leader-regional-window/v1', window, sourceDigest, reports: ['east', 'west'].map(region => {
    const rows = data.rows.filter(row => row.region === region && row.status === 'paid' && row.date >= window.startDate && row.date <= window.endDate);
    return {region, ...window, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)};
  })};
}
export async function waitPhase(read, wanted, deadline, sleep = pause) {
  while (Date.now() < deadline) {
    const task = await read();
    if (task.status === wanted) return task;
    check(!['failed', 'cancelled', 'intervention', 'completed'].includes(task.status), 'unexpected_terminal');
    await sleep(150);
  }
  throw new Error('phase_deadline');
}
export function checkAuthorization(authorization, {taskId, plan, report, audit, leader, now = Date.now()}) {
  check(audit.acceptance.status === 'passed' && leader.review.verdict === 'accept', 'acceptance_required');
  const artifactDigest = digest(report.content);
  same(authorization, {taskId, planDigest: plan.digest, artifactId: report.artifact.id, artifactDigest, bytes: report.content.length,
    acceptanceDigest: audit.acceptance.digest, reviewDigest: leader.review.digest, targetId: 'local-window-report',
    targetPolicyDigest: authorization.targetPolicyDigest, operation: 'create-if-absent',
    name: taskId + '-' + artifactDigest.slice(7) + '.json', expiresAt: authorization.expiresAt}, 'authorization_mismatch');
  check(/^sha256:[a-f0-9]{64}$/.test(authorization.targetPolicyDigest) && Date.parse(authorization.expiresAt) > now, 'authorization_expired');
}
function privateDirectory(directory) {
  const stat = fs.lstatSync(directory);
  check(stat.isDirectory() && !stat.isSymbolicLink() && stat.uid === process.getuid() && (stat.mode & 0o777) === 0o700 && fs.realpathSync(directory) === directory, 'private_directory_required');
}
// Only this closed metadata shape is retained. All other stderr is counted,
// never logged; chunks and lines are bounded before decoding.
export function diagnosticCollector(items = []) {
  const enums = {executionType: ['leader', 'review'], status: ['completed', 'failed', 'cancelled', 'unknown'],
    stopReason: ['end_turn', 'cancelled', 'error', 'length', 'toolUse', 'deferred', 'max_tokens', 'max_turn_requests', 'refusal'],
    reason: ['pi_agent_stop', 'pi_agent_error', 'pi_agent_aborted', 'pi_agent_length', 'pi_agent_toolUse', 'pi_agent_deferred',
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
      'custody_not_launched', 'custody_storage_limit', 'custody_storage_unavailable'],
    stage: ['provider-result', 'cleanup', 'parse'],
    parseCode: ['invalid_json', 'invalid_leader_result', 'invalid_leader_decision', 'invalid_review_report']};
  const keys = ['code', 'authority', 'taskId', 'workerId', 'providerId', ...Object.keys(enums)].sort();
  const rejectedKeys = ['authority', 'bytes', 'code', 'digest', 'encoding', 'executionType', 'head', 'providerId', 'tail',
    'taskId', 'truncated', 'wellformed', 'workerId'];
  const identity = value => ['taskId', 'workerId', 'providerId'].every(key => typeof value[key] === 'string' &&
    /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value[key]));
  const base64 = (value, maxBytes) => typeof value === 'string' &&
    (value.length === 0 || /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) &&
    Buffer.from(value, 'base64').length <= maxBytes && Buffer.from(value, 'base64').toString('base64') === value;
  // Bounded parse-stage raw-output copy: same closed-shape rule, separately
  // capped; arbitrary provider text stays base64 and never enters other fields.
  // Envelope invariant: reachable lines (failure ≤ ~800B enumeration/ids,
  // rejected ≤ ~3500B with 1536+512 payload) keep 32 + 8 records under the
  // 65536-byte stderr budget that launchService enforces with SIGTERM.
  const counts = {failure: 0, rejected: 0};
  let line = [], length = 0, dropping = false;
  return bytes => {
    for (const byte of bytes) {
      if (byte !== 10) {
        if (++length > 4096) {dropping = true; line = [];} else if (!dropping) line.push(byte);
        continue;
      }
      if (!dropping) try {
        const value = JSON.parse(new TextDecoder('utf-8', {fatal: true}).decode(Uint8Array.from(line)));
        if (value && typeof value === 'object' && !Array.isArray(value)) {
          if (value.code === 'managed_provider_failure' && length <= 2048 && counts.failure < 32 &&
            Object.keys(value).sort().join(',') === keys.join(',') && value.authority === false && identity(value) &&
            Object.entries(enums).every(([key, values]) => value[key] === null || values.includes(value[key]))) {
            counts.failure++; items.push(value);
          } else if (value.code === 'managed_provider_rejected_output' && counts.rejected < 8 &&
            Object.keys(value).sort().join(',') === rejectedKeys.join(',') && value.authority === false && identity(value) &&
            enums.executionType.includes(value.executionType) && value.encoding === 'utf8-base64' &&
            typeof value.wellformed === 'boolean' &&
            (value.bytes === null || Number.isSafeInteger(value.bytes) && value.bytes >= 0) &&
            (value.digest === null || /^sha256:[a-f0-9]{64}$/.test(value.digest)) && typeof value.truncated === 'boolean' &&
            base64(value.head, 1536) && base64(value.tail, 512) && Buffer.byteLength(JSON.stringify(value)) <= 4096) {
            counts.rejected++; items.push(value);
          }
        }
      } catch {}
      line = []; length = 0; dropping = false;
    }
  };
}
export function launchService(node, args, env, cwd, diagnostics = []) {
  const child = spawn(node, args, {env, cwd, stdio: ['ignore', 'pipe', 'pipe']});
  const collectDiagnostic = diagnosticCollector(diagnostics);
  let stdout = '', stderrBytes = 0, closed = false, ended, stopping, resolveReady, rejectReady;
  const ready = new Promise((resolve, reject) => {resolveReady = resolve; rejectReady = reject;});
  const exit = new Promise(resolve => {
    child.once('error', () => rejectReady(new Error('service_spawn_failed')));
    child.once('close', (code, signal) => {closed = true; ended = {code, signal, stderrBytes}; rejectReady(new Error('service_closed')); resolve(ended);});
  });
  child.stdout.on('data', bytes => {
    stdout += bytes.toString();
    if (stdout.length > 8192) {rejectReady(new Error('service_output_bound')); child.kill('SIGTERM');}
    else if (stdout.includes('\n')) {try {resolveReady(JSON.parse(stdout.split('\n')[0]));} catch {rejectReady(new Error('service_ready_invalid'));}}
  });
  child.stderr.on('data', bytes => {stderrBytes += bytes.length; collectDiagnostic(bytes); if (stderrBytes > 65536) child.kill('SIGTERM');});
  const timer = setTimeout(() => rejectReady(new Error('service_ready_deadline')), 10000);
  return {ready: ready.finally(() => clearTimeout(timer)), stop() {
    stopping ??= (async () => {
      if (!closed) child.kill('SIGTERM');
      const killTimer = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 15000);
      let deadlineTimer;
      try {
        await Promise.race([exit, new Promise((_, reject) => {deadlineTimer = setTimeout(() => reject(new Error('service_stop_deadline')), 20000);})]);
        check(ended.code === 0 && ended.signal === null && JSON.parse(stdout.trim().split('\n').at(-1)).clean === true, 'service_unclean');
        return ended;
      } finally {clearTimeout(killTimer); clearTimeout(deadlineTimer);}
    })(); return stopping;
  }};
}
export async function gracefulApprovalRestart({client, handle, start, taskId, task, plan, answer, answerReceipt, save}) {
  const before = {task: await client.getTask(taskId), plan: await client.request('task.plan', {path: {taskId}}),
    leader: await client.getLeader(taskId), audit: await client.getAudit(taskId),
    workers: await client.request('task.workers', {path: {taskId}, query: {limit: 100}}), answerReceipt};
  check(before.task.status === 'awaiting-approval' && before.workers.nextCursor === null &&
    (await client.request('supervisor.get')).activeWorkers === 0, 'approval_restart_not_quiescent');
  same(before.task, task, 'approval_restart_task_changed'); same(before.plan, plan, 'approval_restart_plan_changed');
  save('graceful-approval-restart-before.json', before);
  // launchService.stop only resolves after original CLI exit0 + clean:true.
  await handle.stop();
  const next = await start('open'), reopened = next.client;
  const after = {task: await reopened.getTask(taskId), plan: await reopened.request('task.plan', {path: {taskId}}),
    leader: await reopened.getLeader(taskId), audit: await reopened.getAudit(taskId),
    workers: await reopened.request('task.workers', {path: {taskId}, query: {limit: 100}})};
  same(after.task, before.task, 'approval_restart_task_changed');
  same(after.plan, before.plan, 'approval_restart_plan_changed');
  same(after.leader, before.leader, 'approval_restart_leader_changed');
  same(after.workers, before.workers, 'approval_restart_workers_changed');
  // Nonterminal elapsedMs is wall-clock time by the original Audit contract.
  const {elapsedMs: elapsedBefore, ...auditBefore} = before.audit, {elapsedMs: elapsedAfter, ...auditAfter} = after.audit;
  check(Number.isFinite(elapsedBefore) && Number.isFinite(elapsedAfter) && elapsedAfter >= elapsedBefore, 'approval_restart_elapsed_invalid');
  same(auditAfter, auditBefore, 'approval_restart_audit_changed');
  check((await reopened.request('supervisor.get')).activeWorkers === 0, 'approval_restart_new_worker');
  const replay = await reopened.request('task.leader.reply', answer);
  check(replay.replayed === true, 'approval_restart_reply_not_replayed');
  same({...replay, replayed: false}, answerReceipt, 'approval_restart_receipt_changed');
  after.answerReceipt = replay;
  save('graceful-approval-restart-after.json', after);
  return {...next, evidence: {passed: true, kind: 'graceful-approval-restart', activeFault: false, taskId,
    attemptsBefore: before.audit.attempts, attemptsAfter: after.audit.attempts, deadlineAt: after.task.deadlineAt,
    planDigest: plan.digest, budget: plan.budget, answerReplyDigest: replay.replyDigest, newWorkers: 0,
    before: 'graceful-approval-restart-before.json', after: 'graceful-approval-restart-after.json'}};
}
export async function run(options) {
  // Revalidate even programmatic callers before any execution.
  const o = parseOptions(Object.entries(options).flatMap(([key, value]) => value === true ? ['--' + key] : ['--' + key, value]));
  check(supportsNode() && fs.realpathSync(o.node) === fs.realpathSync(process.execPath), 'fixed_node_required');
  const manifest = verify({root: o.package, manifestDigest: o['manifest-digest']});
  check(manifest.sourceHead === o['source-head'], 'source_pin_mismatch');
  privateDirectory(path.dirname(o['run-dir']));
  const piRoot = path.resolve(path.dirname(o['pi-entry']), '../..');
  check(o['pi-entry'] === path.join(piRoot, 'dist/bundle/cli.js') && o['pi-sdk'] === path.join(piRoot, 'dist/index.js') &&
    fs.realpathSync(o['pi-entry']) === o['pi-entry'] && fs.realpathSync(o['pi-sdk']) === o['pi-sdk'], 'pi_identity');
  const pi = JSON.parse(fs.readFileSync(path.join(piRoot, 'package.json')));
  check(pi.name === '@earendil-works/pi-coding-agent' && typeof pi.version === 'string' && path.isAbsolute(process.env.HOME ?? ''), 'pi_identity');
  fs.mkdirSync(o['run-dir'], {mode: 0o700});
  const root = o['run-dir'], reportRoot = path.join(root, 'reports'), state = path.join(root, 'data');
  const evidence = {profile: 'installed-pi-leader-report-live/v1', passed: false, production: false, trueProcessOverlapProven: false,
    sourceHead: manifest.sourceHead, manifestDigest: o['manifest-digest'], nodeVersion: process.versions.node, piVersion: pi.version,
    piEntryDigest: digest(fs.readFileSync(o['pi-entry'])), piSdkDigest: digest(fs.readFileSync(o['pi-sdk'])), diagnostics: [], repairObservations: [], tasks: [], stage: 'loading'};
  const save = (name, value) => fs.writeFileSync(path.join(root, name), JSON.stringify(value), {mode: 0o600, flag: 'wx'});
  const handles = []; let reader;
  let interrupted = false;
  const interrupt = () => {interrupted = true; for (const handle of handles) void handle.stop().catch(() => {});};
  process.on('SIGINT', interrupt); process.on('SIGTERM', interrupt);
  try {
    const load = file => import(pathToFileURL(path.join(o.package, 'packages', file)).href);
    const [{TaskClient}, {taskBody}, {startReportServer}] = await Promise.all([load('task-client/index.mjs'), load('task-leader-report/policy.mjs'), load('task-leader-report/report-server.mjs')]);
    fs.mkdirSync(reportRoot, {mode: 0o700}); reader = await startReportServer({root: reportRoot, port: 0});
    const env = {PATH: path.dirname(o.node) + ':' + (process.env.PATH ?? '/usr/bin:/bin'), HOME: process.env.HOME,
      MARSHAL_PI_ENTRY: o['pi-entry'], MARSHAL_PI_SDK: o['pi-sdk'], MARSHAL_REPORT_ROOT: reportRoot, MARSHAL_REPORT_URL: reader.url};
    for (const key of ['LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (process.env[key]) env[key] = process.env[key];
    async function start(mode) {
      check(!interrupted, 'interrupted');
      same(verify({root: o.package, manifestDigest: o['manifest-digest']}), manifest);
      const handle = launchService(o.node, [path.join(o.package, manifest.entrypoint), '--root', state, '--mode', mode,
        '--port', '0', '--config', path.join(o.package, 'packages/task-leader-report/service-config.mjs')], env, root, evidence.diagnostics);
      handles.push(handle); const ready = await handle.ready;
      check((fs.statSync(ready.connectionFile).mode & 0o777) === 0o600, 'connection_mode');
      const connection = JSON.parse(fs.readFileSync(ready.connectionFile));
      return {handle, client: new TaskClient({baseURL: connection.url, token: connection.token, timeoutMs: 10000})};
    }
    let {handle, client} = await start('create'); const completed = [];
    for (const index of [0, 1]) {
      evidence.stage = 'task-' + index;
      const window = index === 0 ? {startDate: '2026-09-01', endDate: '2026-09-01'} : {startDate: '2026-09-02', endDate: '2026-09-03'};
      const data = {rows: [{date: '2026-09-01', region: 'east', status: 'paid', cents: index ? 300 : 100},
        {date: '2026-09-02', region: 'east', status: 'paid', cents: -20}, {date: '2026-09-02', region: 'east', status: 'paid', cents: 0},
        {date: '2026-09-03', region: 'west', status: 'paid', cents: index ? 350 : 150}, {date: '2026-09-03', region: 'west', status: 'cancelled', cents: 999999}]};
      const bytes = Buffer.from(JSON.stringify(data));
      const uploaded = await client.request('input.create', {idempotencyKey: 'live-input-' + index, body: {name: 'sales.json', mediaType: 'application/json', contentBase64: bytes.toString('base64')}});
      const create = {idempotencyKey: 'live-task-' + index, body: taskBody(uploaded.id)};
      const created = await client.request('task.create', create), taskId = created.id, deadline = Date.parse(created.deadlineAt);
      const observer = repairObserver(taskId); evidence.repairObservations.push(observer.evidence);
      const phase = status => {evidence.stage = 'task-' + index + '-' + status; return waitPhase(async () => {
        check(!interrupted, 'interrupted'); const task = await client.getTask(taskId); await observer.sample(client, task, deadline); return task;
      }, status, deadline);};
      let task = await phase('awaiting-answer'), leader = await client.getLeader(taskId);
      check(leader.pendingRequest.kind === 'business' && task.plan === null, 'business_question_required');
      const answer = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'live-answer-' + index,
        body: {expectedRevision: task.revision, requestDigest: leader.pendingRequest.requestDigest, answer: JSON.stringify(window)}};
      const answerReceipt = await client.request('task.leader.reply', answer);
      task = await phase('awaiting-approval'); const plan = await client.request('task.plan', {path: {taskId}});
      checkPlan(plan, create.body.limits);
      if (index === 0) {
        evidence.stage = 'graceful-approval-restart';
        evidence.gracefulApprovalRestart = {passed: false, kind: 'graceful-approval-restart', activeFault: false, taskId};
        const restarted = await gracefulApprovalRestart({client, handle, start, taskId, task, plan, answer, answerReceipt, save});
        ({client, handle} = restarted); evidence.gracefulApprovalRestart = restarted.evidence;
      }
      const approve = {path: {taskId}, idempotencyKey: 'live-approve-' + index,
        body: {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}};
      const approval = await client.request('task.approve', approve);
      task = await phase('awaiting-confirmation'); leader = await client.getLeader(taskId);
      check(leader.pendingRequest.kind === 'publication', 'publication_request_required');
      const authorization = leader.pendingRequest.authorization, report = await client.downloadArtifact(authorization.artifactId), audit = await client.getAudit(taskId);
      same(JSON.parse(report.content), expectedReport(data, window, digest(bytes)));
      checkAuthorization(authorization, {taskId, plan, report, audit, leader});
      check(Date.parse(authorization.expiresAt) <= deadline && fs.readdirSync(reportRoot).length === index, 'publication_boundary');
      save('task-' + index + '-authorization.json', authorization);
      const allow = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'live-allow-' + index,
        body: {expectedRevision: task.revision, requestDigest: leader.pendingRequest.requestDigest, decision: 'allow'}};
      const allowReceipt = await client.request('task.leader.reply', allow);
      const done = await phase('completed'), final = await client.getLeader(taskId), finalAudit = await client.getAudit(taskId);
      check(final.review.verdict === 'accept' && final.publication.status === 'succeeded' && final.postverify.status === 'succeeded' && final.stage === 'terminal', 'delivery_incomplete');
      check(done.deadlineAt === created.deadlineAt && finalAudit.acceptance.status === 'passed' && finalAudit.attempts <= 17, 'budget_or_acceptance');
      for (const id of [final.summaryArtifactId, ...final.review.evidenceIds, final.publication.receiptArtifactId, final.postverify.evidenceArtifactId, ...finalAudit.acceptance.evidenceIds])
        check((await client.downloadArtifact(id)).artifact.taskId === taskId, 'artifact_binding');
      const response = await fetch(new URL(authorization.name, reader.url), {redirect: 'error', signal: AbortSignal.timeout(5000)});
      check(response.status === 200 && Number(response.headers.get('content-length')) === report.content.length, 'published_get');
      same(Buffer.from(await response.arrayBuffer()), Buffer.from(report.content));
      fs.writeFileSync(path.join(root, 'task-' + index + '-report.json'), report.content, {mode: 0o600, flag: 'wx'});
      const workers = await client.request('task.workers', {path: {taskId}, query: {limit: 100}});
      observer.finish(finalAudit, final, workers);
      check(workers.nextCursor === null && (await client.request('supervisor.get')).activeWorkers === 0, 'workers_unsettled');
      completed.push({taskId, create, created, approve, approval, answer, answerReceipt, allow, allowReceipt, done, final, audit: finalAudit, workers, report});
      evidence.tasks.push({taskId, status: done.status, artifactDigest: digest(report.content), attempts: finalAudit.attempts,
        reworkCount: finalAudit.reworkCount, retryCount: finalAudit.retryCount, measurement: finalAudit.measurement,
        reviewDigest: final.review.digest, summaryArtifactId: final.summaryArtifactId,
        publicationReceiptArtifactId: final.publication.receiptArtifactId, postverifyEvidenceArtifactId: final.postverify.evidenceArtifactId});
    }
    evidence.stage = 'cold-replay'; await handle.stop();
    const files = fs.readdirSync(reportRoot).sort().map(name => {const stat = fs.statSync(path.join(reportRoot, name)); return {name, ino: stat.ino, mtimeMs: stat.mtimeMs};});
    ({handle, client} = await start('open'));
    for (const item of completed) {
      same(await client.request('task.create', item.create), item.created); same(await client.request('task.approve', item.approve), item.approval);
      for (const [request, receipt] of [[item.answer, item.answerReceipt], [item.allow, item.allowReceipt]]) {
        const replay = await client.request('task.leader.reply', request); check(replay.replayed === true, 'replay_required'); same({...replay, replayed: false}, receipt);
      }
      same(await client.getTask(item.taskId), item.done); same(await client.getLeader(item.taskId), item.final); same(await client.getAudit(item.taskId), item.audit);
      same(await client.request('task.workers', {path: {taskId: item.taskId}, query: {limit: 100}}), item.workers);
      same(Buffer.from((await client.downloadArtifact(item.report.artifact.id)).content), Buffer.from(item.report.content));
    }
    await handle.stop();
    same(fs.readdirSync(reportRoot).sort().map(name => {const stat = fs.statSync(path.join(reportRoot, name)); return {name, ino: stat.ino, mtimeMs: stat.mtimeMs};}), files, 'publication_files_changed');
    same(verify({root: o.package, manifestDigest: o['manifest-digest']}), manifest);
    evidence.passed = true; evidence.stage = 'completed';
  } catch (error) {evidence.failure = error?.code && /^[a-z_]{1,64}$/.test(error.code) ? error.code :
    /^[a-z_]{1,64}$/.test(error?.message ?? '') ? error.message : 'live_acceptance_failed';}
  finally {
    for (const handle of handles) try {await handle.stop();} catch {evidence.passed = false; evidence.cleanupFailure = true;}
    try {await reader?.close();} catch {evidence.passed = false; evidence.readerFailure = true;}
    if (interrupted) {evidence.passed = false; evidence.interrupted = true;}
    process.off('SIGINT', interrupt); process.off('SIGTERM', interrupt);
    save('evidence.json', evidence);
  }
  return evidence;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {const result = await run(parseOptions(process.argv.slice(2))); process.stdout.write(JSON.stringify(result) + '\n'); if (!result.passed) process.exitCode = 1;}
  catch {process.stderr.write('{"code":"live_preflight_failed"}\n'); process.exitCode = 1;}
}
