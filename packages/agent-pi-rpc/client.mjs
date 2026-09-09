// Pi's native RPC is LF JSONL, not JSON-RPC/ACP. This parser owns no process.
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const text = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const positive = (value, max) => Number.isSafeInteger(value) && value > 0 && value <= max;
const eventTypes = new Set(['agent_start', 'agent_end', 'agent_settled', 'turn_start', 'turn_end', 'message_start', 'message_update', 'message_end',
  'tool_execution_start', 'tool_execution_update', 'tool_execution_end', 'auto_compaction_start', 'auto_compaction_end', 'compaction_start', 'compaction_end', 'queue_update',
  'auto_retry_start', 'auto_retry_end', 'summarization_retry_scheduled', 'summarization_retry_attempt_start', 'summarization_retry_finished']);
const dialogs = new Set(['select', 'confirm', 'input', 'editor']);
export class PiRpcError extends Error { constructor(code) { super(code); this.name = 'PiRpcError'; this.code = code; } }
const fail = code => new PiRpcError(code);
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return {promise, resolve, reject}; }

/** One caller, one native Pi session, at most one prompt. Explicit requests only;
 * no model/config switching, queue injection, tool execution or session replay.
 * onEvent is trusted and awaited in wire order, with its own bounded deadline.
 * Raw events are ephemeral protocol data, never public progress/Task evidence.
 */
