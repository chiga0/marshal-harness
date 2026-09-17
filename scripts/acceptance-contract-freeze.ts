// ADR0107 的离线合同候选检查器。
//
// 这里只保护候选合同的摘要域、绑定关系和拒绝变异行为；它不是
// TaskApplication 的 verifier，也不登记 capability、不写 Store、不授予 Core
// 权威。正式实现前必须把本文件测试保护的候选字段重新冻结为闭合协议。
import {encode, digest} from '../packages/task-store/store.ts';

export const ACCEPTANCE_EVIDENCE_PROFILE = 'task-acceptance-evidence/v1';
export const ATTEMPT_REF_PROFILE = 'task-attempt-ref/v1';

const ENTRY_KEYS = [
  'contractDigest', 'checkId', 'method', 'capabilityId', 'capabilityDigest',
  'owner', 'planDigest', 'repairId', 'candidateDigest', 'selectionDigest',
  'externalAction', 'receiptDigest', 'sourceArtifact', 'applicability', 'result',
];
const ATTEMPT_KEYS = [
  'profile', 'taskId', 'workerId', 'commandId', 'generation',
  'reservationDigest', 'reservationEvent',
];
const RESERVATION_EVENT_KEYS = ['stream', 'sequence', 'digest'];
const OWNER_KEYS = ['kind', 'workerId', 'attemptRef', 'generation'];
const ARTIFACT_REF_KEYS = ['id', 'digest'];

const object = value => value !== null && typeof value === 'object' && !Array.isArray(value) &&
  [Object.prototype, null].includes(Object.getPrototypeOf(value));
const closed = (value, keys) => object(value) && Reflect.ownKeys(value).length === keys.length &&
  keys.every(key => Object.hasOwn(value, key));
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$/.test(value);
const capabilityId = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,255}$/.test(value);
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const decimal = value => typeof value === 'string' && /^(?:0|[1-9][0-9]*)$/.test(value);
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');

function fail(code) {
  throw Object.assign(new Error(code), {code});
}

function check(value, code = 'invalid_acceptance_evidence') {
  if (!value) fail(code);
}

/** Digest a structured value with the Store's canonical encoding only. */
export function canonicalDigest(value) {
  return digest(encode(value));
}

/** Digest exact artifact bytes. This is deliberately separate from canonicalDigest. */
export function bytesDigest(bytes) {
  check(bytes instanceof Uint8Array, 'invalid_artifact_bytes');
  return digest(bytes);
}

function decodeProjection(tx, kind, projectionId) {
  const row = tx.projection(kind, projectionId);
  check(row, 'missing_projection');
  let value;
  try { value = JSON.parse(row.bytes.toString('utf8')); } catch { fail('invalid_projection'); }
  check(object(value), 'invalid_projection');
  return {row, value};
}

/**
 * Check the candidate attemptRef against the durable worker projection, input
 * snapshot, and exactly one worker.reserved event in the same Store.
 *
 * The current Node runtime has no independent durable attemptId. The worker
 * projection ID plus command/generation/reservation/event identity is therefore
 * the identity tested here.
 */
