import {encode, digest, REPAIR_FORMAT, UNPERMITTED_FORMAT} from '../task-store/store.mjs';
import {affectedNodes} from './graph.mjs';
import {clone, isText, nextRevision, publicTask, reject, terminal} from './model.mjs';

const PROFILE = 'task-local-repair/v1', ports = new WeakMap();
const hash = value => digest(encode(value));
const decode = row => row ? JSON.parse(row.bytes.toString()) : null;
const same = (a, b) => hash(a) === hash(b);
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const closed = (value, keys) => value && [null, Object.prototype].includes(Object.getPrototypeOf(value)) && Object.keys(value).sort().join(',') === [...keys].sort().join(',');
const check = (value, code = 'state_conflict', status = 409) => {if (!value) reject(code, status);};
const live = record => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(record.worker.status);

/** Trusted policy data only. No Agent callback can manufacture this capability. */
export function createRepairPort({policy, nodeIds, assertions} = {}) {
  check(closed(policy, ['id', 'version', 'description']) && id(policy.id) && isText(policy.version, 128) && isText(policy.description, 4096) &&
    Array.isArray(nodeIds) && nodeIds.length > 0 && nodeIds.length <= 64 && nodeIds.every(id) && new Set(nodeIds).size === nodeIds.length &&
    Array.isArray(assertions) && assertions.length > 0 && assertions.length <= 64 &&
    assertions.every(value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(value)) && new Set(assertions).size === assertions.length,
  'unsupported_task', 422);
  const descriptor = {profile: PROFILE, policy: clone(policy), nodeIds: [...nodeIds].sort(), assertions: [...assertions].sort()};
  const port = Object.freeze({profile: PROFILE, policyDigest: hash(descriptor)}); ports.set(port, descriptor); return port;
}

