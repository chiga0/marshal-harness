import fs from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { Store, FORMAT, Fault, fail, digest, artifactHash, nativeEnvironment, id, text, closedObject, privateRoot, readPrivate, sameSecret } from './store.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const TERMINAL = new Set(['completed', 'failed', 'cancelled', 'intervention']);
const LIVE = new Set(['approved', 'running', 'verifying', 'cancelling']);
const jsonCopy = value => structuredClone(value);

export function publicTask(task) {
  const { id, status, revision, intent, plan, previewDigest, createdAt, approvedAt, deadline, finishedAt, reason, attempts } = task;
  return { id, status, revision, intent, plan, previewDigest, createdAt, approvedAt, deadline, finishedAt, reason, attempts, usage: null,
    workers: task.workers.map(worker => {
      const { id, nodeId, role, status, pid, guardPid, startedAt, agentExitedAt, finishedAt, stdoutBytes, stderrBytes, lastObservedAt, exitCode, reason, cleaned } = worker;
      return { id, nodeId, role, status, pid, guardPid, startedAt, agentExitedAt, finishedAt, stdoutBytes, stderrBytes, lastObservedAt, exitCode, reason, cleaned, usage: null };
    }) };
}

function key(value) { if (!id(value)) fail('invalid-idempotency-key', 400); return value; }
function controlBody(value, approval = false) {
  if (!closedObject(value, approval ? ['expectedRevision', 'previewDigest'] : ['expectedRevision']) || !Number.isSafeInteger(value.expectedRevision) || value.expectedRevision < 1 || approval && !/^sha256:[0-9a-f]{64}$/.test(value.previewDigest)) fail('invalid-request', 400);
}
function checkedPlan(plan, intent) {
  if (!closedObject(plan, ['version', 'intent', 'nodes', 'timeoutMs']) || !text(plan.version, 128) || plan.intent !== intent || plan.timeoutMs !== 300000 || !Array.isArray(plan.nodes) || plan.nodes.length !== 2) fail('invalid-plan', 400);
  const ids = new Set(), files = new Set();
  for (const node of plan.nodes) {
    if (!closedObject(node, ['id', 'role', 'file', 'prompt']) || !id(node.id) || ids.has(node.id) || !text(node.role, 64) || !/^[a-z][a-z0-9_-]*\.mjs$/.test(node.file) || files.has(node.file) || !text(node.prompt, 65536)) fail('invalid-plan', 400);
    ids.add(node.id); files.add(node.file);
  }
  return jsonCopy(plan);
}
function checkedCommand(command) {
  if (!closedObject(command, ['command', 'args', 'env', 'prompt']) || !path.isAbsolute(command.command ?? '') || !Array.isArray(command.args) || command.args.length > 64 || command.args.some(v => typeof v !== 'string' || Buffer.byteLength(v) > 8192 || v.includes('\0')) || !closedObject(command.env, Object.keys(command.env ?? {})) || Object.entries(command.env).some(([k, v]) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(k) || typeof v !== 'string' || v.includes('\0')) || Buffer.byteLength(JSON.stringify(command.env)) > 65536 || Object.hasOwn(command, 'prompt') && !text(command.prompt, 65536)) fail('invalid-provider-command', 503);
  return command;
}

