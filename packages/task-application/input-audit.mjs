import {digest, makeEvent} from '../task-store/store.mjs';
import {clone, isText, reject} from './model.mjs';

const policies = new WeakMap(), PROFILE = 'task-input-observation/v1';
const MAX_PROMPT = 256 * 1024, PREVIEW_BYTES = 2048;
const plain = value => value && [Object.prototype, null].includes(Object.getPrototypeOf(value));
const validText = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= MAX_PROMPT;
const check = value => {if (!value) reject('invalid_request', 400);};
function preview(value) {
  let result = '', size = 0;
  for (const character of value) {const bytes = Buffer.byteLength(character); if (size + bytes > PREVIEW_BYTES) break; result += character; size += bytes;}
  return result;
}

/** Explicit trusted disclosure, not a credential detector or an HTTP setting.
 * Null/throw/async means do not retain content. Only the returned text is stored;
 * this policy makes no claim about hidden Agent/model/Skill inputs. */
export function createAuditDisclosure({id, version, redact} = {}) {
  if (!isText(id, 128) || !isText(version, 128) || typeof redact !== 'function') reject('unsupported_task', 422);
  const port = Object.freeze({id, version}); policies.set(port, redact); return port;
}

// Optional observations only: never read by mayStart, budget, cleanup, candidate
// selection or Verification. Older records are queried as unavailable; opening
// a root does not backfill observations or rewrite any original input/receipt.
export class TaskInputAudit {
  constructor(app, disclosure = null) {
    if (disclosure !== null && !policies.has(disclosure)) reject('unsupported_task', 422);
    this.app = app; this.disclosure = disclosure;
  }
  observe(ticket, stage, prompt) {
    check(['prepared', 'handed-off'].includes(stage));
    if (stage === 'handed-off') return this.handoff(ticket);
    check(validText(prompt) && prompt.trim());
    const promptBytes = Buffer.byteLength(prompt), promptDigest = digest(Buffer.from(prompt));
    const original = this.app.transaction(false, tx => {
      const {record} = this.app.execution.ticket(tx, ticket);
      if (record.inputObservation) {
        check(record.inputObservation.promptDigest === promptDigest && record.inputObservation.promptBytes === promptBytes);
        return true;
      }
      check(record.worker.status === 'queued' && record.executionId === null); return false;
    });
    if (original) return true;
    let staged = null, text = '', coverage = this.disclosure ? 'unavailable' : 'metadata-only';
    if (this.disclosure) {
      try {
        // No raw bytes are sent to Depot, an event, a projection or diagnostics.
        // The callback is trusted synchronous deployment code, not a sandbox.
        const value = policies.get(this.disclosure)(prompt, Object.freeze({taskId: ticket.taskId, workerId: ticket.workerId,
          nodeId: ticket.nodeId, role: ticket.role, inputDigest: ticket.inputDigest, promptDigest}));
        if (value && typeof value.then === 'function') {void Promise.resolve(value).catch(() => {});}
        else if (typeof value === 'string' && validText(value)) {
          staged = this.app.artifacts.stageOutputs([['evidence', {name: ticket.workerId + '.input.txt', mediaType: 'text/plain', content: Buffer.from(value)}]])[0];
          text = preview(value); coverage = 'policy-redacted';
        }
      } catch { /* Declined/missing bytes is audit unavailability, not Task failure. */ }
    }
    return this.app.transaction(true, tx => {
      const {row, record} = this.app.execution.ticket(tx, ticket);
      if (record.inputObservation) {
        check(record.inputObservation.promptDigest === promptDigest && record.inputObservation.promptBytes === promptBytes); return true;
      }
      check(record.worker.status === 'queued' && record.executionId === null);
      const at = new Date(this.app.now()).toISOString(), stream = ticket.taskId, head = tx.head(stream);
      const source = {stream, ...tx.append(stream, head, [makeEvent(stream, head.sequence + 1n,
        {type: 'worker.input-prepared', at, workerId: ticket.workerId})])};
      const snapshot = staged ? this.app.artifacts.commitOutputs(tx, ticket.taskId, [staged], source)[0] : null;
      record.inputObservation = {profile: PROFILE, taskId: ticket.taskId, workerId: ticket.workerId,
        reservationDigest: ticket.reservationDigest, inputDigest: ticket.inputDigest, promptDigest, promptBytes,
        preparedAt: at, handedOffAt: null, coverage, policy: this.disclosure ? clone(this.disclosure) : null,
        text, previewTruncated: snapshot !== null && Buffer.byteLength(text) < snapshot.bytes, snapshot,
        contextRefs: (ticket.input.inputArtifacts ?? []).map(ref => ref.id)};
      this.app.execution.putWorker(tx, row, record, source); return true;
    });
  }
  handoff(ticket) {
    return this.app.transaction(true, tx => {
      const {row, record} = this.app.execution.ticket(tx, ticket), observation = record.inputObservation;
      if (!observation) return false;
      this.checked(record);
      if (observation.handedOffAt !== null) return true;
      check(record.worker.status === 'queued' && record.executionId === null);
      const at = new Date(this.app.now()).toISOString(), stream = ticket.taskId, head = tx.head(stream);
      const source = {stream, ...tx.append(stream, head, [makeEvent(stream, head.sequence + 1n,
        {type: 'worker.input-handed-off', at, workerId: ticket.workerId})])};
      observation.handedOffAt = at; this.app.execution.putWorker(tx, row, record, source); return true;
    });
  }
  checked(record) {
    const value = record.inputObservation;
    if (!value) return null;
    if (!plain(value) || value.profile !== PROFILE || value.taskId !== record.worker.taskId || value.workerId !== record.worker.id ||
      value.reservationDigest !== record.ticket.reservationDigest || value.inputDigest !== record.ticket.inputDigest ||
      typeof value.promptDigest !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(value.promptDigest) ||
      !Number.isSafeInteger(value.promptBytes) || value.promptBytes < 1 || value.promptBytes > MAX_PROMPT ||
      !Number.isFinite(Date.parse(value.preparedAt)) || value.handedOffAt !== null && !Number.isFinite(Date.parse(value.handedOffAt)) ||
      !['metadata-only', 'policy-redacted', 'unavailable'].includes(value.coverage) ||
      !validText(value.text) || Buffer.byteLength(value.text) > PREVIEW_BYTES || typeof value.previewTruncated !== 'boolean' ||
      !Array.isArray(value.contextRefs) || value.contextRefs.length > 32 ||
      value.contextRefs.some(id => typeof id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id)) ||
      value.policy !== null && (!plain(value.policy) || !isText(value.policy.id, 128) || !isText(value.policy.version, 128)) ||
      (value.coverage === 'policy-redacted' ? !value.policy || !value.snapshot || value.snapshot.taskId !== value.taskId ||
        value.snapshot.name !== value.workerId + '.input.txt' || value.snapshot.status !== 'ready' ||
        value.snapshot.kind !== 'evidence' || value.snapshot.mediaType !== 'text/plain' || value.snapshot.bytes > MAX_PROMPT :
        value.snapshot !== null || value.text !== '' || value.previewTruncated)) reject('application_unavailable', 503);
    return value;
  }
  prompts(records) {
    return records.map(record => {
      const value = this.checked(record), stage = !value ? 'unavailable' : value.handedOffAt === null ? 'prepared' : 'handed-off';
      return {workerId: record.worker.id, text: value?.text ?? '', contextRefs: clone(value?.contextRefs ?? []),
        source: value?.coverage === 'policy-redacted' ? stage + '-redacted' : 'unavailable',
        observation: {stage, promptDigest: value?.promptDigest ?? null, promptBytes: value?.promptBytes ?? null,
          inputDigest: record.ticket.inputDigest, reservationDigest: record.ticket.reservationDigest,
          preparedAt: value?.preparedAt ?? null, handedOffAt: value?.handedOffAt ?? null,
          coverage: value?.coverage ?? 'unavailable', policy: clone(value?.policy ?? null),
          snapshot: clone(value?.snapshot ?? null), previewTruncated: value?.previewTruncated ?? false}};
    });
  }
  workers(records) {
    return records.map(record => {
      const started = Date.parse(record.worker.startedAt), finished = Date.parse(record.worker.finishedAt);
      return {...clone(record.worker), audit: {repairId: record.ticket.repairId ?? null,
        elapsedMs: Number.isFinite(started) && Number.isFinite(finished) && finished >= started ? finished - started : null,
        elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'}};
    });
  }
}
