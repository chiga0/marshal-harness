import {encode, digest, makeEvent} from '../task-store/store.mjs';
import {clone, reject, terminal, nextRevision, isText} from './model.mjs';
import {TaskCleanup} from './cleanup.mjs';

const decode = entry => entry ? JSON.parse(entry.bytes.toString('utf8')) : null;
const hash = value => digest(encode(value));
const live = worker => ['queued', 'running', 'awaiting-answer', 'stopping', 'unknown'].includes(worker.status);
const usage = () => ({tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0});
const rolePhase = role => ({planner: 'planning', author: 'development', reviewer: 'review',
  integrator: 'integration', verifier: 'verification'})[role];

// A reducer over the SAME Application/SQLite authority. No timers, subprocess,
// Agent brands, runtime handles or filesystem side effects live in this class.
export class TaskExecution {
  constructor(application, {maxWorkers = 2, providerIds = [], defaultProvider = null, questionProviderIds = []} = {}) {
    if (!Number.isSafeInteger(maxWorkers) || maxWorkers < 1 || maxWorkers > 64 || !Array.isArray(providerIds) ||
        providerIds.length > 32 || providerIds.some(id => !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id)) ||
        new Set(providerIds).size !== providerIds.length || defaultProvider !== null && !providerIds.includes(defaultProvider) ||
        !Array.isArray(questionProviderIds) || questionProviderIds.some(id => !providerIds.includes(id)) || new Set(questionProviderIds).size !== questionProviderIds.length)
      reject('invalid_execution_config', 503);
    this.app = application; this.maxWorkers = maxWorkers;
    this.providers = new Set(providerIds); this.defaultProvider = defaultProvider;
    this.questionProviders = new Set(questionProviderIds);
    this.cleanup = new TaskCleanup(this);
  }
  custodyBinding(ticket, profile) { return this.cleanup.binding(ticket, profile); }
  bindCustody(ticket, descriptor, profile) { return this.cleanup.bind(ticket, descriptor, profile); }
  recordExtraScope(ticket, code) { return this.cleanup.extraScope(ticket, code); }
  pendingCleanup(after = '', limit = 25) { return this.cleanup.pending(after, limit); }
  reconcileCleanup(workerId, observation) { return this.cleanup.settle(workerId, observation); }
  registerQuestion(ticket, request) {return this.app.runtimeQuestions.register(ticket, request);}
  dispatchAnswer(ticket, questionId) {return this.app.runtimeQuestions.dispatch(ticket, questionId);}
  acknowledgeAnswer(ticket, questionId, receipt) {return this.app.runtimeQuestions.acknowledge(ticket, questionId, receipt);}
  observeInput(ticket, stage, prompt) {return this.app.inputAudit.observe(ticket, stage, prompt);}
  worker(tx, id) {
    const row = tx.projection('attempt', id); if (!row) reject('not_found', 404);
    return {row, record: decode(row)};
  }
  workers(tx, task) { return (task.workerIds ?? []).map(id => this.worker(tx, id)); }
  putWorker(tx, row, record, source) {
    tx.putProjection('attempt', record.worker.id, row ? row.revision : 0n, source, encode(record));
  }
  capacity(tx) {
    const row = tx.projection('budget', 'service-capacity');
    return {row, value: decode(row) ?? {active: []}};
  }
  putCapacity(tx, row, value) {
    const stream = 'service-capacity', head = tx.head(stream);
    const event = makeEvent(stream, head.sequence + 1n, {type: 'capacity.changed', active: value.active,
      at: new Date(this.app.now()).toISOString()});
    const source = {stream, ...tx.append(stream, head, [event])};
    tx.putProjection('budget', stream, row ? row.revision : 0n, source, encode(value));
  }
  poll(after = '', limit = 50) {
    return this.app.transaction(false, tx => {
      const page = tx.commands(after, limit);
      return {items: page.filter(command => command.status === 'pending'),
        nextCursor: page.length === limit ? page.at(-1).id : null};
    });
  }
  scan(after = '', limit = 25) {
    return this.app.transaction(false, tx => {
      const page = tx.projections('task', after, limit);
      return {items: page.map(decode).filter(record => !['completed', 'failed', 'cancelled'].includes(record.task.status))
        .map(record => record.task.id), nextCursor: page.length === limit ? page.at(-1).id : null};
    });
  }
  // A generation change never proves old processes stopped. Only current
  // in-memory handles can act on the returned IDs; no persisted PID is used.
  reconcile(taskId) {
    return this.app.transaction(true, tx => {
      const task = this.app.get(tx, taskId), workers = this.workers(tx, task);
      if (this.app.repair.recoverUnstarted(tx, task)) return {taskId, status: task.task.status, stopWorkerIds: []};
      const before = hash(task), active = workers.filter(({record}) => live(record.worker));
      if (active.some(({record}) => record.ticket.generation !== this.app.owner.generation.toString())) {
        task.task.status = 'intervention'; task.task.code = 'previous_execution_unresolved';
      } else if (!terminal.has(task.task.status) && (this.app.now() >= Date.parse(task.task.deadlineAt) || this.app.runtimeQuestions.expired(task))) {
        task.task.status = 'cancelling'; task.failureCode ??= this.app.runtimeQuestions.expired(task) ? 'question_expired' : 'task_deadline';
      }
      if (task.task.status === 'cancelling' && active.length === 0) {
        task.task.status = task.failureCode ? 'failed' : 'cancelled'; task.task.phase = 'terminal';
        if (task.failureCode) task.task.code = task.failureCode;
        for (const node of task.nodes) if (['pending', 'ready', 'waiting', 'running'].includes(node.status)) node.status = 'cancelled';
      }
      const closedQuestions = ['cancelling', 'intervention', 'failed', 'cancelled'].includes(task.task.status) ?
        this.app.runtimeQuestions.close(task, task.failureCode === 'question_expired' ? 'expired' : 'cancelled') : [];
      if (hash(task) !== before) {
        task.task.revision = nextRevision(task.task.revision);
        const source = this.app.save(tx, task, 'task.execution-reconciled', {status: task.task.status});
        this.app.runtimeQuestions.settleClosed(tx, task, source, closedQuestions);
        this.app.repair.settle(tx, task, source);
      }
      return {taskId, status: task.task.status,
        stopWorkerIds: task.task.status === 'cancelling' || terminal.has(task.task.status) ? active
          .filter(({record}) => record.ticket.generation === this.app.owner.generation.toString())
          .map(({record}) => record.worker.id) : []};
    });
  }
  settleOperation(tx, operationId, status, source, task) {
    if (!operationId) return;
    const row = tx.projection('operation', operationId), operation = decode(row);
    if (!operation || operation.taskId !== task.task.id) reject('application_unavailable', 503);
    if (!['accepted', 'running'].includes(operation.status)) return;
    operation.status = status; operation.taskRevision = task.task.revision;
    operation.updatedAt = new Date(this.app.now()).toISOString();
    tx.putProjection('operation', operationId, row.revision, source, encode(operation));
  }
  // Control receipt bytes stay immutable; querying the Operation returns its
  // reconciled observation. Pause fences new dispatch, not existing workers.
  settleControl(commandId, expectedRevision) {
    return this.app.transaction(true, tx => {
      const command = tx.command(commandId);
      if (!command || command.status !== 'pending' || command.revision !== BigInt(expectedRevision)) return false;
      const payload = decode({bytes: command.payload}), task = this.app.get(tx, command.taskId);
      if (payload.action === 'answer') return false; // Runtime question reducer owns this obligation.
      // Observing a durable control is not replaying an external execution.
      // Old-generation launches are never made eligible by this exception.
      if (command.generation !== this.app.owner.generation && !['cancel', 'pause', 'resume'].includes(payload.action)) return false;
      const fenced = terminal.has(task.task.status) || task.task.status === 'cancelling';
      let status = 'succeeded';
      if (payload.action === 'cancel') {
        if (!terminal.has(task.task.status)) return false;
        if (task.task.status === 'intervention') status = 'unknown';
      } else if (!['pause', 'resume'].includes(payload.action) && !fenced) return false;
      else if (!['pause', 'resume'].includes(payload.action)) status = 'failed';
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'task.obligation-settled', {commandId, action: payload.action, status});
      this.settleOperation(tx, payload.operationId, status, source, task);
      tx.observeCommand(command.id, command.revision, status === 'unknown' ? 'unknown' : 'observed', source);
      return true;
    });
  }
  ticket(tx, ticket) {
    const {row, record} = this.worker(tx, ticket.workerId);
    const {input, ...identity} = ticket;
    if (hash(record.ticket) !== hash(identity) || hash(input) !== ticket.inputDigest ||
        ticket.generation !== this.app.owner.generation.toString()) reject('recovery_required', 409);
    const task = this.app.get(tx, ticket.taskId);
    return {row, record, task};
  }
  // A known local Worker failure fences the entire Task BEFORE external stop or
  // cleanup can finish. It is not a cleanup, refund, new attempt or acceptance.
  fail(ticket, reasonCode) {
    if (reasonCode !== 'worker_failed') reject('invalid_request', 400);
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.ticket(tx, ticket);
      // Preserve an earlier user cancel/terminal conclusion and make repeated
      // reports append-free. Old completed Workers cannot fail a later phase.
      if (this.app.repair.current(task, ticket) && live(record.worker) && task.task.status !== 'cancelling' && !terminal.has(task.task.status)) {
        task.task.status = 'cancelling'; task.failureCode = reasonCode;
        task.task.revision = nextRevision(task.task.revision);
        const closedQuestions = this.app.runtimeQuestions.close(task);
        const source = this.app.save(tx, task, 'task.worker-failure-fenced', {workerId: ticket.workerId, reasonCode});
        this.app.runtimeQuestions.settleClosed(tx, task, source, closedQuestions);
        record.failureCode = reasonCode;
        this.putWorker(tx, row, record, source);
      }
      return {taskId: task.task.id, status: task.task.status};
    });
  }
  // null means a current dependency/capacity/fence prevents launch. A ticket is
  // returned exactly once: reservation + command UNKNOWN commit before spawn.
  nextWork(commandId, expectedRevision) {
    return this.app.transaction(true, tx => {
      const command = tx.command(commandId);
      if (!command || command.revision !== BigInt(expectedRevision) || command.status !== 'pending' ||
          command.generation !== this.app.owner.generation) return null;
      const payload = decode({bytes: command.payload}), task = this.app.get(tx, command.taskId);
      if (task.repair && payload.action === 'execute' && (payload.repairId ?? null) !== (task.activeRepair?.repairId ?? null)) return null;
      if (terminal.has(task.task.status) || task.task.status === 'cancelling' || task.task.status === 'paused') return null;
      if (!['plan', 'execute'].includes(payload.action)) return null;
      if (this.app.now() >= Date.parse(task.task.deadlineAt)) return null;
      let node;
      if (payload.action === 'plan') {
        if (task.task.status !== 'draft' || task.plan) return null;
        node = {id: 'planning', role: 'planner', goal: task.task.intent, scope: [], providerId: null};
      } else {
        if (!['queued', 'running', 'awaiting-answer'].includes(task.task.status) || !task.approved || !task.plan ||
            task.approved.planDigest !== task.plan.digest || payload.planDigest !== task.plan.digest) return null;
        node = task.plan.nodes.find(node => node.id === payload.nodeId);
        const state = task.nodes.find(state => state.id === payload.nodeId);
        if (!node || !state || state.status !== 'pending') return null;
        const dependencies = task.plan.edges.filter(edge => edge.to === node.id).map(edge => edge.from);
        if (dependencies.some(id => task.nodes.find(state => state.id === id)?.status !== 'completed')) return null;
      }
      const verification = task.verification && task.verification.nodeId === node.id;
      if (task.verification) this.app.verification.configured(task.verification);
      if (task.runtimeQuestions) this.app.runtimeQuestions.configured(task.runtimeQuestions, task.plan);
      if (task.repair) this.app.repair.configured(task);
      const providerId = verification ? task.verification.providerId : node.providerId ?? this.defaultProvider;
      if (!verification && !this.providers.has(providerId)) reject('unsupported_task', 422);
      const capacity = this.capacity(tx), taskWorkers = this.workers(tx, task);
      const budget = task.approved ? task.plan.budget : task.limits;
      if (capacity.value.active.length >= this.maxWorkers || taskWorkers.filter(({record}) => live(record.worker)).length >= budget.maxWorkers) return null;
      if (task.attempts >= budget.maxAttempts) reject('capacity_exceeded', 429);
      const id = this.app.newId('worker'), at = new Date(this.app.now()).toISOString();
      const dependencies = new Set((task.plan?.edges ?? []).filter(edge => edge.to === node.id).map(edge => edge.from));
      const upstreamWorkers = task.repair ? this.app.repair.selected(tx, task, [...dependencies]) : taskWorkers;
      const input = {task: task.input, inputArtifacts: task.inputArtifacts ?? [], node, plan: task.plan, upstream: upstreamWorkers
        .filter(({record}) => dependencies.has(record.worker.nodeId) && record.ticket.planDigest === task.approved?.planDigest &&
          record.worker.status === 'completed' && record.resultRef !== null)
        .map(({record}) => {
          if (task.verification) {
            if (!record.candidate) reject('candidate_manifest_conflict', 422);
            return {workerId: record.worker.id, nodeId: record.worker.nodeId, result: clone(record.candidate),
              ...(task.runtimeQuestions ? {interactionRefs: clone(record.interactionRefs ?? [])} : {})};
          }
          const entry = tx.projection('attempt', record.resultRef);
          if (!entry || digest(entry.bytes) !== record.resultDigest) reject('application_unavailable', 503);
          return {workerId: record.worker.id, nodeId: record.worker.nodeId, result: decode(entry)};
        })};
      if (task.verification) input.fileLayout = this.app.verification.resolve(task, node.id, input.upstream);
      if (task.runtimeQuestions) {
        input.runtimeQuestions = {profile: task.runtimeQuestions.descriptor.profile, policyDigest: task.runtimeQuestions.policyDigest,
          maxWaitMs: task.runtimeQuestions.descriptor.maxWaitMs, enabled: task.runtimeQuestions.descriptor.nodeIds.includes(node.id)};
        input.interactionRefs = verification && !task.repair ? this.app.runtimeQuestions.refs(tx, task) :
          this.app.runtimeQuestions.inherited(tx, task, input.upstream.flatMap(item => item.interactionRefs ?? []));
      }
      if (verification) {
        if (taskWorkers.some(({record}) => live(record.worker))) return null;
        const producers = task.repair ? this.app.repair.selected(tx, task) : taskWorkers.filter(({record}) => record.ticket.planDigest === task.approved.planDigest);
        if (producers.length !== task.plan.nodes.length - 1 || producers.some(({record}) => record.worker.status !== 'completed' || !record.candidate)) return null;
        input.verification = {binding: clone(task.verification), manifests: producers.map(({record}) => ({workerId: record.worker.id,
          nodeId: record.worker.nodeId, resultDigest: record.resultDigest, manifest: clone(record.candidate)})).sort((a, b) => a.nodeId < b.nodeId ? -1 : 1)};
        if (task.repair && task.runtimeQuestions) input.interactionRefs = this.app.runtimeQuestions.inherited(tx, task,
          producers.flatMap(({record}) => record.interactionRefs ?? []));
      }
      const repair = task.repair ? this.app.repair.input(tx, task, node.id) : null;
      if (repair) input.repair = repair;
      const frozen = {workerId: id, taskId: task.task.id, nodeId: node.id, role: node.role, providerId,
        executionType: verification ? 'verification' : 'agent',
        generation: this.app.owner.generation.toString(), commandId, inputDigest: hash(input),
        planDigest: task.approved?.planDigest ?? null,
        deadline: Math.min(Date.parse(task.task.deadlineAt), this.app.now() + 86400000), input,
        ...(this.app.repair.port ? {repairId: task.activeRepair?.repairId ?? null} : {})};
      const ticket = {...frozen, reservationDigest: hash(frozen)};
      const {input: _input, ...identity} = ticket;
      const record = {ticket: identity, inputRef: this.app.newId('input'), worker: {id, taskId: task.task.id, nodeId: node.id, providerId, role: node.role,
        status: 'queued', phase: rolePhase(node.role), attempt: task.attempts + 1,
        startedAt: null, finishedAt: null, lastObservedAt: at, progress: null, usage: usage()},
      executionId: null, cleanup: null, resultRef: null, resultDigest: null, progressSequence: 0};
      task.attempts++; task.workerIds ??= []; task.workerIds.push(id);
      task.task.status = payload.action === 'plan' ? 'planning' : task.task.status === 'awaiting-answer' ? 'awaiting-answer' : 'running';
      task.task.phase = payload.action === 'plan' ? 'planning' : 'execution';
      if (payload.action === 'execute') {
        const state = task.nodes.find(state => state.id === node.id); state.status = 'running'; state.workerIds.push(id);
      }
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.reserved', {workerId: id, reservationDigest: ticket.reservationDigest});
      // Immutable input/result snapshots are separate from compact Worker
      // observations. Reading capacity/history must not repeatedly load every
      // prior copy of a large plan into this bounded transaction.
      tx.putProjection('attempt', record.inputRef, 0, source, encode(input));
      this.putWorker(tx, null, record, source);
      tx.observeCommand(command.id, command.revision, 'unknown', source);
      capacity.value.active.push({workerId: id, taskId: task.task.id, generation: frozen.generation});
      this.putCapacity(tx, capacity.row, capacity.value);
      return clone(ticket);
    });
  }
  mayStart(ticket) {
    return this.app.transaction(false, tx => {
      const {record, task} = this.ticket(tx, ticket);
      return this.app.repair.current(task, ticket) && record.worker.status === 'queued' && !terminal.has(task.task.status) &&
        !['cancelling', 'paused'].includes(task.task.status) && this.app.now() < ticket.deadline;
    });
  }
  approvedLayout(ticket) {
    return this.app.transaction(false, tx => {
      const {task} = this.ticket(tx, ticket);
      if (!task.approved || task.approved.planDigest !== ticket.planDigest || !task.verification) reject('unsupported_task', 422);
      const layout = this.app.verification.resolve(task, ticket.nodeId, ticket.input.upstream);
      if (hash(layout) !== hash(ticket.input.fileLayout)) reject('candidate_manifest_conflict', 422);
      return {planDigest: ticket.planDigest, nodeId: ticket.nodeId, layoutDigest: hash({profile: 'task-file-business/v1', ...layout})};
    });
  }
  observeExecution(ticket) {
    return this.app.transaction(false, tx => {
      const {record} = this.ticket(tx, ticket);
      if (!record.executionId || !record.worker.startedAt) reject('state_conflict', 409);
      return {executionId: record.executionId, startedAt: record.worker.startedAt};
    });
  }
  started(ticket, started) {
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.ticket(tx, ticket);
      if (!started || !isText(started.executionId, 128) || !Number.isFinite(Date.parse(started.startedAt))) reject('invalid_request', 400);
      if (record.custody && record.custody.descriptor.executionId !== started.executionId) reject('recovery_required', 409);
      if (record.executionId !== null) {
        if (record.executionId !== started.executionId) reject('state_conflict', 409);
        return {stop: task.task.status === 'cancelling' || terminal.has(task.task.status) || this.app.now() >= ticket.deadline};
      }
      if (record.worker.status !== 'queued') reject('state_conflict', 409);
      record.executionId = started.executionId; record.worker.startedAt = started.startedAt;
      const stop = task.task.status === 'cancelling' || terminal.has(task.task.status) || this.app.now() >= ticket.deadline;
      record.worker.status = stop ? 'stopping' : 'running';
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'worker.started', {workerId: ticket.workerId, executionId: record.executionId});
      this.putWorker(tx, row, record, source);
      return {stop};
    });
  }
  progress(ticket, sequence, progress) {
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.ticket(tx, ticket);
      if (!this.app.repair.current(task, ticket) || !Number.isSafeInteger(sequence) || sequence <= record.progressSequence || !live(record.worker) ||
          task.task.status === 'cancelling' || terminal.has(task.task.status)) return false;
      if (!progress || !isText(progress.summary, 2048) || progress.tool !== null && !isText(progress.tool, 256) ||
          !['agent', 'execution'].includes(progress.source)) reject('invalid_request', 400);
      record.progressSequence = sequence;
      record.worker.progress = {summary: progress.summary, tool: progress.tool, source: progress.source};
      record.worker.lastObservedAt = new Date(this.app.now()).toISOString();
      const stream = task.task.id, head = tx.head(stream);
      const event = makeEvent(stream, head.sequence + 1n, {type: 'worker.progress', at: record.worker.lastObservedAt,
        taskRevision: task.task.revision, workerId: ticket.workerId, progressSequence: sequence});
      const source = {stream, ...tx.append(stream, head, [event])};
      this.putWorker(tx, row, record, source); return true;
    });
  }
  finish(ticket, result) {
    const verification = ticket.executionType === 'verification';
    // No files, checker, promises or depot writes inside the Store callback.
    const verified = verification && result?.type === 'verification' && (result.status === 'passed' || result.receipt !== undefined) ?
      this.app.verification.stage(ticket, result) : null;
    return this.app.transaction(true, tx => {
      const {row, record, task} = this.ticket(tx, ticket);
      if (!live(record.worker)) return clone(record.worker);
      const completion = result?.cleanup;
      if (!completion || completion.started?.executionId !== record.executionId &&
          !(completion.started === null && record.executionId === null)) reject('recovery_required', 409);
      const clean = completion.cleaned === true && !(record.custody?.extraScopes.length);
      const currentCycle = this.app.repair.current(task, ticket);
      const cancelled = task.task.status === 'cancelling' || !currentCycle;
      let success = clean && (verification ? verified?.data.status === 'passed' && result.status === 'passed' && verified.staged !== null :
        result.status === 'completed' && result.stopReason === 'end_turn') &&
        !cancelled && !terminal.has(task.task.status) && this.app.now() < ticket.deadline;
      let candidate = success && !verification ? clone(result.result ?? null) : null;
      if (success && task.runtimeQuestions) {
        try {record.interactionRefs = verification ? (task.repair ? this.app.runtimeQuestions.inherited(tx, task,
          this.app.repair.selected(tx, task).flatMap(({record}) => record.interactionRefs ?? [])) : this.app.runtimeQuestions.refs(tx, task)) :
          this.app.runtimeQuestions.resultRefs(tx, task, ticket);}
        catch {success = false; candidate = null;}
      }
      const verificationFailed = clean && verified?.data.status === 'failed' && result.status === 'failed' && verified.staged !== null &&
        !cancelled && !terminal.has(task.task.status) && this.app.now() < ticket.deadline;
      if (verification && (success || verificationFailed)) {
        if (completion.started?.startedAt !== record.worker.startedAt) reject('invalid_verification_receipt', 422);
        this.app.verification.recheck(tx, task, ticket);
      } else if (success && task.verification && ticket.planDigest !== null) {
        try { record.candidate = this.app.verification.candidate(ticket, candidate); }
        catch { success = false; candidate = null; }
      }
      const failedWorker = record.failureCode === 'worker_failed';
      record.cleanup = clone(completion);
      if (!clean && completion.cleaned) record.cleanup = {...record.cleanup, cleaned: false, reason: 'extra_scope_unresolved'};
      record.worker.status = !clean ? 'unknown' : cancelled && !failedWorker ? 'cancelled' : success ? 'completed' : 'failed';
      record.worker.finishedAt = new Date(this.app.now()).toISOString(); record.worker.phase = 'terminal';
      record.resultRef = candidate === null ? null : this.app.newId('result');
      record.resultDigest = candidate === null ? null : hash(task.runtimeQuestions ? {candidate, interactionRefs: record.interactionRefs} : candidate);
      if (!clean) { task.task.status = 'intervention'; task.task.code = 'cleanup_unconfirmed'; }
      else if (!cancelled && !success && !terminal.has(task.task.status)) {
        task.task.status = 'cancelling'; task.failureCode = 'worker_failed';
      }
      if (ticket.role === 'planner' && ticket.planDigest === null && success) {
        try {
          const plan = this.app.freezePlan(task, result.plan);
          if (plan.nodes.length + task.attempts > plan.budget.maxAttempts) reject('plan_budget_exceeded', 400);
          task.plan = plan; task.task.plan = {revision: plan.revision, digest: plan.digest};
          task.task.status = 'awaiting-approval'; task.task.phase = 'planning';
          task.nodes = plan.nodes.map(node => ({id: node.id, role: node.role, status: 'pending', workerIds: []}));
        } catch {
          record.worker.status = 'failed'; task.task.status = 'failed'; task.task.phase = 'terminal'; task.task.code = 'invalid_plan';
        }
      } else if (ticket.planDigest !== null && currentCycle) {
        const node = task.nodes.find(node => node.id === ticket.nodeId);
        node.status = !clean ? 'unknown' : cancelled && !failedWorker ? 'cancelled' : success ? 'completed' : 'failed';
      }
      this.app.repair.recordSelection(task, record);
      const closedQuestions = this.app.runtimeQuestions.close(task, 'cancelled', ticket.workerId);
      task.task.revision = nextRevision(task.task.revision);
      let decision = null;
      if (verification && (success || verificationFailed)) {
        const at = new Date(this.app.now()).toISOString();
        for (const item of verified.staged) item.artifact = {id: this.app.newId('artifact'), taskId: task.task.id,
          name: item.name, kind: item.kind, status: 'ready', mediaType: item.mediaType, ...item.ref, createdAt: at};
        const artifacts = verified.staged.map(item => item.artifact);
        decision = {id: this.app.newId('decision'), type: 'independent-verification', status: success ? 'accepted' : 'rejected', taskId: ticket.taskId,
          workerId: ticket.workerId, planDigest: ticket.planDigest, reservationDigest: ticket.reservationDigest,
          inputDigest: ticket.inputDigest, policyDigest: task.verification.policyDigest,
          manifestsDigest: hash(ticket.input.verification.manifests), cleanupDigest: hash(completion), artifacts, at};
        if (verificationFailed && typeof verified.data.reason === 'string' && /^[a-z][a-z0-9_-]{0,127}$/.test(verified.data.reason))
          decision.reasonCode = verified.data.reason;
        if (task.repair) {
          decision.repairId = task.activeRepair?.repairId ?? null;
          decision.contentRejection = verificationFailed ? clone(verified.data.contentRejection ?? null) : null;
        }
        task.decision = {id: decision.id, digest: hash(decision)};
        task.acceptance = {status: success ? 'passed' : 'failed', evidenceIds: artifacts.filter(item => item.kind === 'evidence').map(item => item.id), digest: task.decision.digest};
        task.task.artifactIds = artifacts.map(item => item.id);
        if (success) { task.task.status = 'completed'; task.task.phase = 'terminal'; }
      }
      const source = this.app.save(tx, task, 'worker.finished', {workerId: ticket.workerId,
        status: record.worker.status, resultDigest: record.resultDigest, decisionDigest: task.decision?.digest ?? null});
      this.app.runtimeQuestions.settleClosed(tx, task, source, closedQuestions);
      this.app.repair.settle(tx, task, source);
      if (decision) {
        this.app.artifacts.commitOutputs(tx, task.task.id, verified.staged, source);
        tx.putProjection('attempt', decision.id, 0, source, encode(decision));
      }
      if (record.resultRef) tx.putProjection('attempt', record.resultRef, 0, source, encode(candidate));
      this.putWorker(tx, row, record, source);
      const command = tx.command(ticket.commandId);
      if (clean && command.status !== 'observed') tx.observeCommand(command.id, command.revision, 'observed', source);
      if (clean) {
        this.app.runtimeQuestions.cleanupConfirmed(tx, task, source, ticket.workerId);
        const capacity = this.capacity(tx); capacity.value.active = capacity.value.active.filter(item => item.workerId !== ticket.workerId);
        this.putCapacity(tx, capacity.row, capacity.value);
      }
      return clone(record.worker);
    });
  }
  expandDispatch(commandId, expectedRevision) {
    return this.app.transaction(true, tx => {
      const command = tx.command(commandId);
      if (!command || command.status !== 'pending' || command.revision !== BigInt(expectedRevision) ||
          command.generation !== this.app.owner.generation) return false;
      const payload = decode({bytes: command.payload}), task = this.app.get(tx, command.taskId);
      if (payload.action !== 'dispatch' || task.task.status !== 'queued' || !task.approved ||
          payload.planDigest !== task.plan?.digest) return false;
      task.task.revision = nextRevision(task.task.revision);
      const source = this.app.save(tx, task, 'task.dispatch-expanded', {planDigest: task.plan.digest});
      for (const node of task.plan.nodes) this.app.enqueue(tx, source, task.task.id, 'execute',
        {taskId: task.task.id, nodeId: node.id, planDigest: task.plan.digest, ...(task.repair ? {repairId: null} : {})});
      this.settleOperation(tx, payload.operationId, 'succeeded', source, task);
      tx.observeCommand(command.id, command.revision, 'observed', source); return true;
    });
  }
  query(tx, request) {
    if (request.operation === 'worker.get') return clone(this.worker(tx, request.workerId).record.worker);
    if (request.operation === 'task.workers') {
      const task = this.app.get(tx, request.taskId), all = this.workers(tx, task)
        .map(({record}) => record.worker).sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
      const page = request.page ?? {}, limit = page.limit ?? 50;
      const items = all.filter(worker => !page.cursor || worker.id > page.cursor).slice(0, limit);
      return {taskId: task.task.id, items: clone(items), nextCursor: items.length === limit ? items.at(-1).id : null};
    }
    reject('unsupported_operation', 501);
  }
}
