import {WORKER_CANCELLATION_FORMAT, encode, digest} from '../task-store/store.mjs';
import {affectedNodes} from './graph.mjs';
import {clone, nextRevision, reject, terminal} from './model.mjs';

const PROFILE = 'task-worker-cancellation/v1', hash = value => digest(encode(value));
const same = (a, b) => hash(a) === hash(b), decode = row => row ? JSON.parse(row.bytes) : null;
const live = record => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(record.worker.status);
const check = value => {if (!value) reject('recovery_required', 409);};
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);

// A target-specific branch of the SAME execution/cleanup reducer. No handles,
// callbacks, replacement attempts, generic unknown-operation rewrite or SQL side ledger.
export class TaskWorkerCancellation {
  constructor(app) {this.app = app; this.enabled = app.store.info?.().format === WORKER_CANCELLATION_FORMAT;}
  shape(request) {
    const body = request.body;
    if (!id(request.workerId) || !body || ![Object.prototype, null].includes(Object.getPrototypeOf(body)) ||
      Object.keys(body).length !== 1 || !Number.isSafeInteger(body.expectedRevision) || body.expectedRevision < 1)
      reject('invalid_request', 400);
  }
  request(tx, request) {
    this.shape(request);
    const {record} = this.app.execution.worker(tx, request.workerId);
    return {...request, taskId: record.ticket.taskId};
  }
  async cancel(request) {
    if (!this.enabled) reject('unsupported_operation', 501);
    this.shape(request);
    return this.app.transaction(true, tx => {
      request = this.request(tx, request);
      const previous = this.app.receipt(tx, request); if (previous) return clone(previous);
      const {row, record} = this.app.execution.worker(tx, request.workerId), task = this.app.get(tx, request.taskId), ticket = record.ticket;
      if (task.task.revision !== request.body.expectedRevision) reject('revision_conflict', 409);
      if (record.stopIntent || !['queued', 'running', 'awaiting-answer'].includes(record.worker.status) ||
        terminal.has(task.task.status) || task.task.status === 'cancelling' || task.cancelIntent ||
        ticket.generation !== this.app.owner.generation.toString() || !this.app.repair.current(task, ticket) ||
        this.app.now() >= ticket.deadline || this.app.now() >= Date.parse(task.task.deadlineAt)) reject('state_conflict', 409);
      check(record.worker.taskId === task.task.id && task.workerIds.includes(record.worker.id));
      const affected = ticket.planDigest === null ? [] : affectedNodes(task.plan.nodes, task.plan.edges, [ticket.nodeId]);
      const descendants = affected.filter(nodeId => nodeId !== ticket.nodeId);
      check(descendants.every(nodeId => {const node = task.nodes.find(value => value.id === nodeId);
        return node && ['pending', 'cancelled'].includes(node.status) && !this.app.execution.workers(tx, task).some(({record: other}) =>
          other.worker.nodeId === nodeId && this.app.repair.current(task, other.ticket) && live(other));}));
      const {key, requestDigest} = this.app.receiptKey(request), revision = nextRevision(task.task.revision);
      record.stopIntent = {profile: PROFILE, workerId: ticket.workerId, taskId: ticket.taskId, nodeId: ticket.nodeId,
        generation: ticket.generation, reservationDigest: ticket.reservationDigest, inputDigest: ticket.inputDigest,
        planDigest: ticket.planDigest, repairId: ticket.repairId ?? null, affectedNodes: affected, revision,
        operationId: this.app.newId('operation'), commandId: this.app.newId('command'), keyDigest: key.keyDigest, requestDigest,
        at: new Date(this.app.now()).toISOString()};
      task.workerCancelled = true; task.task.revision = revision;
      for (const node of task.nodes) if (descendants.includes(node.id)) node.status = 'cancelled';
      const questions = this.app.runtimeQuestions.close(task, 'cancelled', ticket.workerId);
      const source = this.app.save(tx, task, 'worker.cancel-requested', {workerId: ticket.workerId, stop: record.stopIntent,
        dependencyCancelled: descendants});
      this.app.runtimeQuestions.settleClosed(tx, task, source, questions);
      this.app.execution.putWorker(tx, row, record, source);
      const operation = this.app.operation(tx, task.task, source, 'worker.cancel', 'accepted', record.stopIntent.operationId, ticket.workerId);
      this.app.enqueue(tx, source, task.task.id, 'worker-cancel', {taskId: task.task.id, workerId: ticket.workerId,
        operationId: operation.id, reservationDigest: ticket.reservationDigest}, 'stop', record.stopIntent.commandId);
      // No future Worker or cleanup is manufactured for a cancelled dependency.
      for (const command of this.app.repair.commands(tx, task)) {
        const payload = JSON.parse(command.payload);
        if (command.status === 'pending' && payload.action === 'execute' && descendants.includes(payload.nodeId) &&
          (payload.repairId ?? null) === (ticket.repairId ?? null)) tx.observeCommand(command.id, command.revision, 'observed', source);
      }
      tx.putReceipt(key, requestDigest, source, encode(operation)); return clone(operation);
    });
  }
  // This validates the original stop event/receipt/command together. It is used
  // again in current-owner recovery, never trusted from a preclaim snapshot.
  checked(tx, record) {
    if (!record.stopIntent) return null;
    check(this.enabled);
    const stop = record.stopIntent, ticket = record.ticket;
    check(stop.profile === PROFILE && stop.workerId === ticket.workerId && stop.taskId === ticket.taskId && stop.nodeId === ticket.nodeId &&
      stop.generation === ticket.generation && stop.reservationDigest === ticket.reservationDigest && stop.inputDigest === ticket.inputDigest &&
      stop.planDigest === ticket.planDigest && stop.repairId === (ticket.repairId ?? null));
    const receipt = tx.receipt(stop.workerId, 'worker.cancel', stop.keyDigest, stop.requestDigest), original = decode(receipt);
    const events = tx.eventsWithField(stop.taskId, 'workerId', stop.workerId, 100, ['worker.cancel-requested']);
    check(receipt && events.length === 1 && receipt.source.stream === stop.taskId && receipt.source.digest === events[0].digest &&
      receipt.source.sequence === events[0].sequence && events[0].generation.toString() === stop.generation &&
      same(JSON.parse(events[0].bytes).payload.stop, stop) && original.id === stop.operationId && original.workerId === stop.workerId &&
      original.taskId === stop.taskId && original.kind === 'worker.cancel' && original.status === 'accepted' && original.taskRevision === stop.revision);
    const command = tx.command(stop.commandId), payload = command && JSON.parse(command.payload);
    check(command && command.kind === 'stop' && command.taskId === stop.taskId && command.generation.toString() === stop.generation &&
      command.source.stream === receipt.source.stream && command.source.sequence === receipt.source.sequence && command.source.digest === receipt.source.digest &&
      same(payload, {action: 'worker-cancel', taskId: stop.taskId, workerId: stop.workerId,
        operationId: stop.operationId, reservationDigest: stop.reservationDigest}));
    return {stop, command};
  }
  aggregate(task, records) {
    if (!this.enabled || !task.workerCancelled || terminal.has(task.task.status) || task.task.status === 'cancelling' || task.cancelIntent || task.failureCode) return;
    if (records.some(live) || task.nodes.some(node => ['pending', 'running', 'waiting', 'ready'].includes(node.status))) return;
    task.task.status = 'failed'; task.task.phase = 'terminal'; task.task.code = 'worker_cancelled'; task.failureCode = 'worker_cancelled';
  }
  settle(tx, task, record, source) {
    const checked = this.checked(tx, record); if (!checked) return;
    const {stop, command} = checked, row = tx.projection('operation', stop.operationId), op = decode(row);
    check(op && op.kind === 'worker.cancel' && op.workerId === stop.workerId && op.taskId === stop.taskId);
    // Only original cleanup consumers or strict never-permitted consumers call
    // this after updating the Worker and releasing its exact capacity.
    const clean = !live(record) && (record.cleanup?.cleaned === true && !record.custody?.extraScopes.length ||
      record.unpermittedSettlement?.disposition === 'never-permitted' && record.cleanup === null);
    const occupied = this.app.execution.capacity(tx).value.active.some(value => value.workerId === stop.workerId);
    if (clean) check(!occupied);
    const status = clean ? 'succeeded' : record.worker.status === 'unknown' || record.ticket.generation !== this.app.owner.generation.toString() ? 'unknown' : null;
    if (!status || op.status === status || op.status === 'succeeded') return;
    check(['accepted', 'running', 'unknown'].includes(op.status));
    op.status = status; op.taskRevision = task.task.revision; op.updatedAt = new Date(this.app.now()).toISOString();
    tx.putProjection('operation', op.id, row.revision, source, encode(op));
    const state = status === 'unknown' ? 'unknown' : 'observed';
    if (command.status !== state) tx.observeCommand(command.id, command.revision, state, source);
  }
}
