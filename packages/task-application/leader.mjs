import {LEADER_FORMAT, encode, digest} from '../task-store/store.mjs';
import {clone, nextRevision, publicTask, terminal, reject, isText} from './model.mjs';
import {affectedNodes} from './graph.mjs';
import {LEADER_PROFILE, REVIEW_PROFILE, configuration, receipt, hash, sha, id, closed, check, createEffectPort} from './leader-ports.mjs';
import {nameFor} from '../task-publication-report/index.mjs';
import {hasPublicationExpected} from './verification.mjs';

const decode = row => row ? JSON.parse(row.bytes.toString()) : null;
const live = record => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(record.worker.status);
const managed = new Set(['leader', 'review', 'publication', 'postverify']);
const ended = new Set(['completed', 'failed', 'cancelled']);
export const isManagedExecution = type => managed.has(type);

export function leaderConfiguration(leader, review, publication = null, verification = null) {
  const config = configuration(leader, 'leader'), independent = configuration(review, 'review');
  check(config.policy.review.providerId === independent.providerId && config.policy.review.policyDigest === independent.policyDigest,
    'invalid_leader_config');
  check(config.policy.publication === null ? publication === null : publication &&
    config.policy.publication.targetId === publication.id && config.policy.publication.policyDigest === publication.policyDigest &&
    ['start', 'lookup', 'assertDisjoint'].every(name => typeof publication[name] === 'function') &&
    typeof publication.postverify?.start === 'function' && hasPublicationExpected(verification), 'invalid_leader_config');
  return {leader: config, review: independent, publication: publication ? {policy: config.policy.publication,
    configuration: clone(publication.configuration), configurationDigest: publication.configurationDigest} : null};
}

/** v7 business reducer over the original Store. No Provider, filesystem launch,
 * timers, model calls or handle ownership here. Opaque results are minted only
 * by the trusted parent ports and accepted against the current transaction. */
