import path from 'node:path';
import {setImmediate as yieldTurn} from 'node:timers/promises';

const PORTS = ['scan', 'reconcile', 'poll', 'settleControl', 'expandDispatch', 'nextWork', 'mayStart', 'started', 'progress', 'fail', 'finish'];
const PHASES = new Set(['starting', 'initializing', 'session', 'running', 'stopping', 'terminal']);
const TOOL_KINDS = new Set(['read', 'edit', 'delete', 'move', 'search', 'execute', 'think', 'fetch', 'other']);
const TOOL_STATES = new Set(['pending', 'in_progress', 'completed', 'failed']);
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const text = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const freeze = value => { if (object(value) || Array.isArray(value)) { for (const item of Object.values(value)) freeze(item); Object.freeze(value); } return value; };
class SupervisorError extends Error {
  constructor(code) { super(code); this.code = code; }
}
class ExecutionPortError extends SupervisorError {
  constructor(method) { super('supervisor_execution_unavailable'); this.method = method; }
}
class WorkRejected extends SupervisorError {}
const requireValue = value => { if (!value) throw new SupervisorError('supervisor_invalid_port'); };

/** Same Application authority; only live handles, timers and observation queues here. */
export class TaskSupervisor {
  #execution; #providers; #prepare; #collect; #release; #verification; #onError; #clock; #options;
  #owned = new Map(); #works = new Set(); #timer; #tick; #close;
  #running = false; #closing = false; #closed = false; #failure = null;
  #diagnostics = []; #notificationFailures = 0; #rejected = new Set(); #rejectedOverflow = false;
  #scanCursor = ''; #pollCursor = '';
  constructor({execution, providers, prepare, collect, release = () => {}, verification = null, onError = () => {}, clock = Date.now,
    intervalMs = 100, prepareMs = 30000, collectMs = 30000, pageSize = 25, maxPagesPerTick = 100} = {}) {
    requireValue(execution && PORTS.every(name => typeof execution[name] === 'function') && providers instanceof Map &&
      typeof prepare === 'function' && typeof collect === 'function' && typeof release === 'function' &&
      (verification === null || typeof verification.start === 'function') && typeof onError === 'function' && typeof clock === 'function');
    for (const [id, provider] of providers) requireValue(typeof id === 'string' && provider?.id === id && typeof provider.start === 'function');
    for (const [value, max] of [[intervalMs, 30000], [prepareMs, 30000], [collectMs, 30000], [pageSize, 100], [maxPagesPerTick, 100]])
      requireValue(Number.isSafeInteger(value) && value >= 1 && value <= max);
    this.#execution = execution; this.#providers = new Map(providers); this.#prepare = prepare; this.#collect = collect;
    this.#verification = verification; this.#release = release;
    this.#onError = onError; this.#clock = clock; this.#options = {intervalMs, prepareMs, collectMs, pageSize, maxPagesPerTick};
  }
  #call(name, ...args) {
    try {
      const result = this.#execution[name](...args);
      requireValue(!result || typeof result.then !== 'function'); // SQL callbacks remain synchronous.
      return result;
    } catch (error) {
      if (name === 'nextWork' && ['unsupported_task', 'capacity_exceeded'].includes(error?.code)) throw new WorkRejected(error.code);
      throw new ExecutionPortError(name);
    }
  }
  snapshot() {
    return {state: this.#closed ? 'closed' : this.#closing ? 'closing' : this.#failure ? 'failed' : this.#running ? 'running' : 'idle',
      failure: this.#failure ? {...this.#failure} : null,
      diagnostics: this.#diagnostics.map(item => ({...item})), notificationFailures: this.#notificationFailures,
      owned: [...this.#owned.values()].map(entry => ({taskId: entry.ticket.taskId, workerId: entry.ticket.workerId,
        stage: entry.stage, stopRequested: entry.stopping, cleanupConfirmed: entry.clean}))};
  }
  start() {
    if (this.#closing || this.#closed || this.#failure) throw new SupervisorError('supervisor_not_available');
    this.#running = true; this.#schedule(0); return this;
  }
  #schedule(delay = this.#options.intervalMs) {
    if (!this.#running || this.#closing || this.#failure || this.#timer) return;
    this.#timer = setTimeout(() => {
      this.#timer = undefined;
      void this.tick().finally(() => this.#schedule());
    }, delay);
  }
  tick() {
    if (this.#tick) return this.#tick;
    if (this.#closing || this.#closed || this.#failure) return Promise.resolve(this.snapshot());
    this.#tick = this.#cycle(true).catch(() => this.#fault('reconcile-or-dispatch'))
      .then(() => this.snapshot()).finally(() => { this.#tick = undefined; });
    return this.#tick;
  }
  async #cycle(admit) {
    const {pageSize, maxPagesPerTick} = this.#options;
    // Stop observations get a complete bounded turn before new admission.
    for (let pages = 0; pages < maxPagesPerTick; pages++) {
      const page = this.#call('scan', this.#scanCursor, pageSize);
      this.#page(page, this.#scanCursor);
      for (const taskId of page.items) {
        const state = this.#call('reconcile', taskId);
        for (const workerId of state.stopWorkerIds) {
          const entry = this.#owned.get(workerId);
          if (entry) this.#stop(entry); // Never construct a handle from a stored PID.
        }
      }
      this.#scanCursor = page.nextCursor ?? '';
      if (page.nextCursor === null) break;
      await yieldTurn();
    }
    for (const entry of this.#owned.values()) entry.wake.resolve();
    for (let pages = 0; pages < maxPagesPerTick; pages++) {
      const page = this.#call('poll', this.#pollCursor, pageSize);
      this.#page(page, this.#pollCursor);
      for (const command of page.items) {
        if (this.#call('settleControl', command.id, command.revision)) continue;
        if (!admit || this.#closing || this.#failure) continue;
        if (this.#call('expandDispatch', command.id, command.revision)) continue;
        let ticket;
        try { ticket = this.#call('nextWork', command.id, command.revision); }
        catch (error) {
          if (!(error instanceof WorkRejected)) throw error;
          // No ticket means no authority to invent a Worker/cleanup. Preserve the
          // pending obligation for Application diagnosis, without noisy retries.
          if (!this.#rejected.has(command.id)) {
            if (this.#rejected.size < 128) {
              this.#rejected.add(command.id);
              this.#notify({code: error.code, stage: 'admission', taskId: command.taskId, commandId: command.id});
            } else if (!this.#rejectedOverflow) {
              this.#rejectedOverflow = true; this.#notify({code: 'admission_diagnostics_bounded', stage: 'admission'});
            }
          }
          continue;
        }
        if (ticket) this.#admit(ticket);
      }
      // Filtering observed/unknown commands can produce an EMPTY nonfinal page.
      this.#pollCursor = page.nextCursor ?? '';
      if (page.nextCursor === null) break;
      await yieldTurn();
    }
  }
  #page(page, after) {
    requireValue(object(page) && Array.isArray(page.items) &&
      (page.nextCursor === null || typeof page.nextCursor === 'string' && page.nextCursor > after));
  }
  #fault(stage, entry) {
    if (!this.#failure) {
      this.#failure = {code: 'supervisor_failed', stage,
        ...(entry ? {taskId: entry.ticket.taskId, workerId: entry.ticket.workerId} : {})};
      this.#running = false; clearTimeout(this.#timer); this.#timer = undefined;
      for (const owned of this.#owned.values()) this.#stop(owned);
      this.#notify(this.#failure);
    }
  }
  #notify(report) {
    // Bounded snapshots, one notification per failed Worker/global failure.
    // Raw exceptions, provider output and host paths never enter this channel.
    this.#diagnostics.push({...report}); if (this.#diagnostics.length > 32) this.#diagnostics.shift();
    try { Promise.resolve(this.#onError({...report})).catch(() => { this.#notificationFailures++; }); }
    catch { this.#notificationFailures++; }
  }
  #failEntry(entry, stage, error) {
    if (error instanceof ExecutionPortError) { this.#fault(stage, entry); return; }
    if (!entry.failure) {
      entry.failure = true;
      // This is a synchronous same-owner transaction, before any stop callback
      // can run or delayed cleanup can leave a downstream admission window.
      try { this.#call('fail', entry.ticket, 'worker_failed'); }
      catch { this.#fault('failure-fence', entry); return; }
      this.#notify({code: 'worker_failed', stage, taskId: entry.ticket.taskId, workerId: entry.ticket.workerId});
    }
    for (const owned of this.#owned.values()) if (owned.ticket.taskId === entry.ticket.taskId) this.#stop(owned);
  }
  #deadline(entry) {
    // Prefer the original Task deadline's durable reason where it has expired;
    // a shorter execution deadline still needs a Worker failure admission fence.
    try { this.#call('reconcile', entry.ticket.taskId); }
    catch { this.#fault('deadline-reconcile', entry); return; }
    this.#failEntry(entry, 'deadline', new SupervisorError('supervisor_deadline'));
  }
  #stop(entry) {
    entry.stopping = true; entry.abort.abort(); entry.wake.resolve();
    if (entry.handle && !entry.stopSent) {
      entry.stopSent = true;
      try { Promise.resolve(entry.handle.stop()).catch(error => this.#failEntry(entry, 'provider-stop', error)); }
      catch (error) { this.#failEntry(entry, 'provider-stop', error); }
    }
  }
  #admit(value) {
    const ticket = freeze(structuredClone(value));
    requireValue(!this.#owned.has(ticket.workerId) && Number.isSafeInteger(ticket.deadline));
    const entry = {ticket, abort: new AbortController(), wake: deferred(), startedGate: deferred(), stage: 'preparing',
      handle: null, invoked: false, startFact: null, acceptStarted: true, progress: Promise.resolve(), sequence: 0, pendingProgress: 0,
      stopping: false, stopSent: false, clean: false, finalized: false};
    this.#owned.set(ticket.workerId, entry);
    const timer = setTimeout(() => this.#deadline(entry), Math.max(1, ticket.deadline - this.#clock()));
    const work = this.#run(entry).catch(() => this.#fault(entry.stage, entry)).finally(() => {
      try {
        const released = this.#release(entry.ticket);
        if (released && typeof released.then === 'function') void Promise.resolve(released).catch(() => {});
        requireValue(!released || typeof released.then !== 'function');
      } catch { this.#notify({code: 'worker_release_failed', stage: 'release', taskId: ticket.taskId, workerId: ticket.workerId}); }
      clearTimeout(timer); this.#works.delete(work);
      if (entry.clean && entry.finalized) this.#owned.delete(ticket.workerId);
      this.#schedule(0);
    });
    this.#works.add(work);
  }
  #bounded(entry, milliseconds, callback) {
    return new Promise((resolve, reject) => {
      const {signal} = entry.abort;
      const remaining = Math.min(milliseconds, entry.ticket.deadline - this.#clock());
      let timer;
      const finish = (error, value) => { clearTimeout(timer); signal.removeEventListener('abort', abort); error ? reject(error) : resolve(value); };
      const abort = () => finish(new SupervisorError('supervisor_stopped'));
      if (signal.aborted || remaining <= 0) { if (remaining <= 0 && !signal.aborted) this.#deadline(entry); abort(); return; }
      signal.addEventListener('abort', abort, {once: true});
      timer = setTimeout(() => {
        const error = new SupervisorError('supervisor_callback_timeout');
        finish(error); this.#failEntry(entry, entry.stage, error);
      }, remaining);
      Promise.resolve().then(() => {
        if (signal.aborted) throw new SupervisorError('supervisor_stopped');
        return callback({signal, deadline: entry.ticket.deadline});
      }).then(value => finish(null, value), error => finish(error));
    });
  }
  #bindStarted(entry, fact) {
    if (!entry.acceptStarted || fact === null) { entry.startedGate.resolve(); return; }
    requireValue(object(fact) && text(fact.executionId, 128) && fact.executionId.length > 0 && Number.isFinite(Date.parse(fact.startedAt)));
    if (entry.startFact) requireValue(entry.startFact.executionId === fact.executionId);
    else {
      const observation = this.#call('started', entry.ticket, fact);
      entry.startFact = structuredClone(fact); entry.stage = 'running';
      if (observation.stop) this.#stop(entry);
    }
    entry.startedGate.resolve();
  }
  #progress(entry, update) {
    if (entry.stopping || entry.finalized) return Promise.resolve(false);
    let progress, sequence;
    try {
      requireValue(object(update) && PHASES.has(update.phase) && entry.sequence < 4096 && entry.pendingProgress < 128);
      let tool = null;
      if (update.tool !== null) {
        requireValue(object(update.tool) && TOOL_KINDS.has(update.tool.kind) && TOOL_STATES.has(update.tool.status));
        tool = update.tool.kind + ':' + update.tool.status;
      }
      // Copy only the normalized projection, never arbitrary provider fields.
      progress = {summary: 'agent.' + update.phase, tool, source: 'agent'};
      sequence = ++entry.sequence; entry.pendingProgress++;
    } catch (error) {
      this.#failEntry(entry, 'progress', error);
      const rejected = Promise.reject(error); void rejected.catch(() => {}); return rejected;
    }
    const observed = entry.progress.then(async () => {
      await entry.startedGate.promise;
      if (entry.stopping || entry.finalized || !entry.startFact) return false;
      return this.#call('progress', entry.ticket, sequence, progress);
    });
    entry.progress = observed.catch(error => { this.#failEntry(entry, 'progress', error); return false; }).finally(() => { entry.pendingProgress--; });
    return observed;
  }
  async #run(entry) {
    let result, collected = {}, failure = false;
    try {
      const prepared = await this.#bounded(entry, this.#options.prepareMs, context => this.#prepare(entry.ticket, context));
      requireValue(object(prepared) && Object.keys(prepared).every(key => ['cwd', 'prompt', 'onPermission'].includes(key)) &&
        text(prepared.cwd, 8192) && path.isAbsolute(prepared.cwd) && text(prepared.prompt, 256 * 1024) && prepared.prompt.trim() &&
        (prepared.onPermission === undefined || typeof prepared.onPermission === 'function'));
      entry.stage = 'prepared';
      while (!entry.stopping && !this.#closing && !this.#failure) {
        if (this.#call('mayStart', entry.ticket)) break;
        // A pause is an admission fence, not an execution failure or new attempt.
        entry.wake = deferred(); await entry.wake.promise;
      }
      if (entry.stopping || this.#closing || this.#failure) throw new SupervisorError('supervisor_stopped');
      const verifying = entry.ticket.executionType === 'verification';
      const provider = verifying ? this.#verification : this.#providers.get(entry.ticket.providerId);
      requireValue(provider && provider.id === entry.ticket.providerId);
      // No await between final current-ledger check and synchronous start.
      if (!this.#call('mayStart', entry.ticket)) throw new SupervisorError('supervisor_stopped');
      entry.invoked = true; entry.stage = 'starting';
      entry.handle = verifying ? provider.start({ticket: entry.ticket, prepared}) :
        provider.start({...prepared, deadline: entry.ticket.deadline, onProgress: update => this.#progress(entry, update)});
      requireValue(object(entry.handle) && typeof entry.handle.stop === 'function' &&
        typeof entry.handle.started?.then === 'function' && typeof entry.handle.completion?.then === 'function');
      const completion = Promise.resolve(entry.handle.completion);
      // Retain the ORIGINAL completion even when progress/start observations fail.
      entry.completion = completion.catch(error => { this.#failEntry(entry, 'provider-completion', error); return null; });
      Promise.resolve(entry.handle.started).then(fact => this.#bindStarted(entry, fact))
        .catch(error => { this.#failEntry(entry, 'started', error); entry.startedGate.resolve(); });
      if (entry.stopping) this.#stop(entry);
      result = await entry.completion;
      if (!entry.startFact && result?.cleanup?.started) this.#bindStarted(entry, result.cleanup.started);
      entry.acceptStarted = false; entry.startedGate.resolve();
      await entry.progress;
      requireValue(object(result) && (verifying ? result.type === 'verification' && ['passed', 'failed'].includes(result.status) :
        result.providerId === entry.ticket.providerId && ['completed', 'failed', 'cancelled', 'unknown'].includes(result.status)));
      if (!verifying && !entry.stopping && !this.#failure && result.status === 'completed' && result.stopReason === 'end_turn' && result.cleanup?.cleaned === true) {
        entry.stage = 'collecting';
        collected = await this.#bounded(entry, this.#options.collectMs,
          context => this.#collect(entry.ticket, freeze(structuredClone(result)), context));
        requireValue(object(collected) && Object.keys(collected).every(key => ['plan', 'result'].includes(key)));
      }
    } catch (error) {
      failure = true;
      if (error?.code !== 'supervisor_stopped') this.#failEntry(entry, entry.stage, error);
      this.#stop(entry);
      if (entry.completion) result = await entry.completion;
      if (!entry.startFact && result?.cleanup?.started) {
        try { this.#bindStarted(entry, result.cleanup.started); } catch (error) { this.#failEntry(entry, 'started', error); }
      }
      entry.acceptStarted = false; entry.startedGate.resolve(); await entry.progress;
    }
    entry.stage = 'finishing';
    // Only never-invoked start is a positive no-execution proof. A null or
    // rejected provider cleanup is UNKNOWN, not permission to release capacity.
    let cleanup = result?.cleanup;
    if (!entry.invoked) {
      cleanup = {started: null, cleaned: true, scope: 'none-start', reason: 'provider_start_not_invoked'};
    } else if (!object(cleanup) || typeof cleanup.cleaned !== 'boolean' ||
      !(cleanup.started === null && entry.startFact === null ||
        object(cleanup.started) && cleanup.started.executionId === entry.startFact?.executionId)) {
      this.#failEntry(entry, 'provider-cleanup', new SupervisorError('provider_cleanup_unconfirmed'));
      // Malformed/foreign cleanup cannot be presented to the reducer as a valid
      // release fact. The original completion is retained on the owned handle;
      // only our truthful lack of a bound cleanup observation is recorded here.
      cleanup = {started: entry.startFact, cleaned: false, scope: 'unconfirmed', reason: 'cleanup_unconfirmed'};
    }
    const outcome = {status: failure || entry.failure || entry.stopping || this.#failure ? 'failed' : result?.status ?? 'failed',
      stopReason: result?.stopReason ?? null, cleanup, ...collected};
    if (entry.ticket.executionType === 'verification') {
      outcome.type = 'verification'; outcome.receipt = result?.receipt; // Parent-only identity: NEVER structuredClone this capability.
    }
    const worker = this.#call('finish', entry.ticket, outcome);
    entry.clean = cleanup.cleaned === true; entry.finalized = true;
    entry.stage = worker.status === 'unknown' ? 'unknown' : 'terminal';
    if (['failed', 'unknown'].includes(worker.status) && !entry.failure && !entry.stopping && !this.#failure) {
      entry.failure = true;
      this.#notify({code: 'worker_failed', stage: 'result', taskId: entry.ticket.taskId, workerId: entry.ticket.workerId});
    }
  }
  close() {
    if (this.#close) return this.#close;
    this.#closing = true; this.#running = false; clearTimeout(this.#timer); this.#timer = undefined;
    for (const entry of this.#owned.values()) this.#stop(entry);
    this.#close = (async () => {
      await this.#tick;
      // Existing provider obligations, including bootstrap, retain their handles
      // until original completion. No timer fabricates a cleanup on shutdown.
      while (this.#works.size) await Promise.all([...this.#works]);
      try { await this.#cycle(false); } catch { this.#fault('close-reconcile'); }
      this.#closed = true; this.#closing = false;
      return {...this.snapshot(), clean: this.#owned.size === 0};
    })();
    return this.#close;
  }
}
