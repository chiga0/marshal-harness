import {randomBytes} from 'node:crypto';
import {encode, digest, INTERACTION_FORMAT, REPAIR_FORMAT} from '../task-store/store.mjs';
import {clone, isText, nextRevision, publicTask, reject, terminal} from './model.mjs';

const PROFILE = 'task-runtime-question/v1', ports = new WeakMap(), hash = value => digest(encode(value));
const decode = row => row ? JSON.parse(row.bytes.toString('utf8')) : null;
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const closed = (value, keys) => value !== null && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => keys.includes(key));
const requireValue = value => {if (!value) reject('unsupported_task', 422);};
const sync = fn => {const value = fn(); if (value?.then) {Promise.resolve(value).catch(() => {}); requireValue(false);} return value;};
const pending = q => q.status === 'open' || ['pending', 'dispatched'].includes(q.deliveryStatus);
const exact = (a, b) => hash(a) === hash(b);

/** Trusted composition only. Persisted descriptors contain data, never callbacks.
 * A different callback must advertise a different policy version/digest. */
export function createRuntimeQuestionPort({policy, nodeIds, maxQuestions = 3, maxWaitMs = 120000, applies, validateQuestion, validateAnswer}) {
  requireValue(closed(policy, ['id', 'version', 'description']) && id(policy.id) && isText(policy.version, 128) && isText(policy.description, 4096) &&
    Array.isArray(nodeIds) && nodeIds.length > 0 && nodeIds.length <= 32 && nodeIds.every(id) && new Set(nodeIds).size === nodeIds.length &&
    Number.isSafeInteger(maxQuestions) && maxQuestions >= 1 && maxQuestions <= 3 && Number.isSafeInteger(maxWaitMs) && maxWaitMs >= 1 && maxWaitMs <= 120000 &&
    [applies, validateQuestion, validateAnswer].every(fn => typeof fn === 'function'));
  const descriptor = {profile: PROFILE, policy: clone(policy), nodeIds: [...nodeIds].sort(), maxQuestions, maxWaitMs};
  const port = Object.freeze({profile: PROFILE, policyDigest: hash(descriptor)});
  ports.set(port, {descriptor, applies, validateQuestion, validateAnswer}); return port;
}

