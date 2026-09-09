import {randomUUID} from 'node:crypto';
import {encode, digest, makeEvent, UNPERMITTED_FORMAT} from '../task-store/store.mjs';
import {TaskError, reject, limits, freezePlan, publicTask, nextRevision, terminal, isText, clone} from './model.mjs';
import {TaskExecution} from './execution.mjs';
import {TaskArtifacts} from './artifacts.mjs';
import {TaskVerification} from './verification.mjs';
import {TaskClarification} from './clarification.mjs';
import {TaskRuntimeQuestions} from './runtime-questions.mjs';
import {TaskRepair} from './repair.mjs';
import {TaskInputAudit} from './input-audit.mjs';
export {createVerificationPort} from './verification.mjs';
export {createClarificationPort} from './clarification.mjs';
export {createRuntimeQuestionPort} from './runtime-questions.mjs';
export {createRepairPort} from './repair.mjs';
export {createAuditDisclosure} from './input-audit.mjs';

const hash = value => digest(encode(value));
const parse = entry => entry ? JSON.parse(entry.bytes.toString('utf8')) : null;
const integer = value => {
  const number = Number(value);
  if (!Number.isSafeInteger(number)) reject('application_unavailable', 503);
  return number;
};
const unavailableUsage = () => ({tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0});
const idOK = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);

/**
 * Application is the only Task reducer. Store owns transactions, not business
 * transitions; HTTP/Agent callbacks cannot patch projections. This component
 * emits durable obligations, and never spawns a process inside a transaction.
 */
