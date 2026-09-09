import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomBytes} from 'node:crypto';
import {launchProtocol, RuntimeError} from '../agent-runtime/index.mjs';
import {PiRpcClient, PiRpcError} from '../agent-pi-rpc/client.mjs';
import {BRIDGE_PROFILE, BRIDGE_ENV, BRIDGE_TITLE, TOOL_KINDS} from './bridge-contract.mjs';

const text = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const id = value => text(value, 128) && /^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(value);
const kinds = Object.freeze({read: 'read', write: 'edit', edit: 'edit'});
const usage = () => ({tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0});
export class PiProviderError extends Error { constructor(code) { super(code); this.name = 'PiProviderError'; this.code = code; } }
const fault = code => new PiProviderError(code);
function bounded(callback, value, signal) {
  if (!callback) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const abort = () => finish(fault('pi_provider_stopped'));
    const timer = setTimeout(() => finish(fault('pi_progress_timeout')), 1000);
    function finish(error, result) { clearTimeout(timer); signal.removeEventListener('abort', abort); error ? reject(error) : resolve(result); }
    signal.addEventListener('abort', abort, {once: true}); if (signal.aborted) { abort(); return; }
    Promise.resolve().then(() => { if (signal.aborted) throw fault('pi_provider_stopped'); return callback(value, {signal}); })
      .then(value => finish(null, value), () => finish(fault('pi_progress_failed')));
  });
}

/** COMPONENT candidate, not automatically registered in a service. Composition
 * supplies a trusted native `pi --mode rpc` entry and environment unchanged.
 * No tools/Skill/config are secretly disabled and no model/provider fallback is
 * selected. An explicitly installed native bridge adapts permission requests
 * to the existing callback Port; this does not make Pi's transport ACP.
 */
