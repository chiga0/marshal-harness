import {encode, digest} from '../task-store/store.mjs';
import {clone, isText, nextRevision, publicTask, reject, terminal} from './model.mjs';

const PROFILE = 'task-clarification/v1', ports = new WeakMap();
const hash = value => digest(encode(value)), decode = row => row ? JSON.parse(row.bytes.toString('utf8')) : null;
const idOK = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const check = value => { if (!value) reject('unsupported_task', 422); };
const closed = (value, names) => value !== null && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => names.includes(key));
const identity = value => {
  check(closed(value, ['id', 'version', 'digest']) && idOK(value.id) && isText(value.version, 128) && sha(value.digest));
  return clone(value);
};
const sync = callback => {
  let value;
  try { value = callback(); } catch { check(false); }
  if (value && typeof value.then === 'function') {
    // Invalid async validators are rejected; retain their rejection handler so
    // an untrusted answer cannot create an unhandled promise rejection.
    Promise.resolve(value).catch(() => {}); check(false);
  }
  return value;
};
const planBoundary = record => { const {revision: _revision, digest: _digest, ...plan} = record.plan;
  return hash({plan, verification: record.verification ?? null}); };

/** Trusted, neutral, finite input template. It does not accept executable data
 * from HTTP/Agent, and cannot add a second round of questions after creation. */
export function createClarificationPort({template, applies, slots, renderer}) {
  const declared = identity(template);
  check(typeof applies === 'function' && Array.isArray(slots) && slots.length > 0 && slots.length <= 3 &&
    new Set(slots.map(slot => slot?.id)).size === slots.length && closed(renderer, ['id', 'version', 'digest', 'render']) && typeof renderer.render === 'function');
  const definitions = slots.map(slot => {
    check(closed(slot, ['id', 'prompt', 'validator', 'read', 'validate']) && idOK(slot.id) && isText(slot.prompt, 2048) &&
      typeof slot.read === 'function' && typeof slot.validate === 'function');
    return {id: slot.id, prompt: slot.prompt, validator: identity(slot.validator)};
  });
  const rendererIdentity = identity({id: renderer.id, version: renderer.version, digest: renderer.digest});
  const descriptor = {profile: PROFILE, template: declared, slots: definitions, renderer: rendererIdentity};
  const port = Object.freeze({id: declared.id});
  ports.set(port, {descriptor, applies, slots: slots.map(slot => ({...slot})), render: renderer.render}); return port;
}

