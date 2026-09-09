import {createPublicKey, verify} from 'node:crypto';
import {encode, digest, CUSTODY_FORMAT} from '../task-store/store.mjs';
import {clone, reject, nextRevision} from './model.mjs';

const PROFILE = 'node-execution-custody/v1', hash = value => digest(encode(value));
const decode = row => row ? JSON.parse(row.bytes.toString()) : null;
const live = record => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(record.worker.status);
const same = (a, b) => hash(a) === hash(b);
const keys = (value, names) => value && Object.getPrototypeOf(value) === Object.prototype && Object.keys(value).sort().join(',') === names.sort().join(',');
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const check = value => { if (!value) reject('recovery_required', 409); };
const receiptKey = workerId => ({scope: workerId, operation: 'execution.custody-binding', keyDigest: hash('binding')});
function profile(value) {
  check(keys(value, ['id', 'scope', 'eligible']) && id(value.id) && value.scope === 'inherited-process-group' && typeof value.eligible === 'boolean');
  return clone(value);
}

/** Cleanup-only exception. Every trust key comes from the ORIGINAL same-Store
 * binding receipt. This class never accepts a candidate/result/Decision, calls a
 * Provider, issues a new ticket or signals a persisted PID. */
export class TaskCleanup {
  constructor(execution) { this.execution = execution; this.app = execution.app; this.supported = this.app.store.info?.().format === CUSTODY_FORMAT; }
  enabled() { return this.supported; }
  static inspectBeforeClaim(store, after = '', limit = 25) {
    return store.inspectRecovery(tx => {
      const rows = tx.projections('attempt', after, limit), items = [];
      for (const row of rows) {
        const record = decode(row);
        if (!record?.ticket || !record.custody || !live(record)) continue;
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
      const cancelled = !!task.cancelIntent && !task.failureCode;
      custody.settledDigest = observationDigest; record.cleanup = clone(c);
      record.worker.status = cancelled ? 'cancelled' : 'failed'; record.worker.phase = 'terminal';
      record.worker.finishedAt = new Date(this.app.now()).toISOString(); record.failureCode = 'service_interrupted';
      if (record.ticket.planDigest !== null) {
        const node = task.nodes.find(item => item.id === record.ticket.nodeId); check(node);
        if (node.status !== 'completed') node.status = cancelled ? 'cancelled' : 'failed';
      }
      const remaining = this.execution.workers(tx, task).some(({record: other}) => other.worker.id !== workerId && live(other));
      if (!cancelled) task.failureCode ??= 'service_interrupted';
      task.task.status = remaining ? 'intervention' : cancelled ? 'cancelled' : 'failed';
      task.task.code = remaining ? 'previous_execution_unresolved' : cancelled ? 'task_cancelled' : 'service_interrupted';
      if (!remaining) { task.task.phase = 'terminal'; for (const node of task.nodes) if (['pending', 'ready', 'waiting'].includes(node.status)) node.status = 'cancelled'; }
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.cleanup-reconciled', {workerId, observationDigest, status: record.worker.status});
      this.execution.putWorker(tx, row, record, source);
      if (!remaining && task.cancelIntent) {
        const stop = tx.command(task.cancelIntent.commandId);
        check(stop && stop.taskId === task.task.id && stop.kind === 'stop');
        this.execution.settleOperation(tx, task.cancelIntent.operationId, 'succeeded', source, task);
        if (stop.status !== 'observed') tx.observeCommand(stop.id, stop.revision, 'observed', source);
      }
      const command = tx.command(record.ticket.commandId); check(command && command.status === 'unknown');
      tx.observeCommand(command.id, command.revision, 'observed', source);
      const capacity = this.execution.capacity(tx);
      check(capacity.value.active.filter(item => item.workerId === workerId && item.generation === record.ticket.generation).length === 1);
      capacity.value.active = capacity.value.active.filter(item => item.workerId !== workerId);
      this.execution.putCapacity(tx, capacity.row, capacity.value);
      return {observationDigest, status: record.worker.status};
    });
  }
}
