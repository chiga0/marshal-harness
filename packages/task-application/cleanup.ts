import {createPublicKey, verify} from 'node:crypto';
import {encode, digest, CUSTODY_FORMAT, INTERACTION_FORMAT, REPAIR_FORMAT, UNPERMITTED_FORMAT, WORKER_CANCELLATION_FORMAT, LEADER_FORMAT} from '../task-store/store.mjs';
import {clone, reject, nextRevision} from './model.mjs';

const PROFILE = 'node-execution-custody/v1', hash = value => digest(encode(value));
const decode = row => row ? JSON.parse(row.bytes.toString()) : null;
const live = record => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(record.worker.status);
const same = (a, b) => hash(a) === hash(b);
const keys = (value, names) => value && Object.getPrototypeOf(value) === Object.prototype && Object.keys(value).sort().join(',') === names.sort().join(',');
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const check = value => { if (!value) reject('recovery_required', 409); };
const receiptKey = workerId => ({scope: workerId, operation: 'execution.custody-binding', keyDigest: hash('binding')});
const START_PROTOCOL = {profile: 'node-unpermitted-reservation/v1', preparation: 'file-staging-only/v1'};
const attachedRoles = {leader: 'planner', review: 'reviewer', publication: 'integrator', postverify: 'verifier'};
function permitFacts(tx, record, complete = false) {
  const key = receiptKey(record.worker.id);
  const events = tx.eventsWithField(record.ticket.taskId, 'workerId', record.worker.id, 100, complete ? null : ['worker.custody-permitted']);
  return {events, permits: events.filter(event => JSON.parse(event.bytes).payload.type === 'worker.custody-permitted'),
    receipt: tx.receiptByKey(key.scope, key.operation, key.keyDigest)};
}
function profile(value) {
  check(keys(value, ['id', 'scope', 'eligible']) && id(value.id) && value.scope === 'inherited-process-group' && typeof value.eligible === 'boolean');
  return clone(value);
}

/** Cleanup-only exception. Every trust key comes from the ORIGINAL same-Store
 * binding receipt. This class never accepts a candidate/result/Decision, calls a
 * Provider, issues a new ticket or signals a persisted PID. */