export class TaskRepair {
  constructor(app, port) {
    check(port === null || ports.has(port), 'unsupported_task', 422); this.app = app; this.port = port;
    this.format = app.store.info?.().format;
    if (port) {
      // Reject an unusable trusted composition before a Task can spend even
      // its Planner Attempt. Per-plan and persisted bindings are rechecked too.
      check([REPAIR_FORMAT, UNPERMITTED_FORMAT].includes(this.format), 'unsupported_task', 422);
      const command = app.verification.repairBinding(port.policyDigest);
      check(closed(command, ['policyDigest', 'checkerDigest', 'verificationPolicyDigest', 'assertions']) &&
        sha(command.checkerDigest) && Array.isArray(command.assertions) &&
        same(command.assertions, ports.get(port).assertions), 'unsupported_task', 422);
    }
  }
  bind(task, plan, verification) {
    if (!this.port) return null;
    check([REPAIR_FORMAT, UNPERMITTED_FORMAT].includes(this.format) && verification, 'unsupported_task', 422);
    const descriptor = clone(ports.get(this.port)), command = this.app.verification.repairBinding(this.port.policyDigest);
    check(descriptor.nodeIds.every(id => plan.nodes.some(node => node.id === id && ['author', 'integrator'].includes(node.role))) &&
      same(command.assertions, descriptor.assertions) && command.verificationPolicyDigest === verification.policyDigest, 'unsupported_task', 422);
    return {descriptor, policyDigest: this.port.policyDigest, command};
  }
  configured(task) {
    check(task.repair && this.port && task.repair.policyDigest === this.port.policyDigest &&
      same(task.repair.descriptor, ports.get(this.port)) && same(task.repair.command, this.app.verification.repairBinding(this.port.policyDigest)) &&
      task.plan.repair?.policyDigest === this.port.policyDigest, 'unsupported_task', 422);
  }
  current(task, ticket) {return !task.repair || ticket.planDigest === null || (ticket.repairId ?? null) === (task.activeRepair?.repairId ?? null);}
  selected(tx, task, nodeIds = task.plan.nodes.filter(node => node.id !== task.verification.nodeId).map(node => node.id)) {
    check(task.repair && task.selectedResults, 'candidate_manifest_conflict', 422);
    return nodeIds.map(nodeId => {
      const selected = task.selectedResults[nodeId]; check(selected, 'candidate_manifest_conflict', 422);
      const row = tx.projection('attempt', selected.workerId), record = decode(row);
      const original = record?.resultRef ? tx.projection('attempt', record.resultRef) : null;
      const input = record?.inputRef ? tx.projection('attempt', record.inputRef) : null;
      check(record && record.worker.id === selected.workerId && record.worker.taskId === task.task.id && record.worker.nodeId === nodeId &&
        record.ticket.planDigest === task.plan.digest && record.worker.status === 'completed' && record.cleanup?.cleaned === true &&
        !record.custody?.extraScopes.length && record.executionId === record.cleanup.started?.executionId &&
        task.nodes.find(node => node.id === nodeId)?.status === 'completed' && record.candidate && original?.revision === 1n && input?.revision === 1n &&
        record.resultDigest === selected.resultDigest && hash(record.candidate) === selected.candidateManifestDigest &&
        hash(task.runtimeQuestions ? {candidate: decode(original), interactionRefs: record.interactionRefs} : decode(original)) === record.resultDigest,
      'candidate_manifest_conflict', 422);
      check(digest(input.bytes) === record.ticket.inputDigest &&
        same(this.app.verification.candidate({...record.ticket, input: decode(input)}, decode(original)), record.candidate),
        'candidate_manifest_conflict', 422);
      if (task.runtimeQuestions) this.app.runtimeQuestions.inherited(tx, task, record.interactionRefs ?? []);
      return {row, record};
    });
  }
  recordSelection(task, record) {
    if (!task.repair || record.ticket.planDigest === null || record.worker.nodeId === task.verification.nodeId || record.worker.status !== 'completed') return;
    check(this.current(task, record.ticket) && !task.selectedResults[record.worker.nodeId], 'candidate_manifest_conflict', 422);
    task.selectedResults[record.worker.nodeId] = {workerId: record.worker.id, resultDigest: record.resultDigest, candidateManifestDigest: hash(record.candidate)};
  }
  commands(tx, task) {
    const result = []; let after = '';
    for (let page = 0; page < 100; page++) {
      const rows = tx.taskCommands(task.task.id, after, 25);
      check(rows.every(row => row.taskId === task.task.id && row.source.stream === task.task.id), 'application_unavailable', 503);
      result.push(...rows);
      if (rows.length < 25) return result; after = rows.at(-1).id;
    }
    reject('application_unavailable', 503);
  }
  decision(tx, task) {
    const row = task.decision ? tx.projection('attempt', task.decision.id) : null, decision = decode(row);
    check(row?.revision === 1n && decision && digest(row.bytes) === task.decision.digest && decision.taskId === task.task.id &&
      decision.planDigest === task.plan.digest, 'state_conflict', 409); return decision;
  }
  eligible(tx, task, nodeIds) {
    this.configured(task);
    check(task.task.status === 'failed' && !task.cancelIntent && !task.userCancelled && this.app.now() < Date.parse(task.task.deadlineAt) &&
      task.approved?.planDigest === task.plan.digest && task.reworkCount < 100);
    const decision = this.decision(tx, task), rejection = decision.contentRejection;
    check(decision.status === 'rejected' && rejection && rejection.policyDigest === task.repair.policyDigest &&
      rejection.failedAssertions.length > 0 && rejection.failedAssertions.every(name => task.repair.descriptor.assertions.includes(name)) &&
      !(task.repairs ?? []).some(repair => repair.decisionDigest === task.decision.digest));
    const evidence = decision.artifacts.filter(artifact => artifact.kind === 'evidence');
    check(evidence.length === 1 && evidence[0].taskId === task.task.id && evidence[0].status === 'ready');
    this.app.artifacts.recheck(tx, evidence);
    check(this.app.execution.workers(tx, task).every(({record}) => !live(record) && record.cleanup?.cleaned === true && !record.custody?.extraScopes.length) &&
      this.commands(tx, task).every(command => command.status === 'observed'));
    check(nodeIds.every(id => task.repair.descriptor.nodeIds.includes(id)));
    const affected = affectedNodes(task.plan.nodes, task.plan.edges, nodeIds);
    check(affected.includes(task.verification.nodeId));
    const retained = this.selected(tx, task, task.plan.nodes.filter(node => node.id !== task.verification.nodeId && !affected.includes(node.id)).map(node => node.id));
    check(task.plan.budget.maxAttempts - task.attempts >= affected.length, 'capacity_exceeded', 429);
    return {decision, evidence: evidence[0], affected, retainedFiles: retained.flatMap(({record}) => record.candidate.files),
      retained: retained.map(({record}) => ({nodeId: record.worker.nodeId, ...task.selectedResults[record.worker.nodeId]}))};
  }
  view(tx, task) {
    const value = publicTask(task, this.app.now());
    if (task.repair && task.task.status === 'failed') {
      const roots = [...task.repair.descriptor.nodeIds].sort((a, b) =>
        affectedNodes(task.plan.nodes, task.plan.edges, [a]).length - affectedNodes(task.plan.nodes, task.plan.edges, [b]).length);
      // The action means at least one requested closure is admissible, not that
      // the caller may rerun every branch. Mutation rechecks its exact subset.
      for (const root of roots) {
        try {this.eligible(tx, task, [root]); value.allowedActions = ['repair']; break;} catch {}
      }
    }
    return value;
  }
  shape(request) {
    const b = request.body;
    check(id(request.taskId) && closed(b, ['expectedRevision', 'planDigest', 'decisionDigest', 'nodeIds', 'feedback']) &&
      Number.isSafeInteger(b.expectedRevision) && b.expectedRevision >= 1 && sha(b.planDigest) && sha(b.decisionDigest) &&
      Array.isArray(b.nodeIds) && b.nodeIds.length > 0 && b.nodeIds.length <= 64 && b.nodeIds.every(id) && new Set(b.nodeIds).size === b.nodeIds.length &&
      isText(b.feedback, 4096), 'invalid_request', 400);
  }
  replay(tx, previous) {return {...clone(previous), currentTask: this.view(tx, this.app.get(tx, previous.taskId)), replayed: true};}
  repair(request) {
    this.shape(request); const previous = this.app.replay(request); if (previous) return previous;
    const inspect = tx => {
      const task = this.app.get(tx, request.taskId), b = request.body;
      check(task.task.revision === b.expectedRevision, 'revision_conflict');
      check(task.plan?.digest === b.planDigest && task.decision?.digest === b.decisionDigest, 'plan_conflict');
      return {task, ...this.eligible(tx, task, b.nodeIds)};
    };
    const original = this.app.transaction(false, inspect);
    // The old original evidence is an immutable diagnostic input, never a new
    // success assertion. Missing committed bytes cannot be silently recreated.
    const bytes = this.app.artifacts.bytes(original.evidence);
    for (const file of original.retainedFiles) this.app.artifacts.bytes(file);
    let report; try {report = JSON.parse(bytes.toString());} catch {reject('application_unavailable', 503);}
    check(report.profile === 'task-verification-command/v1' && report.reportDigest === original.decision.contentRejection.reportDigest &&
      report.binding.reservationDigest === original.decision.reservationDigest && report.binding.inputDigest === original.decision.inputDigest &&
      report.binding.planDigest === original.decision.planDigest && report.binding.checkerDigest === original.task.repair.command.checkerDigest &&
      digest(Buffer.from(report.originalReport)) === report.reportDigest, 'state_conflict');
    return this.app.mutate(request, tx => {
      const current = inspect(tx), {task} = current;
      check(same(original, current));
      const repairId = this.app.newId('repair'), operationId = this.app.newId('operation');
      const fact = {profile: PROFILE, repairId, taskId: task.task.id, cycle: task.reworkCount + 1,
        generation: this.app.owner.generation.toString(), planDigest: task.plan.digest, policyDigest: task.repair.policyDigest,
        decisionDigest: task.decision.digest, nodeIds: [...request.body.nodeIds].sort(), affectedNodes: current.affected,
        feedback: request.body.feedback, feedbackDigest: hash(request.body.feedback), evidence: current.evidence,
        failedAssertions: current.decision.contentRejection.failedAssertions, retained: current.retained,
        deadlineAt: task.task.deadlineAt, operationId};
      task.repairs ??= []; task.repairs.push({repairId, decisionDigest: fact.decisionDigest, nodeIds: fact.nodeIds, affectedNodes: fact.affectedNodes, operationId});
      task.activeRepair = fact;
      for (const node of task.nodes) if (fact.affectedNodes.includes(node.id)) {
        node.status = 'pending'; if (Object.hasOwn(task.selectedResults, node.id)) task.selectedResults[node.id] = null;
      }
      task.reworkCount++; task.task.revision = nextRevision(task.task.revision); task.task.status = 'queued'; task.task.phase = 'execution';
      delete task.failureCode; delete task.task.code;
      task.acceptance = {status: 'pending', evidenceIds: [], digest: null}; task.task.artifactIds = [];
      const source = this.app.save(tx, task, 'task.repair-accepted', {repairId, decisionDigest: fact.decisionDigest, affectedNodes: fact.affectedNodes});
      tx.putProjection('attempt', repairId, 0, source, encode(fact));
      const operation = this.app.operation(tx, task.task, source, 'task.repair', 'accepted', operationId);
      for (const nodeId of fact.affectedNodes) this.app.enqueue(tx, source, task.task.id, 'execute',
        {taskId: task.task.id, nodeId, planDigest: task.plan.digest, repairId});
      const projected = publicTask(task, this.app.now());
      return {source, result: {taskId: task.task.id, repairId, operation, acceptedRevision: task.task.revision,
        planDigest: task.plan.digest, decisionDigest: fact.decisionDigest, affectedNodes: fact.affectedNodes,
        task: projected, currentTask: clone(projected), replayed: false}};
    });
  }
  input(tx, task, nodeId) {
    if (!task.activeRepair || !task.activeRepair.affectedNodes.includes(nodeId)) return null;
    const fact = decode(tx.projection('attempt', task.activeRepair.repairId)); check(same(fact, task.activeRepair));
    this.app.artifacts.recheck(tx, [fact.evidence]);
    return {repairId: fact.repairId, decisionDigest: fact.decisionDigest, policyDigest: fact.policyDigest,
      failedAssertions: clone(fact.failedAssertions), feedback: fact.feedback, affectedNodes: clone(fact.affectedNodes), evidence: clone(fact.evidence)};
  }
  settle(tx, task, source) {
    if (!task.activeRepair || !terminal.has(task.task.status)) return;
    this.app.execution.settleOperation(tx, task.activeRepair.operationId,
      task.task.status === 'completed' ? 'succeeded' : task.task.status === 'intervention' ? 'unknown' : 'failed', source, task);
  }
  recoverUnstarted(tx, task) {
    if (!task.activeRepair || task.activeRepair.generation === this.app.owner.generation.toString() || terminal.has(task.task.status)) return false;
    const commands = this.commands(tx, task).filter(command => decode({bytes: command.payload}).repairId === task.activeRepair.repairId);
    const workers = this.app.execution.workers(tx, task).filter(({record}) => record.ticket.repairId === task.activeRepair.repairId);
    // Never replay a remaining old-generation command. Completed original
    // reservations are usable as cleanup facts only; live/unknown belong to
    // original custody. The zero-reservation cut is this same proof with [] .
    if (commands.length !== task.activeRepair.affectedNodes.length || workers.some(({record}) => live(record) ||
      record.cleanup?.cleaned !== true || record.custody?.extraScopes.length) || commands.some(command => {
      if (command.generation.toString() !== task.activeRepair.generation) return true;
      const reserved = workers.filter(({record}) => record.ticket.commandId === command.id);
      return command.status === 'pending' ? command.attemptId !== '' || reserved.length !== 0 :
        command.status !== 'observed' || reserved.length !== 1;
    })) return false;
    task.task.status = task.cancelIntent ? 'cancelled' : 'failed'; task.task.phase = 'terminal';
    task.failureCode = 'service_interrupted'; task.task.code = 'service_interrupted';
    for (const node of task.nodes) if (task.activeRepair.affectedNodes.includes(node.id) && node.status === 'pending') node.status = 'cancelled';
    task.task.revision = nextRevision(task.task.revision);
    const source = this.app.save(tx, task, 'task.repair-interrupted-unstarted', {repairId: task.activeRepair.repairId});
    this.settle(tx, task, source);
    for (const command of commands) if (command.status !== 'observed') tx.observeCommand(command.id, command.revision, 'observed', source);
    return true;
  }
  audit(tx, task) {
    if (!task.repair) return {};
    const decision = task.decision ? this.decision(tx, task) : null;
    return {decision: decision ? {id: decision.id, digest: task.decision.digest, status: decision.status, workerId: decision.workerId,
      planDigest: decision.planDigest, artifacts: clone(decision.artifacts), contentRejection: clone(decision.contentRejection ?? null)} : null,
    repairs: clone(task.repairs ?? [])};
  }
}