export class TaskApplication {
  constructor({store, owner, clock = Date.now, makeId = prefix => prefix + '-' + randomUUID(),
    defaultLimits = {timeoutMs: 300000, maxAttempts: 16, maxWorkers: 2}, execution = {}, depot = null, verification = null, clarification = null, runtimeQuestions = null, repair = null, auditDisclosure = null}) {
    this.store = store; this.owner = owner; this.clock = clock; this.makeId = makeId;
    if (store.info?.().format === UNPERMITTED_FORMAT && auditDisclosure !== null) reject('unsupported_task', 422);
    this.defaultLimits = limits(defaultLimits);
    this.execution = new TaskExecution(this, execution);
    this.artifacts = new TaskArtifacts(this, depot);
    this.verification = new TaskVerification(this, verification);
    this.clarification = new TaskClarification(this, clarification);
    this.runtimeQuestions = new TaskRuntimeQuestions(this, runtimeQuestions);
    this.repair = new TaskRepair(this, repair);
    this.inputAudit = new TaskInputAudit(this, auditDisclosure);
    this.dispatch = this.dispatch.bind(this);
  }
  now() {
    const value = this.clock();
    if (!Number.isSafeInteger(value) || value <= 0) reject('application_unavailable', 503);
    return value;
  }
  newId(prefix) {
    const value = this.makeId(prefix);
    if (!idOK(value)) reject('application_unavailable', 503);
    return value;
  }
  transaction(write, callback) {
    // Store deliberately sanitizes callback exceptions. Retain only our own
    // closed domain errors, after Store has rolled back the whole transaction.
    let domainError;
    try {
      return this.store[write ? 'write' : 'read'](this.owner, tx => {
        try { return callback(tx); }
        catch (error) { if (error instanceof TaskError) domainError = error; throw error; }
      });
    } catch (error) {
      if (domainError) throw domainError;
      if (error.code === 'owner') reject('recovery_required', 409);
      reject('application_unavailable', 503);
    }
  }
  get(tx, taskId) {
    if (!idOK(taskId)) reject('invalid_request', 400);
    const entry = tx.projection('task', taskId);
    if (!entry) reject('not_found', 404);
    return this.taskRecord(entry, taskId);
  }
  taskRecord(entry, taskId = entry.id) {
    const record = parse(entry);
    if (record.task.id !== taskId || record.task.revision !== integer(entry.revision)) reject('application_unavailable', 503);
    if (record.clarification && record.clarification.profile !== 'task-clarification/v1') reject('recovery_required', 409);
    if (record.runtimeQuestions && record.runtimeQuestions.descriptor?.profile !== 'task-runtime-question/v1') reject('recovery_required', 409);
    if (record.repair && record.repair.descriptor?.profile !== 'task-local-repair/v1') reject('recovery_required', 409);
    return record;
  }
  save(tx, record, type, detail = {}) {
    const task = record.task, head = tx.head(task.id), at = new Date(this.now()).toISOString();
    task.updatedAt = at;
    const event = makeEvent(task.id, head.sequence + 1n, {type, at, taskRevision: task.revision, ...detail});
    const source = {stream: task.id, ...tx.append(task.id, head, [event])};
    tx.putProjection('task', task.id, task.revision - 1, source, encode(record));
    return source;
  }
  operation(tx, task, source, kind, status, id = this.newId('operation')) {
    const now = new Date(this.now()).toISOString();
    const op = {id, taskId: task.id, kind, status,
      taskRevision: task.revision, createdAt: now, updatedAt: now};
    tx.putProjection('operation', op.id, 0, source, encode(op));
    return op;
  }
  enqueue(tx, source, taskId, action, payload, kind = 'start', id = this.newId('command')) {
    const bytes = encode({action, ...payload});
    return tx.enqueue({id, taskId, kind, inputDigest: digest(bytes), payload: bytes, source});
  }
  // Replay lookup deliberately precedes CAS. A lost HTTP reply must not make a
  // previously accepted operation consume another budget or dispatch twice.
  receiptKey(request) {
    if (!idOK(request.key)) reject('invalid_request', 400);
    const scope = request.taskId ?? 'tasks', key = {scope, operation: request.operation, keyDigest: hash(request.key)};
    const input = {operation: request.operation, taskId: request.taskId ?? null, body: request.body};
    // Only the new answer operation adds its route subject. Keep every old
    // operation's exact digest algorithm and historical receipt bytes unchanged.
    if (request.operation === 'task.answer') {
      (Object.hasOwn(request.body ?? {}, 'questionDigest') ? this.runtimeQuestions : this.clarification).shape(request);
      input.questionId = request.questionId;
    }
    if (request.operation === 'task.repair') this.repair.shape(request);
    const requestDigest = hash(input);
    return {key, requestDigest};
  }
  receipt(tx, request) {
    const {key, requestDigest} = this.receiptKey(request);
    let previous;
    try { previous = tx.receipt(key.scope, key.operation, key.keyDigest, requestDigest); }
    catch (error) { if (error.code === 'conflict') reject('idempotency_conflict', 409); throw error; }
    return previous ? parse(previous) : null;
  }
  replay(request) { return this.transaction(false, tx => {
    const previous = this.receipt(tx, request);
    return previous && request.operation === 'task.answer' ? this.clarification.replay(tx, previous) :
      previous && request.operation === 'task.repair' ? this.repair.replay(tx, previous) : previous;
  }); }
  mutate(request, callback) {
    const {key, requestDigest} = this.receiptKey(request);
    return this.transaction(true, tx => {
      const previous = this.receipt(tx, request);
      if (previous) return request.operation === 'task.repair' ? this.repair.replay(tx, previous) : previous;
      const {result, source} = callback(tx);
      tx.putReceipt(key, requestDigest, source, encode(result));
      return clone(result);
    });
  }
  async dispatch(request, context) {
    if (context?.principal !== 'local-operator') reject('forbidden', 403);
    if (context.signal?.aborted) reject('application_unavailable', 503);
    if (!request || typeof request.operation !== 'string') reject('invalid_request', 400);
    if (['input.create', 'artifact.get', 'artifact.content'].includes(request.operation)) return this.artifacts.dispatch(request);
    if (request.operation === 'task.create') return this.create(request, context);
    if (request.operation === 'task.repair') return this.repair.repair(request);
    if (request.operation === 'task.answer') return (Object.hasOwn(request.body ?? {}, 'questionDigest') ? this.runtimeQuestions : this.clarification).answer(request, context);
    if (['task.approve', 'task.cancel', 'task.pause', 'task.resume'].includes(request.operation)) return this.control(request);
    return this.transaction(false, tx => this.query(tx, request));
  }
  newTaskRecord(body, budget, inputArtifacts) {
    const now = this.now(), at = new Date(now).toISOString(), taskId = this.newId('task');
    return {task: {id: taskId, revision: 1, status: 'draft', phase: 'intake', intent: body.intent,
      createdAt: at, updatedAt: at, allowedActions: ['cancel'], plan: null, artifactIds: [],
      deadlineAt: new Date(now + budget.timeoutMs).toISOString()},
    input: clone(body), inputArtifacts: clone(inputArtifacts), inputDigest: inputArtifacts.length ? hash({body, inputArtifacts}) : hash(body), limits: clone(budget), plan: null,
    approved: null, nodes: [], attempts: 0, reworkCount: 0, retryCount: 0};
  }
  async create(request, context) {
    const body = request.body;
    if (!body || !isText(body.intent) || Object.keys(body).some(key => !['intent', 'context', 'requirements', 'limits'].includes(key))) reject('invalid_request', 400);
    const budget = limits(body.limits ?? this.defaultLimits);
    const previous = this.replay(request);
    if (previous) return previous;
    const inputArtifacts = this.artifacts.inputs(body.context?.inputRefs);
    const prepared = await this.clarification.prepare(body, budget, inputArtifacts, context);
    return this.mutate(request, tx => {
      this.artifacts.recheck(tx, inputArtifacts);
      const record = prepared?.record ?? this.newTaskRecord(body, budget, inputArtifacts), taskId = record.task.id;
      if (prepared) {
        this.clarification.configuration(record);
        if (this.now() >= Date.parse(record.clarification.confirmBefore)) reject('question_expired', 410);
        if (context?.signal?.aborted) reject('application_unavailable', 503);
      }
      const source = this.save(tx, record, prepared ? 'task.clarification-created' : 'task.created', {inputDigest: record.inputDigest});
      // Planning is a distinct read/clarification obligation; it grants no
      // unapproved implementation, file edits or publication authority.
      if (prepared) this.clarification.persist(tx, record, prepared.preview, source);
      else this.enqueue(tx, source, taskId, 'plan', {taskId, expectedRevision: 1, inputDigest: record.inputDigest});
      return {source, result: publicTask(record, this.now())};
    });
  }
  proposePlan(taskId, expectedRevision, proposal) {
    return this.transaction(true, tx => {
      const record = this.get(tx, taskId);
      if (record.clarification) reject('state_conflict', 409);
      if (record.task.revision !== expectedRevision) reject('revision_conflict', 409);
      if (!['draft', 'planning', 'awaiting-approval'].includes(record.task.status) || record.approved) reject('state_conflict', 409);
      if (this.now() >= Date.parse(record.task.deadlineAt)) reject('state_conflict', 409);
      const plan = this.freezePlan(record, proposal);
      record.plan = plan; record.task.plan = {revision: plan.revision, digest: plan.digest};
      record.task.revision = nextRevision(record.task.revision);
      record.task.status = 'awaiting-approval'; record.task.phase = 'planning';
      record.nodes = plan.nodes.map(node => ({id: node.id, role: node.role, status: 'pending', workerIds: []}));
      this.save(tx, record, 'task.plan-proposed', {planRevision: plan.revision, planDigest: plan.digest});
      return clone(plan);
    });
  }
  freezePlan(record, proposal) {
    const plan = freezePlan(record, proposal, value => hash({plan: value, inputDigest: record.inputDigest}));
    const {digest: _digest, ...body} = plan;
    const binding = this.verification.bind(record, body);
    const interaction = this.runtimeQuestions.bind(record, body);
    const repair = this.repair.bind(record, body, binding);
    if (repair) {
      record.repair = repair;
      body.repair = {profile: repair.descriptor.profile, policyDigest: repair.policyDigest};
      record.selectedResults = Object.fromEntries(body.nodes.filter(node => node.id !== binding.nodeId).map(node => [node.id, null]));
    }
    if (interaction) {
      record.runtimeQuestions = interaction;
      body.interaction = {profile: interaction.descriptor.profile, policyDigest: interaction.policyDigest,
        maxQuestions: interaction.descriptor.maxQuestions, maxWaitMs: interaction.descriptor.maxWaitMs};
    } else delete record.runtimeQuestions;
    if (binding) {
      record.verification = binding;
      return {...body, digest: hash({plan: body, inputDigest: record.inputDigest, verification: binding,
        ...(interaction ? {runtimeQuestions: interaction.descriptor} : {}), ...(repair ? {repair} : {})})};
    }
    delete record.verification; return plan;
  }
  control(request) {
    return this.mutate(request, tx => {
      const record = this.get(tx, request.taskId), task = record.task, body = request.body;
      if (!body || task.revision !== body.expectedRevision) reject('revision_conflict', 409);
      if (terminal.has(task.status) || task.status === 'cancelling') reject('state_conflict', 409);
      const original = task.status;
      let status = 'succeeded', action;
      if (request.operation === 'task.approve') {
        this.clarification.approve(tx, record);
        if (original !== (record.clarification ? 'awaiting-confirmation' : 'awaiting-approval') || !record.plan || body.planRevision !== record.plan.revision ||
            body.planDigest !== record.plan.digest) reject('plan_conflict', 409);
        if (record.verification) this.verification.configured(record.verification);
        if (record.runtimeQuestions) this.runtimeQuestions.configured(record.runtimeQuestions, record.plan);
        if (record.repair) this.repair.configured(record);
        const deadline = Math.min(Date.parse(task.deadlineAt), Date.parse(task.createdAt) + record.plan.budget.timeoutMs);
        if (this.now() >= deadline) reject('state_conflict', 409);
        task.deadlineAt = new Date(deadline).toISOString();
        record.approved = {planRevision: record.plan.revision, planDigest: record.plan.digest, at: new Date(this.now()).toISOString()};
        if (record.clarification) record.approved.preview = {digest: record.clarification.previewDigest,
          revision: record.clarification.previewRevision, inputsDigest: record.inputDigest};
        task.status = 'queued'; task.phase = 'execution'; status = 'accepted'; action = 'dispatch';
      } else if (request.operation === 'task.cancel') {
        // Draft planning also has an obligation. Cancellation always emits a
        // stop/fence; completion must be established by execution reconciliation.
        task.status = 'cancelling'; status = 'accepted'; action = 'cancel';
        if (record.repair) record.userCancelled = true;
        if (this.execution.cleanup.enabled()) record.cancelIntent = {revision: nextRevision(task.revision), at: new Date(this.now()).toISOString(),
          operationId: this.newId('operation'), commandId: this.newId('command')};
      } else if (request.operation === 'task.pause') {
        if (record.clarification && !record.approved) reject('state_conflict', 409);
        if (!['queued', 'running', 'awaiting-answer'].includes(original)) reject('state_conflict', 409);
        record.pausedFrom = original; task.status = 'paused'; status = 'accepted'; action = 'pause';
      } else {
        if (original !== 'paused' || !record.pausedFrom) reject('state_conflict', 409);
        if (this.now() >= Date.parse(task.deadlineAt)) reject('state_conflict', 409);
        task.status = record.pausedFrom; delete record.pausedFrom; status = 'accepted'; action = 'resume';
      }
      task.revision = nextRevision(task.revision);
      const closedQuestions = action === 'cancel' ? this.runtimeQuestions.close(record) : [];
      const source = this.save(tx, record, request.operation, {from: original, to: task.status});
      this.runtimeQuestions.settleClosed(tx, record, source, closedQuestions);
      const cancellation = action === 'cancel' ? record.cancelIntent : null;
      const op = this.operation(tx, task, source, request.operation, status, cancellation?.operationId);
      if (action) this.enqueue(tx, source, task.id, action,
        {taskId: task.id, expectedRevision: task.revision, operationId: op.id, planDigest: record.approved?.planDigest ?? null},
        ['cancel', 'pause'].includes(action) ? 'stop' : 'start', cancellation?.commandId);
      return {source, result: op};
    });
  }
  query(tx, request) {
    if (request.operation === 'task.questions') return this.runtimeQuestions.questions(tx, request);
    if (['worker.get', 'task.workers'].includes(request.operation)) return this.execution.query(tx, request);
    const page = request.page ?? {}, limit = page.limit ?? 50, after = page.cursor ?? '';
    if (request.operation === 'task.list') {
      const entries = tx.projections('task', after, limit);
      return {items: entries.map(entry => this.repair.view(tx, this.taskRecord(entry))), nextCursor: entries.length === limit ? entries.at(-1).id : null};
    }
    if (request.operation === 'operation.get') {
      const op = parse(tx.projection('operation', request.operationId));
      if (!op) reject('not_found', 404);
      return op;
    }
    if (!request.operation.startsWith('task.')) reject('unsupported_operation', 501);
    const record = this.get(tx, request.taskId), task = record.task;
    if (request.operation === 'task.get') return this.repair.view(tx, record);
    if (request.operation === 'task.plan') { if (!record.plan) reject('plan_conflict', 409); return clone(record.plan); }
    if (request.operation === 'task.graph') {
      if (!record.plan) reject('plan_conflict', 409);
      return {taskId: task.id, planRevision: record.plan.revision,
        nodes: clone(record.nodes), edges: clone(record.plan.edges)};
    }
    if (request.operation === 'task.events') {
      if (after && !/^[1-9][0-9]{0,15}$/.test(after)) reject('invalid_request', 400);
      const entries = tx.events(task.id, after ? BigInt(after) : 0n, limit);
      const items = entries.map(entry => {
        const envelope = JSON.parse(entry.bytes.toString('utf8')), event = envelope.payload;
        return {id: 'event-' + envelope.sequence, taskId: task.id, sequence: integer(entry.sequence),
          type: event.type, at: event.at, workerId: event.workerId ?? null, summary: event.type, source: 'application'};
      });
      return {taskId: task.id, items, nextCursor: entries.length === limit ? entries.at(-1).sequence.toString() : null};
    }
    if (request.operation === 'task.audit') {
      const records = this.execution.workers(tx, record).map(({record}) => record);
      return {taskId: task.id,
      elapsedMs: Math.max(0, (terminal.has(task.status) ? Date.parse(task.updatedAt) : this.now()) - Date.parse(task.createdAt)),
      attempts: record.attempts, retryCount: record.retryCount, reworkCount: record.reworkCount,
      // Final verification is not an independently observed first code review.
      firstReview: {passed: 0, total: 0, pending: 0},
      acceptance: clone(record.acceptance ?? {status: 'pending', evidenceIds: [], digest: null}),
      usage: unavailableUsage(), workers: this.inputAudit.workers(records), prompts: this.inputAudit.prompts(records),
      measurement: {elapsedSource: 'task-lifecycle', firstReviewSource: 'unavailable', usageSource: 'unavailable'}, ...this.repair.audit(tx, record)};
    }
    // Never implement the remaining surface with fabricated success/empty
    // records. Execution, interactions and artifacts must bind actual facts.
    reject('unsupported_operation', 501);
  }
}