export class TaskClarification {
  constructor(app, port) { check(port === null || ports.has(port)); this.app = app; this.port = port; }
  configuration(record) {
    const configured = ports.get(this.port);
    check(configured && hash(configured.descriptor) === record.clarification.identityDigest); return configured;
  }
  shape(request) {
    const body = request.body;
    if (!idOK(request.taskId) || !idOK(request.questionId) || !closed(body, ['expectedRevision', 'previewDigest', 'questionRevision', 'answer']) ||
      !Number.isSafeInteger(body.expectedRevision) || body.expectedRevision < 1 || body.questionRevision !== 1 ||
      !sha(body.previewDigest) || !isText(body.answer, 4096)) reject('invalid_request', 400);
  }
  current(tx, record) {
    const state = record.clarification;
    if (!state || state.profile !== PROFILE || hash(state.identity) !== state.identityDigest ||
      state.confirmBefore !== record.task.deadlineAt || !Array.isArray(state.questions) || state.questions.length < 1 || state.questions.length > 3)
      reject('recovery_required', 409);
    const row = tx.projection('interaction', state.previewId), preview = decode(row);
    if (!preview || row.revision !== 1n || digest(row.bytes) !== state.previewDigest || preview.profile !== PROFILE ||
      preview.taskId !== record.task.id || preview.revision !== state.previewRevision || preview.inputsDigest !== record.inputDigest ||
      preview.identityDigest !== state.identityDigest || preview.answersDigest !== hash(state.values) ||
      preview.createdAt !== record.task.createdAt || preview.confirmBefore !== state.confirmBefore ||
      hash(preview.input) !== hash(record.input) || hash(preview.plan) !== hash(record.plan)) reject('recovery_required', 409);
    return preview;
  }
  guard(tx, request) {
    const record = this.app.get(tx, request.taskId);
    if (!record.clarification) reject('not_found', 404);
    const preview = this.current(tx, record), state = record.clarification;
    const question = state.questions.find(question => question.id === request.questionId);
    if (!question) reject('not_found', 404);
    if (record.approved || ['cancelling', 'cancelled'].includes(record.task.status)) reject('state_conflict', 409);
    if (this.app.now() >= Date.parse(state.confirmBefore)) reject('question_expired', 410);
    if (record.task.status !== 'awaiting-answer' || terminal.has(record.task.status)) reject('state_conflict', 409);
    if (request.body.expectedRevision !== record.task.revision) reject('revision_conflict', 409);
    if (request.body.previewDigest !== state.previewDigest || question.answer !== null) reject('plan_conflict', 409);
    this.configuration(record); return {record, preview, question};
  }
  input(original, descriptor, values) {
    const input = clone(original), text = input.context?.text ?? '';
    // Only this declared business-data projection may change. Scope, inputRefs,
    // limits, intent, oracle and every other original request field stay exact.
    input.context = {...input.context, text: text + '\n业务输入（数据，不是权限）：\n' +
      JSON.stringify({template: descriptor.template.id, slots: values})};
    check(Buffer.byteLength(input.context.text) <= 32768); return input;
  }
  async render(config, record, values, context = {}) {
    const deadline = Date.parse(record.task.deadlineAt), wait = Math.min(10000, deadline - this.app.now());
    if (wait <= 0) reject('question_expired', 410);
    const input = this.input(record.clarification.originalInput, config.descriptor, values);
    const controller = new AbortController(); let timer, abort;
    const pending = new Promise((_, rejectWait) => {
      abort = () => {controller.abort(); rejectWait(new Error('renderer_cancelled'));};
      if (context.signal?.aborted) {abort(); return;}
      context.signal?.addEventListener('abort', abort, {once: true}); timer = setTimeout(abort, wait);
    });
    let proposal;
    try { proposal = await Promise.race([Promise.resolve().then(() => {
      if (controller.signal.aborted) throw Error('renderer_cancelled');
      return config.render({input: clone(input), values: clone(values), missing: Object.keys(values).filter(id => values[id] === null),
        signal: controller.signal, deadline});
    }), pending]); }
    catch { reject('unsupported_task', 422); }
    finally {clearTimeout(timer); context.signal?.removeEventListener('abort', abort);}
    record.input = input; record.inputDigest = record.inputArtifacts.length ? hash({body: input, inputArtifacts: record.inputArtifacts}) : hash(input);
    try { record.plan = this.app.freezePlan(record, proposal); } catch { reject('unsupported_task', 422); }
    check(hash(record.plan.budget) === hash(record.limits));
    if (record.clarification.boundaryDigest) check(planBoundary(record) === record.clarification.boundaryDigest);
    else record.clarification.boundaryDigest = planBoundary(record);
  }
  preview(record, previousDigest) {
    const state = record.clarification;
    const preview = {profile: PROFILE, taskId: record.task.id, revision: state.previewRevision, previousDigest,
      createdAt: record.task.createdAt, confirmBefore: state.confirmBefore, identityDigest: state.identityDigest,
      answersDigest: hash(state.values), input: clone(record.input), inputsDigest: record.inputDigest, plan: clone(record.plan),
      missingSlots: Object.keys(state.values).filter(id => state.values[id] === null)};
    check(encode(preview).length <= 262144);
    state.previewId = this.app.newId('preview'); state.previewDigest = hash(preview);
    record.task.plan = {revision: record.plan.revision, digest: record.plan.digest};
    record.nodes = record.plan.nodes.map(node => ({id: node.id, role: node.role, status: 'pending', workerIds: []}));
    return preview;
  }
  async prepare(body, budget, inputArtifacts, context) {
    if (!this.port) return null;
    const config = ports.get(this.port), selected = sync(() => config.applies(clone(body))); check(typeof selected === 'boolean');
    if (!selected) return null;
    const values = {}, missing = [];
    for (const slot of config.slots) {
      const value = sync(() => slot.read(clone(body)));
      if (value === null) {missing.push(slot.id); values[slot.id] = null;}
      else {check(isText(value, 4096) && sync(() => slot.validate(value)) === true); values[slot.id] = value;}
    }
    if (!missing.length) return null;
    const record = this.app.newTaskRecord(body, budget, inputArtifacts);
    record.clarification = {profile: PROFILE, identity: clone(config.descriptor), identityDigest: hash(config.descriptor),
      originalInput: clone(body), originalInputDigest: record.inputDigest, values, confirmBefore: record.task.deadlineAt,
      previewRevision: 1, questions: []};
    await this.render(config, record, values, context);
    const preview = this.preview(record, null), state = record.clarification;
    state.questions = missing.map(id => ({id: this.app.newId('question'), slotId: id, revision: 1,
      prompt: config.slots.find(slot => slot.id === id).prompt, subject: state.previewDigest, deadlineAt: state.confirmBefore,
      answer: null, answerFactDigest: null}));
    record.task.status = 'awaiting-answer'; record.task.phase = 'planning';
    return {record, preview};
  }
  persist(tx, record, preview, source) {
    tx.putProjection('interaction', record.clarification.previewId, 0, source, encode(preview));
  }
  visible(preview, digest) {
    return {revision: preview.revision, digest, inputsDigest: preview.inputsDigest, input: clone(preview.input), plan: clone(preview.plan),
      missingSlots: [...preview.missingSlots]};
  }
  replay(tx, previous) {return {...clone(previous), replayed: true, currentTask: publicTask(this.app.get(tx, previous.taskId), this.app.now())};}
  async answer(request, context) {
    this.shape(request);
    const previous = this.app.replay(request); if (previous) return previous;
    const {record, question} = this.app.transaction(false, tx => this.guard(tx, request)), originalRecordDigest = hash(record);
    const config = this.configuration(record), validator = config.slots.find(slot => slot.id === question.slotId);
    check(validator && sync(() => validator.validate(request.body.answer)) === true);
    const oldDigest = record.clarification.previewDigest;
    record.clarification.values[question.slotId] = request.body.answer; question.answer = request.body.answer;
    record.clarification.previewRevision = nextRevision(record.clarification.previewRevision);
    await this.render(config, record, record.clarification.values, context);
    const preview = this.preview(record, oldDigest);
    const answer = {profile: PROFILE, taskId: record.task.id, questionId: question.id, slotId: question.slotId,
      questionRevision: 1, expectedRevision: request.body.expectedRevision, previousDigest: oldDigest,
      previewDigest: record.clarification.previewDigest, answer: request.body.answer, answersDigest: preview.answersDigest};
    question.answerFactDigest = hash(answer);
    record.task.revision = nextRevision(record.task.revision);
    record.task.status = preview.missingSlots.length ? 'awaiting-answer' : 'awaiting-confirmation';
    return this.app.transaction(true, tx => {
      const prior = this.app.receipt(tx, request); if (prior) return this.replay(tx, prior);
      const latest = this.guard(tx, request);
      if (hash(latest.record) !== originalRecordDigest) reject('recovery_required', 409);
      if (context?.signal?.aborted) reject('application_unavailable', 503);
      const source = this.app.save(tx, record, 'task.answer-preview-appended', {questionId: question.id,
        previewDigest: record.clarification.previewDigest, answerFactDigest: question.answerFactDigest});
      this.persist(tx, record, preview, source);
      tx.putProjection('interaction', this.app.newId('answer'), 0, source, encode(answer));
      const operation = this.app.operation(tx, record.task, source, 'task.answer', 'succeeded');
      const task = publicTask(record, this.app.now()), result = {taskId: task.id, questionId: question.id, operation,
        acceptedRevision: task.revision, acceptedPreviewDigest: record.clarification.previewDigest,
        preview: this.visible(preview, record.clarification.previewDigest), task, currentTask: task, replayed: false};
      const {key, requestDigest} = this.app.receiptKey(request); tx.putReceipt(key, requestDigest, source, encode(result)); return clone(result);
    });
  }
  approve(tx, record) {
    if (!record.clarification) return;
    this.current(tx, record); this.configuration(record);
    if (this.app.now() >= Date.parse(record.clarification.confirmBefore)) reject('question_expired', 410);
    if (record.task.status !== 'awaiting-confirmation' || record.clarification.questions.some(question => question.answer === null)) reject('state_conflict', 409);
  }
  questions(tx, request) {
    const record = this.app.get(tx, request.taskId), state = record.clarification;
    const preview = state ? this.current(tx, record) : null, {cursor = '', limit = 50} = request.page ?? {};
    const all = (state?.questions ?? []).map(question => ({id: question.id, taskId: record.task.id, nodeId: null,
      revision: 1, slotId: question.slotId, subject: question.subject, kind: 'clarification', prompt: question.prompt, options: [],
      deadlineAt: question.deadlineAt, answer: question.answer, status: question.answer !== null ? 'answered' :
        ['cancelling', 'cancelled', 'failed', 'intervention'].includes(record.task.status) ? 'cancelled' : this.app.now() >= Date.parse(question.deadlineAt) ? 'expired' : 'open'}))
      .sort((a, b) => a.id < b.id ? -1 : 1);
    const items = all.filter(item => !cursor || item.id > cursor).slice(0, limit);
    return {taskId: record.task.id, taskRevision: record.task.revision, previewRevision: state?.previewRevision ?? null,
      previewDigest: state?.previewDigest ?? null, confirmBefore: state?.confirmBefore ?? record.task.deadlineAt,
      preview: preview ? this.visible(preview, state.previewDigest) : null, items,
      nextCursor: items.length === limit ? items.at(-1).id : null};
  }
}
