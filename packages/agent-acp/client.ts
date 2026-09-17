import { TextDecoder } from 'node:util';
import { posix, win32 } from 'node:path';

const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const own = (value, key) => Object.hasOwn(value, key);
const string = (value, max = 256) => typeof value === 'string' && value.trim().length > 0 &&
  value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const rpcId = value => string(value) || Number.isSafeInteger(value);
const stopped = new Set(['end_turn', 'max_tokens', 'max_turn_requests', 'refusal', 'cancelled']);
const cancelled = () => ({ outcome: { outcome: 'cancelled' } });

export class AcpError extends Error {
  constructor(code, rpcCode) { super(code); this.name = 'AcpError'; this.code = code; if (Number.isSafeInteger(rpcCode)) this.rpcCode = rpcCode; }
}
const fault = code => new AcpError(code);
function positive(value, maximum) {
  if (!Number.isSafeInteger(value) || value < 1 || value > maximum) throw fault('acp_invalid_options');
  return value;
}

/**
 * Exclusive NDJSON protocol consumer over injected Node byte streams. This does
 * not spawn, kill, own, authenticate or recover an Agent process. close() rejects
 * protocol work and detaches listeners; the caller still owns both streams.
 * The caller must retain its own stream error listeners and close/destroy the
 * streams when appropriate. onUpdate is awaited in wire order and must finish
 * boundedly; its persistence/timeout policy belongs to that caller. initialize
 * reports Agent capabilities, not this client's support for session/load.
 * No update/permission/response is durable evidence merely because it arrived.
 */
export class AcpClient {
  #readable; #writable; #onUpdate; #onPermission; #onClose;
  #limits; #timeouts; #versions; #state = 'new'; #failure; #sequence = 0;
  #frame; #used = 0; #queuedBytes = 0; #queue = []; #pumping = false; #readEnded = false;
  #writes = []; #writing = false; #writeBytes = 0;
  #pending = new Map(); #sessions = new Map(); #creation;
  #permissions = new Map(); #peerIds = new Set();
  #listeners;