export class TaskLeader {
  constructor(app, leader, review, publication) {
    this.app = app; this.port = leader; this.review = review; this.publication = publication;
    if (leader === null) {check(review === null && publication === null, 'invalid_leader_config'); return;}
    check(app.store.info().format === LEADER_FORMAT && app.execution.maxWorkers >= 3 && app.verification.port !== null, 'invalid_leader_config');
    this.config = leaderConfiguration(leader, review, publication, app.verification.port);
    this.effects = publication ? {publication: createEffectPort('publication', publication), postverify: createEffectPort('postverify', publication.postverify)} : {};
    check(app.execution.providers.has(leader.providerId) && app.execution.providers.has(review.providerId), 'invalid_leader_config');
  }
  initial(record) {
    if (!this.port) return;
    check(record.limits.maxWorkers >= 3 && record.limits.maxAttempts >= (this.publication ? 10 : 8) &&
      this.config.leader.policy.maxCalls >= 5, 'unsupported_task');
    record.leader = {profile: LEADER_PROFILE, policyDigest: this.port.policyDigest, stage: 'intake', calls: 0, repairRounds: 0,
      activeCallId: null, obligationId: null, cursor: 0, requestIds: [], history: [], lastDecision: null, review: null,
      publication: null, postverify: null, delivery: null, summaryArtifactId: null};
  }
  configured(task) {
    check(this.port && task.leader?.profile === LEADER_PROFILE && task.leader.policyDigest === this.port.policyDigest, 'unsupported_task');
  }
  recoveryAllowed(task, record, count = 0, lookupStatus = null) {
    if (!task.leader || ended.has(task.task.status) || task.cancelIntent || task.failureCode || task.workerCancelled || record?.stopIntent ||
      task.task.status === 'intervention' && !['leader_recovery_pending', 'publication_effect_unresolved', 'previous_execution_unresolved'].includes(task.task.code) ||
      this.app.now() >= Date.parse(task.task.deadlineAt) || count >= 1) return false;
    // Include the minimum unfinished delivery path, not just the replacement
    // invocation. Recovery never raises original attempts/call/deadline limits.
    let calls, attempts;
    if (!task.plan) {
      calls = 5;
      // Even the smallest future Plan needs its mandatory author nodes, one
      // Review and verifier, then the configured publication/postverification.
      // These are additional to the interrupted Attempt already charged.
      attempts = calls + Math.max(1, this.config.leader.policy.repair.nodeIds.length) + 2 + (this.publication ? 2 : 0);
    }
    else if (task.leader.stage === 'finalizing') {calls = 1; attempts = 1;}
    else if (task.leader.postverify) {calls = 1; attempts = task.leader.postverify.status === 'passed' ? 1 : 2;}
    else if (task.leader.publication) {calls = 1; attempts = lookupStatus === 'matched' ? 2 : 3;}
    else if (task.acceptance?.status === 'passed') {calls = 2; attempts = 2 + (this.publication ? 2 : 0);}
    else if (task.leader.review?.verdict === 'accept') {calls = 3; attempts = 4 + (this.publication ? 2 : 0);}
    else {calls = record?.ticket.executionType === 'review' ? 3 : 4;
      attempts = calls + 2 + task.nodes.filter(node => node.role === 'author' && node.status !== 'completed').length + (this.publication ? 2 : 0);}
    return task.attempts + attempts <= (task.plan?.budget ?? task.limits).maxAttempts && task.leader.calls + calls <= this.config.leader.policy.maxCalls;
  }
  recoveryStatus(task, continuing, unresolved = false) {
    if (unresolved) {task.task.status = 'intervention'; task.task.code = 'publication_effect_unresolved'; return;}
    if (continuing) {
      task.task.status = task.pausedFrom ? 'paused' : 'running'; delete task.task.code; delete task.failureCode; return;
    }
    task.task.status = task.cancelIntent && !task.failureCode ? 'cancelled' : 'failed';
    task.task.code = task.task.status === 'cancelled' ? 'task_cancelled' : task.failureCode ?? 'service_interrupted';
    task.task.phase = 'terminal'; task.leader.stage = 'terminal'; task.leader.obligationId = null;
    for (const node of task.nodes) if (['pending', 'ready', 'waiting'].includes(node.status)) node.status = 'cancelled';
  }
  settleRecoveryControl(tx, task, source) {
    if (!ended.has(task.task.status) || !task.cancelIntent) return;
    if (this.app.execution.workers(tx, task).some(({record}) => live(record)) ||
      this.app.execution.capacity(tx).value.active.some(item => item.taskId === task.task.id)) return;
    const stop = tx.command(task.cancelIntent.commandId), row = tx.projection('operation', task.cancelIntent.operationId), operation = decode(row);
    check(stop?.taskId === task.task.id && stop.kind === 'stop' && operation?.taskId === task.task.id && operation.kind === 'task.cancel', 'recovery_required');
    if (['accepted', 'running', 'unknown'].includes(operation.status)) {
      operation.status = 'succeeded'; operation.taskRevision = task.task.revision; operation.updatedAt = new Date(this.app.now()).toISOString();
      tx.putProjection('operation', row.id, row.revision, source, encode(operation));
    }
    if (stop.status !== 'observed') tx.observeCommand(stop.id, stop.revision, 'observed', source);
  }
  interrupted(task, record, original, observationDigest) {
    const type = record.ticket.executionType, origin = original.obligation ?? original.action, count = origin.successor ?? origin.recoveryCount ?? 0;
    // A publication's effect still needs read-only lookup after cancellation or
    // expiry. No such lookup grants a new publication permission.
    if ((type !== 'publication' || record.cleanup.started === null) && !this.recoveryAllowed(task, record, count)) return false;
    record.recovery = {type, originId: origin.id, commandId: record.ticket.commandId, count, observationDigest,
      status: 'pending', sourceGeneration: record.ticket.generation};
    return true;
  }
  recover(taskId) {
    if (!this.port) return;
    const pending = this.app.transaction(false, tx => {
      const task = this.app.get(tx, taskId); if (!task.leader || ended.has(task.task.status)) return [];
      const workers = this.app.execution.workers(tx, task);
      if (workers.some(({record}) => live(record) && record.ticket.generation !== this.app.owner.generation.toString())) return [];
      return workers.filter(({record}) => record.recovery?.status === 'pending' && record.recovery.lookupGeneration !== this.app.owner.generation.toString())
        .map(({record}) => ({workerId: record.worker.id, recovery: clone(record.recovery)}));
    });
    for (const entry of pending) this.recoverExecution(taskId, entry);
    this.recoverPublicationAction(taskId);
    this.recoverUnreserved(taskId);
  }
  recoverExecution(taskId, entry) {
    const original = this.app.transaction(false, tx => {
      const {record} = this.app.execution.worker(tx, entry.workerId), task = this.app.get(tx, taskId);
      check(hash(record.recovery) === hash(entry.recovery) && record.cleanup?.cleaned && record.custody?.settledDigest === entry.recovery.observationDigest,
        'recovery_required');
      const inputRow = tx.projection('attempt', record.inputRef), input = decode(inputRow);
      check(inputRow?.revision === 1n && digest(inputRow.bytes) === record.ticket.inputDigest, 'recovery_required');
      return {ticket: {...record.ticket, input}, task, record, readSet: hash(this.semantic(tx, task))};
    });
    const lookup = entry.recovery.type === 'publication' ? this.effects.publication.lookup(original.ticket, {deadline: this.app.now() + 1000}) : null;
    const data = lookup ? receipt(this.effects.publication, original.ticket, lookup) : null;
    const staged = data ? this.app.artifacts.stageOutputs([['evidence', data.value.evidence]]) : [];
    this.app.transaction(true, tx => {
      const {row, record} = this.app.execution.worker(tx, entry.workerId), task = this.app.get(tx, taskId), recovery = record.recovery;
      if (ended.has(task.task.status) || hash(recovery) !== hash(entry.recovery)) return;
      check(record.cleanup?.cleaned && record.custody?.settledDigest === recovery.observationDigest &&
        hash(record.ticket) === hash(original.record.ticket), 'recovery_required');
      if (this.app.execution.workers(tx, task).some(({record: other}) => live(other) && other.ticket.generation !== this.app.owner.generation.toString())) return;
      const allowed = this.recoveryAllowed(task, record, recovery.count, data?.status) && (!data || original.readSet === hash(this.semantic(tx, task))) &&
        (!data || this.app.now() < Date.parse(original.ticket.input.publication.authorization.expiresAt)), command = tx.command(recovery.commandId);
      check(command && command.taskId === taskId && command.generation.toString() === recovery.sourceGeneration, 'recovery_required');
      let enqueue = null, projection = null, unresolved = false;
      if (recovery.type === 'leader') {
        const old = decode(tx.projection('interaction', recovery.originId));
        check(old?.status === 'closed' && old.workerId === record.worker.id && old.commandId === command.id &&
          old.generation === recovery.sourceGeneration && (old.successor ?? 0) === recovery.count, 'recovery_required');
        if (allowed) {
          const id = this.app.newId('obligation'), commandId = this.app.newId('command');
          const value = {...old, id, commandId, status: 'pending', generation: this.app.owner.generation.toString(),
            readSetDigest: null, successor: recovery.count + 1, predecessor: {obligationId: old.id, workerId: record.worker.id, commandId: command.id}};
          delete value.workerId;
          task.leader.obligationId = id; projection = {kind: 'interaction', id, revision: 0, value};
          enqueue = {id: commandId, type: 'leader', kind: 'start', payload: {taskId, obligationId: id}};
        }
      } else {
        const actionRow = tx.projection('attempt', recovery.originId), action = decode(actionRow);
        check(action?.workerId === record.worker.id && action.commandId === command.id && (action.recoveryCount ?? 0) === recovery.count,
          'recovery_required');
        if (data) {
          check(hash(action.binding) === hash(data.value.binding) && hash(action.authorization) === hash(original.ticket.input.publication.authorization), 'recovery_required');
          const artifact = {id: this.app.newId('artifact'), taskId, name: staged[0].name, kind: 'evidence', status: 'ready',
            mediaType: staged[0].mediaType, ...staged[0].ref, createdAt: new Date(this.app.now()).toISOString()};
          staged[0].artifact = artifact;
          action.lookup = {status: data.status, evidenceId: artifact.id, digest: artifact.digest};
          if (data.status === 'matched') {
            action.status = 'succeeded'; action.result = {evidenceId: artifact.id, digest: artifact.digest, observedStatus: 'matched'};
            task.leader.publication.status = 'matched'; task.leader.publication.receiptArtifactId = artifact.id;
            if (allowed) {
              const id = 'action-' + hash({publicationActionId: action.id, kind: 'postverify'}).slice(7), commandId = this.app.newId('command');
              check(!task.leader.postverify && !tx.projection('attempt', id), 'recovery_required');
              projection = {kind: 'attempt', id, revision: 0, value: {id, taskId, sourceDecision: action.sourceDecision,
                payloadDigest: hash({publication: action.id}), payload: {type: 'postverify', publicationActionId: action.id},
                status: 'pending', commandId, workerId: null, result: null}};
              task.leader.postverify = {actionId: id, status: 'pending', evidenceArtifactId: null};
              enqueue = {id: commandId, type: 'postverify', kind: 'verify', payload: {taskId, actionId: id}};
            }
          } else if (data.status === 'conflict') {action.status = 'failed'; task.leader.publication.status = 'failed';}
          else if (data.status === 'absent' && record.cleanup.started === null && allowed) {
            action.status = 'pending'; task.leader.publication.status = 'pending';
          } else {unresolved = true; action.status = 'unknown'; task.leader.publication.status = 'unknown';}
        }
        if (allowed && (!data || data.status === 'absent' && record.cleanup.started === null)) {
          const predecessor = {workerId: action.workerId, commandId: action.commandId};
          action.commandId = this.app.newId('command'); action.workerId = null; action.status = 'pending';
          action.recoveryCount = recovery.count + 1; action.predecessor = predecessor;
          enqueue = {id: action.commandId, type: recovery.type, kind: recovery.type === 'postverify' ? 'verify' : 'start', payload: {taskId, actionId: action.id}};
          if (recovery.type === 'postverify') task.leader.postverify.status = 'pending';
        }
        projection ??= {kind: 'attempt', id: action.id, revision: actionRow.revision, value: action};
        if (projection.id !== action.id) projection.additional = {kind: 'attempt', id: action.id, revision: actionRow.revision, value: action};
      }
      recovery.status = unresolved ? 'pending' : enqueue ? 'resumed' : 'closed';
      if (data) recovery.lookupGeneration = this.app.owner.generation.toString();
      if (enqueue) recovery.successorCommandId = enqueue.id;
      this.recoveryStatus(task, !!enqueue, unresolved);
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'leader.stage.changed', {workerId: record.worker.id, recovery: clone(recovery)});
      if (projection) for (const item of [projection, ...(projection.additional ? [projection.additional] : [])])
        tx.putProjection(item.kind, item.id, item.revision, source, encode(item.value));
      if (staged.length) this.app.artifacts.commitOutputs(tx, taskId, staged, source);
      if (!unresolved && command.status !== 'observed') tx.observeCommand(command.id, command.revision, 'observed', source);
      if (enqueue) this.app.enqueue(tx, source, taskId, enqueue.type, enqueue.payload, enqueue.kind, enqueue.id);
      this.app.execution.putWorker(tx, row, record, source);
      this.settleRecoveryControl(tx, task, source);
    });
  }
  unreservedPublication(tx, task) {
    if (!task.leader?.publication || task.leader.publication.receiptArtifactId || ended.has(task.task.status)) return null;
    const row = tx.projection('attempt', task.leader.publication.actionId), action = decode(row), command = action && tx.command(action.commandId);
    if (!command || command.generation === this.app.owner.generation || action.lookupGeneration === this.app.owner.generation.toString()) return null;
    const workers = this.app.execution.workers(tx, task);
    if (workers.some(({record}) => record.ticket.commandId === command.id || live(record))) return null;
    const payload = decode({bytes: command.payload});
    check(['pending', 'unknown'].includes(command.status) && command.attemptId === '' && command.kind === 'start' && command.taskId === task.task.id &&
      command.inputDigest === digest(command.payload) && payload.action === 'publication' && payload.actionId === action.id && payload.taskId === task.task.id &&
      action.taskId === task.task.id && ['pending', 'unknown'].includes(action.status) && action.workerId === null, 'recovery_required');
    const history = task.leader.history.find(item => item.digest === action.sourceDecision), decision = history && decode(tx.projection('attempt', history.callId));
    check(decision && hash(decision) === history.digest && action.payloadDigest === hash(action.payload) && action.payload.type === 'deliver' &&
      decision.actions.some((value, index) => action.id === 'action-' + hash({callId: history.callId, decisionDigest: history.digest, index}).slice(7) &&
        hash(value) === hash(action.payload)), 'recovery_required');
    const authorization = action.authorization, question = this.request(tx, task).find(value => value.actionId === action.id);
    const answer = question?.replyRef && decode(tx.projection('interaction', question.replyRef));
    const artifact = this.app.artifacts.metadata(tx, authorization.artifactId);
    check(question?.status === 'replied' && answer?.decision === 'allow' && question.subject === hash(authorization) && question.replyDigest === hash(answer) &&
      authorization.taskId === task.task.id && authorization.planDigest === task.plan.digest && authorization.targetId === this.publication.id &&
      authorization.targetPolicyDigest === this.publication.policyDigest && artifact.digest === authorization.artifactDigest && artifact.bytes === authorization.bytes &&
      action.binding.authorizationDigest === hash(authorization) && action.binding.actionId === action.id, 'recovery_required');
    const replies = this.replies(tx, task);
    return {row, action, command, readSet: hash(this.semantic(tx, task)), expectedInput: {taskId: task.task.id, planDigest: task.plan.digest,
      input: {task: clone(task.input), plan: clone(task.plan), inputArtifacts: clone(task.inputArtifacts), leaderReplies: replies.answers,
        leaderReplyRefs: replies.refs, interactionRefs: this.app.runtimeQuestions.refs(tx, task)}},
      subject: {profile: 'publication-action-lookup', taskId: task.task.id, commandId: command.id,
        generation: command.generation.toString(), actionDigest: hash(action), binding: clone(action.binding), authorization: clone(authorization)}};
  }
  recoverPublicationAction(taskId) {
    if (!this.publication) return;
    const original = this.app.transaction(false, tx => this.unreservedPublication(tx, this.app.get(tx, taskId)));
    if (!original) return;
    const lookup = this.effects.publication.lookup(original.subject, {deadline: this.app.now() + 1000});
    const data = receipt(this.effects.publication, original.subject, lookup);
    const expected = data.status === 'matched' ? this.app.verification.expectedPublication(original.expectedInput) : null;
    const staged = this.app.artifacts.stageOutputs([['evidence', data.value.evidence]]);
    this.app.transaction(true, tx => {
      const task = this.app.get(tx, taskId), current = this.unreservedPublication(tx, task);
      if (!current) return;
      check(hash(current.subject) === hash(original.subject), 'recovery_required');
      const {row, action, command} = current;
      const allowed = this.recoveryAllowed(task, null, action.recoveryCount ?? 0, data.status) && this.app.now() < Date.parse(action.authorization.expiresAt) &&
        current.readSet === original.readSet;
      const artifact = {id: this.app.newId('artifact'), taskId, name: staged[0].name, kind: 'evidence', status: 'ready', mediaType: staged[0].mediaType,
        ...staged[0].ref, createdAt: new Date(this.app.now()).toISOString()}; staged[0].artifact = artifact;
      action.lookup = {status: data.status, evidenceId: artifact.id, digest: artifact.digest}; action.lookupGeneration = this.app.owner.generation.toString();
      let enqueue = null, post = null;
      if (data.status === 'matched') {
        action.status = 'succeeded'; action.result = {evidenceId: artifact.id, digest: artifact.digest, observedStatus: 'matched'};
        task.leader.publication.status = 'matched'; task.leader.publication.receiptArtifactId = artifact.id;
        if (allowed) {
          action.expected = clone(expected);
          const id = 'action-' + hash({publicationActionId: action.id, kind: 'postverify'}).slice(7), commandId = this.app.newId('command');
          check(!task.leader.postverify && !tx.projection('attempt', id), 'recovery_required');
          post = {id, taskId, sourceDecision: action.sourceDecision, payloadDigest: hash({publication: action.id}),
            payload: {type: 'postverify', publicationActionId: action.id}, status: 'pending', commandId, workerId: null, result: null};
          task.leader.postverify = {actionId: id, status: 'pending', evidenceArtifactId: null};
          enqueue = {id: commandId, type: 'postverify', payload: {taskId, actionId: id}};
        }
      } else if (data.status === 'absent' && allowed) {
        action.commandId = this.app.newId('command'); action.recoveryCount = (action.recoveryCount ?? 0) + 1;
        action.predecessor = {commandId: command.id, workerId: null}; action.status = 'pending'; task.leader.publication.status = 'pending';
        enqueue = {id: action.commandId, type: 'publication', payload: {taskId, actionId: action.id}};
      } else if (data.status === 'unknown') {action.status = 'unknown'; task.leader.publication.status = 'unknown';}
      else {action.status = task.cancelIntent ? 'cancelled' : 'failed'; task.leader.publication.status = action.status;}
      this.recoveryStatus(task, !!enqueue, data.status === 'unknown');
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'leader.action.settled', {actionId: action.id, lookup: clone(action.lookup), unreservedCommandId: command.id});
      tx.putProjection('attempt', action.id, row.revision, source, encode(action));
      if (post) tx.putProjection('attempt', post.id, 0, source, encode(post));
      this.app.artifacts.commitOutputs(tx, taskId, staged, source);
      if (command.status !== (data.status === 'unknown' ? 'unknown' : 'observed'))
        tx.observeCommand(command.id, command.revision, data.status === 'unknown' ? 'unknown' : 'observed', source);
      if (enqueue) this.app.enqueue(tx, source, taskId, enqueue.type, enqueue.payload, enqueue.type === 'postverify' ? 'verify' : 'start', enqueue.id);
      this.settleRecoveryControl(tx, task, source);
    });
  }
  recoverUnreserved(taskId) {
    this.app.transaction(true, tx => {
      const task = this.app.get(tx, taskId); if (!task.leader || ended.has(task.task.status)) return;
      const workers = this.app.execution.workers(tx, task);
      if (workers.some(({record}) => live(record) && record.ticket.generation !== this.app.owner.generation.toString()) ||
        task.leader.publication?.status === 'unknown') return;
      const commands = tx.taskCommands(taskId, '', 100); check(commands.length < 100, 'recovery_required');
      const prior = task.leader.commandSuccessors ?? [], priorCount = prior.length, updates = []; let cannotContinue = false;
      for (const command of commands) {
        if (command.status !== 'pending' || command.generation === this.app.owner.generation) continue;
        const payload = decode({bytes: command.payload});
        if (!['leader', 'review', 'postverify', 'execute', 'dispatch'].includes(payload.action)) continue;
        check(command.attemptId === '' && command.inputDigest === digest(command.payload) && payload.taskId === taskId &&
          !workers.some(({record}) => record.ticket.commandId === command.id), 'recovery_required');
        const permitted = this.recoveryAllowed(task, null, 0) && !prior.some(item => item.to === command.id);
        cannotContinue ||= !permitted;
        const commandId = this.app.newId('command'); let projection;
        if (payload.action === 'leader') {
          const row = tx.projection('interaction', payload.obligationId), value = decode(row);
          check(value?.status === 'pending' && value.commandId === command.id && !value.workerId &&
            value.id === task.leader.obligationId && value.generation === command.generation.toString(), 'recovery_required');
          if (permitted) {value.commandId = commandId; value.generation = this.app.owner.generation.toString();}
          else value.status = 'closed';
          projection = {kind: 'interaction', row, value};
        } else if (['review', 'postverify'].includes(payload.action)) {
          const row = tx.projection('attempt', payload.actionId), value = decode(row);
          check(value?.status === 'pending' && value.commandId === command.id && value.workerId === null && value.taskId === taskId, 'recovery_required');
          if (permitted) value.commandId = commandId; else value.status = task.cancelIntent ? 'cancelled' : 'failed';
          projection = {kind: 'attempt', row, value};
        } else {
          check(task.approved?.planDigest === payload.planDigest, 'recovery_required');
          if (payload.action === 'execute') check(task.nodes.some(node => node.id === payload.nodeId &&
            ['pending', 'ready', 'waiting'].includes(node.status)) &&
            (payload.repairId ?? null) === (task.activeRepair?.repairId ?? null), 'recovery_required');
        }
        updates.push({command, commandId, payload, projection, permitted});
        if (permitted) prior.push({from: command.id, to: commandId});
      }
      if (!updates.length) return;
      check(prior.length <= 64, 'recovery_required'); task.leader.commandSuccessors = prior;
      if (cannotContinue) {
        // A pending command is not an execution. Exhausted finite recovery must
        // close it, rather than leave an unreachable old-generation outbox.
        if (workers.some(({record}) => live(record))) {task.failureCode ??= 'service_interrupted'; task.task.status = 'cancelling';}
        else this.recoveryStatus(task, false);
        prior.length = priorCount;
        for (const {command, projection} of updates) if (projection) {
          projection.value.commandId = command.id;
          projection.value.status = projection.kind === 'interaction' ? 'closed' : task.cancelIntent ? 'cancelled' : 'failed';
          if (projection.kind === 'interaction') projection.value.generation = command.generation.toString();
        }
      }
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'leader.stage.changed', {unreservedSuccessors: cannotContinue ? [] : updates.map(item => ({from: item.command.id, to: item.commandId})),
        closedCommands: cannotContinue ? updates.map(item => item.command.id) : []});
      for (const {command, commandId, payload, projection} of updates) {
        if (projection) tx.putProjection(projection.kind, projection.row.id, projection.row.revision, source, encode(projection.value));
        tx.observeCommand(command.id, command.revision, 'observed', source);
        if (!cannotContinue) {const {action, ...body} = payload; this.app.enqueue(tx, source, taskId, action, body, command.kind, commandId);}
      }
      this.settleRecoveryControl(tx, task, source);
    });
  }
  prepareObligation(task) {
    task.leader.obligationId ??= this.app.newId('obligation');
    return task.leader.obligationId;
  }
  bind(task, plan) {
    if (!task.leader) return;
    this.configured(task);
    const policy = this.config.leader.policy;
    check(plan.budget.maxWorkers >= 3 && policy.repair.nodeIds.every(id => plan.nodes.some(node => node.id === id && node.role === 'author')),
      'unsupported_task');
    const term = {profile: LEADER_PROFILE, policyDigest: this.port.policyDigest, repair: policy.repair, review: policy.review,
      publication: policy.publication, completion: 'leader-delivery'};
    check(plan.acceptance.length < 32); plan.acceptance.push(JSON.stringify(term));
    task.selectedResults ??= Object.fromEntries(plan.nodes.filter(node => node.id !== task.verification.nodeId).map(node => [node.id, null]));
  }
  request(tx, task) {
    const requests = task.leader.requestIds.map(id => decode(tx.projection('interaction', id)));
    check(requests.every(value => value?.taskId === task.task.id), 'application_unavailable');
    return requests;
  }
  replies(tx, task, nodeId = null, upstream = []) {
    if (!task.leader) return {refs: [], answers: []};
    const inherited = new Set(upstream.flatMap(value => value.leaderReplyRefs ?? []).map(ref => ref.requestId));
    const answers = this.request(tx, task).filter(value => value.kind === 'business' && value.status === 'replied' &&
      (nodeId === null || value.nodeIds.length === 0 || value.nodeIds.includes(nodeId) || inherited.has(value.id))).map(value => {
      const reply = decode(tx.projection('interaction', value.replyRef));
      check(reply?.taskId === task.task.id && reply.requestId === value.id && reply.requestDigest === value.requestDigest &&
        hash({taskId: task.task.id, requestId: value.id, requestDigest: value.requestDigest, answer: reply.answer}) === value.replyDigest,
      'candidate_manifest_conflict');
      return {requestId: value.id, requestDigest: value.requestDigest, replyDigest: value.replyDigest, answer: reply.answer};
    }).sort((a, b) => a.requestId < b.requestId ? -1 : 1);
    return {refs: answers.map(({answer, ...ref}) => ref), answers};
  }
  selected(tx, task, complete = true) {
    const ids = Object.keys(task.selectedResults ?? {}).filter(id => task.selectedResults[id]);
    if (complete) check(ids.length === task.plan.nodes.length - 1, 'candidate_manifest_conflict');
    return this.app.repair.selected(tx, task, ids);
  }
  selection(tx, task, complete = false) {
    return this.selected(tx, task, complete).map(({record}) => ({nodeId: record.worker.nodeId, workerId: record.worker.id,
      resultDigest: record.resultDigest, manifestDigest: record.candidate.manifestDigest})).sort((a, b) => a.nodeId < b.nodeId ? -1 : 1);
  }
  semantic(tx, task) {
    const selection = task.plan ? this.selection(tx, task) : [];
    const answers = this.replies(tx, task);
    return [{kind: 'input', digest: task.inputDigest}, {kind: 'plan', digest: task.plan?.digest ?? null},
      {kind: 'selected', digest: hash(selection)}, {kind: 'replies', digest: hash(answers)},
      {kind: 'review', digest: task.leader.review?.digest ?? null}, {kind: 'acceptance', digest: task.acceptance?.digest ?? null},
      {kind: 'history', digest: hash(task.leader.history)}, {kind: 'publication', digest: hash(task.leader.publication)},
      {kind: 'postverify', digest: hash(task.leader.postverify)}, {kind: 'worker-interactions', digest: hash(this.workerInteractions(tx, task))}];
  }
  workerInteractions(tx, task) {
    if (!task.runtimeQuestions) return [];
    return this.app.runtimeQuestions.questions(tx, {taskId: task.task.id, page: {limit: 100}}).items;
  }
  selectionDigest(tx, task) {return hash(this.selection(tx, task, true));}
  obligation(tx, task, source, reason, nodeIds = [], inherited = []) {
    this.configured(task);
    if (task.leader.obligationId) {
      const row = tx.projection('interaction', task.leader.obligationId), old = decode(row);
      check(!old || old.taskId === task.task.id, 'application_unavailable');
      if (old?.status === 'pending' || old?.status === 'claimed') {
        const item = {event: source.sequence.toString(), reason, nodeIds};
        if (!old.sources.some(value => hash(value) === hash(item))) old.sources.push(item);
        check(old.sources.length <= 64); tx.putProjection('interaction', old.id, row.revision, source, encode(old)); return old.id;
      }
    }
    const id = task.leader.obligationId ?? this.app.newId('obligation'), commandId = this.app.newId('command');
    const value = {id, taskId: task.task.id, status: 'pending', generation: this.app.owner.generation.toString(), commandId,
      sources: inherited.length ? clone(inherited) : [{event: source.sequence.toString(), reason, nodeIds}], readSetDigest: null, successor: 0};
    check(value.sources.length <= 64);
    task.leader.obligationId = id;
    tx.putProjection('interaction', id, 0, source, encode(value));
    this.app.enqueue(tx, source, task.task.id, 'leader', {taskId: task.task.id, obligationId: id}, 'start', commandId);
    return id;
  }
  snapshot(tx, task, obligation, callId) {
    const workers = this.app.execution.workers(tx, task), selected = task.plan ? this.selected(tx, task, false) : [];
    const requests = this.request(tx, task), replies = this.replies(tx, task);
    const evidence = [];
    if (task.leader.review) evidence.push({kind: 'review', ...task.leader.review});
    if (task.acceptance?.digest) evidence.push({kind: 'verification', ...task.acceptance});
    for (const {record} of workers) if (record.worker.status === 'failed') evidence.push({kind: 'execution-failure', workerId: record.worker.id,
      nodeId: record.worker.nodeId, digest: hash({ticket: record.ticket, cleanup: record.cleanup}), cleanup: record.cleanup,
      reason: record.failureCode ?? 'worker_failed', retryable: record.failureClass === 'ordinary'});
    check(evidence.length <= 64);
    return {profile: LEADER_PROFILE, taskId: task.task.id, callId, obligationId: obligation.id,
      generation: this.app.owner.generation.toString(), cursor: task.leader.cursor,
      snapshot: {task: {input: clone(task.input), inputArtifacts: clone(task.inputArtifacts), deadlineAt: task.task.deadlineAt,
        remainingAttempts: (task.plan?.budget ?? task.limits).maxAttempts - task.attempts, limits: clone(task.plan?.budget ?? task.limits), usage: null},
      plan: clone(task.plan), policy: clone(this.config.leader.policy), selection: task.plan ? this.selection(tx, task) : [],
      interactions: {replies: replies.answers, requests: requests.map(({replyRef, ...value}) => value),
        worker: this.workerInteractions(tx, task)}, evidence,
      history: clone(task.leader.history), obligation: clone(obligation.sources), readSet: this.semantic(tx, task)},
      materials: [...selected.flatMap(({record}) => record.candidate.files.map(file => ({workerId: record.worker.id, nodeId: record.worker.nodeId, ...file}))),
        ...task.task.artifactIds.map(id => this.app.artifacts.metadata(tx, id))]};
  }
  expand(input) {
    let total = encode(input).length;
    const materials = [...(input.materials ?? []), ...input.snapshot.task.inputArtifacts.map(ref => ({inputId: ref.id, ...ref}))].map(ref => {
      total += ref.bytes; check(total <= 196608, 'unsupported_task');
      const bytes = this.app.artifacts.bytes(ref); const text = new TextDecoder('utf-8', {fatal: true}).decode(bytes);
      return {...ref, content: text};
    });
    for (const evidence of input.snapshot.evidence) for (const id of evidence.evidenceIds ?? []) {
      if (materials.some(ref => ref.id === id)) continue;
      const ref = this.app.transaction(false, tx => this.app.artifacts.metadata(tx, id));
      check(ref?.taskId === input.taskId, 'candidate_manifest_conflict'); total += ref.bytes; check(total <= 196608);
      materials.push({...ref, content: new TextDecoder('utf-8', {fatal: true}).decode(this.app.artifacts.bytes(ref))});
    }
    const value = {...input, materials}; check(encode(value).length <= 196608); return {...value, inputDigest: hash(value)};
  }
  nextWork(commandId, expectedRevision) {
    const original = this.app.transaction(false, tx => {
      const command = tx.command(commandId);
      if (!command || command.status !== 'pending' || command.revision !== BigInt(expectedRevision) || command.generation !== this.app.owner.generation) return null;
      const payload = decode({bytes: command.payload});
      if (!managed.has(payload.action)) return null;
      const task = this.app.get(tx, command.taskId); this.configured(task);
      if (terminal.has(task.task.status) || ['cancelling', 'paused'].includes(task.task.status) || this.app.now() >= Date.parse(task.task.deadlineAt)) return null;
      if (payload.action === 'leader') {
        const obligation = decode(tx.projection('interaction', payload.obligationId));
        if (obligation?.status !== 'pending' || task.leader.activeCallId) return null;
        check(task.leader.calls < this.config.leader.policy.maxCalls, 'capacity_exceeded');
        const callId = this.app.newId('call');
        return {command, task, payload, input: this.snapshot(tx, task, obligation, callId)};
      }
      const action = decode(tx.projection('attempt', payload.actionId));
      if (action?.status !== 'pending') return null;
      const obligation = {id: action.id, sources: [{reason: payload.action, nodeIds: action.payload.nodeIds ?? []}]};
      return {command, task, payload, action, input: this.snapshot(tx, task, obligation, action.id),
        expected: payload.action === 'postverify' ? decode(tx.projection('attempt', action.payload.publicationActionId))?.expected : null,
        artifact: ['publication', 'postverify'].includes(payload.action) ? this.app.artifacts.metadata(tx, task.leader.delivery.artifactId) : null};
    });
    if (!original) return null;
    const expanded = this.expand(original.input);
    let expected;
    if (original.payload.action === 'publication') expected = this.app.verification.expectedPublication({taskId: original.task.task.id,
      planDigest: original.task.plan.digest, input: {task: original.task.input, plan: original.task.plan, inputArtifacts: original.task.inputArtifacts,
        leaderReplies: expanded.snapshot.interactions.replies, leaderReplyRefs: expanded.snapshot.interactions.replies.map(({answer, ...ref}) => ref),
        interactionRefs: this.app.transaction(false, tx => this.app.runtimeQuestions.refs(tx, original.task))}});
    else if (original.payload.action === 'postverify') {expected = original.expected; check(expected !== undefined, 'candidate_manifest_conflict');}
    return this.app.transaction(true, tx => {
      const command = tx.command(commandId), task = this.app.get(tx, original.task.task.id);
      if (command.status !== 'pending' || command.revision !== BigInt(expectedRevision) || command.generation !== this.app.owner.generation ||
          terminal.has(task.task.status) || ['cancelling', 'paused'].includes(task.task.status) || this.app.now() >= Date.parse(task.task.deadlineAt)) return null;
      if (hash(this.semantic(tx, task)) !== hash(original.input.snapshot.readSet)) return null;
      const type = original.payload.action;
      let input = {task: clone(task.input), inputArtifacts: clone(task.inputArtifacts), plan: clone(task.plan), upstream: [],
        leaderReplyRefs: this.replies(tx, task).refs, leaderReplies: this.replies(tx, task).answers};
      const node = {id: 'managed-' + this.app.newId(type), role: type === 'leader' ? 'planner' : type === 'review' ? 'reviewer' : type === 'publication' ? 'integrator' : 'verifier',
        goal: task.task.intent, scope: [], providerId: null};
      let providerId;
      if (type === 'leader') {
        const obligation = decode(tx.projection('interaction', original.payload.obligationId));
        if (obligation.status !== 'pending' || task.leader.activeCallId) return null;
        input.leader = expanded; providerId = this.port.providerId;
      } else if (type === 'review') {
        const selection = this.selection(tx, task, true), body = {profile: REVIEW_PROFILE, taskId: task.task.id, selection,
          selectionDigest: hash(selection), snapshot: expanded.snapshot, materials: expanded.materials};
        input.review = {...body, inputDigest: hash(body)}; providerId = this.review.providerId;
      } else {
        const artifact = this.app.artifacts.metadata(tx, original.artifact.id);
        check(hash(artifact) === hash(original.artifact) && task.acceptance.status === 'passed' && task.leader.review?.verdict === 'accept', 'candidate_manifest_conflict');
        input.publicationArtifact = artifact; input.managedReadSet = expanded.snapshot.readSet;
        input.interactionRefs = task.runtimeQuestions ? this.app.runtimeQuestions.refs(tx, task) : [];
        if (type === 'publication') {
          const authorization = original.action.authorization;
          const question = this.request(tx, task).find(value => value.actionId === original.action.id);
          const answer = question?.replyRef ? decode(tx.projection('interaction', question.replyRef)) : null;
          check(question?.status === 'replied' && answer?.decision === 'allow' && question.subject === hash(authorization) &&
            question.replyDigest === hash(answer) && this.app.now() < Date.parse(authorization.expiresAt), 'candidate_manifest_conflict');
          input.publication = {binding: original.action.binding, authorization}; input.publicationExpected = expected;
          providerId = this.effects.publication.id;
        } else {
          check(task.leader.publication?.receiptArtifactId && ['created', 'matched'].includes(task.leader.publication.status), 'candidate_manifest_conflict');
          const ref = this.app.artifacts.metadata(tx, task.leader.publication.receiptArtifactId);
          input.postverify = {publicationReceiptDigest: ref.digest, targetId: this.publication.id, name: nameFor({taskId: task.task.id, artifactDigest: artifact.digest}),
            artifactDigest: artifact.digest, bytes: artifact.bytes, policyDigest: this.publication.policyDigest, expected,
            interactionRefs: input.interactionRefs, leaderReplyRefs: input.leaderReplyRefs}; providerId = this.effects.postverify.id;
        }
      }
      input.node = node;
      const ticket = this.app.execution.reserve(tx, task, command, {node, input, providerId, executionType: type});
      if (!ticket) return null;
      const source = {stream: task.task.id, ...tx.head(task.task.id)};
      if (type === 'leader') {
        const row = tx.projection('interaction', original.payload.obligationId), value = decode(row);
        value.status = 'claimed'; value.readSetDigest = hash(original.input.snapshot.readSet); value.workerId = ticket.workerId;
        tx.putProjection('interaction', value.id, row.revision, source, encode(value));
        task.leader.activeCallId = expanded.callId; task.leader.activeWorkerId = ticket.workerId; task.leader.calls++;
      } else {
        const row = tx.projection('attempt', original.action.id), action = decode(row);
        action.status = 'running'; action.workerId = ticket.workerId;
        if (type === 'publication') action.expected = clone(expected);
        tx.putProjection('attempt', action.id, row.revision, source, encode(action));
      }
      task.task.revision = nextRevision(task.task.revision);
      this.app.save(tx, task, type === 'leader' ? 'leader.call.reserved' : 'leader.stage.changed', {workerId: ticket.workerId, executionType: type});
      return ticket;
    });
  }
  shape(request) {
    const body = request.body;
    if (!id(request.taskId) || !id(request.requestId) || !body || !Number.isSafeInteger(body.expectedRevision) || body.expectedRevision < 1 ||
        !sha(body.requestDigest) || !(closed(body, ['expectedRevision', 'requestDigest', 'answer']) && isText(body.answer, 4096) ||
          closed(body, ['expectedRevision', 'requestDigest', 'decision']) && ['allow', 'deny'].includes(body.decision))) reject('invalid_request', 400);
  }
  actions(tx, task, ticket, decision, evidence) {
    const writes = [], actionRecords = [], selection = task.plan ? this.selection(tx, task) : [], selectionDigest = hash(selection);
    const policy = this.config.leader.policy, decisionDigest = hash(decision);
    const pending = this.request(tx, task).some(value => value.status === 'pending');
    for (let index = 0; index < decision.actions.length; index++) {
      const action = decision.actions[index], actionId = 'action-' + hash({callId: decision.callId, decisionDigest, index}).slice(7);
      const record = {id: actionId, taskId: task.task.id, sourceDecision: decisionDigest, payloadDigest: hash(action), payload: clone(action),
        status: 'succeeded', commandId: null, workerId: null, result: null};
      if (action.type === 'ask') {
        check(!pending && task.leader.requestIds.length < policy.maxRequests && action.kind === 'business', 'invalid_leader_decision');
        check(task.approved ? action.nodeIds.length > 0 && action.nodeIds.every(id => task.plan.nodes.some(node => node.id === id)) : action.nodeIds.length === 0,
          'invalid_leader_decision');
        const subjects = new Set([task.inputDigest, task.plan?.digest, ...selection.map(item => item.resultDigest), task.leader.review?.digest, task.acceptance?.digest]);
        check(subjects.has(action.subject), 'invalid_leader_decision');
        const value = {taskId: task.task.id, id: this.app.newId('request'), kind: action.kind, subject: action.subject,
          nodeIds: clone(action.nodeIds), prompt: action.prompt, options: clone(action.options), authorization: null, deadlineAt: task.task.deadlineAt};
        const question = {...value, requestDigest: hash(value), status: 'pending', replyDigest: null, replyRef: null};
        task.leader.requestIds.push(value.id); task.task.status = 'awaiting-answer'; record.result = {requestId: value.id};
        writes.push(source => tx.putProjection('interaction', value.id, 0, source, encode(question)));
      } else if (action.type === 'plan') {
        check(!task.approved && !pending, 'invalid_leader_decision');
        const plan = this.app.freezePlan(task, action.proposal);
        check(plan.nodes.length + task.attempts + 5 + (this.publication ? 2 : 0) <= plan.budget.maxAttempts &&
          task.leader.calls + 4 <= policy.maxCalls, 'capacity_exceeded');
        task.plan = plan; task.task.plan = {revision: plan.revision, digest: plan.digest};
        task.nodes = plan.nodes.map(node => ({id: node.id, role: node.role, status: 'pending', workerIds: []}));
        task.task.status = 'awaiting-approval'; task.task.phase = 'planning'; record.result = {planDigest: plan.digest};
      } else if (action.type === 'work') {
        check(task.approved?.planDigest === task.plan?.digest && action.selectionDigest === selectionDigest &&
          action.nodeIds.every(id => task.plan.nodes.some(node => node.id === id)), 'invalid_leader_decision');
        if (action.kind === 'review') {
          check(selection.length === task.plan.nodes.length - 1 && hash([...action.nodeIds].sort()) === hash(selection.map(item => item.nodeId).sort()) &&
            !task.leader.reviewAction, 'invalid_leader_decision');
          record.status = 'pending'; record.commandId = this.app.newId('command'); task.leader.stage = 'review'; task.leader.reviewAction = actionId;
          writes.push(source => this.app.enqueue(tx, source, task.task.id, 'review', {taskId: task.task.id, actionId}, 'start', record.commandId));
        } else if (action.kind === 'verify') {
          check(action.nodeIds.length === 1 && action.nodeIds[0] === task.verification.nodeId && task.leader.review?.verdict === 'accept' &&
            task.leader.review.selectionDigest === selectionDigest && !task.leader.verifyRequested, 'invalid_leader_decision');
          task.leader.verifyRequested = true; task.leader.stage = 'verification';
        } else check(action.nodeIds.every(id => task.nodes.find(node => node.id === id)?.status === 'pending'), 'invalid_leader_decision');
      } else if (action.type === 'repair') {
        check(task.approved?.planDigest === task.plan?.digest && !pending && !task.cancelIntent && !task.workerCancelled &&
          task.leader.repairRounds < policy.repair.maxRounds && action.nodeIds.every(id => policy.repair.nodeIds.includes(id)), 'invalid_leader_decision');
        const workers = this.app.execution.workers(tx, task);
        check(workers.every(({record}) => record.worker.id === ticket.workerId || !live(record)), 'invalid_leader_decision');
        let basisArtifact;
        if (action.basis.kind === 'review') {
          const review = task.leader.review;
          check(review?.verdict === 'rework' && review.digest === action.basis.digest && review.selectionDigest === selectionDigest, 'invalid_leader_decision');
          const report = decode(tx.projection('attempt', task.leader.reviewRecord));
          check(report?.report && report.report.findings.every(finding => finding.nodeIds.every(id => action.nodeIds.includes(id))), 'invalid_leader_decision');
          basisArtifact = this.app.artifacts.metadata(tx, review.evidenceIds[0]);
        } else if (action.basis.kind === 'content-rejection') {
          const basis = this.app.repair.decision(tx, task);
          check(basis.status === 'rejected' && basis.contentRejection && task.decision.digest === action.basis.digest, 'invalid_leader_decision');
          basisArtifact = basis.artifacts.find(value => value.kind === 'evidence');
        } else {
          const failed = workers.find(({record}) => record.worker.status === 'failed' && record.failureClass === 'ordinary' && record.cleanup?.cleaned && !record.stopIntent &&
            record.ticket.executionType === 'agent' && action.nodeIds.includes(record.worker.nodeId) &&
            hash({ticket: record.ticket, cleanup: record.cleanup}) === action.basis.digest);
          check(failed && !task.leader.failureRetried, 'invalid_leader_decision'); task.leader.failureRetried = true; basisArtifact = evidence;
        }
        check(basisArtifact?.status === 'ready', 'invalid_leader_decision');
        const affected = affectedNodes(task.plan.nodes, task.plan.edges, action.nodeIds), repairId = this.app.newId('repair');
        check((task.plan.budget.maxAttempts - task.attempts) >= affected.length + 3, 'capacity_exceeded');
        const fact = {profile: LEADER_PROFILE, repairId, taskId: task.task.id, planDigest: task.plan.digest,
          generation: this.app.owner.generation.toString(), policyDigest: this.port.policyDigest, decisionDigest: action.basis.digest,
          basis: clone(action.basis), affectedNodes: affected, feedback: action.feedback, failedAssertions: [], evidence: basisArtifact, operationId: null};
        task.activeRepair = fact; task.leader.repairRounds++; task.reworkCount++; task.leader.review = null; task.leader.reviewAction = null;
        task.leader.verifyRequested = false; task.leader.stage = 'work'; task.leader.delivery = null;
        task.acceptance = {status: 'pending', digest: null, evidenceIds: []}; task.task.artifactIds = [];
        for (const node of task.nodes) if (affected.includes(node.id)) {node.status = 'pending'; if (Object.hasOwn(task.selectedResults, node.id)) task.selectedResults[node.id] = null;}
        writes.push(source => {tx.putProjection('attempt', repairId, 0, source, encode(fact));
          for (const nodeId of affected) this.app.enqueue(tx, source, task.task.id, 'execute', {taskId: task.task.id, nodeId, planDigest: task.plan.digest, repairId});});
        task.task.status = 'running'; delete task.failureCode; delete task.task.code; record.result = {repairId};
      } else if (action.type === 'deliver') {
        check(task.acceptance?.status === 'passed' && action.acceptanceDigest === task.acceptance.digest &&
          task.leader.review?.verdict === 'accept' && action.reviewDigest === task.leader.review.digest &&
          task.leader.review.selectionDigest === selectionDigest, 'invalid_leader_decision');
        const artifact = this.app.artifacts.metadata(tx, action.artifactId);
        check(artifact?.taskId === task.task.id && artifact.kind === 'delivery' && artifact.status === 'ready' &&
          task.task.artifactIds.includes(artifact.id), 'invalid_leader_decision');
        task.leader.delivery = {artifactId: artifact.id, acceptanceDigest: action.acceptanceDigest, reviewDigest: action.reviewDigest};
        task.leader.stage = this.publication ? 'delivery' : 'finalizing'; record.result = clone(task.leader.delivery);
        if (this.publication) {
          check(!pending && !task.leader.publication && artifact.mediaType === 'application/json' && artifact.bytes > 0 && artifact.bytes <= 1048576 &&
            task.leader.requestIds.length < policy.maxRequests, 'invalid_leader_decision');
          const authorization = {taskId: task.task.id, planDigest: task.plan.digest, artifactId: artifact.id, artifactDigest: artifact.digest,
            bytes: artifact.bytes, acceptanceDigest: action.acceptanceDigest, reviewDigest: action.reviewDigest, targetId: this.publication.id,
            targetPolicyDigest: this.publication.policyDigest, name: nameFor({taskId: task.task.id, artifactDigest: artifact.digest}),
            operation: 'create-if-absent', expiresAt: task.task.deadlineAt};
          const value = {taskId: task.task.id, id: this.app.newId('request'), kind: 'publication', subject: hash(authorization), nodeIds: [],
            prompt: '是否允许将已独立验收的精确成果创建到已配置的有限目标？', options: [{value: 'allow', label: '允许'}, {value: 'deny', label: '拒绝'}],
            authorization, deadlineAt: task.task.deadlineAt};
          const question = {...value, requestDigest: hash(value), status: 'pending', replyDigest: null, replyRef: null, actionId};
          task.leader.requestIds.push(value.id); task.task.status = 'awaiting-confirmation'; task.task.phase = 'delivery';
          record.status = 'pending'; record.commandId = this.app.newId('command'); record.authorization = authorization;
          record.binding = {actionId, targetId: authorization.targetId, name: authorization.name, artifactDigest: artifact.digest,
            bytes: artifact.bytes, authorizationDigest: hash(authorization)};
          task.leader.publication = {actionId, status: 'pending', authorizationDigest: hash(authorization), receiptArtifactId: null};
          writes.push(source => tx.putProjection('interaction', question.id, 0, source, encode(question)));
        }
      } else {
        const current = new Set([task.leader.review?.digest, task.acceptance?.digest, ...task.leader.history.map(item => item.digest)]);
        check(action.basisDigests.every(value => current.has(value)), 'invalid_leader_decision');
        if (action.outcome === 'succeeded') {
          check(!pending && task.leader.delivery && task.leader.review?.verdict === 'accept' && task.acceptance?.status === 'passed' &&
            task.leader.review.selectionDigest === selectionDigest && this.app.execution.workers(tx, task).every(({record}) =>
              record.worker.id === ticket.workerId || !live(record) && record.cleanup?.cleaned === true) &&
            (!this.publication || task.leader.postverify?.status === 'passed'), 'invalid_leader_decision');
          task.task.status = 'completed'; task.task.phase = 'terminal'; task.leader.stage = 'terminal'; task.leader.summaryArtifactId = evidence.id;
        } else if (action.outcome === 'failed') {
          task.task.status = 'cancelling'; task.failureCode = 'leader_concluded_failure'; task.leader.stage = 'terminal';
        } else check(pending || task.task.status === 'awaiting-approval' || this.app.execution.workers(tx, task).some(({record}) => record.worker.id !== ticket.workerId && live(record)), 'invalid_leader_decision');
      }
      actionRecords.push(record);
    }
    return source => {for (const write of writes) write(source); for (const action of actionRecords) tx.putProjection('attempt', action.id, 0, source, encode(action));};
  }
  finish(ticket, result) {
    if (['publication', 'postverify'].includes(ticket.executionType)) return this.finishEffect(ticket, result);
    const port = ticket.executionType === 'leader' ? this.port : this.review;
    const data = result?.receipt ? receipt(port, ticket, result) : null;
    // Byte durability precedes the final transaction; cancelled, stale and
    // unknown attempts cannot create ready refs. A rollback leaves only bytes.
    const eligible = this.app.transaction(false, tx => {
      const {record, task} = this.app.execution.ticket(tx, ticket);
      return live(record) && !record.stopIntent && !task.cancelIntent && !terminal.has(task.task.status) &&
        task.task.status !== 'cancelling' && this.app.now() < ticket.deadline && data?.value && data.cleanup?.cleaned &&
        data.cleanup.started?.executionId === record.executionId && data.cleanup.started?.startedAt === record.worker.startedAt;
    });
    const staged = eligible ? this.app.artifacts.stageOutputs([['evidence', {name: ticket.executionType + '-decision.json', mediaType: 'application/json',
      content: encode({profile: ticket.executionType === 'leader' ? LEADER_PROFILE : REVIEW_PROFILE, ticketDigest: hash(ticket), report: data.value})}]]) : [];
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.app.execution.ticket(tx, ticket);
      if (!live(record)) return clone(record.worker);
      const cleanup = result?.cleanup;
      if (!cleanup || !(cleanup.started === null && record.executionId === null || cleanup.started?.executionId === record.executionId &&
        cleanup.started.startedAt === record.worker.startedAt)) reject('recovery_required', 409);
      const clean = cleanup.cleaned === true && !record.custody?.extraScopes.length;
      const cancelled = !!record.stopIntent || !!task.cancelIntent || task.task.status === 'cancelling' || terminal.has(task.task.status) || this.app.now() >= ticket.deadline;
      const readSet = ticket.input.leader?.snapshot.readSet ?? ticket.input.review?.snapshot.readSet;
      const current = readSet && hash(readSet) === hash(this.semantic(tx, task));
      let accepted = clean && !cancelled && data?.value && staged.length === 1 && current;
      let stale = ticket.executionType === 'leader' && clean && !cancelled && !current, successorSources = [];
      const at = new Date(this.app.now()).toISOString();
      let commitActions = () => {}, rejected = data?.reason ?? 'leader_result_rejected';
      const artifact = accepted ? {id: this.app.newId('artifact'), taskId: task.task.id, name: staged[0].name, kind: 'evidence', status: 'ready',
        mediaType: staged[0].mediaType, ...staged[0].ref, createdAt: at} : null;
      if (accepted) staged[0].artifact = artifact;
      if (ticket.executionType === 'leader') {
        const obligation = decode(tx.projection('interaction', ticket.input.leader.obligationId));
        check(obligation?.status === 'claimed' && obligation.workerId === ticket.workerId && task.leader.activeCallId === ticket.input.leader.callId,
          'invalid_leader_receipt');
        if (clean && !cancelled && hash(obligation.sources) !== hash(ticket.input.leader.snapshot.obligation)) {stale = true; accepted = false;}
        if (stale) {successorSources = obligation.sources; rejected = 'leader_readset_stale';}
        if (accepted) {
          const proposed = clone(task);
          try {commitActions = this.actions(tx, proposed, ticket, data.value, artifact); Object.assign(task, proposed);}
          catch (error) {accepted = false; rejected = error?.code ?? rejected;}
        }
        task.leader.activeCallId = null; task.leader.activeWorkerId = null; task.leader.obligationId = null;
      } else if (accepted) {
        check(ticket.input.review.selectionDigest === this.selectionDigest(tx, task), 'invalid_leader_receipt');
        check(this.selected(tx, task, true).every(({record: author}) => author.executionId !== record.executionId), 'invalid_leader_receipt');
        const decision = {verdict: data.value.verdict, selectionDigest: ticket.input.review.selectionDigest, policyDigest: this.review.policyDigest,
          workerId: ticket.workerId, evidenceIds: [artifact.id]};
        task.leader.review = {digest: hash(decision), ...decision}; task.leader.reviewRecord = this.app.newId('review');
      }
      record.cleanup = clone(cleanup); record.worker.status = !clean ? 'unknown' : cancelled ? 'cancelled' : accepted ? 'completed' : 'failed';
      record.worker.finishedAt = at; record.worker.phase = 'terminal';
      if (!clean) {task.task.status = 'intervention'; task.task.code = 'cleanup_unconfirmed';}
      else if (!cancelled && !accepted && !stale) {task.task.status = 'cancelling'; task.failureCode = rejected;}
      if (accepted && ticket.executionType === 'leader') {
        const digest = hash(data.value); task.leader.lastDecision = {digest, callId: data.value.callId, evidenceId: artifact.id};
        task.leader.history.push({digest, callId: data.value.callId, evidenceId: artifact.id}); check(task.leader.history.length <= 32);
      }
      const follow = stale || accepted && (ticket.executionType === 'review' || task.leader.stage === 'finalizing');
      if (follow) this.prepareObligation(task);
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, accepted ? ticket.executionType === 'leader' ? 'leader.decision.accepted' : 'leader.action.settled' :
        'leader.decision.rejected', {workerId: ticket.workerId, status: record.worker.status, reason: accepted ? null : rejected});
      if (ticket.executionType === 'leader') {
        const obligationRow = tx.projection('interaction', ticket.input.leader.obligationId), obligation = decode(obligationRow);
        obligation.status = accepted ? 'consumed' : 'closed'; tx.putProjection('interaction', obligation.id, obligationRow.revision, source, encode(obligation));
        if (accepted) {commitActions(source); tx.putProjection('attempt', data.value.callId, 0, source, encode(data.value));}
      } else if (accepted) {
        tx.putProjection('attempt', task.leader.reviewRecord, 0, source, encode({report: data.value, decision: task.leader.review}));
        const actionRow = tx.projection('attempt', task.leader.reviewAction), action = decode(actionRow);
        check(action?.workerId === ticket.workerId && action.status === 'running', 'invalid_leader_receipt');
        action.status = 'succeeded'; action.result = clone(task.leader.review); tx.putProjection('attempt', action.id, actionRow.revision, source, encode(action));
      }
      if (accepted) this.app.artifacts.commitOutputs(tx, task.task.id, staged, source);
      this.app.execution.putWorker(tx, row, record, source);
      const command = tx.command(ticket.commandId);
      if (clean) {
        if (command.status !== 'observed') tx.observeCommand(command.id, command.revision, 'observed', source);
        const capacity = this.app.execution.capacity(tx); capacity.value.active = capacity.value.active.filter(value => value.workerId !== ticket.workerId);
        this.app.execution.putCapacity(tx, capacity.row, capacity.value);
      }
      if (follow) this.obligation(tx, task, source, stale ? 'semantic-successor' : ticket.executionType === 'review' ? 'review-finished' : 'delivery-ready', [], successorSources);
      return clone(record.worker);
    });
  }
  finishEffect(ticket, result) {
    const data = result?.receipt ? receipt(this.effects[ticket.executionType], ticket, result) : null;
    const eligible = this.app.transaction(false, tx => {
      const {record, task} = this.app.execution.ticket(tx, ticket);
      return live(record) && data?.cleanup && (data.cleanup.started === null && record.executionId === null ||
        data.cleanup.started?.executionId === record.executionId && data.cleanup.started?.startedAt === record.worker.startedAt);
    });
    const staged = eligible && data.value.evidence ? this.app.artifacts.stageOutputs([['evidence', data.value.evidence]]) : [];
    if (eligible && ticket.executionType === 'postverify' && data.status === 'completed') {
      check(data.value.delivery?.content instanceof Uint8Array && digest(data.value.delivery.content) === ticket.input.publicationArtifact.digest &&
        data.value.delivery.content.length === ticket.input.publicationArtifact.bytes, 'invalid_leader_receipt');
    }
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.app.execution.ticket(tx, ticket);
      if (!live(record)) return clone(record.worker);
      const cleanup = result?.cleanup;
      if (!cleanup || !(cleanup.started === null && record.executionId === null || cleanup.started?.executionId === record.executionId &&
        cleanup.started.startedAt === record.worker.startedAt)) reject('recovery_required', 409);
      const clean = cleanup.cleaned === true && !record.custody?.extraScopes.length;
      const cancelled = !!task.cancelIntent || !!record.stopIntent || !!record.failureCode || result?.stopRequested === true ||
        task.task.status === 'cancelling' || terminal.has(task.task.status) || this.app.now() >= ticket.deadline;
      const actionId = ticket.executionType === 'publication' ? task.leader.publication.actionId : task.leader.postverify.actionId;
      const actionRow = tx.projection('attempt', actionId), action = decode(actionRow);
      check(action?.workerId === ticket.workerId && action.commandId === ticket.commandId && action.status === 'running', 'invalid_leader_receipt');
      const current = hash(ticket.input.managedReadSet) === hash(this.semantic(tx, task));
      // Current-owner, ticket-bound observation survives a control fence.
      // Admission of the NEXT effect remains separately fenced below.
      const observed = eligible && staged.length === 1, success = observed && data.status === 'completed';
      const advance = success && clean && !cancelled && current;
      const noneStarted = clean && cleanup.started === null && record.executionId === null;
      const unknown = !clean || observed && data.status === 'unknown' || !observed && !noneStarted;
      const artifact = observed ? {id: this.app.newId('artifact'), taskId: task.task.id, name: staged[0].name, kind: 'evidence', status: 'ready',
        mediaType: staged[0].mediaType, ...staged[0].ref, createdAt: new Date(this.app.now()).toISOString()} : null;
      if (artifact) staged[0].artifact = artifact;
      record.cleanup = clone(cleanup); record.worker.status = !clean ? 'unknown' : cancelled ? 'cancelled' : advance ? 'completed' : 'failed';
      record.worker.phase = 'terminal'; record.worker.finishedAt = new Date(this.app.now()).toISOString();
      action.status = unknown ? 'unknown' : ticket.executionType === 'publication' && success ? 'succeeded' : cancelled ? 'cancelled' : advance ? 'succeeded' : 'failed';
      action.result = artifact ? {evidenceId: artifact.id, digest: artifact.digest, observedStatus: data.value.status} : null;
      let post = null;
      if (ticket.executionType === 'publication') {
        task.leader.publication.status = unknown ? 'unknown' : observed ? data.value.status : cancelled ? 'cancelled' : 'failed';
        task.leader.publication.receiptArtifactId = artifact?.id ?? null;
        if (advance) {
          const id = 'action-' + hash({publicationActionId: action.id, kind: 'postverify'}).slice(7), commandId = this.app.newId('command');
          post = {id, taskId: task.task.id, sourceDecision: action.sourceDecision, payloadDigest: hash({publication: action.id}),
            payload: {type: 'postverify', publicationActionId: action.id}, status: 'pending', commandId, workerId: null, result: null};
          task.leader.postverify = {actionId: id, status: 'pending', evidenceArtifactId: null};
        }
      } else {
        task.leader.postverify.status = advance ? 'passed' : unknown ? 'unknown' : cancelled ? 'cancelled' : 'failed';
        task.leader.postverify.evidenceArtifactId = artifact?.id ?? null;
        if (advance) {task.leader.stage = 'finalizing'; this.prepareObligation(task);}
      }
      if (unknown) {task.task.status = 'intervention'; task.task.code = 'publication_effect_unresolved';}
      else if ((result?.stopRequested || record.failureCode) && !task.cancelIntent && !terminal.has(task.task.status)) {
        task.task.status = 'cancelling'; task.failureCode = record.failureCode ?? 'publication_stopped';
      }
      else if (!advance && !cancelled) {task.task.status = 'cancelling'; task.failureCode = 'publication_failed';}
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'leader.action.settled', {actionId: action.id, workerId: ticket.workerId, status: action.status});
      tx.putProjection('attempt', action.id, actionRow.revision, source, encode(action));
      if (artifact) this.app.artifacts.commitOutputs(tx, task.task.id, staged, source);
      this.app.execution.putWorker(tx, row, record, source);
      if (clean) {
        const capacity = this.app.execution.capacity(tx); capacity.value.active = capacity.value.active.filter(value => value.workerId !== ticket.workerId);
        this.app.execution.putCapacity(tx, capacity.row, capacity.value);
        const command = tx.command(ticket.commandId); if (!unknown && command.status !== 'observed') tx.observeCommand(command.id, command.revision, 'observed', source);
      }
      if (post) {tx.putProjection('attempt', post.id, 0, source, encode(post)); this.app.enqueue(tx, source, task.task.id, 'postverify', {taskId: task.task.id, actionId: post.id}, 'verify', post.commandId);}
      if (advance && ticket.executionType === 'postverify') this.obligation(tx, task, source, 'postverify-finished');
      return clone(record.worker);
    });
  }
  reply(request) {
    this.shape(request);
    return this.app.mutate(request, tx => {
      const task = this.app.get(tx, request.taskId); this.configured(task);
      const row = tx.projection('interaction', request.requestId), question = decode(row), body = request.body;
      if (!question || question.taskId !== task.task.id) reject('not_found', 404);
      if (body.expectedRevision !== task.task.revision) reject('revision_conflict', 409);
      if (question.requestDigest !== body.requestDigest || question.status !== 'pending' || task.cancelIntent || terminal.has(task.task.status) ||
          task.task.status === 'cancelling' || this.app.now() >= Date.parse(question.deadlineAt)) reject('state_conflict', 409);
      if (question.kind === 'business' ? !Object.hasOwn(body, 'answer') || question.options.length && !question.options.some(item => item.value === body.answer) :
        !Object.hasOwn(body, 'decision')) reject('invalid_request', 400);
      const replyRef = this.app.newId('reply'), value = {taskId: task.task.id, requestId: question.id, requestDigest: question.requestDigest,
        ...(question.kind === 'business' ? {answer: body.answer} : {decision: body.decision})};
      const replyDigest = hash(value); question.status = 'replied'; question.replyRef = replyRef; question.replyDigest = replyDigest;
      task.task.revision = nextRevision(task.task.revision); task.leader.cursor++;
      const publicationAllowed = question.kind === 'publication' && body.decision === 'allow';
      if (!publicationAllowed) this.prepareObligation(task);
      if (task.task.status !== 'paused') task.task.status = task.approved ? 'running' : 'draft';
      const source = this.app.save(tx, task, 'leader.request.replied', {requestId: question.id, replyDigest});
      tx.putProjection('interaction', replyRef, 0, source, encode(value));
      tx.putProjection('interaction', question.id, row.revision, source, encode(question));
      if (publicationAllowed) {
        const action = decode(tx.projection('attempt', question.actionId));
        check(action?.status === 'pending' && hash(action.authorization) === question.subject, 'candidate_manifest_conflict');
        this.app.enqueue(tx, source, task.task.id, 'publication', {taskId: task.task.id, actionId: action.id}, 'start', action.commandId);
      } else this.obligation(tx, task, source, question.kind === 'business' ? 'business-replied' : 'publication-denied', question.nodeIds);
      return {source, result: {taskId: task.task.id, requestId: question.id, receiptId: replyRef, requestDigest: question.requestDigest,
        replyDigest, acceptedRevision: body.expectedRevision + 1, replayed: false}};
    });
  }
  view(tx, task) {
    this.configured(task);
    const requests = this.request(tx, task), pending = requests.find(value => value.status === 'pending') ?? requests.at(-1) ?? null;
    // Preserve the exact native outcome internally; the public operation-like
    // projection uses the already frozen LeaderView status vocabulary.
    const effect = value => value ? {...clone(value), status: ['created', 'matched', 'passed'].includes(value.status) ? 'succeeded' : value.status} : null;
    return {taskId: task.task.id, taskRevision: task.task.revision, profile: LEADER_PROFILE, stage: task.leader.stage,
      policyDigest: task.leader.policyDigest, activeWorkerId: task.leader.activeWorkerId ?? null,
      pendingRequest: pending ? Object.fromEntries(['id', 'kind', 'requestDigest', 'subject', 'nodeIds', 'prompt', 'options', 'authorization', 'deadlineAt', 'status', 'replyDigest'].map(key => [key, pending[key]])) : null,
      lastDecision: clone(task.leader.lastDecision), review: clone(task.leader.review), publication: effect(task.leader.publication),
      postverify: effect(task.leader.postverify), summaryArtifactId: task.leader.summaryArtifactId};
  }
}
