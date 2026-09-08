import path from 'node:path';
import {launchAcp, RuntimeError} from '../agent-runtime/index.mjs';
import {AcpError} from '../agent-acp/client.mjs';

export const MAX_OUTPUT_TEXT_BYTES = 64 * 1024;
const MAX_UPDATES = 4096, CALLBACK_WAIT_MS = 1000;
const text = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const unknownUsage = () => ({tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0});
export class AcpProviderError extends Error {
  constructor(code) { super(code); this.name = 'AcpProviderError'; this.code = code; }
}
const error = code => new AcpProviderError(code);
function bounded(callback, value, signal) {
  if (!callback) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const abort = () => finish(error('provider_stopped'));
    const timer = setTimeout(() => finish(error('provider_progress_timeout')), CALLBACK_WAIT_MS);
    function finish(failure, result) { clearTimeout(timer); signal.removeEventListener('abort', abort); failure ? reject(failure) : resolve(result); }
    signal.addEventListener('abort', abort, {once: true});
    if (signal.aborted) { abort(); return; }
    Promise.resolve().then(() => {
      if (signal.aborted) throw error('provider_stopped');
      return callback(value, {signal});
    }).then(value => finish(null, value), () => finish(error('provider_progress_failed')));
  });
}

/** Trusted composition only; no brand switch, ambient env copy or Task authority. */
export function createAcpProvider({id, executable, args = [], env = {}} = {}) {
  if (!text(id, 128) || !/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(id) || !text(executable, 8192) || !path.isAbsolute(executable) ||
    !Array.isArray(args) || args.length > 128 || args.some(value => !text(value, 32768)) || !object(env) || Object.keys(env).length > 128 ||
    Object.entries(env).some(([key, value]) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key) || !text(value, 65536))) throw error('provider_invalid_configuration');
  const config = structuredClone({executable, args, env});
  if (Buffer.byteLength(JSON.stringify(config)) > 100 * 1024) throw error('provider_invalid_configuration');
  function start({cwd, deadline, prompt, onProgress, onPermission} = {}) {
    if (!text(cwd, 8192) || !path.isAbsolute(cwd) || !Number.isSafeInteger(deadline) || deadline <= Date.now() || deadline - Date.now() > 86400000 ||
      !text(prompt, 256 * 1024) || !prompt.trim() || onProgress !== undefined && typeof onProgress !== 'function' ||
      onPermission !== undefined && typeof onPermission !== 'function') throw error('provider_invalid_input');
    let runtime, sessionId = null, stopping = false, settled = false, outputText = '', outputBytes = 0, updates = 0, updateFailure;
    let resolveStarted; const started = new Promise(resolve => { resolveStarted = resolve; });
    const observation = new AbortController();
    let progress = {phase: 'starting', observedAt: new Date().toISOString(), tool: null};
    const snapshot = () => structuredClone(progress);
    async function phase(value) {
      progress = {...progress, phase: value, observedAt: new Date().toISOString()};
      await bounded(onProgress, snapshot(), observation.signal);
    }
    async function update(event) {
      if (stopping || settled) return;
      if (++updates > MAX_UPDATES) throw error('provider_progress_limit');
      const item = event.update;
      progress = {...progress, observedAt: new Date().toISOString()};
      if (item.sessionUpdate === 'agent_message_chunk') {
        if (!object(item.content) || item.content.type !== 'text') return;
        if (!text(item.content.text, MAX_OUTPUT_TEXT_BYTES) || outputBytes + Buffer.byteLength(item.content.text) > MAX_OUTPUT_TEXT_BYTES) throw error('provider_output_limit');
        outputText += item.content.text; outputBytes += Buffer.byteLength(item.content.text);
      } else if (['tool_call', 'tool_call_update'].includes(item.sessionUpdate)) {
        if (!text(item.toolCallId, 128) || !item.toolCallId) throw error('provider_invalid_progress');
        const prior = progress.tool?.id === item.toolCallId ? progress.tool : null;
        const status = item.status ?? prior?.status ?? 'pending';
        const kind = item.kind ?? prior?.kind ?? 'other';
        if (!['pending', 'in_progress', 'completed', 'failed'].includes(status) ||
          !['read', 'edit', 'delete', 'move', 'search', 'execute', 'think', 'fetch', 'other'].includes(kind)) throw error('provider_invalid_progress');
        progress = {...progress, tool: {id: item.toolCallId, kind, status}};
      } else {
        // Thinking, raw tool inputs/outputs, _meta and provider-specific usage
        // are not normalized public progress or billable token evidence.
        return;
      }
      await bounded(onProgress, snapshot(), observation.signal);
    }
    function permission(params, context) {
      if (stopping || settled || !onPermission || Date.now() >= deadline) return {outcome: {outcome: 'cancelled'}};
      const toolCall = {};
      for (const key of ['toolCallId', 'title', 'kind', 'status', 'rawInput']) if (Object.hasOwn(params.toolCall, key)) toolCall[key] = structuredClone(params.toolCall[key]);
      // This callback is trusted policy, not the progress/UI channel. It needs
      // actual tool input to authorize the bound request. Never forward _meta.
      return onPermission({sessionId: params.sessionId, toolCall, options: structuredClone(params.options)}, context);
    }
    const completion = (async () => {
      let status = 'failed', reason = 'provider_failed', stopReason = null, cleanup = null;
      try {
        runtime = await launchAcp({...config, cwd, deadline, onUpdate: async event => {
          try { await update(event); } catch (failure) { updateFailure = failure; throw failure; }
        }, onPermission: permission});
        resolveStarted(runtime.started);
        if (stopping) throw error('provider_stopped');
        await phase('initializing');
        await runtime.client.initialize();
        if (stopping) throw error('provider_stopped');
        await phase('session');
        sessionId = (await runtime.client.newSession({cwd})).sessionId;
        if (stopping) throw error('provider_stopped');
        await phase('running');
        const remaining = deadline - Date.now();
        if (remaining <= 0) throw error('provider_deadline');
        const terminal = await runtime.client.prompt(sessionId, [{type: 'text', text: prompt}], {timeoutMs: remaining});
        stopReason = terminal.stopReason;
        if (Date.now() >= deadline) throw error('provider_deadline');
        status = stopReason === 'end_turn' ? 'completed' : stopReason === 'cancelled' ? 'cancelled' : 'failed';
        reason = 'agent_' + stopReason;
      } catch (failure) {
        cleanup = failure instanceof RuntimeError ? failure.completion ?? null : null;
        reason = failure instanceof AcpProviderError || failure instanceof RuntimeError || failure instanceof AcpError ? failure.code : 'provider_failed';
        if (updateFailure instanceof AcpProviderError) reason = updateFailure.code;
      } finally {
        resolveStarted(runtime?.started ?? null);
        progress = {...progress, phase: 'stopping', observedAt: new Date().toISOString()};
        observation.abort();
        if (runtime) cleanup = await runtime.stop();
      }
      if (stopping) { status = 'cancelled'; reason = 'provider_stopped'; }
      else if (Date.now() >= deadline && status !== 'completed') { status = 'failed'; reason = 'provider_deadline'; }
      if (!cleanup?.cleaned) { status = 'unknown'; reason = 'cleanup_unconfirmed'; }
      settled = true; progress = {...progress, phase: 'terminal', observedAt: new Date().toISOString()};
      return Object.freeze({providerId: id, status, stopReason, reason, sessionId, outputText, usage: unknownUsage(), cleanup});
    })();
    const stop = () => {
      if (settled) return completion;
      stopping = true; observation.abort();
      if (runtime) {
        if (sessionId && !runtime.client.closed) void runtime.client.cancel(sessionId).catch(() => {});
        void runtime.stop();
      }
      return completion;
    };
    return Object.freeze({started, completion, stop, snapshot});
  }
  return Object.freeze({id, profile: 'ordinary-user', start});
}