export class PiRpcClient {
  #readable; #writable; #onEvent; #onClose; #onInteraction; #listeners; #failure; #closed = false;
  #frame; #used = 0; #queue = []; #queuedBytes = 0; #pumping = false; #ended = false;
  #writes = []; #writeBytes = 0; #writing = false; #pending = new Map(); #sequence = 0;
  #turn = null; #cancelling = null; #peerIds = new Set(); #interactions = new Map(); #limits; #requestMs; #callbackMs; #interactionMs; #businessMs;
  constructor({readable, writable, onEvent = () => {}, onClose = () => {}, onInteraction, maxFrameBytes = 1024 * 1024,
    maxQueueBytes = 4 * 1024 * 1024, maxEvents = 4096, requestTimeoutMs = 10000, callbackTimeoutMs = 1000, interactionTimeoutMs = 10000,
    businessInteractionTimeoutMs = 10000} = {}) {
    if (!readable?.on || !readable?.off || !writable?.write || !writable?.on || !writable?.off ||
      typeof onEvent !== 'function' || typeof onClose !== 'function' || onInteraction !== undefined && typeof onInteraction !== 'function' || !positive(maxFrameBytes, 4 * 1024 * 1024) ||
      !positive(maxQueueBytes, 16 * 1024 * 1024) || !positive(maxEvents, 16384) || !positive(requestTimeoutMs, 86400000) ||
      !positive(callbackTimeoutMs, 30000) || !positive(interactionTimeoutMs, 30000) || !positive(businessInteractionTimeoutMs, 120000)) throw fail('pi_invalid_options');
    this.#readable = readable; this.#writable = writable; this.#onEvent = onEvent; this.#onClose = onClose;
    this.#limits = {frame: maxFrameBytes, queue: maxQueueBytes, events: maxEvents}; this.#requestMs = requestTimeoutMs; this.#callbackMs = callbackTimeoutMs;
    this.#onInteraction = onInteraction; this.#interactionMs = interactionTimeoutMs; this.#businessMs = businessInteractionTimeoutMs;
    this.#frame = Buffer.allocUnsafe(maxFrameBytes);
    this.#listeners = {data: chunk => this.#receive(chunk), end: () => { this.#ended = true;
      if (this.#used) this.#fail('pi_truncated_frame'); else if (!this.#pumping && !this.#queue.length) this.#fail('pi_disconnected'); },
    error: () => this.#fail('pi_stream_error'), close: () => { if (!this.#ended) this.#fail('pi_disconnected'); }};
    for (const [event, listener] of Object.entries(this.#listeners)) readable.on(event, listener);
    writable.on('error', this.#listeners.error); writable.on('close', this.#listeners.writeClose = () => this.#fail('pi_disconnected'));
    if (readable.destroyed || readable.readableEnded || writable.destroyed || writable.writableEnded) this.#fail('pi_disconnected');
  }
  get closed() { return this.#closed; }
  #available() { if (this.#closed) throw this.#failure; }
  close() { this.#fail('pi_closed'); }
  async getState() {
    const state = await this.#request('get_state');
    if (!object(state) || !text(state.sessionId, 256) || !state.sessionId || typeof state.isStreaming !== 'boolean' ||
      typeof state.isCompacting !== 'boolean' || !Number.isSafeInteger(state.pendingMessageCount) || state.pendingMessageCount < 0 ||
      !Number.isSafeInteger(state.messageCount) || state.messageCount < 0) { this.#fail('pi_invalid_state'); throw this.#failure; }
    // Do not expose sessionFile/model details/native config as public metadata.
    return {sessionId: state.sessionId, isStreaming: state.isStreaming, isCompacting: state.isCompacting,
      pendingMessageCount: state.pendingMessageCount, messageCount: state.messageCount};
  }
  async prompt(message, {timeoutMs = 300000} = {}) {
    this.#available();
    if (!text(message, 256 * 1024) || !message.trim() || !positive(timeoutMs, 86400000)) throw fail('pi_invalid_prompt');
    if (this.#turn || this.#cancelling) throw fail('pi_busy');
    const settled = deferred(), turn = {settled, accepted: false, terminal: null, final: null, ended: false, cancelled: false,
      interaction: false, events: 0, timer: setTimeout(() => this.#fail('pi_prompt_timeout'), timeoutMs)};
    // A malformed event can reject while awaiting the prompt acknowledgement.
    settled.promise.catch(() => {}); this.#turn = turn;
    try {
      await this.#request('prompt', {message}); turn.accepted = true; this.#settle(turn);
      return await settled.promise;
    } finally { clearTimeout(turn.timer); if (this.#turn === turn) this.#turn = null; }
  }
  cancel() {
    this.#available();
    if (this.#cancelling) return this.#cancelling;
    if (this.#turn) this.#turn.cancelled = true;
    for (const entry of this.#interactions.values()) this.#answer(entry, undefined);
    // Native abort continues queued messages unless clear_queue happens first.
    this.#cancelling = (async () => { await this.#request('clear_queue'); await this.#request('abort'); })()
      .finally(() => { this.#cancelling = null; });
    return this.#cancelling; // Protocol acknowledgement only, never cleanup.
  }
  #settle(turn) {
    if (!turn.accepted || !turn.terminal) return;
    turn.settled.resolve(turn.terminal);
  }
  #request(command, body = {}) {
    this.#available();
    if (this.#pending.size >= 8 || this.#sequence >= Number.MAX_SAFE_INTEGER) throw fail('pi_request_limit');
    const id = 'pi-' + ++this.#sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => this.#fail('pi_request_timeout'), this.#requestMs);
      this.#pending.set(id, {command, resolve, reject, timer});
      this.#send({id, type: command, ...body}).catch(() => this.#fail('pi_write_failed'));
    });
  }
  #send(value) {
    this.#available(); const bytes = Buffer.from(JSON.stringify(value) + '\n');
    if (bytes.length - 1 > this.#limits.frame || this.#writeBytes + bytes.length > this.#limits.queue) {
      this.#fail('pi_write_limit'); return Promise.reject(this.#failure);
    }
    return new Promise((resolve, reject) => { this.#writes.push({bytes, resolve, reject}); this.#writeBytes += bytes.length; this.#flush(); });
  }
  #flush() {
    if (this.#closed || this.#writing || !this.#writes.length) return;
    this.#writing = true; const entry = this.#writes[0];
    try { this.#writable.write(entry.bytes, error => {
      if (this.#closed) return; if (error) { this.#fail('pi_stream_error'); return; }
      this.#writes.shift(); this.#writeBytes -= entry.bytes.length; this.#writing = false; entry.resolve(); this.#flush();
    }); } catch { this.#fail('pi_stream_error'); }
  }
  #receive(chunk) {
    if (this.#closed) return;
    if (!(chunk instanceof Uint8Array)) { this.#fail('pi_byte_stream_required'); return; }
    const bytes = Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    for (let offset = 0; offset < bytes.length && !this.#closed;) {
      const newline = bytes.indexOf(10, offset), end = newline < 0 ? bytes.length : newline;
      if (this.#used + end - offset > this.#limits.frame) { this.#fail('pi_frame_limit'); return; }
      bytes.copy(this.#frame, this.#used, offset, end); this.#used += end - offset;
      if (newline < 0) return;
      const length = this.#used; this.#used = 0; offset = newline + 1;
      let message;
      try { const raw = new TextDecoder('utf-8', {fatal: true}).decode(this.#frame.subarray(0, length));
        if (!raw.trim() || raw.includes('\0')) throw Error('empty frame'); message = JSON.parse(raw); }
      catch { this.#fail('pi_invalid_frame'); return; }
      if (!object(message) || !text(message.type, 128) || !message.type) { this.#fail('pi_invalid_message'); return; }
      if (this.#queuedBytes + length > this.#limits.queue) { this.#fail('pi_read_limit'); return; }
      this.#queue.push({message, length}); this.#queuedBytes += length; void this.#pump();
    }
  }
  async #notify(message) {
    let timer;
    try { await Promise.race([Promise.resolve().then(() => this.#onEvent(structuredClone(message))),
      new Promise((_, reject) => { timer = setTimeout(() => reject(fail('pi_callback_timeout')), this.#callbackMs); })]); }
    finally { clearTimeout(timer); }
  }
  async #pump() {
    if (this.#closed || this.#pumping) return; this.#pumping = true;
    try { while (this.#queue.length && !this.#closed) {
      const {message, length} = this.#queue.shift(); await this.#handle(message); this.#queuedBytes -= length;
    }} catch (error) { this.#fail(error instanceof PiRpcError ? error.code : 'pi_callback_failed'); }
    finally { this.#pumping = false; if (this.#ended && !this.#closed) this.#fail('pi_disconnected'); }
  }
  #answer(entry, result) {
    if (this.#closed || this.#interactions.get(entry.id) !== entry) return;
    this.#interactions.delete(entry.id); clearTimeout(entry.timer); entry.controller.abort();
    const confirmed = entry.method === 'confirm' && typeof result?.confirmed === 'boolean';
    const value = ['input', 'select'].includes(entry.method) && text(result?.value, 4096) && result.value.trim().length > 0;
    const handled = result?.handled === true && (confirmed || value);
    if (!handled && this.#turn) this.#turn.interaction = true;
    void this.#send({type: 'extension_ui_response', id: entry.id,
      ...(handled && !this.#turn?.cancelled ? confirmed ? {confirmed: result.confirmed} : {value: result.value} : {cancelled: true})}).catch(() => this.#fail('pi_write_failed'));
  }
  async #handle(message) {
    if (message.type === 'response') {
      const pending = this.#pending.get(message.id);
      if (!pending || pending.command !== message.command || typeof message.success !== 'boolean') throw fail('pi_unmatched_response');
      this.#pending.delete(message.id); clearTimeout(pending.timer);
      if (!message.success) pending.reject(fail('pi_remote_rejected')); else pending.resolve(message.data);
      return;
    }
    if (message.type === 'extension_error') throw fail('pi_extension_failed');
    if (message.type === 'extension_ui_request') {
      if (!text(message.id, 256) || !message.id || !text(message.method, 128) || this.#peerIds.has(message.id) || this.#peerIds.size >= 4096)
        throw fail('pi_invalid_interaction');
      this.#peerIds.add(message.id);
      if (dialogs.has(message.method)) {
        if (!this.#turn) throw fail('pi_unsolicited_interaction');
        if (this.#interactions.size >= 16) throw fail('pi_interaction_limit');
        const entry = {id: message.id, method: message.method, controller: new AbortController()};
        entry.timer = setTimeout(() => this.#answer(entry, undefined), ['input', 'select'].includes(message.method) ? this.#businessMs : this.#interactionMs);
        this.#interactions.set(entry.id, entry);
        if (!this.#onInteraction || this.#turn.cancelled) this.#answer(entry, undefined);
        else Promise.resolve().then(() => entry.controller.signal.aborted ? undefined :
          this.#onInteraction(structuredClone(message), {signal: entry.controller.signal}))
          .then(result => this.#answer(entry, result), () => { this.#answer(entry, undefined); this.#fail('pi_interaction_failed'); });
      } else if (message.method === 'notify' && this.#onInteraction) {
        // Startup handshake and bounded bridge observations; never wait for a
        // permission answer in the receive loop (cancel must still be read).
        let timer;
        try { await Promise.race([Promise.resolve().then(() => this.#onInteraction(structuredClone(message))),
          new Promise((_, reject) => { timer = setTimeout(() => reject(fail('pi_callback_timeout')), this.#callbackMs); })]); }
        finally { clearTimeout(timer); }
      }
      return; // Notifications are not user approval, progress, or evidence.
    }
    const turn = this.#turn;
    if (!turn || !eventTypes.has(message.type) || ++turn.events > this.#limits.events || turn.terminal) throw fail('pi_unexpected_event');
    if (message.type === 'queue_update' && (!Array.isArray(message.steering) || !Array.isArray(message.followUp) ||
      message.steering.length + message.followUp.length > 0)) throw fail('pi_unapproved_queue');
    if (message.type === 'agent_start') { turn.ended = false; turn.final = null; }
    if (message.type === 'message_end' && message.message?.role === 'assistant') turn.final = structuredClone(message.message);
    if (message.type === 'agent_end') turn.ended = true;
    await this.#notify(message);
    if (this.#closed) return;
    if (message.type === 'agent_settled') {
      if (this.#interactions.size) throw fail('pi_pending_interaction');
      if (turn.interaction) throw fail('pi_interaction_required');
      const final = turn.final;
      if (!turn.ended || !object(final) || !Array.isArray(final.content) ||
        !['stop', 'error', 'aborted', 'length', 'toolUse', 'deferred'].includes(final.stopReason)) throw fail('pi_missing_terminal');
      let outputText = '';
      for (const block of final.content) {
        if (!object(block)) throw fail('pi_invalid_content');
        if (block.type === 'text') { if (!text(block.text, 65536)) throw fail('pi_output_limit'); outputText += block.text; }
        else if (!['thinking', 'toolCall'].includes(block.type)) throw fail('pi_invalid_content');
      }
      if (!text(outputText, 65536)) throw fail('pi_output_limit');
      const stopReason = turn.cancelled ? 'aborted' : final.errorMessage ? 'error' : final.stopReason;
      if (stopReason === 'stop' && (!outputText.trim() || final.content.some(block => block.type === 'toolCall'))) throw fail('pi_invalid_terminal');
      turn.terminal = Object.freeze({stopReason, outputText: stopReason === 'stop' ? outputText : ''}); this.#settle(turn);
    }
  }
  #fail(code) {
    if (this.#closed) return; this.#closed = true; this.#failure = fail(code);
    for (const [event, listener] of Object.entries(this.#listeners)) if (event !== 'writeClose') this.#readable.off(event, listener);
    this.#writable.off('error', this.#listeners.error); this.#writable.off('close', this.#listeners.writeClose);
    for (const pending of this.#pending.values()) { clearTimeout(pending.timer); pending.reject(this.#failure); }
    this.#pending.clear(); for (const entry of this.#writes) entry.reject(this.#failure);
    for (const entry of this.#interactions.values()) { clearTimeout(entry.timer); entry.controller.abort(); }
    this.#interactions.clear();
    this.#writes = []; this.#queue = []; this.#writeBytes = 0; this.#queuedBytes = 0;
    if (this.#turn) { clearTimeout(this.#turn.timer); this.#turn.settled.reject(this.#failure); }
    try { this.#onClose(this.#failure); } catch {}
  }
}