export class Supervisor {
  constructor(store, config, ports) {
    this.store = store; this.config = config; this.ports = ports;
    this.queue = Promise.resolve(); this.guards = new Map(); this.verifications = new Map(); this.timers = new Map(); this.closing = false; this.faulted = false;
  }
  static async open(directory, config, ports) {
    const store = await Store.open(directory);
    if (store.state.tasks.some(task => LIVE.has(task.status) || task.workers.some(worker => !worker.cleaned))) fail('supervisor-recovery-needs-intervention', 503);
    if (store.fingerprint === null) await store.save(jsonCopy(store.state));
    return new Supervisor(store, config, ports);
  }
  enqueue(fn) {
    const result = this.queue.then(() => { if (this.faulted) fail('storage-needs-intervention', 503); return fn(); });
    this.queue = result.catch(() => {}); return result;
  }
  async change(fn) {
    const next = jsonCopy(this.store.state); const result = fn(next);
    try { await this.store.save(next); } catch (err) { this.faulted = true; for (const entry of this.guards.values()) this.cleanGuard(entry); throw err; }
    return result;
  }
  task(taskId) { const task = this.store.state.tasks.find(t => t.id === taskId); if (!task) fail('task-not-found', 404); return task; }
  async command(operation, taskId, body, requestKey) {
    return this.enqueue(async () => {
      if (operation === 'list') return { tasks: this.store.state.tasks.map(publicTask) };
      if (operation === 'get') return publicTask(this.task(taskId));
      if (operation === 'workers') return { taskId, workers: publicTask(this.task(taskId)).workers };
      if (operation === 'audit') {
        const task = this.task(taskId);
        return { ...publicTask(task), taskId, elapsedMs: task.approvedAt ? Date.parse(task.finishedAt ?? new Date().toISOString()) - Date.parse(task.approvedAt) : null,
          reworkCount: 0, retryCount: 0, acceptance: task.delivery ? { passed: true, checks: task.delivery.checks } : null };
      }
      if (operation === 'delivery') {
        const task = this.task(taskId); if (task.status !== 'completed' || !task.delivery) fail('delivery-not-ready');
        for (const file of task.delivery.files) if (artifactHash(file.content) !== file.sha256) fail('delivery-corrupt', 503);
        return jsonCopy(task.delivery);
      }
      if (this.closing) fail('supervisor-closing', 503);
      key(requestKey);
      if (operation === 'create') {
        if (!closedObject(body, ['intent']) || !text(body.intent, 4096)) fail('invalid-request', 400);
        const requestDigest = digest(body), receipt = Object.hasOwn(this.store.state.createKeys, requestKey) ? this.store.state.createKeys[requestKey] : undefined;
        if (receipt) { if (receipt.digest !== requestDigest) fail('idempotency-conflict'); return publicTask(this.task(receipt.taskId)); }
        if (this.store.state.tasks.length >= 100) fail('task-capacity-exceeded');
        let plan; try { plan = checkedPlan(this.ports.plan(body.intent), body.intent); } catch { fail('unsupported-task-intent', 400); }
        const task = { id: 'task-' + crypto.randomUUID(), status: 'awaiting-approval', revision: 1, intent: body.intent, plan, previewDigest: digest(plan), createdAt: new Date().toISOString(), attempts: 0, controls: [], workers: plan.nodes.map(node => ({ id: 'worker-' + crypto.randomUUID(), nodeId: node.id, role: node.role, status: 'planned', pid: null, guardPid: null, stdoutBytes: 0, stderrBytes: 0, cleaned: true })) };
        await this.change(state => { state.tasks.push(task); state.createKeys[requestKey] = { digest: requestDigest, taskId: task.id }; });
        return publicTask(task);
      }
      if (!['approve', 'cancel'].includes(operation)) fail('not-found', 404);
      controlBody(body, operation === 'approve');
      let task = this.task(taskId);
      const requestDigest = digest(body), receipt = task.controls.find(r => r.operation === operation && r.key === requestKey);
      if (receipt) { if (receipt.digest !== requestDigest) fail('idempotency-conflict'); return publicTask(task); }
      if (body.expectedRevision !== task.revision) fail('revision-conflict');
      if (operation === 'approve') {
        if (task.status !== 'awaiting-approval' || body.previewDigest !== task.previewDigest) fail('preview-conflict');
        if (this.guards.size || this.verifications.size || this.store.state.tasks.some(t => LIVE.has(t.status) || t.workers.some(w => !w.cleaned))) fail('capacity-busy');
        const now = Date.now();
        await this.change(state => {
          const t = state.tasks.find(t => t.id === taskId);
          t.status = 'approved'; t.revision++; t.attempts = 1; t.approvedAt = new Date(now).toISOString(); t.deadline = new Date(now + t.plan.timeoutMs).toISOString();
          t.controls.push({ operation, key: requestKey, digest: requestDigest }); t.workers.forEach(w => { w.status = 'queued'; });
        });
        this.armDeadline(taskId);
        setImmediate(() => { this.launch(taskId).catch(() => this.internalFailure(taskId, 'launch-failed')); });
      } else {
        if (TERMINAL.has(task.status)) fail('task-already-terminal');
        if (task.cancelRequested || task.status === 'cancelling') fail('cancel-already-requested');
        await this.change(state => { const t = state.tasks.find(t => t.id === taskId); t.cancelRequested = true; t.status = 'cancelling'; t.reason = 'user-cancelled'; t.revision++; t.controls.push({ operation, key: requestKey, digest: requestDigest }); });
        this.stopExecutions(taskId);
        setImmediate(() => { this.settle(taskId).catch(() => {}); });
      }
      task = this.task(taskId); return publicTask(task);
    });
  }
  armDeadline(taskId) {
    const task = this.task(taskId);
    const timer = setTimeout(() => this.internalFailure(taskId, 'deadline-exceeded'), Math.max(0, Date.parse(task.deadline) - Date.now()));
    this.timers.set(taskId, timer);
  }
  async launch(taskId) {
    for (const node of this.task(taskId).plan.nodes) await this.enqueue(async () => {
      const task = this.task(taskId);
      if (task.cancelRequested || TERMINAL.has(task.status) || this.closing) return;
      if (Date.now() >= Date.parse(task.deadline)) { await this.fence(taskId, 'deadline-exceeded'); return; }
      const worker = task.workers.find(w => w.nodeId === node.id);
      if (worker.status !== 'queued') return;
      const output = path.join(this.store.directory, 'outputs');
      await fs.mkdir(output, { mode: 0o700 }).catch(err => { if (err.code !== 'EEXIST') throw err; });
      const parent = await fs.lstat(output); if (!parent.isDirectory() || parent.isSymbolicLink() || parent.uid !== process.getuid() || (parent.mode & 0o777) !== 0o700) fail('unsafe-output-directory', 503);
      const cwd = path.join(output, worker.id); await fs.mkdir(cwd, { mode: 0o700 });
      const command = checkedCommand(this.ports.makeCommand(this.config, node, node.prompt));
      await this.change(state => { const t = state.tasks.find(t => t.id === taskId), w = t.workers.find(w => w.id === worker.id); t.status = 'running'; w.status = 'launching'; w.cleaned = false; });
      const guard = spawn(process.execPath, [path.join(HERE, 'guard.mjs')], { detached: true, env: nativeEnvironment(), stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
      let resolveExit; const exited = new Promise(resolve => { resolveExit = resolve; });
      const entry = { guard, taskId, workerId: worker.id, node, command, cwd, exited, resolveExit, cleaning: false, cleanupNonce: null, cleanupAck: false, terminal: null };
      this.guards.set(worker.id, entry);
      guard.on('message', message => { this.guardMessage(entry, message).catch(() => this.internalFailure(taskId, 'guard-message-failed')); });
      guard.on('error', () => this.internalFailure(taskId, 'guard-start-failed'));
      guard.on('exit', (code, signal) => { entry.resolveExit(); this.guardExit(entry, code, signal).catch(() => this.internalFailure(taskId, 'guard-exit-failed')); });
    });
  }
  async guardMessage(entry, message) {
    if (message?.type === 'cleaning') { if (message.nonce === entry.cleanupNonce) entry.cleanupAck = true; return; }
    if (message?.type === 'cleanup-error') { entry.cleanupAck = false; return; }
    await this.enqueue(async () => {
      const task = this.task(entry.taskId);
      if (message?.type === 'ready') {
        if (task.cancelRequested || this.closing) { this.cleanGuard(entry); return; }
        await this.change(state => { state.tasks.find(t => t.id === entry.taskId).workers.find(w => w.id === entry.workerId).guardPid = entry.guard.pid; });
        // Adapter transport encoding is deterministic from the persisted original
        // plan + pinned source/config. Do not overwrite the approved plan prompt.
        entry.guard.send({ type: 'launch', ...entry.command, cwd: entry.cwd,
          prompt: Object.hasOwn(entry.command, 'prompt') ? entry.command.prompt : entry.node.prompt });
      } else if (message?.type === 'started' || message?.type === 'progress') {
        await this.change(state => {
          const w = state.tasks.find(t => t.id === entry.taskId).workers.find(w => w.id === entry.workerId);
          if (message.type === 'started') { w.pid = message.pid; w.startedAt = message.at; w.status = 'running'; }
          w.lastObservedAt = message.at; w.stdoutBytes = message.stdoutBytes ?? w.stdoutBytes; w.stderrBytes = message.stderrBytes ?? w.stderrBytes;
        });
      } else if (message?.type === 'overflow') {
        await this.fence(entry.taskId, 'output-limit');
      } else if (message?.type === 'terminal') {
        if (entry.terminal) return;
        entry.terminal = message;
        await this.change(state => { const w = state.tasks.find(t => t.id === entry.taskId).workers.find(w => w.id === entry.workerId); w.status = 'collecting'; w.exitCode = message.code; w.stdoutBytes = message.stdoutBytes; w.stderrBytes = message.stderrBytes; if (message.agentExitedAt) w.agentExitedAt = message.agentExitedAt; });
        this.cleanGuard(entry);
      }
    });
  }
  cleanGuard(entry) {
    if (entry.cleaning) return;
    entry.cleaning = true; entry.cleanupNonce = crypto.randomUUID();
    if (entry.guard.connected) entry.guard.send({ type: 'cleanup', nonce: entry.cleanupNonce }, () => {});
    // No fallback signal to a PID. A guard that cannot acknowledge/exit is
    // retained as intervention, never falsely declared cancelled.
    entry.cleanupTimer = setTimeout(() => { this.enqueue(async () => {
      if (!this.guards.has(entry.workerId)) return;
      await this.change(state => { const t = state.tasks.find(t => t.id === entry.taskId); t.status = 'intervention'; t.reason = 'guard-cleanup-unconfirmed'; t.finishedAt = new Date().toISOString(); t.revision++; });
    }).catch(() => {}); }, 5000);
  }
  async guardExit(entry, code, signal) {
    clearTimeout(entry.cleanupTimer);
    await this.enqueue(async () => {
      this.guards.delete(entry.workerId);
      const clean = entry.cleanupAck && signal === 'SIGKILL';
      const task = this.task(entry.taskId);
      let candidate, reason;
      if (!clean) reason = 'guard-cleanup-unconfirmed';
      else if (!task.cancelRequested) {
        if (!entry.terminal || entry.terminal.code !== 0 || entry.terminal.reason) reason = entry.terminal?.reason ?? 'agent-failed';
        else {
          try {
            candidate = this.ports.parseCandidate(this.config, entry.node, entry.terminal.stdout);
            if (!closedObject(candidate, ['name', 'content']) || candidate.name !== entry.node.file || !text(candidate.content, 65536)) throw new Error();
          } catch { reason = 'invalid-candidate'; }
        }
      }
      entry.terminal = null; entry.command = null;
      await this.change(state => {
        const t = state.tasks.find(t => t.id === entry.taskId), w = t.workers.find(w => w.id === entry.workerId);
        w.cleaned = clean; w.finishedAt = new Date().toISOString(); w.status = task.cancelRequested ? 'cancelled' : candidate ? 'collected' : 'failed';
        if (candidate) w.candidate = candidate;
        if (reason) w.reason = reason;
      });
      if (!clean) { await this.fence(entry.taskId, reason, true); }
      else if (reason && !task.cancelRequested) await this.fence(entry.taskId, reason);
    });
    await this.settle(entry.taskId);
  }
  stopExecutions(taskId) {
    for (const entry of this.guards.values()) if (entry.taskId === taskId) this.cleanGuard(entry);
    this.verifications.get(taskId)?.controller.abort();
  }
  async fence(taskId, reason, intervention = false) {
    const task = this.task(taskId); if (TERMINAL.has(task.status)) return;
    await this.change(state => { const t = state.tasks.find(t => t.id === taskId); t.cancelRequested = true; t.reason = reason; t.status = intervention ? 'intervention' : 'cancelling'; if (intervention) { t.revision++; t.finishedAt = new Date().toISOString(); } });
    this.stopExecutions(taskId);
  }
  async internalFailure(taskId, reason) { await this.enqueue(() => this.fence(taskId, reason)).catch(() => {}); await this.settle(taskId).catch(() => {}); }
  async settle(taskId) {
    let verification;
    await this.enqueue(async () => {
      const task = this.task(taskId);
      if (TERMINAL.has(task.status)) { clearTimeout(this.timers.get(taskId)); return; }
      if ([...this.guards.values()].some(e => e.taskId === taskId) || this.verifications.has(taskId)) return;
      if (task.cancelRequested) {
        await this.change(state => { const t = state.tasks.find(t => t.id === taskId); t.status = t.reason === 'user-cancelled' || t.reason === 'supervisor-stopped' ? 'cancelled' : 'failed'; t.revision++; t.finishedAt = new Date().toISOString(); for (const w of t.workers) if (w.status === 'queued' || w.status === 'planned') w.status = 'cancelled'; });
        clearTimeout(this.timers.get(taskId)); return;
      }
      if (!task.workers.every(w => w.status === 'collected')) return;
      await this.change(state => { state.tasks.find(t => t.id === taskId).status = 'verifying'; });
      verification = { controller: new AbortController(), files: task.workers.map(w => jsonCopy(w.candidate)) };
      this.verifications.set(taskId, verification);
    });
    if (!verification) return;
    let result;
    try { result = await this.ports.verifyFiles(verification.files, { signal: verification.controller.signal }); }
    catch { result = { passed: false, reason: 'verification-failed', files: [] }; }
    await this.enqueue(async () => {
      this.verifications.delete(taskId); const task = this.task(taskId);
      if (TERMINAL.has(task.status) || task.cancelRequested) return;
      if (Date.now() >= Date.parse(task.deadline)) { await this.fence(taskId, 'deadline-exceeded'); return; }
      const valid = result?.passed === true && Number.isInteger(result.checks) && result.checks > 0 && Array.isArray(result.files) && result.files.length === 2 && result.files.every(file => verification.files.some(v => v.name === file.name && v.content === file.content) && file.sha256 === artifactHash(file.content)) && new Set(result.files.map(f => f.name)).size === 2;
      if (!valid) { await this.fence(taskId, 'independent-verification-failed'); return; }
      await this.change(state => { const t = state.tasks.find(t => t.id === taskId); t.delivery = { taskId, files: result.files.map(file => ({ name: file.name, content: file.content, sha256: file.sha256 })), checks: result.checks, verifiedAt: new Date().toISOString() }; t.status = 'completed'; t.revision++; t.finishedAt = t.delivery.verifiedAt; });
      clearTimeout(this.timers.get(taskId));
    });
    await this.settle(taskId);
  }
  async shutdown() {
    await this.enqueue(async () => { this.closing = true; for (const task of this.store.state.tasks) if (LIVE.has(task.status)) await this.fence(task.id, 'supervisor-stopped'); });
    const until = Date.now() + 7000;
    while (this.guards.size || this.verifications.size) {
      if (Date.now() >= until) fail('shutdown-needs-intervention', 503);
      await new Promise(resolve => setTimeout(resolve, 20));
    }
    for (const task of this.store.state.tasks) await this.settle(task.id);
    for (const timer of this.timers.values()) clearTimeout(timer);
  }
}

export async function readBody(request, limit = 32768) {
  let size = 0; const chunks = [];
  for await (const chunk of request) { size += chunk.length; if (size > limit) fail('request-too-large', 413); chunks.push(chunk); }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { fail('invalid-json', 400); }
}
export function response(res, status, body) {
  const raw = JSON.stringify(body); res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'Content-Length': Buffer.byteLength(raw) }); res.end(raw);
}
export function errorResponse(res, error) { response(res, error instanceof Fault ? error.status : 503, { error: error instanceof Fault ? error.code : 'internal-unavailable' }); }
export function authorized(request, token) {
  const values = request.rawHeaders.filter((_, n, all) => n % 2 === 0 && all[n].toLowerCase() === 'authorization');
  return values.length === 1 && sameSecret(request.headers.authorization, 'Bearer ' + token);
}