export function validateAttemptRef(tx, attemptRef, {taskId, inputBytesById = new Map()} = {}) {
  check(closed(attemptRef, ATTEMPT_KEYS), 'invalid_attempt_ref');
  check(attemptRef.profile === ATTEMPT_REF_PROFILE && attemptRef.taskId === taskId &&
    id(attemptRef.taskId) && id(attemptRef.workerId) && id(attemptRef.commandId) &&
    decimal(attemptRef.generation) && sha(attemptRef.reservationDigest), 'invalid_attempt_ref');
  check(closed(attemptRef.reservationEvent, RESERVATION_EVENT_KEYS) &&
    attemptRef.reservationEvent.stream === taskId && id(attemptRef.reservationEvent.stream) &&
    decimal(attemptRef.reservationEvent.sequence) && BigInt(attemptRef.reservationEvent.sequence) > 0n &&
    sha(attemptRef.reservationEvent.digest), 'invalid_reservation_event');

  const {row: workerRow, value: record} = decodeProjection(tx, 'attempt', attemptRef.workerId);
  check(workerRow.source.stream === taskId && workerRow.id === attemptRef.workerId && record.worker?.id === attemptRef.workerId &&
    record.worker.taskId === taskId && record.ticket?.taskId === taskId &&
    record.ticket.commandId === attemptRef.commandId &&
    record.ticket.generation === attemptRef.generation &&
    record.ticket.reservationDigest === attemptRef.reservationDigest &&
    record.inputRef && id(record.inputRef), 'attempt_projection_mismatch');

  const input = decodeProjection(tx, 'attempt', record.inputRef);
  check(input.row.source.stream === taskId && input.row.source.stream === workerRow.source.stream &&
    input.row.source.sequence === workerRow.source.sequence && input.row.source.digest === workerRow.source.digest,
  'attempt_input_source_mismatch');
  // TaskExecution.reserve stores the real input as {task,inputArtifacts,node,
  // plan,upstream,...}; taskId is carried by the ticket, not duplicated at the
  // input root. Keep this check broad enough for managed Leader inputs while
  // still rejecting a non-TaskExecution-shaped snapshot.
  check(object(input.value) && object(input.value.task) && Array.isArray(input.value.inputArtifacts) &&
    object(input.value.node) && Array.isArray(input.value.upstream) &&
    canonicalDigest(input.value) === record.ticket.inputDigest, 'attempt_input_mismatch');
  if (inputBytesById.has(record.inputRef)) {
    const bytes = inputBytesById.get(record.inputRef);
    check(bytes instanceof Uint8Array && bytesDigest(bytes) === record.ticket.inputDigest,
      'attempt_input_bytes_mismatch');
  }

  // The current outbox has no independent durable attemptId; commandId is one
  // part of the composite identity and must resolve to the same Task/generation.
  const command = tx.command(attemptRef.commandId);
  check(command && command.taskId === taskId && command.generation === BigInt(attemptRef.generation) &&
    command.source.stream === taskId, 'attempt_command_mismatch');

  const reserved = tx.eventsWithField(taskId, 'workerId', attemptRef.workerId, 2, ['worker.reserved']);
  check(reserved.length === 1, 'reservation_event_count');
  const event = reserved[0], payload = JSON.parse(event.bytes.toString('utf8')).payload;
  check(event.generation === BigInt(attemptRef.generation) && payload?.type === 'worker.reserved' && payload.workerId === attemptRef.workerId &&
    payload.reservationDigest === attemptRef.reservationDigest &&
    String(event.sequence) === attemptRef.reservationEvent.sequence &&
    event.digest === attemptRef.reservationEvent.digest, 'reservation_event_mismatch');

  // Rebuild the exact ticket shape used by TaskExecution.reserve. Merely
  // changing the projection and reservation event together must not create a
  // second valid identity.
  const {reservationDigest: _reservationDigest, ...frozenTicket} = record.ticket;
  check(canonicalDigest({...frozenTicket, input: input.value}) === attemptRef.reservationDigest,
    'reservation_digest_mismatch');
  return {worker: record, input: input.value, reservationEvent: event};
}

/** Resolve an {id,digest} sourceArtifact through the current Task manifest and bytes. */
export function validateSourceArtifact(tx, sourceArtifact, {taskId, bytesByArtifactId = new Map()} = {}) {
  check(closed(sourceArtifact, ARTIFACT_REF_KEYS) && id(sourceArtifact.id) && sha(sourceArtifact.digest),
    'invalid_source_artifact');
  const {row, value: envelope} = decodeProjection(tx, 'artifact', sourceArtifact.id);
  check(row.source.stream === taskId, 'source_artifact_source_mismatch');
  const artifact = envelope?.type === 'manifest' && envelope.owner === 'local-operator' ? envelope.artifact : null;
  check(object(artifact) && artifact.id === sourceArtifact.id && artifact.taskId === taskId &&
    artifact.status === 'ready' && artifact.kind === 'evidence' && text(artifact.mediaType) &&
    artifact.digest === sourceArtifact.digest && sha(artifact.digest) && Number.isSafeInteger(artifact.bytes) &&
    artifact.bytes >= 0, 'source_artifact_manifest_mismatch');
  check(bytesByArtifactId instanceof Map && bytesByArtifactId.has(sourceArtifact.id), 'missing_source_artifact_bytes');
  if (bytesByArtifactId.has(sourceArtifact.id)) {
    const bytes = bytesByArtifactId.get(sourceArtifact.id);
    check(bytes instanceof Uint8Array && bytes.byteLength === artifact.bytes &&
      bytesDigest(bytes) === artifact.digest, 'source_artifact_bytes_mismatch');
  }
  return artifact;
}