export class TaskRuntimeQuestions {
  constructor(app, port) {requireValue(port === null || ports.has(port)); this.app = app; this.port = port; this.storeInfo = app.store.info?.();}
  bind(record, plan) {
    if (!this.port) return null;
    const config = ports.get(this.port), applies = sync(() => config.applies(clone(record.input)));
    requireValue(typeof applies === 'boolean'); if (!applies) return null;
    requireValue([INTERACTION_FORMAT, REPAIR_FORMAT].includes(this.storeInfo?.format));
    const descriptor = clone(config.descriptor);
    requireValue(descriptor.nodeIds.every(id => plan.nodes.some(node => node.id === id && node.role !== 'verifier' && node.role !== 'planner')));
    const state = {descriptor, policyDigest: hash(descriptor), questions: []};
    this.configured(state, plan); return state;
  }
  configured(state, plan) {
    requireValue(state && this.port && state.policyDigest === this.port.policyDigest && exact(state.descriptor, ports.get(this.port).descriptor));
    requireValue(this.app.verification.supportsQuestions(state.policyDigest));
    for (const nodeId of state.descriptor.nodeIds) {
      const node = plan.nodes.find(node => node.id === nodeId), provider = node?.providerId ?? this.app.execution.defaultProvider;
      requireValue(node && this.app.execution.questionProviders.has(provider));
    }
    return ports.get(this.port);
  }
  shape(request) {
    const body = request.body;
    if (!id(request.taskId) || !id(request.questionId) || !closed(body, ['expectedRevision', 'questionDigest', 'questionRevision', 'answer']) ||
      !Number.isSafeInteger(body.expectedRevision) || body.expectedRevision < 1 || body.questionRevision !== 1 || !sha(body.questionDigest) || !isText(body.answer, 4096))
      reject('invalid_request', 400);
  }
  fact(tx, q) {
    const row = tx.projection('interaction', q.id), fact = decode(row);
    if (!row || row.revision !== 1n || digest(row.bytes) !== q.questionDigest || !fact || fact.profile !== PROFILE) reject('recovery_required', 409);
    return fact;
  }
  current(tx, ticket, questionId = null, {allowPaused = false} = {}) {
    const current = this.app.execution.ticket(tx, ticket), {record, task} = current;
    if (!task.runtimeQuestions || !task.approved || ticket.planDigest !== task.approved.planDigest || ticket.executionType !== 'agent' ||
      !record.executionId || !record.worker.startedAt || !['running', 'awaiting-answer'].includes(record.worker.status) ||
      terminal.has(task.task.status) || task.task.status === 'cancelling' || !allowPaused && task.task.status === 'paused') reject('state_conflict', 409);
    this.configured(task.runtimeQuestions, task.plan);
    if (this.app.now() >= ticket.deadline) reject('question_expired', 410);
    if (!task.runtimeQuestions.descriptor.nodeIds.includes(ticket.nodeId)) reject('unsupported_task', 422);
    const question = questionId === null ? null : task.runtimeQuestions.questions.find(q => q.id === questionId);
    if (questionId !== null && (!question || question.workerId !== ticket.workerId)) reject('not_found', 404);
    const fact = question ? this.fact(tx, question) : null;
    if (fact && (fact.generation !== ticket.generation || fact.executionId !== record.executionId || fact.reservationDigest !== ticket.reservationDigest ||
      fact.inputDigest !== ticket.inputDigest || fact.planDigest !== ticket.planDigest || fact.taskId !== ticket.taskId || fact.workerId !== ticket.workerId)) reject('recovery_required', 409);
    return {...current, question, fact};
  }
  register(ticket, request) {
    if (!closed(request, ['sessionId', 'nativeRequestId', 'toolCallId', 'questionNonce', 'kind', 'prompt', 'options']) ||
      !isText(request.sessionId, 256) || !isText(request.nativeRequestId, 256) || !isText(request.toolCallId, 128) || !/^[a-f0-9]{64}$/.test(request.questionNonce ?? '') ||
      !['input', 'select'].includes(request.kind) || !isText(request.prompt, 2048) || !Array.isArray(request.options) ||
      request.kind === 'input' && request.options.length !== 0 || request.kind === 'select' && (request.options.length < 1 || request.options.length > 16) ||
      request.options.some(value => !isText(value, 4096)) || new Set(request.options).size !== request.options.length) reject('invalid_request', 400);
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.current(tx, ticket, null, {allowPaused: true}), state = task.runtimeQuestions;
      const previous = state.questions.find(q => q.workerId === ticket.workerId && this.fact(tx, q).nativeRequestId === request.nativeRequestId);
      if (previous) {
        if (!exact(this.fact(tx, previous).request, request)) reject('state_conflict', 409);
        return {questionId: previous.id, questionDigest: previous.questionDigest, deadlineAt: previous.deadlineAt};
      }
      if (state.questions.some(q => q.workerId === ticket.workerId && pending(q)) || state.questions.length >= state.descriptor.maxQuestions ||
        record.runtimeSession !== undefined && record.runtimeSession !== request.sessionId ||
        state.questions.some(q => this.fact(tx, q).questionNonce === request.questionNonce)) reject('state_conflict', 409);
      requireValue(sync(() => this.configured(state, task.plan).validateQuestion(clone(request), clone(ticket.input))) === true);
      const at = new Date(this.app.now()).toISOString(), deadlineAt = new Date(Math.min(ticket.deadline, this.app.now() + state.descriptor.maxWaitMs)).toISOString();
      const questionId = this.app.newId('question'), fact = {profile: PROFILE, storeId: this.storeInfo.storeId,
        generation: ticket.generation, taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, attempt: record.worker.attempt,
        executionId: record.executionId, sessionId: request.sessionId, nativeRequestId: request.nativeRequestId, toolCallId: request.toolCallId,
        questionNonce: request.questionNonce, reservationDigest: ticket.reservationDigest, planDigest: ticket.planDigest, inputDigest: ticket.inputDigest,
        policyDigest: state.policyDigest, sequence: state.questions.length + 1, request: clone(request), createdAt: at, deadlineAt};
      const q = {id: questionId, workerId: ticket.workerId, questionDigest: hash(fact), deadlineAt, status: 'open', deliveryStatus: null,
        answerId: null, answerDigest: null, dispatchId: null, dispatchDigest: null, ackId: null, ackDigest: null, operationId: null, commandId: null};
      state.questions.push(q); record.runtimeSession = request.sessionId; record.worker.status = 'awaiting-answer';
      if (task.task.status !== 'paused') task.task.status = 'awaiting-answer';
      else task.pausedFrom = 'awaiting-answer';
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.question-opened', {workerId: ticket.workerId, questionId, questionDigest: q.questionDigest});
      tx.putProjection('interaction', questionId, 0, source, encode(fact)); this.app.execution.putWorker(tx, row, record, source);
      return {questionId, questionDigest: q.questionDigest, deadlineAt};
    });
  }
  answer(request) {
    this.shape(request); const previous = this.app.replay(request); if (previous) return previous;
    return this.app.transaction(true, tx => {
      const prior = this.app.receipt(tx, request); if (prior) return this.app.clarification.replay(tx, prior);
      const task = this.app.get(tx, request.taskId), q = task.runtimeQuestions?.questions.find(q => q.id === request.questionId);
      if (!q) reject('not_found', 404);
      const worker = this.app.execution.worker(tx, q.workerId).record;
      const input = decode(tx.projection('attempt', worker.inputRef)), ticket = {...clone(worker.ticket), input};
      const {fact} = this.current(tx, ticket, q.id);
      if (task.task.revision !== request.body.expectedRevision) reject('revision_conflict', 409);
      if (q.questionDigest !== request.body.questionDigest || q.status !== 'open') reject('state_conflict', 409);
      if (this.app.now() >= Date.parse(q.deadlineAt)) reject('question_expired', 410);
      if (fact.request.kind === 'select' && !fact.request.options.includes(request.body.answer)) reject('invalid_request', 400);
      requireValue(sync(() => this.configured(task.runtimeQuestions, task.plan).validateAnswer(request.body.answer, clone(fact.request), clone(ticket.input))) === true);
      const value = {profile: PROFILE, taskId: task.task.id, questionId: q.id, questionDigest: q.questionDigest,
        answer: request.body.answer, expectedRevision: request.body.expectedRevision, at: new Date(this.app.now()).toISOString()};
      q.answerId = this.app.newId('answer'); q.answerDigest = hash(value); q.status = 'answered'; q.deliveryStatus = 'pending';
      q.operationId = this.app.newId('operation'); q.commandId = this.app.newId('command'); task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.answer-accepted', {workerId: q.workerId, questionId: q.id, answerDigest: q.answerDigest});
      tx.putProjection('interaction', q.answerId, 0, source, encode(value));
      const operation = this.app.operation(tx, task.task, source, 'task.answer', 'accepted', q.operationId);
      this.app.enqueue(tx, source, task.task.id, 'answer', {workerId: q.workerId, questionId: q.id, answerDigest: q.answerDigest}, 'answer', q.commandId);
      const visible = publicTask(task, this.app.now()), result = {taskId: task.task.id, questionId: q.id, operation, acceptedRevision: task.task.revision,
        questionDigest: q.questionDigest, deliveryStatus: 'pending', task: visible, currentTask: visible, replayed: false};
      const {key, requestDigest} = this.app.receiptKey(request); tx.putReceipt(key, requestDigest, source, encode(result)); return clone(result);
    });
  }
  dispatch(ticket, questionId) {
    return this.app.transaction(true, tx => {
      const {task, question: q, fact} = this.current(tx, ticket, questionId, {allowPaused: true});
      if (this.app.now() >= Date.parse(q.deadlineAt)) reject('question_expired', 410);
      if (q.deliveryStatus === null || task.task.status === 'paused') return null;
      if (q.deliveryStatus !== 'pending') reject('state_conflict', 409);
      const row = tx.projection('interaction', q.answerId), answer = decode(row), command = tx.command(q.commandId);
      if (!row || digest(row.bytes) !== q.answerDigest || answer.questionDigest !== q.questionDigest || command?.status !== 'pending' ||
        command.generation !== this.app.owner.generation) reject('recovery_required', 409);
      const value = {profile: PROFILE, questionId, questionDigest: q.questionDigest, answerDigest: q.answerDigest,
        workerId: ticket.workerId, executionId: fact.executionId, generation: ticket.generation, deliveryNonce: randomBytes(32).toString('hex'),
        at: new Date(this.app.now()).toISOString()};
      q.dispatchId = this.app.newId('delivery'); q.dispatchDigest = hash(value); q.deliveryStatus = 'dispatched';
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.answer-dispatched', {workerId: ticket.workerId, questionId, dispatchDigest: q.dispatchDigest});
      tx.putProjection('interaction', q.dispatchId, 0, source, encode(value)); tx.observeCommand(command.id, command.revision, 'unknown', source);
      return {questionId, questionDigest: q.questionDigest, answerDigest: q.answerDigest, deliveryNonce: value.deliveryNonce,
        answer: answer.answer, deadlineAt: q.deadlineAt};
    });
  }
  acknowledge(ticket, questionId, receipt) {
    return this.app.transaction(true, tx => {
      const {row, record, task, question: q} = this.current(tx, ticket, questionId, {allowPaused: true});
      if (!closed(receipt, ['questionDigest', 'answerDigest', 'deliveryNonce']) || receipt.questionDigest !== q.questionDigest ||
        receipt.answerDigest !== q.answerDigest || this.app.now() >= Date.parse(q.deadlineAt)) reject('state_conflict', 409);
      const delivered = tx.projection('interaction', q.dispatchId), dispatch = decode(delivered);
      if (!delivered || digest(delivered.bytes) !== q.dispatchDigest || receipt.deliveryNonce !== dispatch.deliveryNonce) reject('state_conflict', 409);
      if (q.deliveryStatus === 'acknowledged') return true;
      if (q.deliveryStatus !== 'dispatched') reject('state_conflict', 409);
      const ack = {profile: PROFILE, ...clone(receipt), questionId, dispatchDigest: q.dispatchDigest, at: new Date(this.app.now()).toISOString()};
      q.ackId = this.app.newId('ack'); q.ackDigest = hash(ack); q.deliveryStatus = 'acknowledged'; record.worker.status = 'running';
      if (task.task.status !== 'paused' && !task.runtimeQuestions.questions.some(pending)) task.task.status = 'running';
      if (task.task.status === 'paused' && task.pausedFrom === 'awaiting-answer' && !task.runtimeQuestions.questions.some(pending)) task.pausedFrom = 'running';
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.answer-acknowledged', {workerId: ticket.workerId, questionId, ackDigest: q.ackDigest});
      tx.putProjection('interaction', q.ackId, 0, source, encode(ack)); this.app.execution.putWorker(tx, row, record, source);
      this.app.execution.settleOperation(tx, q.operationId, 'succeeded', source, task);
      const command = tx.command(q.commandId); tx.observeCommand(command.id, command.revision, 'observed', source); return true;
    });
  }
  close(task, reason = 'cancelled', workerId = null) {
    const changed = [];
    for (const q of task.runtimeQuestions?.questions ?? []) {
      if (!pending(q) || workerId !== null && q.workerId !== workerId) continue;
      if (q.status === 'open') q.status = reason === 'expired' ? 'expired' : 'cancelled';
      else q.deliveryStatus = q.deliveryStatus === 'dispatched' ? 'unknown' : reason;
      changed.push(q);
    }
    return changed;
  }
  settleClosed(tx, task, source, changed) {
    for (const q of changed) if (q.operationId) {
      this.app.execution.settleOperation(tx, q.operationId, q.deliveryStatus === 'unknown' ? 'unknown' : 'failed', source, task);
      const command = tx.command(q.commandId); if (command && command.status !== 'observed' && !(q.deliveryStatus === 'unknown' && command.status === 'unknown'))
        tx.observeCommand(command.id, command.revision, q.deliveryStatus === 'unknown' ? 'unknown' : 'observed', source);
    }
  }
  cleanupConfirmed(tx, task, source, workerId) {
    for (const q of task.runtimeQuestions?.questions ?? []) if (q.workerId === workerId && q.deliveryStatus === 'unknown' && q.commandId) {
      const command = tx.command(q.commandId);
      // The answer's consumption stays unknown. Original process cleanup makes
      // this delivery obligation terminal; it cannot block future Tasks forever.
      if (command?.status === 'unknown') tx.observeCommand(command.id, command.revision, 'observed', source);
    }
  }
  expired(task) {return (task.runtimeQuestions?.questions ?? []).some(q => pending(q) && this.app.now() >= Date.parse(q.deadlineAt));}
  resultRefs(tx, task, ticket) {
    const dependencies = task.plan.edges.filter(edge => edge.to === ticket.nodeId).map(edge => edge.from), upstream = ticket.input.upstream;
    if (!Array.isArray(upstream) || upstream.length !== dependencies.length || new Set(upstream.map(item => item.nodeId)).size !== upstream.length)
      reject('recovery_required', 409);
    for (const input of upstream) {
      if (!dependencies.includes(input.nodeId)) reject('recovery_required', 409);
      const {record} = this.app.execution.worker(tx, input.workerId);
      if (!id(record.resultRef) || !Array.isArray(record.interactionRefs) || !record.candidate) reject('recovery_required', 409);
      const candidate = tx.projection('attempt', record.resultRef);
      if (record.worker.id !== input.workerId || record.worker.nodeId !== input.nodeId || record.worker.taskId !== ticket.taskId ||
        record.worker.status !== 'completed' || record.cleanup?.cleaned !== true || record.cleanup.started?.executionId !== record.executionId ||
        !task.repair && record.ticket.generation !== ticket.generation || record.ticket.planDigest !== ticket.planDigest || record.ticket.taskId !== ticket.taskId ||
        task.nodes.find(node => node.id === input.nodeId)?.status !== 'completed' || !candidate ||
        record.resultDigest !== hash({candidate: decode(candidate), interactionRefs: record.interactionRefs}) ||
        !exact(record.candidate, input.result) || !exact(record.interactionRefs, input.interactionRefs)) reject('recovery_required', 409);
      if (task.repair && this.app.repair.selected(tx, task, [input.nodeId])[0].record.worker.id !== input.workerId) reject('recovery_required', 409);
    }
    const inherited = this.inherited(tx, task, upstream.flatMap(item => item.interactionRefs));
    if (!exact(inherited, ticket.input.interactionRefs)) reject('recovery_required', 409);
    return this.inherited(tx, task, inherited, ticket.workerId);
  }
  inherited(tx, task, values, workerId = null) {
    if (!Array.isArray(values)) reject('recovery_required', 409);
    const selected = new Map();
    for (const value of values) {
      if (!sha(value?.questionDigest) || !task.runtimeQuestions.questions.some(q => q.questionDigest === value.questionDigest) ||
        selected.has(value.questionDigest) && !exact(selected.get(value.questionDigest), value)) reject('recovery_required', 409);
      selected.set(value.questionDigest, value);
    }
    const result = [];
    // Stable original question order, not the order/depth of the DAG's fan-in.
    // Only selected ancestors and this Worker's own questions are consumed;
    // another branch may still legitimately be awaiting an answer.
    for (const q of task.runtimeQuestions.questions) if (selected.has(q.questionDigest) || q.workerId === workerId) {
      const current = this.reference(tx, q);
      if (selected.has(q.questionDigest) && !exact(selected.get(q.questionDigest), current)) reject('recovery_required', 409);
      result.push(current);
    }
    return result;
  }
  reference(tx, q) {
    if (q.deliveryStatus !== 'acknowledged') reject('state_conflict', 409);
    const question = this.fact(tx, q), answer = tx.projection('interaction', q.answerId), dispatched = tx.projection('interaction', q.dispatchId), ack = tx.projection('interaction', q.ackId);
    if (!answer || !dispatched || !ack || answer.revision !== 1n || dispatched.revision !== 1n || ack.revision !== 1n ||
      digest(answer.bytes) !== q.answerDigest || digest(dispatched.bytes) !== q.dispatchDigest || digest(ack.bytes) !== q.ackDigest) reject('recovery_required', 409);
    return {question, questionDigest: q.questionDigest, answer: decode(answer), answerDigest: q.answerDigest,
      dispatchDigest: q.dispatchDigest, ackDigest: q.ackDigest};
  }
  refs(tx, task, workerId = null) {
    const refs = [];
    for (const q of task.runtimeQuestions?.questions ?? []) {
      if (workerId !== null && q.workerId !== workerId) continue;
      refs.push(this.reference(tx, q));
    }
    return refs;
  }
  questions(tx, request) {
    const result = this.app.clarification.questions(tx, request), task = this.app.get(tx, request.taskId);
    if (!task.runtimeQuestions) return result;
    const all = task.runtimeQuestions.questions.map(q => {const fact = this.fact(tx, q);
      return {id: q.id, taskId: task.task.id, workerId: q.workerId, nodeId: fact.nodeId, revision: 1, kind: 'business', subject: q.questionDigest,
        questionDigest: q.questionDigest, prompt: fact.request.prompt, options: fact.request.options.map(value => ({value, label: value})), deadlineAt: q.deadlineAt,
        answer: q.answerId ? decode(tx.projection('interaction', q.answerId)).answer : null,
        status: q.status === 'open' && this.app.now() >= Date.parse(q.deadlineAt) ? 'expired' : q.status, deliveryStatus: q.deliveryStatus};});
    const {cursor = '', limit = 50} = request.page ?? {};
    // Preapproval entries stay visible after approval; both fact families share
    // the original lexicographic cursor, not two independently paged streams.
    const old = this.app.clarification.questions(tx, {...request, page: {limit: 100}}).items;
    const items = [...old, ...all].sort((a, b) => a.id < b.id ? -1 : 1).filter(q => !cursor || q.id > cursor).slice(0, limit);
    return {...result, items, nextCursor: items.length === limit ? items.at(-1).id : null};
  }
}