export async function runSupervisor(directory, config, sourceDigest, configDigest) {
  await privateRoot(directory);
  const socket = path.join(directory, 's.sock'), lock = path.join(directory, 'supervisor.lock');
  const metadata = { format: FORMAT, instance: crypto.randomUUID(), token: crypto.randomBytes(32).toString('hex'), socket, sourceDigest, configDigest };
  const held = await fs.open(lock, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600).catch(() => fail('supervisor-lock-exists', 503));
  await held.writeFile(JSON.stringify(metadata)); await held.sync(); await held.close();
  const { plan, verifyFiles } = await import('./business.mjs');
  const { makeCommand, parseCandidate } = await import('./providers.mjs');
  const supervisor = await Supervisor.open(directory, config, { plan, verifyFiles, makeCommand, parseCandidate });
  const server = http.createServer(async (req, res) => {
    try {
      if (req.method !== 'POST' || req.url !== '/rpc' || !authorized(req, metadata.token) || req.headers.origin) fail('unauthorized', 401);
      const input = await readBody(req);
      if (!closedObject(input, ['operation', 'taskId', 'body', 'key'])) fail('invalid-request', 400);
      if (input.operation === 'ping') { response(res, 200, { instance: metadata.instance, sourceDigest, configDigest }); return; }
      if (input.operation === 'shutdown') {
        await supervisor.shutdown(); response(res, 200, { stopped: true });
        server.close(async () => {
          // Remove only this instance's authenticated metadata/socket. Never
          // delete a stale lock in order to start a replacement supervisor.
          try { const current = JSON.parse(await readPrivate(lock, 8192)); if (current.instance === metadata.instance && sameSecret(current.token, metadata.token)) { await fs.unlink(lock); } } catch {}
          process.exitCode = 0;
        });
        return;
      }
      response(res, 200, await supervisor.command(input.operation, input.taskId, input.body, input.key));
    } catch (error) { errorResponse(res, error); }
  });
  server.requestTimeout = 10000; server.headersTimeout = 5000; server.keepAliveTimeout = 1000; server.maxConnections = 32;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(socket, resolve); });
  await fs.chmod(socket, 0o600);
  process.send?.({ ready: true });
  // The launcher/HTTP frontend may disconnect. This supervisor deliberately
  // remains alive with the existing children and deadline timers.
  return { supervisor, server, metadata };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.once('message', async input => {
    try { await runSupervisor(input.directory, input.config, input.sourceDigest, input.configDigest); }
    catch { process.send?.({ ready: false }); process.exitCode = 1; process.disconnect?.(); }
  });
}