  constructor({ readable, writable, onUpdate = () => {}, onPermission, onClose = () => {},
    supportedProtocolVersions = [1], maxFrameBytes = 1024 * 1024, maxQueueBytes = 4 * 1024 * 1024,
    maxPending = 64, maxSessions = 64, maxPeerRequests = 4096,
    requestTimeoutMs = 30000, promptTimeoutMs = 300000, permissionTimeoutMs = 300000 } = {}) {
    if (!readable?.on || !readable?.off || !writable?.on || !writable?.off || !writable?.write ||
        typeof onUpdate !== 'function' || typeof onClose !== 'function' ||
        onPermission !== undefined && typeof onPermission !== 'function' ||
        !Array.isArray(supportedProtocolVersions) || !supportedProtocolVersions.length ||
        supportedProtocolVersions.length > 16 || supportedProtocolVersions.some(v => !Number.isSafeInteger(v) || v < 1)) throw fault('acp_invalid_options');
    this.#limits = { frame: positive(maxFrameBytes, 16 * 1024 * 1024), queue: positive(maxQueueBytes, 64 * 1024 * 1024),
      pending: positive(maxPending, 1024), sessions: positive(maxSessions, 1024), peer: positive(maxPeerRequests, 65536) };
    this.#timeouts = { request: positive(requestTimeoutMs, 86400000), prompt: positive(promptTimeoutMs, 86400000),
      permission: positive(permissionTimeoutMs, 86400000) };
    this.#versions = new Set(supportedProtocolVersions);
    this.#readable = readable; this.#writable = writable;
    this.#onUpdate = onUpdate; this.#onPermission = onPermission; this.#onClose = onClose;
    this.#frame = Buffer.allocUnsafe(this.#limits.frame);
    this.#listeners = { data: chunk => this.#receive(chunk), end: () => this.#end(),
      close: () => { if (!this.#readEnded) this.#fail('acp_disconnected'); }, error: () => this.#fail('acp_stream_error') };
    for (const [event, listener] of Object.entries(this.#listeners)) readable.on(event, listener);
    writable.on('error', this.#listeners.error); writable.on('close', this.#listeners.writeClose = () => this.#fail('acp_disconnected'));
    if (readable.readableEnded || readable.destroyed || writable.destroyed || writable.writableEnded) this.#fail('acp_disconnected');
  }

  get closed() { return this.#state === 'closed'; }
  #available() { if (this.closed) throw this.#failure; }
  #ready() { this.#available(); if (this.#state !== 'ready') throw fault('acp_not_initialized'); }
  #session(sessionId) {
    this.#ready(); const session = this.#sessions.get(sessionId);
    if (!session) throw fault('acp_unknown_session');
    return session;
  }

  async initialize({ protocolVersion = Math.max(...this.#versions), clientCapabilities = {}, clientInfo } = {}) {
    this.#available();
    if (this.#state !== 'new') throw fault('acp_already_initialized');
    if (!this.#versions.has(protocolVersion) || !object(clientCapabilities) ||
        clientCapabilities.terminal === true || clientCapabilities.fs?.readTextFile === true ||
        clientCapabilities.fs?.writeTextFile === true || clientInfo !== undefined && !object(clientInfo)) throw fault('acp_invalid_initialize');
    const params = { protocolVersion, clientCapabilities, ...(clientInfo ? { clientInfo } : {}) };
    this.#encode({ jsonrpc: '2.0', id: 'preflight', method: 'initialize', params });
    this.#state = 'initializing';
    return this.#request('initialize', params, this.#timeouts.request, result => {
      if (!object(result) || !this.#versions.has(result.protocolVersion) || !object(result.agentCapabilities)) throw fault('acp_invalid_initialize_result');
      for (const key of ['loadSession']) if (own(result.agentCapabilities, key) && typeof result.agentCapabilities[key] !== 'boolean') throw fault('acp_invalid_initialize_result');
      for (const key of ['promptCapabilities', 'sessionCapabilities', 'mcpCapabilities']) {
        if (own(result.agentCapabilities, key) && !object(result.agentCapabilities[key])) throw fault('acp_invalid_initialize_result');
      }
      this.#state = 'ready';
      return result;
    });
  }

  async newSession({ cwd, mcpServers = [] } = {}) {
    this.#ready();
    if (!string(cwd, 8192) || !posix.isAbsolute(cwd) && !win32.isAbsolute(cwd) || !Array.isArray(mcpServers) || mcpServers.length > 128) throw fault('acp_invalid_session');
    if (this.#creation) throw fault('acp_session_creation_busy');
    if (this.#sessions.size >= this.#limits.sessions) throw fault('acp_session_limit');
    const creation = { early: [], bytes: 0 };
    this.#creation = creation;
    try {
      return await this.#request('session/new', { cwd, mcpServers }, this.#timeouts.request, async result => {
        if (!object(result) || !string(result.sessionId) || this.#sessions.has(result.sessionId) ||
            creation.early.some(event => event.sessionId !== result.sessionId)) throw fault('acp_invalid_session_result');
        this.#sessions.set(result.sessionId, { prompt: null, cancelWrites: 0 });
        // Native Agents can emit initial command/mode updates before new returns.
        // Hold them boundedly, and bind only to the session ID the reply grants.
        for (const event of creation.early) await this.#onUpdate(structuredClone(event));
        creation.early = []; creation.bytes = 0;
        return result;
      });
    } finally { if (this.#creation === creation) this.#creation = undefined; }
  }

  async prompt(sessionId, prompt, { timeoutMs = this.#timeouts.prompt } = {}) {
    const session = this.#session(sessionId);
    if (session.prompt || session.cancelWrites) throw fault('acp_session_busy');
    if (!Array.isArray(prompt) || !prompt.length || prompt.length > 128 || prompt.some(block => !object(block) ||
        !['text', 'image', 'audio', 'resource', 'resource_link'].includes(block.type) ||
        block.type === 'text' && (typeof block.text !== 'string' || !block.text.isWellFormed() || block.text.includes('\0')))) throw fault('acp_invalid_prompt');
    positive(timeoutMs, 86400000);
    const turn = { cancelled: false }; session.prompt = turn;
    try {
      return await this.#request('session/prompt', { sessionId, prompt }, timeoutMs, result => {
        if (!object(result) || !stopped.has(result.stopReason) || own(result, 'sessionId') && result.sessionId !== sessionId) throw fault('acp_invalid_prompt_result');
        return result;
      }, sessionId);
    } finally {
      if (session.prompt === turn) session.prompt = null;
      this.#denyPermissions(sessionId);
    }
  }

  async cancel(sessionId) {
    const session = this.#session(sessionId);
    if (session.prompt) session.prompt.cancelled = true;
    this.#denyPermissions(sessionId);
    session.cancelWrites++;
    try { await this.#send({ jsonrpc: '2.0', method: 'session/cancel', params: { sessionId } }); }
    finally { session.cancelWrites--; }
    // Only confirms writing a notification. The prompt promise is still live;
    // its eventual stopReason and the caller's process cleanup are independent.
  }

  close() { this.#fail('acp_closed'); }

  #encode(message) {
    let raw; try { raw = JSON.stringify(message); } catch { throw fault('acp_invalid_outbound'); }
    const bytes = Buffer.from(raw + '\n');
    if (bytes.length - 1 > this.#limits.frame) throw fault('acp_frame_limit');
    return bytes;
  }
  #send(message) {
    try {
      this.#available(); const bytes = this.#encode(message);
      if (this.#writeBytes + bytes.length > this.#limits.queue) { this.#fail('acp_write_limit'); throw this.#failure; }
      return new Promise((resolve, reject) => {
        this.#writes.push({ bytes, resolve, reject }); this.#writeBytes += bytes.length; this.#flushWrites();
      });
    } catch (error) { return Promise.reject(error); }
  }
  #flushWrites() {
    if (this.closed || this.#writing || !this.#writes.length) return;
    this.#writing = true; const entry = this.#writes[0];
    try {
      this.#writable.write(entry.bytes, error => {
        if (this.closed) return;
        if (error) { this.#fail('acp_stream_error'); return; }
        this.#writes.shift(); this.#writeBytes -= entry.bytes.length; this.#writing = false;
        entry.resolve(); this.#flushWrites();
      });
    } catch { this.#fail('acp_stream_error'); }
  }
  #request(method, params, timeout, validate, sessionId) {
    this.#available();
    if (this.#pending.size >= this.#limits.pending) throw fault('acp_pending_limit');
    if (this.#sequence >= Number.MAX_SAFE_INTEGER) throw fault('acp_request_limit');
    const id = 'acp-' + ++this.#sequence;
    this.#encode({ jsonrpc: '2.0', id, method, params });
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => this.#fail('acp_request_timeout'), timeout);
      this.#pending.set(id, { resolve, reject, timer, validate, method, sessionId });
      this.#send({ jsonrpc: '2.0', id, method, params }).catch(() => this.#fail('acp_write_failed'));
    });
  }

  #receive(chunk) {
    if (this.closed) return;
    if (!(chunk instanceof Uint8Array)) { this.#fail('acp_byte_stream_required'); return; }
    const bytes = Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    for (let start = 0; start < bytes.length && !this.closed;) {
      const newline = bytes.indexOf(10, start), end = newline < 0 ? bytes.length : newline;
      if (this.#used + end - start > this.#limits.frame) { this.#fail('acp_frame_limit'); return; }
      bytes.copy(this.#frame, this.#used, start, end); this.#used += end - start;
      if (newline < 0) return;
      const length = this.#used; this.#used = 0; start = newline + 1;
      let raw, message;
      try {
        raw = new TextDecoder('utf-8', { fatal: true }).decode(this.#frame.subarray(0, length));
        if (!raw.trim()) continue;
        message = JSON.parse(raw);
      } catch { this.#fail('acp_invalid_frame'); return; }
      if (!object(message) || message.jsonrpc !== '2.0') { this.#fail('acp_invalid_message'); return; }
      if (this.#queuedBytes + length > this.#limits.queue) { this.#fail('acp_read_limit'); return; }
      this.#queuedBytes += length; this.#queue.push({ message, length });
      void this.#pump();
    }
  }
  #end() {
    this.#readEnded = true;
    if (this.#used) this.#fail('acp_truncated_frame');
    else if (!this.#pumping && !this.#queue.length) this.#fail('acp_disconnected');
    // A complete response followed immediately by EOF still gets dispatched.
    // Once those frames drain, every unresolved request is disconnected.
  }
  async #pump() {
    if (this.#pumping || this.closed) return;
    this.#pumping = true;
    try {
      while (this.#queue.length && !this.closed) {
        const { message, length } = this.#queue.shift();
        await this.#handle(message, length);
        this.#queuedBytes -= length;
      }
    } catch (error) { this.#fail(error instanceof AcpError ? error.code : 'acp_callback_failed'); }
    finally { this.#pumping = false; if (this.#readEnded && !this.closed) this.#fail('acp_disconnected'); }
  }
  async #handle(message, length) {
    if (own(message, 'method')) {
      if (!string(message.method) || own(message, 'result') || own(message, 'error') || !object(message.params)) throw fault('acp_invalid_message');
      if (own(message, 'id')) { this.#permission(message); return; }
      if (message.method !== 'session/update') throw fault('acp_unsupported_notification');
      const params = message.params;
      if (!string(params.sessionId) || !object(params.update) || !string(params.update.sessionUpdate)) throw fault('acp_invalid_update');
      if (!this.#sessions.has(params.sessionId)) {
        if (!this.#creation || this.#creation.bytes + length > this.#limits.queue) throw fault('acp_unknown_session');
        this.#creation.early.push(params); this.#creation.bytes += length;
        return;
      }
      await this.#onUpdate(structuredClone(params));
      return;
    }
    if (!own(message, 'id') || own(message, 'result') === own(message, 'error')) throw fault('acp_invalid_message');
    const pending = this.#pending.get(message.id);
    if (!pending) throw fault('acp_unknown_response'); // Includes duplicate/late IDs.
    if (own(message, 'error')) {
      if (!object(message.error) || !Number.isSafeInteger(message.error.code)) throw fault('acp_invalid_error');
      this.#pending.delete(message.id); clearTimeout(pending.timer);
      pending.reject(new AcpError('acp_remote_error', message.error.code));
      if (pending.method === 'initialize') this.#fail('acp_initialize_failed');
      return;
    }
    const result = await pending.validate(message.result);
    if (this.closed) return;
    this.#pending.delete(message.id); clearTimeout(pending.timer); pending.resolve(result);
  }

  #permission(message) {
    if (!rpcId(message.id) || this.#peerIds.has(message.id)) throw fault('acp_duplicate_peer_request');
    if (this.#peerIds.size >= this.#limits.peer || this.#permissions.size >= this.#limits.pending) throw fault('acp_peer_request_limit');
    this.#peerIds.add(message.id);
    if (message.method !== 'session/request_permission') {
      void this.#send({ jsonrpc: '2.0', id: message.id, error: { code: -32601, message: 'Method not supported' } }).catch(() => this.#fail('acp_write_failed'));
      return;
    }
    const params = message.params, session = this.#sessions.get(params.sessionId);
    if (!session) throw fault('acp_unknown_session');
    if (!object(params.toolCall) || !string(params.toolCall.toolCallId) || !Array.isArray(params.options) ||
        !params.options.length || params.options.length > 64 || params.options.some(option => !object(option) ||
          !string(option.optionId) || !string(option.name, 8192) || !['allow_once', 'allow_always', 'reject_once', 'reject_always'].includes(option.kind)) ||
        new Set(params.options.map(option => option.optionId)).size !== params.options.length) throw fault('acp_invalid_permission');
    const controller = new AbortController();
    const entry = { id: message.id, sessionId: params.sessionId, controller, options: new Set(params.options.map(option => option.optionId)) };
    entry.timer = setTimeout(() => this.#answer(entry, cancelled()), this.#timeouts.permission);
    this.#permissions.set(message.id, entry);
    if (!session.prompt || session.prompt.cancelled || !this.#onPermission) { this.#answer(entry, cancelled()); return; }
    // Never await this callback in the receive loop: cancel and prompt terminal
    // must continue to arrive while a human is considering a question.
    Promise.resolve().then(() => controller.signal.aborted ? cancelled() : this.#onPermission(structuredClone(params), { signal: controller.signal }))
      .then(result => this.#answer(entry, this.#permissionResult(result, entry.options)))
      .catch(() => this.#answer(entry, cancelled()));
  }
  #permissionResult(result, options) {
    if (!object(result) || !object(result.outcome) || Object.keys(result).some(key => !['outcome', 'answers', '_meta'].includes(key))) return cancelled();
    if (result.outcome.outcome === 'cancelled') return cancelled();
    if (result.outcome.outcome !== 'selected' || !options.has(result.outcome.optionId) ||
        result.answers !== undefined && (!object(result.answers) || Object.values(result.answers).some(value => typeof value !== 'string' || !value.isWellFormed())) ||
        result._meta !== undefined && !object(result._meta)) return cancelled();
    const response = { outcome: { outcome: 'selected', optionId: result.outcome.optionId },
      ...(result.answers !== undefined ? { answers: result.answers } : {}), ...(result._meta !== undefined ? { _meta: result._meta } : {}) };
    try { this.#encode({ jsonrpc: '2.0', id: 'preflight', result: response }); } catch { return cancelled(); }
    return response;
  }
  #answer(entry, result) {
    if (this.closed || this.#permissions.get(entry.id) !== entry) return;
    this.#permissions.delete(entry.id); clearTimeout(entry.timer); entry.controller.abort();
    void this.#send({ jsonrpc: '2.0', id: entry.id, result }).catch(() => this.#fail('acp_write_failed'));
  }
  #denyPermissions(sessionId) {
    for (const entry of this.#permissions.values()) if (entry.sessionId === sessionId) this.#answer(entry, cancelled());
  }
  #fail(code) {
    if (this.closed) return;
    this.#failure = fault(code); this.#state = 'closed';
    for (const pending of this.#pending.values()) { clearTimeout(pending.timer); pending.reject(this.#failure); }
    this.#pending.clear();
    for (const entry of this.#permissions.values()) { clearTimeout(entry.timer); entry.controller.abort(); }
    this.#permissions.clear(); this.#peerIds.clear(); this.#sessions.clear(); this.#creation = undefined;
    for (const entry of this.#writes) entry.reject(this.#failure);
    this.#writes = []; this.#writeBytes = 0; this.#queue = []; this.#queuedBytes = 0; this.#used = 0;
    for (const [event, listener] of Object.entries(this.#listeners)) this.#readable.off(event, listener);
    this.#writable.off('error', this.#listeners.error); this.#writable.off('close', this.#listeners.writeClose);
    try { Promise.resolve(this.#onClose(this.#failure)).catch(() => {}); } catch { /* No raw callback errors on the transport. */ }
  }
}
