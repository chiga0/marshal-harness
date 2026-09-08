import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {BRIDGE_PROFILE, BRIDGE_ENV, BRIDGE_TITLE, TOOL_FACTORIES, text, object} from './bridge-contract.mjs';
import {createInheritedShellOperations} from './shell-operations.mjs';

/** A checked-in, explicitly selected Pi extension, loaded by native Pi itself.
 * Native SDK definitions retain schemas, prompts, file operations and results;
 * permission is checked at execute AFTER any mutable tool_call hooks. */
export function installNativeBridge(pi, {config, sdk, shell} = {}) {
  if (!object(config) || config.profile !== BRIDGE_PROFILE || !/^[a-f0-9]{64}$/.test(config.nonce ?? '') || !path.isAbsolute(config.cwd ?? '') ||
    !Number.isSafeInteger(config.deadline) || !sdk || typeof shell?.getShellConfig !== 'function' || typeof shell?.getShellEnv !== 'function' ||
    typeof pi?.on !== 'function' || typeof pi?.registerTool !== 'function' || typeof pi?.getActiveTools !== 'function' || typeof pi?.setActiveTools !== 'function')
    throw Error('pi_bridge_invalid_configuration');
  const wrapped = new Map(), seen = new Set(); let ready = false;
  const validContext = ctx => ready && ctx?.cwd === config.cwd && Date.now() < config.deadline;
  const envelope = (type, details = {}) => ({profile: BRIDGE_PROFILE, nonce: config.nonce, cwd: config.cwd, deadline: config.deadline, type, ...details});
  const notify = (ctx, type, details) => ctx.ui.notify(JSON.stringify(envelope(type, details)), 'info');
  pi.on('session_start', async (_event, ctx) => {
    if (ready || ctx?.cwd !== config.cwd || Date.now() >= config.deadline) throw Error('pi_bridge_invalid_session');
    const active = pi.getActiveTools();
    if (!Array.isArray(active) || active.length > 128 || new Set(active).size !== active.length || active.some(name => !text(name, 128)))
      throw Error('pi_bridge_invalid_tools');
    const operations = createInheritedShellOperations({cwd: config.cwd, deadline: config.deadline,
      resolveShell: () => shell.getShellConfig(config.shellPath), environment: () => shell.getShellEnv()});
    for (const name of active) {
      const factory = TOOL_FACTORIES[name]; if (!factory) continue;
      if (typeof sdk[factory] !== 'function') throw Error('pi_bridge_sdk_incompatible');
      const original = sdk[factory](config.cwd, name === 'bash' ? {operations} : undefined);
      if (original?.name !== name || typeof original.execute !== 'function') throw Error('pi_bridge_sdk_incompatible');
      const tool = {...original, async execute(callId, params, signal, update, toolContext) {
        if (!validContext(toolContext) || signal?.aborted || !text(callId, 128) || !callId || seen.has(callId) || seen.size >= 4096)
          throw Error('pi_bridge_invalid_call');
        seen.add(callId);
        const input = structuredClone(params);
        if (!object(input) || Buffer.byteLength(JSON.stringify(input)) > 64 * 1024) throw Error('pi_bridge_invalid_arguments');
        const sessionId = toolContext.sessionManager.getSessionId();
        if (!text(sessionId, 256) || !sessionId) throw Error('pi_bridge_invalid_session');
        const allowed = await toolContext.ui.confirm(BRIDGE_TITLE,
          JSON.stringify(envelope('permission', {sessionId, toolCallId: callId, toolName: name, input})),
          {signal, timeout: Math.max(1, Math.min(10000, config.deadline - Date.now()))});
        if (allowed !== true || signal?.aborted || !validContext(toolContext)) throw Error('pi_bridge_permission_denied');
        // Execute the exact copy the parent evaluated. No later extension hook
        // can mutate that object across this permission/execute boundary.
        return original.execute(callId, input, signal, update, toolContext);
      }};
      wrapped.set(name, tool); pi.registerTool(tool);
    }
    // Registration must not silently enable previously disabled native tools.
    pi.setActiveTools(active); ready = true;
    notify(ctx, 'ready', {tools: [...wrapped.keys()].sort(), scope: 'inherited-process-group'});
  });
  pi.on('tool_call', (event, ctx) => {
    if (!validContext(ctx) || !wrapped.has(event.toolName)) {
      if (ready && text(event.toolCallId, 128) && text(event.toolName, 128))
        notify(ctx, 'blocked', {toolCallId: event.toolCallId, toolName: event.toolName});
      return {block: true, reason: 'pi_bridge_tool_capability_unavailable', terminate: true};
    }
  });
  // RPC exposes no user_bash command. If a trusted extension invokes it, never
  // fall through to Pi's detached default; there is no Task prompt authority.
  pi.on('user_bash', () => ({result: {output: 'pi_bridge_user_shell_not_authorized', exitCode: 1, cancelled: true, truncated: false}}));
}

export default async function nativeBridge(pi) {
  const raw = process.env[BRIDGE_ENV]; delete process.env[BRIDGE_ENV];
  if (!text(raw, 16384)) throw Error('pi_bridge_missing_configuration');
  let config; try { config = JSON.parse(raw); } catch { throw Error('pi_bridge_invalid_configuration'); }
  if (!object(config) || !path.isAbsolute(config.sdkEntry ?? '') || !text(config.sdkEntry, 8192)) throw Error('pi_bridge_invalid_sdk');
  const sdkURL = pathToFileURL(config.sdkEntry);
  // This module location is an explicit trusted composition input, not a model
  // module name or a search through HOME. Capability mismatch fails bootstrap.
  const sdk = await import(sdkURL.href), shell = await import(new URL('./utils/shell.js', sdkURL).href);
  installNativeBridge(pi, {config, sdk, shell});
}
