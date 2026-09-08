import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { AcpClient } from '../agent-acp/client.mjs';
import { PROTOCOL, DEFAULT_LIMITS, BOOT_WAIT_MS, CLEANUP_WAIT_MS, validateOptions } from './protocol.mjs';

const GUARD = fileURLToPath(new URL('./guard.mjs', import.meta.url));
const REASONS = new Set(['owner_stop', 'deadline', 'agent_exit', 'spawn_failed', 'input_limit', 'output_limit',
  'stderr_limit', 'parent_disconnected', 'parent_input_closed', 'parent_stream_error', 'stream_error',
  'bootstrap_timeout', 'invalid_launch', 'invalid_control', 'external_stop']);
function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
export class RuntimeError extends Error {
  constructor(code, completion) { super(code); this.name = 'RuntimeError'; this.code = code; if (completion) this.completion = completion; }
}

/**
 * Launch one trusted ACP Agent under a living inherited-process-group guard.
 * Composition supplies executable/cwd/env AFTER persisting the launch fence.
 * env defaults to {}; no native login, HOME or publisher credential is copied.
 * deadline is absolute epoch milliseconds, shared with the guard (<= 24 hours).
 *
 * Returns {client, started, exited, completion, stop}. AcpClient's cancel only
 * cancels a protocol turn. stop() closes the protocol and returns cleanup facts;
 * its caller must first persist cancellation/failure fencing. These observations
 * do not accept business results, release directories or mutate any Task Store.
 */
export async function launchAcp({ executable, args = [], cwd, env = {}, deadline,
  onUpdate, onPermission, limits = {}, executionContext } = {}) {
  return launchManaged({executable, args, cwd, env, deadline, onUpdate, onPermission, limits, executionContext});
}

/** Trusted composition seam for a different framed protocol. The synchronous
 * client factory owns protocol parsing only; it receives streams and onClose,
 * not a PID, spawn capability or Task authority. The guard contract is unchanged.
 * A returned client must expose synchronous close(). No protocol can mint the
 * original Runtime cleanup observation or expand its inherited-group scope.
 */
export async function launchProtocol({executable, args = [], cwd, env = {}, deadline, createClient, limits = {}, executionContext} = {}) {
  if (typeof createClient !== 'function') throw new RuntimeError('runtime_invalid_client_factory');
  return launchManaged({executable, args, cwd, env, deadline, limits, createClient, executionContext});
}

/** Trusted command execution for independent verification, not an ACP session.
 * stdin is a bounded byte frame; it stays open (use a framed request, not EOF).
 * Captured stdout is untrusted data; exit/cleanup are not a business Decision.
 */
export async function launchCommand({executable, args = [], cwd, env = {}, deadline,
  input = new Uint8Array(), limits = {}, executionContext} = {}) {
  if (!(input instanceof Uint8Array) || input.byteLength > 1024 * 1024) throw new RuntimeError('runtime_invalid_input');
  return launchManaged({executable, args, cwd, env, deadline,
    limits: {inputBytes: 1024 * 1024, outputBytes: 1024 * 1024, ...limits}, executionContext}, Buffer.from(input));
}

// Custodian-only internal entry. Protocol parsing and Task authority remain in
// its original service; only its prepared execution ID is used here.
export async function launchCustodyStreams({executionId, ...options}) {
  if (typeof executionId !== 'string' || !/^[0-9a-f-]{36}$/.test(executionId)) throw new RuntimeError('runtime_invalid_identity');
  return launchManaged(options, null, executionId);
}