export function createPiProvider({id: providerId, executable, args = [], env = {}, bridge, custodyProfile} = {}) {
  if (!id(providerId) || !text(executable, 8192) || !path.isAbsolute(executable) || !Array.isArray(args) || args.length > 128 ||
    args.some(value => !text(value, 32768)) || !object(env) || Object.keys(env).length > 128 ||
    Object.entries(env).some(([key, value]) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key) || !text(value, 65536))) throw fault('pi_invalid_configuration');
  if (Object.hasOwn(env, BRIDGE_ENV) || bridge !== undefined && (!object(bridge) ||
    Object.keys(bridge).some(key => !['sdkEntry', 'shellPath'].includes(key)) || !text(bridge.sdkEntry, 8192) || !path.isAbsolute(bridge.sdkEntry) ||
    bridge.shellPath !== undefined && (!text(bridge.shellPath, 8192) || !path.isAbsolute(bridge.shellPath)))) throw fault('pi_invalid_configuration');
  if (bridge && args.length > 126) throw fault('pi_invalid_configuration');
  const config = structuredClone({executable, args, env}), bridgeConfig = bridge === undefined ? null : structuredClone(bridge);
  if (custodyProfile !== undefined && (!bridgeConfig || !object(custodyProfile) ||
      Object.keys(custodyProfile).sort().join(',') !== 'eligible,id,scope' || !id(custodyProfile.id) ||
      custodyProfile.scope !== 'inherited-process-group' || typeof custodyProfile.eligible !== 'boolean')) throw fault('pi_invalid_configuration');
  if (Buffer.byteLength(JSON.stringify(config)) > 100 * 1024) throw fault('pi_invalid_configuration');
  function start({cwd, deadline, prompt, onProgress, onPermission, executionContext} = {}) {
    if (!text(cwd, 8192) || !path.isAbsolute(cwd) || !Number.isSafeInteger(deadline) || deadline <= Date.now() || deadline - Date.now() > 86400000 ||
      !text(prompt, 256 * 1024) || !prompt.trim() || onProgress !== undefined && typeof onProgress !== 'function') throw fault('pi_invalid_input');
    if (onPermission !== undefined && typeof onPermission !== 'function') throw fault('pi_invalid_input');
    if (onPermission !== undefined && !bridgeConfig) throw fault('pi_permission_bridge_unavailable');
    let runtime, stopping = false, settled = false, scopeUnknown = false, callbackFailure, sessionId = null, prompting = false;
    const unproven = () => {
      scopeUnknown = true;
      if (executionContext) executionContext.extraScope('pi_tool_scope_unproven');
    };
    const nonce = bridgeConfig ? randomBytes(32).toString('hex') : null, calls = new Map();
    let bridgeReady = false, supportedTools = new Set(), resolveReady;
    const ready = new Promise(resolve => { resolveReady = resolve; });
    let resolveStarted; const started = new Promise(resolve => { resolveStarted = resolve; });
    const observation = new AbortController();
    let progress = {phase: 'starting', observedAt: new Date().toISOString(), tool: null};
    const snapshot = () => structuredClone(progress);
    async function publish(phase) {
      progress = {...progress, phase, observedAt: new Date().toISOString()};
      try { await bounded(onProgress, snapshot(), observation.signal); }
      catch (error) { callbackFailure = error; throw error; }
    }
    async function event(message) {
      if (message.type.startsWith('tool_execution_')) {
        if (!text(message.toolCallId, 128) || !message.toolCallId || !text(message.toolName, 128)) {
          unproven(); throw fault('pi_invalid_progress');
        }
        if (bridgeConfig) {
          if (!bridgeReady) { unproven(); throw fault('pi_bridge_not_ready'); }
          let call = calls.get(message.toolCallId);
          if (message.type === 'tool_execution_start') {
            if (call || calls.size >= 4096) { unproven(); throw fault('pi_invalid_progress'); }
            // Custody must retain an obligation if the service dies BEFORE
            // the exact bridge refusal arrives. Legacy live-handle cleanup
            // can instead wait for that refusal; missing proof still vetoes it
            // below. No late refusal erases a durable custody scope obligation.
            if (executionContext && !supportedTools.has(message.toolName)) unproven();
            call = {name: message.toolName, authorized: false, safe: false, selected: false, permission: false, notExecuted: false, ended: false}; calls.set(message.toolCallId, call);
          }
          if (!call || call.name !== message.toolName || call.ended) { unproven(); throw fault('pi_invalid_progress'); }
          if (message.type === 'tool_execution_end') {
            if (typeof message.isError !== 'boolean') { unproven(); throw fault('pi_invalid_progress'); }
            call.ended = true;
            // A replacement tool that bypasses the installed wrapper must not
            // inherit its permission/owned-shell guarantee from just a name.
            if (!call.safe || call.notExecuted && !message.isError) { unproven(); throw fault('pi_execution_scope_unproven'); }
          }
        } else {
        // Pi native bash/powershell use detached process groups. Unknown/custom
        // tools can spawn too. Seeing their execution creates an unproven
        // cleanup obligation; never turn inherited-group cleanup into success.
        if (!Object.hasOwn(kinds, message.toolName)) { unproven(); throw fault('pi_execution_scope_unproven'); }
        if (message.type === 'tool_execution_end' && typeof message.isError !== 'boolean') throw fault('pi_invalid_progress');
        }
        if (stopping || settled) return;
        progress = {...progress, tool: {id: message.toolCallId, kind: TOOL_KINDS[message.toolName] ?? 'other',
          status: message.type === 'tool_execution_end' ? message.isError ? 'failed' : 'completed' : 'in_progress'}};
        await publish('running');
      } else if (!stopping && !settled && ['agent_start', 'auto_retry_start', 'compaction_start', 'auto_compaction_start'].includes(message.type)) await publish('running');
      // No raw args/tool results/thinking/session file/model credentials cross
      // the public progress channel. Native usage is not guessed as billing.
    }
    async function interaction(message, context) {
      if (!bridgeConfig) return;
      if (message.method !== 'notify' && !(message.method === 'confirm' && message.title === BRIDGE_TITLE)) return;
      let value;
      try { value = JSON.parse(message.message); } catch { return; }
      if (value?.profile !== BRIDGE_PROFILE) return;
      if (value.nonce !== nonce || value.cwd !== cwd || value.deadline !== deadline) { unproven(); throw fault('pi_bridge_binding_mismatch'); }
      if (message.method === 'notify' && value.type === 'ready') {
        if (bridgeReady || prompting || value.scope !== 'inherited-process-group' || !Array.isArray(value.tools) || value.tools.length > 7 ||
          value.tools.some(name => !Object.hasOwn(TOOL_KINDS, name)) || new Set(value.tools).size !== value.tools.length) throw fault('pi_bridge_invalid_ready');
        supportedTools = new Set(value.tools); bridgeReady = true; resolveReady(); return;
      }
      const call = calls.get(value.toolCallId);
      if (!bridgeReady || !call || call.name !== value.toolName || call.ended) { unproven(); throw fault('pi_bridge_unmatched_call'); }
      if (message.method === 'notify' && value.type === 'definition-selected' && supportedTools.has(value.toolName) && !call.safe) {
        call.selected = true; call.safe = true; return;
      }
      if (message.method === 'notify' && value.type === 'not-executed' && value.disposition === 'truncated-assistant' && !call.safe) {
        call.notExecuted = true; call.safe = true; return;
      }
      if (message.method === 'notify' && value.type === 'blocked' && !call.safe && !call.permission && !call.notExecuted) {
        call.notExecuted = true; call.safe = true; return;
      }
      if (message.method !== 'confirm' || value.type !== 'permission' || value.sessionId !== sessionId || !supportedTools.has(value.toolName) ||
        call.permission || call.notExecuted || !object(value.input) || Buffer.byteLength(JSON.stringify(value.input)) > 64 * 1024) { unproven(); throw fault('pi_bridge_invalid_permission'); }
      // Receipt of the execute wrapper's request proves the call has not yet
      // run and can only use that native definition/operations after our reply.
      call.safe = true; call.permission = true;
      const reject = {handled: true, confirmed: false};
      if (!onPermission || stopping || observation.signal.aborted || context.signal.aborted || Date.now() >= deadline) return reject;
      const request = {sessionId, toolCall: {toolCallId: value.toolCallId, kind: TOOL_KINDS[value.toolName], title: value.toolName,
        rawInput: structuredClone(value.input), _meta: {provider: 'pi', toolName: value.toolName}},
      options: [{optionId: 'allow-once', name: '允许本次已批准操作', kind: 'allow_once'}, {optionId: 'reject-once', name: '拒绝本次操作', kind: 'reject_once'}]};
      const signal = AbortSignal.any([context.signal, observation.signal]);
      let answer;
      try { answer = await onPermission(request, {signal}); } catch { return reject; }
      if (signal.aborted || stopping || Date.now() >= deadline) return reject;
      const selected = answer?.outcome?.outcome === 'selected' && answer.outcome.optionId === 'allow-once';
      // A rejected request never ran. For an allowed shell/unknown effect, the
      // original synchronous Store obligation must precede the confirmed reply.
      if (selected && executionContext && !['read', 'write', 'edit', 'find', 'grep', 'ls'].includes(value.toolName))
        executionContext.extraScope('pi_shell_extra_scope');
      call.authorized = selected; return {handled: true, confirmed: selected};
    }
    const completion = (async () => {
      let status = 'failed', reason = 'pi_provider_failed', stopReason = null, outputText = '', originalCleanup = null;
      try {
        const launch = structuredClone(config);
        if (bridgeConfig) {
          launch.args.push('--extension', fileURLToPath(new URL('./native-bridge.mjs', import.meta.url)));
          launch.env[BRIDGE_ENV] = JSON.stringify({profile: BRIDGE_PROFILE, ...bridgeConfig, nonce, cwd, deadline});
        }
        runtime = await launchProtocol({...launch, cwd, deadline, executionContext, createClient: connection => new PiRpcClient({...connection, onEvent: event,
          onInteraction: interaction,
          requestTimeoutMs: Math.max(1, Math.min(10000, deadline - Date.now()))})});
        resolveStarted(runtime.started); if (stopping) throw fault('pi_provider_stopped');
        await publish('initializing');
        if (bridgeConfig) {
          let timer;
          try { await Promise.race([ready, runtime.completion.then(() => { throw fault('pi_bridge_not_ready'); }),
            new Promise((_, reject) => { timer = setTimeout(() => reject(fault('pi_bridge_not_ready')), Math.max(1, Math.min(10000, deadline - Date.now()))); })]); }
          finally { clearTimeout(timer); }
        }
        const state = await runtime.client.getState();
        if (state.isStreaming || state.isCompacting || state.pendingMessageCount !== 0 || state.messageCount !== 0) throw fault('pi_session_not_fresh');
        sessionId = state.sessionId; if (stopping) throw fault('pi_provider_stopped');
        await publish('running'); prompting = true;
        const remaining = deadline - Date.now(); if (remaining <= 0) throw fault('pi_provider_deadline');
        const terminal = await runtime.client.prompt(prompt, {timeoutMs: remaining});
        if (Date.now() >= deadline) throw fault('pi_provider_deadline');
        stopReason = terminal.stopReason === 'stop' ? 'end_turn' : terminal.stopReason === 'aborted' ? 'cancelled' : terminal.stopReason;
        status = stopReason === 'end_turn' ? 'completed' : stopReason === 'cancelled' ? 'cancelled' : 'failed';
        reason = 'pi_agent_' + terminal.stopReason; outputText = terminal.outputText;
      } catch (error) {
        if (error instanceof RuntimeError) originalCleanup = error.completion ?? null;
        reason = error instanceof RuntimeError || error instanceof PiRpcError || error instanceof PiProviderError ? error.code : 'pi_provider_failed';
        if (callbackFailure instanceof PiProviderError) reason = callbackFailure.code;
      } finally {
        resolveStarted(runtime?.started ?? null); progress = {...progress, phase: 'stopping', observedAt: new Date().toISOString()};
        observation.abort(); if (runtime) originalCleanup = await runtime.stop();
      }
      if (stopping) { status = 'cancelled'; reason = 'pi_provider_stopped'; }
      else if (Date.now() >= deadline && status !== 'completed') { status = 'failed'; reason = 'pi_provider_deadline'; }
      if (bridgeConfig && [...calls.values()].some(call => !call.safe)) unproven();
      if (!originalCleanup?.cleaned || scopeUnknown) { status = 'unknown'; reason = scopeUnknown ? 'pi_execution_scope_unproven' : 'cleanup_unconfirmed'; outputText = ''; }
      settled = true; progress = {...progress, phase: 'terminal', observedAt: new Date().toISOString()};
      return Object.freeze({providerId, status, stopReason, reason, sessionId, outputText, usage: usage(),
        cleanup: scopeUnknown ? null : originalCleanup,
        // Preserve observations separately; this is NOT the cleanup proof the
        // Application consumes to release an execution directory/capacity.
        ...(scopeUnknown ? {runtimeCleanup: originalCleanup} : {})});
    })();
    const stop = () => {
      if (settled || stopping) return completion;
      stopping = true;
      if (executionContext?.stop) void executionContext.stop();
      if (runtime) {
        // Give the original protocol at most 250ms to clear queue then abort;
        // unreachable/stuck protocol never blocks the owned guard stop.
        void (async () => { let timer;
          try { if (prompting && !runtime.client.closed) await Promise.race([runtime.client.cancel(),
            new Promise(resolve => { timer = setTimeout(resolve, 250); })]); }
          catch {} finally { clearTimeout(timer); observation.abort(); await runtime.stop(); }
        })();
      } else observation.abort();
      return completion;
    };
    return Object.freeze({started, completion, stop, snapshot});
  }
  return Object.freeze({id: providerId, profile: 'ordinary-user', maturity: 'COMPONENT',
    ...(custodyProfile === undefined ? {} : {custodyProfile: Object.freeze(structuredClone(custodyProfile))}),
    capabilities: Object.freeze({transport: 'pi-rpc', permission: bridgeConfig ? 'native-extension' : 'unavailable', cleanup: 'inherited-process-group'}), start});
}