/**
 * Validate the closed ADR0107 evidence candidate against durable Store facts.
 * `context` is a test-only read-only view of the already frozen plan/selection;
 * this function does not write, dispatch, or turn the result into authority.
 */
export function validateAcceptanceEvidence(tx, evidence, context = {}) {
  const {taskId, contractDigest, planDigest, candidateDigest, selectionDigest,
    checkIds = [], inputBytesById = new Map(), bytesByArtifactId = new Map()} = context;
  check(closed(evidence, ['profile', 'taskId', 'contractDigest', 'entries']) &&
    evidence.profile === ACCEPTANCE_EVIDENCE_PROFILE && evidence.taskId === taskId &&
    id(taskId) && sha(evidence.contractDigest) && evidence.contractDigest === contractDigest &&
    Array.isArray(evidence.entries) && evidence.entries.length > 0 && evidence.entries.length <= 16,
  'invalid_acceptance_evidence');
  check(new Set(checkIds).size === checkIds.length && evidence.entries.length === checkIds.length &&
    evidence.entries.every(entry => checkIds.includes(entry.checkId)), 'acceptance_check_catalog_mismatch');
  const {value: task} = decodeProjection(tx, 'task', taskId);
  // Current TaskApplication has no acceptanceContract/evidence index yet. The
  // offline candidate can therefore prove only the durable Task identity and
  // the approved plan digest here; contract/candidate/selection values remain
  // explicit test context until the runtime contract is implemented.
  check((task.task?.id ?? task.taskId) === taskId &&
    (task.plan?.digest ?? task.task?.plan?.digest) === planDigest,
  'acceptance_task_binding_mismatch');

  const seen = new Set();
  for (const entry of evidence.entries) {
    check(closed(entry, ENTRY_KEYS) && !seen.has(entry.checkId), 'invalid_acceptance_entry');
    seen.add(entry.checkId);
    check(id(entry.checkId) && entry.contractDigest === contractDigest &&
      ['text-review', 'execution-evidence', 'postcondition'].includes(entry.method) &&
      capabilityId(entry.capabilityId) && sha(entry.capabilityDigest) && sha(entry.planDigest) &&
      entry.planDigest === planDigest && (entry.repairId === null || id(entry.repairId)) &&
      sha(entry.candidateDigest) && entry.candidateDigest === candidateDigest &&
      sha(entry.selectionDigest) && entry.selectionDigest === selectionDigest &&
      (entry.externalAction === null || closed(entry.externalAction, ['actionId', 'authorizationDigest', 'targetDigest']) &&
        id(entry.externalAction.actionId) && sha(entry.externalAction.authorizationDigest) && sha(entry.externalAction.targetDigest)) &&
      (entry.receiptDigest === null || sha(entry.receiptDigest)) &&
      ['applicable', 'not-applicable'].includes(entry.applicability) &&
      ['pass', 'fail', 'unknown'].includes(entry.result), 'invalid_acceptance_entry');

    check(closed(entry.owner, OWNER_KEYS) && ['worker', 'core'].includes(entry.owner.kind) &&
      decimal(entry.owner.generation) && entry.owner.generation === entry.owner.attemptRef?.generation &&
      (entry.owner.kind === 'worker' ? id(entry.owner.workerId) : entry.owner.workerId === null),
    'invalid_acceptance_owner');
    // This candidate fixture covers only worker-backed facts. Core facts need a
    // separate transaction-event reference and must not borrow a Worker ref.
    check(entry.owner.kind === 'worker', 'core_owner_ref_not_implemented');
    validateAttemptRef(tx, entry.owner.attemptRef, {taskId, inputBytesById});
    if (entry.owner.kind === 'worker') check(entry.owner.workerId === entry.owner.attemptRef.workerId, 'owner_worker_mismatch');
    validateSourceArtifact(tx, entry.sourceArtifact, {taskId, bytesByArtifactId});
    if (entry.applicability === 'not-applicable') check(entry.result === 'pass', 'invalid_not_applicable_result');
  }
  check(seen.size === checkIds.length && encode(evidence).byteLength <= 32768, 'acceptance_evidence_limit');
  return evidence;
}