async function launchManaged({executable, args, cwd, env, deadline, onUpdate, onPermission, limits, createClient, executionContext}, commandInput = null, forcedId) {
  if (!['darwin', 'linux'].includes(process.platform)) throw new RuntimeError('runtime_platform_unsupported');
  if (onUpdate !== undefined && typeof onUpdate !== 'function' || onPermission !== undefined && typeof onPermission !== 'function') throw new RuntimeError('runtime_invalid_callbacks');
  let options;
  try { options = validateOptions({ executable, args, cwd, env, deadline, limits: { ...DEFAULT_LIMITS, ...limits } }); }
  catch { throw new RuntimeError('runtime_invalid_options'); }
  if (commandInput !== null && commandInput.length > options.limits.inputBytes) throw new RuntimeError('runtime_invalid_input');
  if (executionContext !== undefined) {
    if (typeof executionContext?.launch !== 'function') throw new RuntimeError('runtime_invalid_context');
    try { return await executionContext.launch(options, {createClient, onUpdate, onPermission, input: commandInput}); }
    catch (error) { throw new RuntimeError(error?.code ?? 'runtime_custody_failed', error?.completion); }
  }
  const executionId = forcedId ?? randomUUID(), ready = deferred(), agentExit = deferred(), done = deferred();
  let guard, client, started, actualExit, receipt, guardExit, cleanupUntil, cleanupError = false, stopping = false, completed = false, bootTimer, deadlineTimer, cleanupTimer, groupTimer;
  let reason = 'launch_failed', resolvedReady = false;
  const counts = { inputBytes: 0, outputBytes: 0, stderrBytes: 0 };
  const commandChunks = []; let commandBytes = 0;
  const scope = 'inherited-process-group';
  const closeClient = () => { try { client?.close(); } catch {} }; // A trusted protocol bug cannot prevent owned cleanup.

  function complete(code, signal, observed = true, groupEmpty = false) {
    if (completed) return;
    completed = true; clearTimeout(bootTimer); clearTimeout(deadlineTimer); clearTimeout(cleanupTimer); clearTimeout(groupTimer);
    // The live peer's cleaning message precedes its final group kill. Its own
    // SIGKILL exit alone cannot prove surviving descendants were terminated.
    const cleaned = observed && !!receipt && !cleanupError && signal === 'SIGKILL' && groupEmpty;
    const exit = actualExit ?? { observed: false, code: null, signal: null, at: null };
    const fact = Object.freeze({ executionId, scope, started: started ?? null, agentExit: exit,
      guardExit: guardExit ?? { observed, code, signal, at: new Date().toISOString() }, cleaned,
      reason: cleaned ? receipt.reason : 'cleanup_unconfirmed', ...counts });
    closeClient(); agentExit.resolve(exit); ready.resolve(undefined); done.resolve(fact);
    if (!observed && guard?.exitCode === null && guard?.signalCode === null) {
      // Ask the CURRENT child handle to run its own cleanup; never SIGKILL the
      // leader alone or signal a stale/replayed group number from the parent.
      guard.kill('SIGTERM');
    }
  }
  function startCleanupWait() {
    if (cleanupUntil !== undefined) return;
    cleanupUntil = performance.now() + CLEANUP_WAIT_MS;
    cleanupTimer = setTimeout(() => complete(guardExit?.code ?? null, guardExit?.signal ?? null, !!guardExit), CLEANUP_WAIT_MS);
  }
  function observeGroupEmpty() {
    if (completed) return;
    const finish = empty => complete(guardExit.code, guardExit.signal, true, empty);
    if (performance.now() >= cleanupUntil) { finish(false); return; }
    // Read-only veto for THIS original child's group after its observed exit.
    // Never stop an exited/replayed PID. Reuse/presence/EPERM is not evidence of
    // emptiness; only ESRCH can close this already identity-bound observation.
    try { process.kill(-guard.pid, 0); }
    catch (error) { finish(error?.code === 'ESRCH'); return; }
    groupTimer = setTimeout(observeGroupEmpty, Math.min(25, Math.max(1, cleanupUntil - performance.now())));
  }
  function observeGuardExit(code, signal) {
    if (completed) return;
    guardExit = Object.freeze({observed: true, code, signal, at: new Date().toISOString()});
    if (!receipt || cleanupError || signal !== 'SIGKILL') { complete(code, signal); return; }
    startCleanupWait(); observeGroupEmpty();
  }
  function control(message) {
    if (!guard?.connected) return false;
    try {
      guard.send({ protocol: PROTOCOL, executionId, ...message }, error => { if (error) requestStop('control_failed'); });
      return true;
    } catch { return false; }
  }
  function requestStop(cause = 'owner_stop') {
    if (completed || stopping) return done.promise;
    stopping = true; reason = cause; clearTimeout(bootTimer);
    closeClient();
    if (!control({ type: 'stop' }) && guard?.exitCode === null && guard?.signalCode === null) guard.kill('SIGTERM');
    startCleanupWait();
    return done.promise;
  }
  try {
    guard = spawn(process.execPath, [GUARD], { cwd: options.cwd, env: {}, detached: true,
      stdio: ['pipe', 'pipe', 'ignore', 'ipc'] });
  } catch { throw new RuntimeError('runtime_guard_spawn_failed'); }
  // Runtime owns stream errors even after AcpClient detaches on close().
  guard.stdin.on('error', () => requestStop('stream_error'));
  guard.stdout.on('error', () => requestStop('stream_error'));
  guard.on('error', () => { reason = 'guard_spawn_failed'; requestStop(reason); });
  guard.on('exit', observeGuardExit);
  guard.on('disconnect', () => { if (!stopping && !completed) requestStop('guard_disconnected'); });
  guard.on('message', message => {
    if (completed) return;
    if (message?.protocol !== PROTOCOL) { requestStop('invalid_guard_message'); return; }
    if (message.type === 'ready' && !resolvedReady) {
      if (message.guardPid !== guard.pid || message.executionId !== null) { requestStop('invalid_guard_message'); return; }
      resolvedReady = true;
      if (!stopping && Date.now() < options.deadline) control({ type: 'launch', options });
      else requestStop('deadline');
      return;
    }
    if (message.executionId !== executionId) { requestStop('invalid_guard_message'); return; }
    for (const key of Object.keys(counts)) if (Number.isSafeInteger(message[key]) && message[key] >= counts[key]) counts[key] = message[key];
    if (message.type === 'started') {
      if (started || !Number.isSafeInteger(message.agentPid) || message.agentPid < 1 || !Number.isFinite(Date.parse(message.at))) { requestStop('invalid_guard_message'); return; }
      started = Object.freeze({ executionId, guardPid: guard.pid, agentPid: message.agentPid, startedAt: message.at });
      clearTimeout(bootTimer);
      if (stopping) return;
      ready.resolve(started);
    } else if (message.type === 'agent_exit') {
      if (actualExit) { requestStop('invalid_guard_message'); return; }
      actualExit = Object.freeze({ observed: true, code: message.code, signal: message.signal, at: message.at });
      agentExit.resolve(actualExit);
    } else if (message.type === 'cleaning') {
      if (!REASONS.has(message.reason)) { requestStop('invalid_guard_message'); return; }
      receipt = { reason: message.reason, at: message.at };
      // The guard is already enforcing its local safety bound. This fact is
      // for the caller to persist, not a second Task authority in this module.
      if (!stopping) { stopping = true; reason = message.reason; closeClient(); }
      startCleanupWait();
    } else if (message.type === 'cleanup_error') cleanupError = true;
    else if (message.type === 'spawn_failed') { reason = 'agent_spawn_failed'; requestStop(reason); }
    else requestStop('invalid_guard_message');
  });
  if (commandInput === null) {
    try {
      const connection = {readable: guard.stdout, writable: guard.stdin, onClose: () => { requestStop('protocol_closed'); }};
      client = createClient ? createClient(connection) : new AcpClient({...connection, onUpdate, onPermission});
      if (!client || typeof client.close !== 'function' || typeof client.then === 'function') {
        if (typeof client?.then === 'function') Promise.resolve(client).catch(() => {});
        client = undefined; throw new Error('invalid client');
      }
    } catch {
      const completion = await requestStop('client_factory_failed');
      throw new RuntimeError('runtime_client_factory_failed', completion);
    }
  } else {
    guard.stdout.on('data', chunk => {
      commandBytes += chunk.length;
      if (commandBytes > Math.min(options.limits.outputBytes, 1024 * 1024)) { requestStop('output_limit'); return; }
      commandChunks.push(Buffer.from(chunk));
    });
  }
  bootTimer = setTimeout(() => requestStop('launch_timeout'), Math.min(BOOT_WAIT_MS, options.deadline - Date.now()));
  deadlineTimer = setTimeout(() => requestStop('deadline'), Math.max(0, options.deadline - Date.now()));
  const observedStart = await ready.promise;
  if (!observedStart) {
    const completion = await done.promise;
    throw new RuntimeError('runtime_launch_failed', completion);
  }
  if (commandInput !== null) {
    if (!stopping && commandInput.length) guard.stdin.write(commandInput);
    const completion = done.promise.then(cleanup => Object.freeze({cleanup,
      stdout: Buffer.concat(commandChunks),
      outputComplete: commandBytes === cleanup.outputBytes && commandBytes <= Math.min(options.limits.outputBytes, 1024 * 1024),
    }));
    return Object.freeze({started: observedStart, exited: agentExit.promise, completion,
      stop: async () => { await requestStop(); return completion; }});
  }
  return Object.freeze({ client, started: observedStart, exited: agentExit.promise, completion: done.promise,
    stop: () => requestStop() });
}
