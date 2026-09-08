import {randomUUID} from 'node:crypto';
import {encode, digest, makeEvent} from '../task-store/store.mjs';
import {TaskError, reject, limits, freezePlan, publicTask, nextRevision, terminal, isText, clone} from './model.mjs';

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
    defaultLimits = {timeoutMs: 300000, maxAttempts: 16, maxWorkers: 2}}) {
    this.store = store; this.owner = owner; this.clock = clock; this.makeId = makeId;
    this.defaultLimits = limits(defaultLimits);
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
    const record = parse(entry);
    if (record.task.id !== taskId || record.task.revision !== integer(entry.revision)) reject('application_unavailable', 503);
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
  operation(tx, task, source, kind, status) {
    const now = new Date(this.now()).toISOString();
    const op = {id: this.newId('operation'), taskId: task.id, kind, status,
      taskRevision: task.revision, createdAt: now, updatedAt: now};
    tx.putProjection('operation', op.id, 0, source, encode(op));
    return op;
  }
  enqueue(tx, source, taskId, action, payload, kind = 'start') {
    const bytes = encode({action, ...payload});
    return tx.enqueue({id: this.newId('command'), taskId, kind, inputDigest: digest(bytes), payload: bytes, source});
  }
  // Replay lookup deliberately precedes CAS. A lost HTTP reply must not make a
  // previously accepted operation consume another budget or dispatch twice.
  mutate(request, callback) {
    if (!idOK(request.key)) reject('invalid_request', 400);
    const scope = request.taskId ?? 'tasks', key = {scope, operation: request.operation, keyDigest: hash(request.key)};
    const requestDigest = hash({operation: request.operation, taskId: request.taskId ?? null, body: request.body});
    return this.transaction(true, tx => {
        let previous;
        try { previous = tx.receipt(key.scope, key.operation, key.keyDigest, requestDigest); }
        catch (error) { if (error.code === 'conflict') reject('idempotency_conflict', 409); throw error; }
        if (previous) return parse(previous);
        const {result, source} = callback(tx);
        tx.putReceipt(key, requestDigest, source, encode(result));
        return clone(result);
      });
  }
  async dispatch(request, context) {
    if (context?.principal !== 'local-operator') reject('forbidden', 403);
    if (context.signal?.aborted) reject('application_unavailable', 503);
    if (!request || typeof request.operation !== 'string') reject('invalid_request', 400);
    if (request.operation === 'task.create') return this.create(request);
    if (['task.approve', 'task.cancel', 'task.pause', 'task.resume'].includes(request.operation)) return this.control(request);
    return this.transaction(false, tx => this.query(tx, request));
  }
  create(request) {
    const body = request.body;
    if (!body || !isText(body.intent) || Object.keys(body).some(key => !['intent', 'context', 'requirements', 'limits'].includes(key))) reject('invalid_request', 400);
    const budget = limits(body.limits ?? this.defaultLimits);
    return this.mutate(request, tx => {
      const now = this.now(), at = new Date(now).toISOString(), taskId = this.newId('task');
      const record = {task: {id: taskId, revision: 1, status: 'draft', phase: 'intake', intent: body.intent,
        createdAt: at, updatedAt: at, allowedActions: ['cancel'], plan: null, artifactIds: [],
        deadlineAt: new Date(now + budget.timeoutMs).toISOString()},
      input: clone(body), inputDigest: hash(body), limits: budget, plan: null,
      approved: null, nodes: [], attempts: 0, reworkCount: 0, retryCount: 0};
      const source = this.save(tx, record, 'task.created', {inputDigest: record.inputDigest});
      // Planning is a distinct read/clarification obligation; it grants no
      // unapproved implementation, file edits or publication authority.
      this.enqueue(tx, source, taskId, 'plan', {taskId, expectedRevision: 1, inputDigest: record.inputDigest});
      return {source, result: publicTask(record)};
    });
  }
  proposePlan(taskId, expectedRevision, proposal) {
    return this.transaction(true, tx => {
      const record = this.get(tx, taskId);
      if (record.task.revision !== expectedRevision) reject('revision_conflict', 409);
      if (!['draft', 'planning', 'awaiting-approval'].includes(record.task.status) || record.approved) reject('state_conflict', 409);
      if (this.now() >= Date.parse(record.task.deadlineAt)) reject('state_conflict', 409);
      const plan = freezePlan(record, proposal, value => hash({plan: value, inputDigest: record.inputDigest}));
      record.plan = plan; record.task.plan = {revision: plan.revision, digest: plan.digest};
      record.task.revision = nextRevision(record.task.revision);
      record.task.status = 'awaiting-approval'; record.task.phase = 'planning';
      record.nodes = plan.nodes.map(node => ({id: node.id, role: node.role, status: 'pending', workerIds: []}));
      this.save(tx, record, 'task.plan-proposed', {planRevision: plan.revision, planDigest: plan.digest});
      return clone(plan);
    });
  }
  control(request) {
    return this.mutate(request, tx => {
      const record = this.get(tx, request.taskId), task = record.task, body = request.body;
      if (!body || task.revision !== body.expectedRevision) reject('revision_conflict', 409);
      if (terminal.has(task.status) || task.status === 'cancelling') reject('state_conflict', 409);
      const original = task.status;
      let status = 'succeeded', action;
      if (request.operation === 'task.approve') {
        if (original !== 'awaiting-approval' || !record.plan || body.planRevision !== record.plan.revision ||
            body.planDigest !== record.plan.digest) reject('plan_conflict', 409);
        if (this.now() >= Date.parse(task.deadlineAt)) reject('state_conflict', 409);
        record.approved = {planRevision: record.plan.revision, planDigest: record.plan.digest, at: new Date(this.now()).toISOString()};
        task.status = 'queued'; task.phase = 'execution'; status = 'accepted'; action = 'dispatch';
      } else if (request.operation === 'task.cancel') {
        // Draft planning also has an obligation. Cancellation always emits a
        // stop/fence; completion must be established by execution reconciliation.
        task.status = 'cancelling'; status = 'accepted'; action = 'cancel';
      } else if (request.operation === 'task.pause') {
        if (!['queued', 'running', 'awaiting-answer'].includes(original)) reject('state_conflict', 409);
        record.pausedFrom = original; task.status = 'paused'; status = 'accepted'; action = 'pause';
      } else {
        if (original !== 'paused' || !record.pausedFrom) reject('state_conflict', 409);
        if (this.now() >= Date.parse(task.deadlineAt)) reject('state_conflict', 409);
        task.status = record.pausedFrom; delete record.pausedFrom; status = 'accepted'; action = 'resume';
      }
      task.revision = nextRevision(task.revision);
      const source = this.save(tx, record, request.operation, {from: original, to: task.status});
      const op = this.operation(tx, task, source, request.operation, status);
      if (action) this.enqueue(tx, source, task.id, action,
        {taskId: task.id, expectedRevision: task.revision, operationId: op.id, planDigest: record.approved?.planDigest ?? null},
        ['cancel', 'pause'].includes(action) ? 'stop' : 'start');
      return {source, result: op};
    });
  }
  query(tx, request) {
    const page = request.page ?? {}, limit = page.limit ?? 50, after = page.cursor ?? '';
    if (request.operation === 'task.list') {
      const entries = tx.projections('task', after, limit);
      return {items: entries.map(entry => publicTask(parse(entry))), nextCursor: entries.length === limit ? entries.at(-1).id : null};
    }
    if (request.operation === 'operation.get') {
      const op = parse(tx.projection('operation', request.operationId));
      if (!op) reject('not_found', 404);
      return op;
    }
    if (!request.operation.startsWith('task.')) reject('unsupported_operation', 501);
    const record = this.get(tx, request.taskId), task = record.task;
    if (request.operation === 'task.get') return publicTask(record);
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
          type: event.type, at: event.at, workerId: null, summary: event.type, source: 'application'};
      });
      return {taskId: task.id, items, nextCursor: entries.length === limit ? entries.at(-1).sequence.toString() : null};
    }
    if (request.operation === 'task.audit') return {taskId: task.id,
      elapsedMs: Math.max(0, (terminal.has(task.status) ? Date.parse(task.updatedAt) : this.now()) - Date.parse(task.createdAt)),
      attempts: record.attempts, retryCount: record.retryCount, reworkCount: record.reworkCount,
      firstReview: {passed: 0, total: 0, pending: 0}, acceptance: {status: 'pending', evidenceIds: [], digest: null},
      usage: unavailableUsage(), workers: [], prompts: []};
    // Never implement the remaining surface with fabricated success/empty
    // records. Execution, interactions and artifacts must bind actual facts.
    reject('unsupported_operation', 501);
  }
}
