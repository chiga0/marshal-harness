import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {randomBytes, randomUUID} from 'node:crypto';
import {Store, CUSTODY_FORMAT, INTERACTION_FORMAT, REPAIR_FORMAT} from '../task-store/store.mjs';
import {TaskCleanup} from '../task-application/cleanup.mjs';
import {createExecutionCustody} from '../agent-runtime/custody.mjs';
import {CUSTODY_PROFILE, verifyObservation} from '../agent-runtime/custody-contract.mjs';
import {setTimeout as wait} from 'node:timers/promises';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication} from '../task-application/application.mjs';
import {TaskSupervisor} from '../task-supervisor/controller.mjs';
import {createTaskApiHandler} from '../task-api/http-handler.mjs';
import {PROFILE, TaskApiError, validate} from '../task-api/contract.mjs';

const format = (custody, questions, repair) => Buffer.from(JSON.stringify({profile: PROFILE, layout: repair ? 4 : questions ? 3 : custody ? 2 : 1}) + '\n');
const NOFOLLOW = fs.constants.O_NOFOLLOW;
const same = (a, b) => a.dev === b.dev && a.ino === b.ino;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const requireValue = (value, code = 'service_invalid_configuration') => { if (!value) throw new TaskServiceError(code); };
export class TaskServiceError extends Error {
  constructor(code) { super(code); this.name = 'TaskServiceError'; this.code = code; }
}
function directory(stat) {
  requireValue(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o700, 'service_root_unavailable');
}
function regular(stat) {
  requireValue(stat.isFile() && stat.nlink === 1 && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o600, 'service_root_unavailable');
}
class ServiceRoot {
  fds = []; directories = new Map();
  constructor(root, mode, custody, questions, repair) {
    this.root = root; this.parent = path.dirname(root);
    this.format = format(custody, questions, repair);
    try {
      requireValue(fs.realpathSync(this.parent) === this.parent, 'service_root_unavailable');
      this.hold(this.parent); // Explicit private parent, no recursive mkdir/adoption.
      if (mode === 'create') fs.mkdirSync(root, {mode: 0o700});
      this.hold(root);
      if (mode === 'create') this.writeNew(path.join(root, 'profile.json'), this.format);
      const fd = fs.openSync(path.join(root, 'profile.json'), fs.constants.O_RDONLY | NOFOLLOW);
      this.fds.push(fd); this.formatFd = fd; this.check();
      if (mode === 'open') {
        requireValue(fs.readdirSync(root).sort().join(',') === (custody ? 'artifacts,connections,custody,executions,profile.json,store' : 'artifacts,connections,executions,profile.json,store'), 'service_root_unavailable');
      }
      for (const name of ['executions', 'connections', ...(custody ? ['custody'] : [])]) {
        const target = path.join(root, name);
        if (mode === 'create') fs.mkdirSync(target, {mode: 0o700});
        this.hold(target);
      }
      this.sync();
    } catch (error) { this.close(); throw error; }
  }
  hold(name) {
    const fd = fs.openSync(name, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | NOFOLLOW);
    this.fds.push(fd); this.directories.set(name, fd); this.check(); return fd;
  }
  check() {
    for (const [name, fd] of this.directories) {
      const stat = fs.fstatSync(fd); directory(stat);
      requireValue(same(stat, fs.lstatSync(name)) && fs.realpathSync(name) === name, 'service_root_unavailable');
    }
    if (this.formatFd !== undefined) {
      const stat = fs.fstatSync(this.formatFd); regular(stat);
      const bytes = Buffer.alloc(this.format.length);
      requireValue(stat.size === bytes.length && same(stat, fs.lstatSync(path.join(this.root, 'profile.json'))) &&
        fs.readSync(this.formatFd, bytes, 0, bytes.length, 0) === bytes.length && bytes.equals(this.format), 'service_root_unavailable');
    }
  }
  writeNew(name, bytes) {
    this.check();
    const fd = fs.openSync(name, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | NOFOLLOW, 0o600);
    try { regular(fs.fstatSync(fd)); fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); }
    finally { fs.closeSync(fd); }
    this.sync();
  }
  sync() { this.check(); for (const fd of [...this.fds].reverse()) fs.fsyncSync(fd); this.check(); }
  close() { for (const fd of this.fds.splice(0).reverse()) fs.closeSync(fd); this.directories.clear(); }
}