export class TaskCleanup {
  constructor(execution) {
    this.execution = execution; this.app = execution.app;
    this.managed = this.app.store.info?.().format === LEADER_FORMAT;
    this.unpermitted = [UNPERMITTED_FORMAT, WORKER_CANCELLATION_FORMAT, LEADER_FORMAT].includes(this.app.store.info?.().format);
    this.supported = [CUSTODY_FORMAT, INTERACTION_FORMAT, REPAIR_FORMAT, UNPERMITTED_FORMAT, WORKER_CANCELLATION_FORMAT, LEADER_FORMAT].includes(this.app.store.info?.().format);
  }
  enabled() { return this.supported; }
  static inspectBeforeClaim(store, after = '', limit = 25) {
    const unpermitted = [UNPERMITTED_FORMAT, WORKER_CANCELLATION_FORMAT, LEADER_FORMAT].includes(store.info().format);
    return store.inspectRecovery(tx => {
      const rows = tx.projections('attempt', after, limit), items = [];
      for (const row of rows) {
        const record = decode(row);
        if (!record?.ticket || !live(record)) continue;
        if (unpermitted) {
          const facts = permitFacts(tx, record);
          // An orphan receipt/event is damaged authority, not an unbound cut.
          if (record.custody || facts.receipt || facts.permits.length) {
            check(record.custody && facts.receipt && facts.permits.length === 1);
            check(facts.receipt.source.stream === record.ticket.taskId && facts.receipt.source.sequence === facts.permits[0].sequence &&
              facts.receipt.source.digest === facts.permits[0].digest && facts.permits[0].generation.toString() === record.ticket.generation &&
              JSON.parse(facts.permits[0].bytes).payload.descriptorDigest === hash(record.custody.descriptor));
          }
        }
        if (!record.custody) continue;
        const descriptor = record.custody.descriptor, key = receiptKey(record.worker.id);
        const original = tx.receipt(key.scope, key.operation, key.keyDigest, hash(descriptor));
        check(original && original.bytes.equals(encode(descriptor)) && descriptor.binding.workerId === record.worker.id &&
          descriptor.binding.generation === record.ticket.generation && descriptor.binding.reservationDigest === record.ticket.reservationDigest);
        items.push(clone(descriptor));
      }
      return {items, nextCursor: rows.length === limit ? rows.at(-1).id : null};
    });
  }
  binding(ticket, executionProfile, ownerExpiresAt = this.app.owner.expiresAt) {
    check(this.enabled());
    return {storeId: this.app.owner.storeId, generation: ticket.generation, taskId: ticket.taskId, workerId: ticket.workerId,
      commandId: ticket.commandId, reservationDigest: ticket.reservationDigest, inputDigest: ticket.inputDigest,
      planDigest: ticket.planDigest, deadline: ticket.deadline, ownerExpiresAt, executionProfile: profile(executionProfile)};
  }
  bind(ticket, descriptor, executionProfile) {
    check(this.enabled());
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.execution.ticket(tx, ticket), binding = this.binding(ticket, executionProfile, descriptor?.binding?.ownerExpiresAt);
      check(keys(descriptor, ['profile', 'custodyId', 'executionId', 'publicKey', 'binding', 'bindingDigest']) && descriptor.profile === PROFILE &&
        id(descriptor.custodyId) && id(descriptor.executionId) && same(descriptor.binding, binding) && descriptor.bindingDigest === hash(binding) &&
        typeof descriptor.publicKey === 'string' && /^[A-Za-z0-9+/]{59}=$/.test(descriptor.publicKey) &&
        Number.isSafeInteger(binding.ownerExpiresAt) && binding.ownerExpiresAt > this.app.now() && binding.ownerExpiresAt <= this.app.owner.expiresAt);
      let publicKey; try { publicKey = createPublicKey({key: Buffer.from(descriptor.publicKey, 'base64'), format: 'der', type: 'spki'}); } catch { check(false); }
      check(publicKey.asymmetricKeyType === 'ed25519');
      check(!record.stopIntent);
      if (record.custody) { check(same(record.custody.descriptor, descriptor)); return clone(record.custody.descriptor); }
      check(record.worker.status === 'queued' && record.executionId === null &&
        !['cancelling', 'paused', 'completed', 'failed', 'cancelled', 'intervention'].includes(task.task.status) && this.app.now() < ticket.deadline);
      record.custody = {descriptor: clone(descriptor), extraScopes: [], settledDigest: null};
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.custody-permitted', {workerId: ticket.workerId, descriptorDigest: hash(descriptor)});
      this.execution.putWorker(tx, row, record, source);
      const key = receiptKey(ticket.workerId); tx.putReceipt(key, hash(descriptor), source, encode(descriptor));
      return clone(descriptor);
    });
  }
  extraScope(ticket, code) {
    check(this.enabled() && typeof code === 'string' && /^[a-z][a-z0-9_-]{0,63}$/.test(code));
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.execution.ticket(tx, ticket);
      check(record.custody && live(record) && !record.custody.settledDigest);
      if (record.custody.extraScopes.includes(code)) return;
      check(record.custody.extraScopes.length < 32);
      record.custody.extraScopes.push(code);
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.extra-scope-recorded', {workerId: ticket.workerId, code});
      this.execution.putWorker(tx, row, record, source);
    });
  }
  pending(after = '', limit = 25) {
    check(this.enabled());
    return this.app.transaction(false, tx => {
      const rows = tx.projections('attempt', after, limit);
      return {items: rows.map(decode).filter(record => record?.ticket && record.custody &&
          record.ticket.generation !== this.app.owner.generation.toString() && (live(record) || record.custody.settledDigest))
        .map(record => ({workerId: record.worker.id, descriptor: clone(record.custody.descriptor), settledDigest: record.custody.settledDigest})),
      nextCursor: rows.length === limit ? rows.at(-1).id : null};
    });
  }
  pendingUnpermitted(after = '', limit = 25) {
    check(this.unpermitted);
    return this.app.transaction(false, tx => {
      const rows = tx.projections('attempt', after, limit);
      return {items: rows.map(decode).filter(record => record?.ticket && !record.custody &&
        record.ticket.generation !== this.app.owner.generation.toString() && (live(record) || record.unpermittedSettlement))
        .map(record => record.worker.id), nextCursor: rows.length === limit ? rows.at(-1).id : null};
    });
  }
  // Attached v7 executions have no DAG node. Reconstruct their ORIGINAL
  // reservation/call/action inside this same settlement TX instead of treating
  // a missing DAG node as permission to skip identity checks. This is cleanup
  // only: no old output, replacement call, action retry or new decision.
  attached(tx, record, task) {
    const ticket = record.ticket, worker = record.worker, type = ticket.executionType;
    if (!Object.hasOwn(attachedRoles, type)) return null;
    check(this.managed && this.app.leader.port &&
      task.leader?.policyDigest === this.app.leader.port.policyDigest && ticket.taskId === task.task.id && worker.taskId === task.task.id &&
      ticket.workerId === worker.id && ticket.nodeId === worker.nodeId && ticket.role === worker.role && ticket.role === attachedRoles[type] &&
      ticket.providerId === worker.providerId && task.workerIds?.filter(id => id === worker.id).length === 1 &&
      ticket.nodeId.startsWith('managed-' + type + '-') && !task.nodes.some(node => node.id === ticket.nodeId));
    const inputRow = tx.projection('attempt', record.inputRef), input = decode(inputRow), {reservationDigest, ...identity} = ticket;
    check(inputRow?.revision === 1n && digest(inputRow.bytes) === ticket.inputDigest && hash({...identity, input}) === reservationDigest &&
      same(input.task, task.input) && same(input.inputArtifacts, task.inputArtifacts ?? []) &&
      input.node.id === ticket.nodeId && input.node.role === ticket.role &&
      (ticket.planDigest === null ? input.plan === null && task.approved === null :
        task.approved?.planDigest === ticket.planDigest && task.plan?.digest === ticket.planDigest && same(input.plan, task.plan)));
    const reserved = tx.eventsWithField(task.task.id, 'workerId', worker.id, 2, ['worker.reserved']);
    check(reserved.length === 1 && reserved[0].generation.toString() === ticket.generation &&
      decode(reserved[0]).payload.reservationDigest === reservationDigest && inputRow.source.stream === task.task.id &&
      inputRow.source.sequence === reserved[0].sequence && inputRow.source.digest === reserved[0].digest);
    const command = tx.command(ticket.commandId), payload = command && decode({bytes: command.payload});
    check(command?.status === 'unknown' && command.taskId === task.task.id && command.generation.toString() === ticket.generation &&
      command.kind === (type === 'postverify' ? 'verify' : 'start') && command.inputDigest === digest(command.payload) &&
      payload.taskId === task.task.id && payload.action === type);
    if (type === 'leader') {
      const original = input.leader; check(id(original?.obligationId));
      const row = tx.projection('interaction', original.obligationId), obligation = decode(row);
      check(original?.profile === 'task-managed-leader/v1' && original.taskId === task.task.id &&
        task.leader.activeCallId === original.callId && task.leader.activeWorkerId === worker.id && task.leader.obligationId === original.obligationId &&
        obligation?.id === original.obligationId && obligation.taskId === task.task.id && obligation.status === 'claimed' &&
        obligation.workerId === worker.id && obligation.generation === ticket.generation && obligation.commandId === command.id &&
        payload.obligationId === obligation.id && obligation.readSetDigest === hash(original.snapshot.readSet));
      task.leader.activeCallId = null; task.leader.activeWorkerId = null; task.leader.obligationId = null;
      return {effectUnknown: false, recovery: {type, obligation: clone(obligation)}, close: source => {
        obligation.status = 'closed'; tx.putProjection('interaction', obligation.id, row.revision, source, encode(obligation));
      }};
    }
    const actionId = type === 'review' ? task.leader.reviewAction : task.leader[type]?.actionId;
    check(id(actionId));
    const row = tx.projection('attempt', actionId), action = decode(row);
    check(action?.id === actionId && action.taskId === task.task.id && action.status === 'running' && action.workerId === worker.id &&
      action.commandId === command.id && payload.actionId === actionId && (type === 'publication' ?
        same(action.result, {artifactId: input.publicationArtifact?.id, acceptanceDigest: action.authorization?.acceptanceDigest, reviewDigest: action.authorization?.reviewDigest}) : action.result === null));
    const decision = task.leader.history.find(item => item.digest === action.sourceDecision), accepted = decision && decode(tx.projection('attempt', decision.callId));
    check(accepted && hash(accepted) === decision.digest);
    if (type !== 'postverify') check(accepted.actions.some((value, index) =>
      action.id === 'action-' + hash({callId: decision.callId, decisionDigest: decision.digest, index}).slice(7) && same(value, action.payload)));
    if (type === 'review') check(action.payload.type === 'work' && action.payload.kind === 'review' &&
      input.review?.profile === 'task-independent-review/v1' && input.review.taskId === task.task.id &&
      input.review.selectionDigest === action.payload.selectionDigest && hash(action.payload) === action.payloadDigest);
    else if (type === 'publication') check(action.payload.type === 'deliver' && hash(action.payload) === action.payloadDigest &&
      same(action.binding, input.publication?.binding) && same(action.authorization, input.publication?.authorization) &&
      task.leader.publication.receiptArtifactId === null);
    else check(action.payload.type === 'postverify' && action.payload.publicationActionId === task.leader.publication?.actionId &&
      action.payloadDigest === hash({publication: task.leader.publication.actionId}) && task.leader.publication.receiptArtifactId &&
      this.app.artifacts.metadata(tx, task.leader.publication.receiptArtifactId).digest === input.postverify?.publicationReceiptDigest &&
      task.leader.postverify.evidenceArtifactId === null);
    // A signed none-start observation proves this publication did not run.
    // Otherwise cleanup cannot prove whether an external effect happened.
    const effectUnknown = type === 'publication' && record.cleanup.started !== null;
    action.status = effectUnknown ? 'unknown' : worker.status === 'cancelled' ? 'cancelled' : 'failed';
    if (type !== 'review') task.leader[type].status = action.status;
    return {effectUnknown, recovery: {type, action: clone(action)}, close: source => {
      tx.putProjection('attempt', action.id, row.revision, source, encode(action));
    }};
  }
  // Reconstruct the ORIGINAL reservation, including its immutable input and
  // original event generation. Called inside the current owner's settlement
  // TX; neither preclaim scans nor the current deployment supply this proof.
  unpermittedProof(tx, record, task) {
    const ticket = record.ticket, worker = record.worker;
    check(this.unpermitted && ['agent', 'verification'].includes(ticket?.executionType) && ticket?.startProtocol && same(ticket.startProtocol, START_PROTOCOL) &&
      /^[1-9][0-9]*$/.test(ticket.generation) && BigInt(ticket.generation) < this.app.owner.generation &&
      ticket.taskId === task.task.id && ticket.workerId === worker.id && worker.taskId === task.task.id &&
      ticket.nodeId === worker.nodeId && ticket.role === worker.role && ticket.providerId === worker.providerId &&
      task.workerIds?.filter(value => value === worker.id).length === 1 &&
      Number.isSafeInteger(worker.attempt) && worker.attempt > 0 && worker.attempt <= task.attempts &&
      task.attempts === task.workerIds.length && task.attempts <= (task.plan?.budget ?? task.limits).maxAttempts &&
      record.executionId === null && worker.startedAt === null && record.cleanup === null &&
      record.resultRef === null && record.resultDigest === null && !record.candidate && !record.interactionRefs && record.progressSequence === 0);
    const input = tx.projection('attempt', record.inputRef); check(input && input.revision === 1n);
    const {reservationDigest, ...identity} = ticket, originalInput = decode(input);
    check(digest(input.bytes) === ticket.inputDigest && hash({...identity, input: originalInput}) === reservationDigest &&
      same(originalInput.task, task.input) && same(originalInput.inputArtifacts, task.inputArtifacts ?? []) &&
      originalInput.node.id === ticket.nodeId && originalInput.node.role === ticket.role &&
      Number.isSafeInteger(ticket.deadline) && ticket.deadline <= Date.parse(task.task.deadlineAt));
    if (ticket.planDigest === null) check(ticket.role === 'planner' && originalInput.plan === null && task.approved === null);
    else check(task.approved?.planDigest === ticket.planDigest && task.plan?.digest === ticket.planDigest &&
      same(originalInput.plan, task.plan) && task.nodes.find(node => node.id === ticket.nodeId)?.workerIds.includes(worker.id));
    if (task.repair) check((ticket.repairId ?? null) === (task.activeRepair?.repairId ?? null));
    const facts = permitFacts(tx, record, true), reservations = facts.events.filter(event => JSON.parse(event.bytes).payload.type === 'worker.reserved');
    check(!record.custody && !facts.receipt && facts.permits.length === 0 && reservations.length === 1);
    const reserved = reservations[0];
    check(reserved.generation.toString() === ticket.generation && JSON.parse(reserved.bytes).payload.reservationDigest === reservationDigest &&
      input.source.stream === ticket.taskId && input.source.sequence === reserved.sequence && input.source.digest === reserved.digest);
    const allowed = new Set(['worker.reserved', 'worker.input-prepared', 'worker.unpermitted-settled']);
    if (this.app.workerCancellation.checked(tx, record)) allowed.add('worker.cancel-requested');
    check(facts.events.every(event => allowed.has(JSON.parse(event.bytes).payload.type)));
    const prepared = facts.events.filter(event => JSON.parse(event.bytes).payload.type === 'worker.input-prepared');
    const observation = this.app.inputAudit.checked(record);
    check(prepared.length === (observation ? 1 : 0) && prepared.every(event => event.generation.toString() === ticket.generation && event.sequence > reserved.sequence) &&
      (!observation || observation.handedOffAt === null &&
      observation.coverage === 'metadata-only' && observation.policy === null && observation.snapshot === null));
    const command = tx.command(ticket.commandId), payload = command && decode({bytes: command.payload});
    check(command && command.taskId === ticket.taskId && command.generation.toString() === ticket.generation &&
      command.attemptId === '' && command.nodeId === '' && command.kind === 'start' && command.inputDigest === digest(command.payload) &&
      payload.taskId === ticket.taskId && (ticket.planDigest === null ? payload.action === 'plan' && payload.inputDigest === task.inputDigest :
        payload.action === 'execute' && payload.nodeId === ticket.nodeId && payload.planDigest === ticket.planDigest));
    return {facts, command, reserved};
  }
  // A named v5 exception only. It never makes generic unknown Operations
  // writable. Question ACK/delivery state and all original receipts stay exact.
  settleUnpermittedOperations(tx, task, source) {
    if (!this.unpermitted || !['failed', 'cancelled'].includes(task.task.status)) return;
    const workers = this.execution.workers(tx, task);
    if (workers.some(({record}) => live(record)) || !workers.some(({record}) => record.unpermittedSettlement)) return;
    check(workers.every(({record}) => record.cleanup?.cleaned === true && !record.custody?.extraScopes.length ||
      record.unpermittedSettlement && record.cleanup === null));
    check(!this.execution.capacity(tx).value.active.some(item => item.taskId === task.task.id));
    for (const {row, record} of workers.filter(({record}) => record.unpermittedSettlement)) {
      const events = tx.eventsWithField(task.task.id, 'workerId', record.worker.id).filter(event =>
        JSON.parse(event.bytes).payload.type === 'worker.unpermitted-settled');
      check(events.length === 1 && row.source.stream === task.task.id && row.source.digest === events[0].digest &&
        same(JSON.parse(events[0].bytes).payload.settlement, record.unpermittedSettlement) &&
        record.unpermittedSettlement.generation === events[0].generation.toString());
    }
    for (const [operationId, kind, status] of [[task.cancelIntent?.operationId, 'task.cancel', 'succeeded'],
      [task.activeRepair?.operationId, 'task.repair', 'failed']]) {
      if (!operationId) continue;
      const row = tx.projection('operation', operationId), operation = decode(row);
      check(operation?.taskId === task.task.id && operation.kind === kind);
      if (!['accepted', 'running', 'unknown'].includes(operation.status)) continue;
      operation.status = status; operation.taskRevision = task.task.revision;
      operation.updatedAt = new Date(this.app.now()).toISOString();
      tx.putProjection('operation', operationId, row.revision, source, encode(operation));
    }
  }
  settleUnpermitted(workerId) {
    check(this.unpermitted);
    return this.app.transaction(true, tx => {
      const {row, record} = this.execution.worker(tx, workerId), task = this.app.get(tx, record.ticket.taskId);
      const {facts, command, reserved} = this.unpermittedProof(tx, record, task), ticket = record.ticket;
      const settlements = facts.events.filter(event => JSON.parse(event.bytes).payload.type === 'worker.unpermitted-settled');
      const capacity = this.execution.capacity(tx), occupied = capacity.value.active.filter(item => item.workerId === workerId);
      if (record.unpermittedSettlement) {
        const settled = record.unpermittedSettlement;
        check(settlements.length === 1 && same(JSON.parse(settlements[0].bytes).payload.settlement, settled) &&
          settled.reservationDigest === ticket.reservationDigest && same(settled.startProtocol, ticket.startProtocol) &&
          settled.disposition === 'never-permitted' && settled.generation === settlements[0].generation.toString() &&
          row.source.digest === settlements[0].digest && ['failed', 'cancelled'].includes(record.worker.status) &&
          command.status === 'observed' && command.observation.digest === settlements[0].digest && occupied.length === 0);
        return clone(settled);
      }
      check(settlements.length === 0 && record.worker.status === 'queued' && record.worker.finishedAt === null &&
        !['completed', 'failed', 'cancelled'].includes(task.task.status) && command.status === 'unknown' &&
        command.observation.stream === ticket.taskId && command.observation.sequence === reserved.sequence && command.observation.digest === reserved.digest &&
        occupied.length === 1 && occupied[0].taskId === ticket.taskId && occupied[0].generation === ticket.generation);
      const targeted = !!this.app.workerCancellation.checked(tx, record);
      const cancelled = targeted || !!task.cancelIntent && !task.failureCode;
      const settlement = {reservationDigest: ticket.reservationDigest, startProtocol: clone(ticket.startProtocol),
        generation: this.app.owner.generation.toString(), disposition: 'never-permitted'};
      record.unpermittedSettlement = settlement; record.failureCode = 'service_interrupted';
      record.worker.status = cancelled ? 'cancelled' : 'failed'; record.worker.phase = 'terminal';
      record.worker.finishedAt = new Date(this.app.now()).toISOString();
      if (ticket.planDigest !== null) {const node = task.nodes.find(node => node.id === ticket.nodeId); check(node); node.status = record.worker.status;}
      const remaining = this.execution.workers(tx, task).some(({record: other}) => other.worker.id !== workerId && live(other));
      const taskCancelled = !!task.cancelIntent && !task.failureCode;
      if (!taskCancelled) task.failureCode ??= targeted ? 'worker_cancelled' : 'service_interrupted';
      const effectUnknown = task.leader?.publication?.status === 'unknown';
      task.task.status = remaining || effectUnknown ? 'intervention' : taskCancelled ? 'cancelled' : 'failed';
      task.task.code = effectUnknown ? 'publication_effect_unresolved' : remaining ? 'previous_execution_unresolved' : taskCancelled ? 'task_cancelled' : targeted ? task.failureCode : 'service_interrupted';
      if (!remaining && !effectUnknown) { task.task.phase = 'terminal'; if (task.leader) task.leader.stage = 'terminal';
        for (const node of task.nodes) if (['pending', 'ready', 'waiting'].includes(node.status)) node.status = 'cancelled'; }
      // A never-permitted Worker cannot have produced its own question/ACK.
      check(!(task.runtimeQuestions?.questions ?? []).some(question => question.workerId === workerId));
      const questions = remaining ? [] : this.app.runtimeQuestions.close(task);
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.unpermitted-settled', {workerId, settlement});
      this.app.runtimeQuestions.settleClosed(tx, task, source, questions);
      this.execution.putWorker(tx, row, record, source);
      tx.observeCommand(command.id, command.revision, 'observed', source);
      capacity.value.active = capacity.value.active.filter(item => item.workerId !== workerId);
      this.execution.putCapacity(tx, capacity.row, capacity.value);
      this.app.repair.settle(tx, task, source);
      this.settleUnpermittedOperations(tx, task, source);
      this.app.workerCancellation.settle(tx, task, record, source);
      if (!remaining && !effectUnknown && task.cancelIntent) {
        const stop = tx.command(task.cancelIntent.commandId); check(stop?.kind === 'stop' && stop.taskId === task.task.id);
        if (stop.status !== 'observed') tx.observeCommand(stop.id, stop.revision, 'observed', source);
      }
      return clone(settlement);
    });
  }
  settle(workerId, observation) {
    check(this.enabled());
    return this.app.transaction(true, tx => {
      const {row, record} = this.execution.worker(tx, workerId), task = this.app.get(tx, record.ticket.taskId), custody = record.custody;
      check(custody && record.ticket.generation !== this.app.owner.generation.toString());
      const d = custody.descriptor, key = receiptKey(workerId);
      const original = tx.receipt(key.scope, key.operation, key.keyDigest, hash(d));
      check(original && original.bytes.equals(encode(d)) && d.binding.storeId === this.app.owner.storeId &&
        same(d.binding, this.binding(record.ticket, d.binding.executionProfile, d.binding.ownerExpiresAt)) && d.bindingDigest === hash(d.binding));
      check(keys(observation, ['payload', 'signature']) && encode(observation).length <= 32768);
      const p = observation.payload;
      check(keys(p, ['profile', 'custodyId', 'executionId', 'bindingDigest', 'permitReceived', 'cleanup', 'observedAt']) &&
        p.profile === PROFILE && p.custodyId === d.custodyId && p.executionId === d.executionId && p.bindingDigest === d.bindingDigest &&
        typeof p.permitReceived === 'boolean' && Number.isFinite(Date.parse(p.observedAt)) &&
        typeof observation.signature === 'string' && /^[A-Za-z0-9+/]{86}==$/.test(observation.signature));
      let signed = false;
      try { signed = verify(null, encode(p), createPublicKey({key: Buffer.from(d.publicKey, 'base64'), format: 'der', type: 'spki'}), Buffer.from(observation.signature, 'base64')); } catch {}
      check(signed);
      const observationDigest = hash(observation);
      if (custody.settledDigest) { check(custody.settledDigest === observationDigest); return {observationDigest, status: record.worker.status}; }
      const c = p.cleanup;
      check(live(record) && d.binding.executionProfile.eligible && custody.extraScopes.length === 0 && c?.cleaned === true && c.executionId === d.executionId &&
        !['completed', 'failed', 'cancelled'].includes(task.task.status));
      if (c.scope === 'none-start') check(p.permitReceived === false && c.started === null && record.executionId === null && c.reason === 'custody_not_launched');
      else check(c.scope === 'inherited-process-group' && c.started?.executionId === d.executionId &&
        (record.executionId === null || record.executionId === d.executionId) && c.guardExit?.observed === true && c.guardExit.signal === 'SIGKILL');
      // Unknown siblings remain live. An earlier cancel intent is retained even
      // after old-generation observation changed the public Task to intervention.
      const targeted = !!this.app.workerCancellation.checked(tx, record);
      const cancelled = targeted || !!task.cancelIntent && !task.failureCode;
      custody.settledDigest = observationDigest; record.cleanup = clone(c);
      record.worker.status = cancelled ? 'cancelled' : 'failed'; record.worker.phase = 'terminal';
      record.worker.finishedAt = new Date(this.app.now()).toISOString(); record.failureCode = 'service_interrupted';
      const attached = this.attached(tx, record, task);
      if (!attached && record.ticket.planDigest !== null) {
        const node = task.nodes.find(item => item.id === record.ticket.nodeId); check(node);
        if (node.status !== 'completed') node.status = cancelled ? 'cancelled' : 'failed';
      }
      const remaining = this.execution.workers(tx, task).some(({record: other}) => other.worker.id !== workerId && live(other));
      const taskCancelled = !!task.cancelIntent && !task.failureCode;
      const recovery = attached && this.app.leader.interrupted(task, record, attached.recovery, observationDigest);
      if (!taskCancelled) task.failureCode ??= targeted ? 'worker_cancelled' : 'service_interrupted';
      const effectUnknown = attached?.effectUnknown || task.leader?.publication?.status === 'unknown';
      task.task.status = remaining || effectUnknown || recovery ? 'intervention' : taskCancelled ? 'cancelled' : 'failed';
      task.task.code = effectUnknown ? 'publication_effect_unresolved' : remaining ? 'previous_execution_unresolved' : recovery ? 'leader_recovery_pending' : taskCancelled ? 'task_cancelled' : targeted ? task.failureCode : 'service_interrupted';
      if (recovery && task.failureCode === 'service_interrupted') delete task.failureCode;
      if (!remaining && !effectUnknown && !recovery) { task.task.phase = 'terminal'; if (task.leader) task.leader.stage = 'terminal';
        for (const node of task.nodes) if (['pending', 'ready', 'waiting'].includes(node.status)) node.status = 'cancelled'; }
      const closedQuestions = this.app.runtimeQuestions.close(task, 'cancelled', workerId);
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.cleanup-reconciled', {workerId, observationDigest, status: record.worker.status});
      attached?.close(source);
      this.app.runtimeQuestions.settleClosed(tx, task, source, closedQuestions);
      this.app.runtimeQuestions.cleanupConfirmed(tx, task, source, workerId);
      this.app.repair.settle(tx, task, source);
      this.execution.putWorker(tx, row, record, source);
      if (!remaining && !effectUnknown && task.cancelIntent) {
        const stop = tx.command(task.cancelIntent.commandId);
        check(stop && stop.taskId === task.task.id && stop.kind === 'stop');
        this.execution.settleOperation(tx, task.cancelIntent.operationId, 'succeeded', source, task);
        if (stop.status !== 'observed') tx.observeCommand(stop.id, stop.revision, 'observed', source);
      }
      const command = tx.command(record.ticket.commandId); check(command && command.status === 'unknown');
      if (!attached?.effectUnknown) tx.observeCommand(command.id, command.revision, 'observed', source);
      const capacity = this.execution.capacity(tx);
      check(capacity.value.active.filter(item => item.workerId === workerId && item.generation === record.ticket.generation).length === 1);
      capacity.value.active = capacity.value.active.filter(item => item.workerId !== workerId);
      this.execution.putCapacity(tx, capacity.row, capacity.value);
      this.settleUnpermittedOperations(tx, task, source);
      this.app.workerCancellation.settle(tx, task, record, source);
      return {observationDigest, status: record.worker.status};
    });
  }
}