/** Composition only: no Task reducer, second ledger, model defaults or publication. */
export async function startTaskService({root, mode, providers, prepare, collect, release, businessFactory, verification, clarification, custody, runtimeQuestions, repair, auditDisclosure, dispose = () => {},
  providerFacts, applicationOptions = {}, port = 0, leaseMs = 60000, renewIntervalMs = 10000,
  requestTimeoutMs = 10000, supervisorOptions = {}, onDiagnostic = () => {}} = {}) {
  requireValue(typeof root === 'string' && path.isAbsolute(root) && path.normalize(root) === root && root !== path.parse(root).root &&
    ['create', 'open'].includes(mode) && providers instanceof Map && providers.size > 0 && providers.size <= 32 &&
    (businessFactory === undefined ? typeof prepare === 'function' && typeof collect === 'function' && (release === undefined || typeof release === 'function') :
      typeof businessFactory === 'function' && prepare === undefined && collect === undefined && release === undefined) &&
    (verification === undefined || verification !== null && typeof verification === 'object') &&
    (clarification === undefined || clarification !== null && typeof clarification === 'object') &&
    (custody === undefined || object(custody) && Object.keys(custody).join(',') === 'profile' && custody.profile === CUSTODY_PROFILE) &&
    typeof dispose === 'function' && typeof onDiagnostic === 'function' &&
    object(applicationOptions) && Object.keys(applicationOptions).every(key => ['defaultLimits', 'execution'].includes(key)) &&
    (applicationOptions.execution === undefined || object(applicationOptions.execution)) &&
    object(supervisorOptions) && Object.keys(supervisorOptions).every(key => ['intervalMs', 'prepareMs', 'collectMs', 'pageSize', 'maxPagesPerTick'].includes(key)) &&
    Number.isSafeInteger(port) && port >= 0 && port <= 65535 && Number.isSafeInteger(leaseMs) && leaseMs >= 200 && leaseMs <= 300000 &&
    Number.isSafeInteger(renewIntervalMs) && renewIntervalMs >= 10 && renewIntervalMs * 2 < leaseMs &&
    Number.isSafeInteger(requestTimeoutMs) && requestTimeoutMs >= 10 && requestTimeoutMs <= 30000);
  const available = new Map(providers);
  for (const [id, provider] of available) requireValue(validate(id, 'Id') && provider?.id === id && typeof provider.start === 'function');
  const facts = providerFacts ?? [...available.keys()].map(id => ({id, displayName: id, availability: 'unknown', coreCapabilities: [], enhancedCapabilities: []}));
  requireValue(Array.isArray(facts) && facts.length === available.size && facts.every(fact => validate(fact, 'Provider') && available.has(fact.id)) && new Set(facts.map(fact => fact.id)).size === facts.length);
  const frozenFacts = structuredClone(facts).sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  requireValue(runtimeQuestions === undefined || custody !== undefined && runtimeQuestions?.profile === 'task-runtime-question/v1');
  requireValue(repair === undefined || custody !== undefined && repair?.profile === 'task-local-repair/v1' && typeof businessFactory === 'function');
  const execution = {maxWorkers: 2, providerIds: [...available.keys()], defaultProvider: available.keys().next().value, ...applicationOptions.execution,
    questionProviderIds: [...available].filter(([, provider]) => provider.runtimeQuestions === 'task-runtime-question/v1').map(([id]) => id)};
  requireValue(Array.isArray(execution.providerIds) && execution.providerIds.length === available.size && execution.providerIds.every(id => available.has(id)));
  let files, store, depot, application, supervisor, business, custodian, server, renewal, closing, address, connectionFile;
  let state = 'starting', failure = null, shutdownClean = null, renewing = false;
  const instanceId = 'service-' + randomUUID(), token = randomBytes(32).toString('hex');
  const diagnostic = code => { try { Promise.resolve(onDiagnostic({code})).catch(() => {}); } catch {} };
  const snapshot = () => ({profile: PROFILE, state, failure, generation: application?.owner.generation.toString() ?? null, shutdownClean});
  const fail = code => {
    failure ??= code; state = 'failed'; diagnostic(code);
    if (supervisor) void shutdown().catch(() => {});
  };
  function renew() {
    if (renewing || !application || state === 'closed' || failure) return;
    renewing = true;
    try {
      files.check();
      // The returned expiresAt is part of owner identity; update both in one turn.
      application.owner = store.renewOwner(application.owner, Date.now() + leaseMs);
      if (supervisor.snapshot().failure) fail('service_supervisor_failed');
    } catch { fail('service_owner_unavailable'); }
    finally { renewing = false; }
  }
  function observation() {
    files.check();
    let queuedTasks = 0, blockedTasks = 0, activeWorkers = 0, recovery = false;
    // Bound complete scans. If larger than the admitted observation budget,
    // return unavailable rather than publish fabricated/truncated counters.
    for (const kind of ['task', 'commands']) {
      let cursor = '', complete = false;
      for (let page = 0; page < 100; page++) {
        const rows = application.transaction(false, tx => kind === 'task' ? tx.projections('task', cursor, 25) : tx.commands(cursor, 25));
        for (const row of rows) {
          if (kind === 'task') {
            const task = JSON.parse(row.bytes.toString('utf8')).task;
            if (['draft', 'queued'].includes(task.status)) queuedTasks++;
            if (['intervention', 'awaiting-answer', 'awaiting-approval', 'awaiting-confirmation', 'paused'].includes(task.status)) blockedTasks++;
            if (task.status === 'intervention') recovery = true;
          } else if (row.generation !== application.owner.generation && row.status !== 'observed' && ['start', 'verify'].includes(row.kind)) {
            // UNKNOWN may already have external effects: never exclude it on
            // the strength of a Task fence. PENDING has not been reserved; once
            // fenced and proven unrelated to any Worker it cannot block ready
            // forever. This is observation only, not refund/replay/settlement.
            const fencedWithoutExecution = row.status === 'pending' && application.transaction(false, tx => {
              const current = tx.command(row.id);
              if (!current || current.status !== 'pending' || current.revision !== row.revision ||
                  current.generation !== row.generation || current.attemptId !== '') return false;
              const task = application.get(tx, current.taskId);
              return ['cancelling', 'cancelled', 'failed', 'completed'].includes(task.task.status) &&
                !application.execution.workers(tx, task).some(({record}) => record.ticket.commandId === current.id);
            });
            if (!fencedWithoutExecution) recovery = true;
          }
        }
        if (rows.length < 25) { complete = true; break; }
        cursor = rows.at(-1).id;
      }
      if (!complete) throw new TaskApiError('application_unavailable');
    }
    activeWorkers = application.transaction(false, tx => application.execution.capacity(tx).value.active.length);
    const runtime = supervisor.snapshot();
    const ready = state === 'running' && !failure && !runtime.failure && runtime.state === 'running' && !recovery;
    return {ready, status: state === 'stopping' || state === 'closed' ? 'stopping' : recovery || failure || runtime.failure ? 'intervention' : activeWorkers ? 'busy' : 'ready',
      activeWorkers, maxWorkers: execution.maxWorkers, queuedTasks, blockedTasks, observedAt: new Date().toISOString()};
  }
  async function dispatch(request, context) {
    if (request.operation === 'health.get') return {status: 'ok', profile: PROFILE};
    if (request.operation === 'ready.get') {
      if (!observation().ready) throw new TaskApiError('not_ready');
      return {ready: true, profile: PROFILE};
    }
    if (request.operation === 'supervisor.get') { const {ready, ...result} = observation(); return result; }
    if (request.operation === 'provider.list') {
      const {cursor = '', limit = 50} = request.page ?? {};
      const selected = frozenFacts.filter(fact => fact.id > (cursor ?? '')).slice(0, limit);
      return {items: structuredClone(selected), nextCursor: selected.length === limit ? selected.at(-1).id : null};
    }
    // HTTP has authenticated/validated the request. Receipt lookup must still
    // use the current owner and happen BEFORE readiness/CAS: recovery cannot
    // turn a committed response into a new admission or hide a key conflict.
    if (context?.principal !== 'local-operator') throw new TaskApiError('forbidden');
    if (context.signal?.aborted) throw new TaskApiError('request_timeout');
    if (request.key !== undefined) {
      const previous = application.replay(request);
      if (previous) return previous;
    }
    if (state !== 'running' || failure) throw new TaskApiError('not_ready');
    // Recovery diagnostics and cancellation remain available, but no new work
    // may be admitted through HTTP while prior effects are unresolved.
    if (['task.create', 'task.approve', 'task.resume', 'input.create', 'task.answer', 'task.repair'].includes(request.operation) && !observation().ready)
      throw new TaskApiError('not_ready');
    return application.dispatch(request, context);
  }
  function shutdown() {
    if (closing) return closing;
    if (!failure) state = 'stopping';
    closing = (async () => {
      let drain = Promise.resolve(), drainTimer;
      if (server?.listening) {
        drain = new Promise(resolve => server.close(() => resolve()));
        server.closeIdleConnections();
        drainTimer = setTimeout(() => server.closeAllConnections(), requestTimeoutMs + 100);
      }
      try {
        // Keep owner renewal alive until the original owned completions commit.
        const result = supervisor ? await supervisor.close() : {clean: true};
        shutdownClean = result.clean === true;
        if (!shutdownClean) { failure ??= 'service_cleanup_unconfirmed'; diagnostic(failure); }
      } catch { shutdownClean = false; failure ??= 'service_shutdown_unavailable'; }
      finally {
        clearInterval(renewal);
        await drain; clearTimeout(drainTimer);
        try { if (business && typeof business.close === 'function') await business.close(); } catch { failure ??= 'service_dispose_failed'; }
        try { if (supervisor) await dispose(); } catch { failure ??= 'service_dispose_failed'; }
        try { await custodian?.close(); } catch { failure ??= 'service_custody_close_failed'; }
        for (const component of [depot, store, files]) {
          try { component?.close(); } catch { failure ??= 'service_close_failed'; }
        }
        state = 'closed';
      }
      return snapshot();
    })();
    return closing;
  }
  try {
    files = new ServiceRoot(root, mode, !!custody, !!runtimeQuestions, !!repair);
    const storeOptions = repair ? {format: REPAIR_FORMAT} : runtimeQuestions ? {format: INTERACTION_FORMAT} : custody ? {format: CUSTODY_FORMAT} : {};
    store = mode === 'create' ? Store.create(path.join(root, 'store'), storeOptions) : Store.openExisting(path.join(root, 'store'), storeOptions);
    depot = mode === 'create' ? ArtifactDepot.create(path.join(root, 'artifacts')) : ArtifactDepot.openExisting(path.join(root, 'artifacts'));
    files.sync();
    if (custody) {
      custodian = createExecutionCustody({root: path.join(root, 'custody')});
      // Do not claim a new generation while an old queued permit might still
      // spawn. A signed CLOSED observer (including unknown cleanup) is required
      // for every original live binding. Physical SQLite exclusion freezes this
      // snapshot; no wall clock, PID existence or file absence substitutes it.
      const pending = []; let after = '', complete = false;
      for (let page = 0; page < 100; page++) {
        const value = TaskCleanup.inspectBeforeClaim(store, after); pending.push(...value.items);
        if (value.nextCursor === null) { complete = true; break; } after = value.nextCursor;
      }
      requireValue(complete, 'service_custody_scan_limit');
      const until = Date.now() + 15000;
      while (pending.length) {
        for (let i = pending.length - 1; i >= 0; i--) {
          const observation = custodian.read(pending[i]);
          if (observation && verifyObservation(pending[i], observation)) pending.splice(i, 1);
        }
        if (!pending.length) break;
        requireValue(Date.now() < until, 'service_custody_unresolved');
        await wait(25);
      }
    }
    const owner = store.claimOwner(store.info().generation, instanceId, Date.now() + leaseMs);
    application = new TaskApplication({...applicationOptions, execution, store, owner, depot, verification, clarification, runtimeQuestions, repair, auditDisclosure});
    if (custody) {
      let after = '', complete = false;
      for (let page = 0; page < 100; page++) {
        const value = application.execution.pendingCleanup(after);
        for (const entry of value.items) {
          const observation = custodian.read(entry.descriptor);
          if (!observation) continue;
          try {
            const result = application.execution.reconcileCleanup(entry.workerId, observation);
            custodian.acknowledge(entry.descriptor, result.observationDigest);
          } catch (error) { if (error.code !== 'recovery_required') throw error; diagnostic('service_custody_unresolved'); }
        }
        if (value.nextCursor === null) { complete = true; break; } after = value.nextCursor;
      }
      requireValue(complete, 'service_custody_scan_limit');
    }
    if (repair && mode === 'open') {
      let after = '';
      for (let page = 0; ; page++) {
        requireValue(page < 100, 'service_custody_scan_limit');
        const tasks = application.execution.scan(after);
        for (const taskId of tasks.items) application.execution.reconcile(taskId);
        if (tasks.nextCursor === null) break; after = tasks.nextCursor;
      }
    }
    const context = Object.freeze({depot, executionParent: path.join(root, 'executions'),
      approvedLayout: ticket => {
        requireValue(typeof application.execution.approvedLayout === 'function', 'service_capability_unavailable');
        return application.execution.approvedLayout(ticket);
      },
      observeExecution: ticket => {
        requireValue(typeof application.execution.observeExecution === 'function', 'service_capability_unavailable');
        return application.execution.observeExecution(ticket);
      },
    });
    if (businessFactory) {
      business = businessFactory(context);
      requireValue(object(business) && typeof business.then !== 'function' &&
        ['prepare', 'collect', 'release', 'close'].every(key => typeof business[key] === 'function'), 'service_invalid_business');
      requireValue(!repair || business.repairProfile === 'task-local-repair/v1', 'service_capability_unavailable');
      prepare = (ticket, wait) => business.prepare(ticket, wait);
      collect = (ticket, result, wait) => business.collect(ticket, result, wait);
      release = ticket => business.release(ticket);
    }
    supervisor = new TaskSupervisor({...supervisorOptions, execution: application.execution, providers: available,
      verification, release, custody: custodian ?? null,
      prepare: (ticket, wait) => prepare(ticket, {...wait, ...context}),
      collect: (ticket, result, wait) => collect(ticket, result, {...wait, ...context}),
      onError: report => { diagnostic(report.code); if (report.code === 'supervisor_failed') fail('service_supervisor_failed'); }});
    let handler;
    server = http.createServer((request, response) => {
      if (!handler) { response.writeHead(503, {'Connection': 'close'}); response.end(); return; }
      void handler(request, response);
    });
    server.requestTimeout = requestTimeoutMs + 1000; server.headersTimeout = requestTimeoutMs;
    server.keepAliveTimeout = 1000; server.maxRequestsPerSocket = 100;
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(port, '127.0.0.1', () => { server.off('error', reject); resolve(); });
    });
    address = `http://127.0.0.1:${server.address().port}`;
    handler = createTaskApiHandler({application: dispatch, token, expectedHost: address.slice('http://'.length), requestTimeoutMs});
    server.on('error', () => fail('service_http_unavailable'));
    renewal = setInterval(renew, renewIntervalMs);
    supervisor.start(); state = 'running';
    await supervisor.tick(); // Inspect prior generation before publishing ready.
    if (failure) throw new TaskServiceError('service_start_unavailable');
    connectionFile = path.join(root, 'connections', instanceId + '.json');
    files.writeNew(connectionFile, Buffer.from(JSON.stringify({profile: PROFILE, url: address, token}) + '\n'));
    return Object.freeze({address, connectionFile, snapshot, shutdown});
  } catch (error) {
    await shutdown();
    if (error instanceof TaskServiceError) throw error;
    throw new TaskServiceError('service_start_unavailable');
  }
}
