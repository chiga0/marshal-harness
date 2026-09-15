import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {randomBytes, createHash} from 'node:crypto';
import {BRIDGE_PROFILE, BRIDGE_ENV, BRIDGE_TITLE, TOOL_FACTORIES, QUESTION_TOOL, QUESTION_PROFILE, text, object} from './bridge-contract.mjs';
import {createInheritedShellOperations} from './shell-operations.mjs';

/** A checked-in, explicitly selected Pi extension, loaded by native Pi itself.
 * Native SDK definitions retain schemas, prompts, file operations and results;
 * permission is checked at execute AFTER any mutable tool_call hooks. */
export function installNativeBridge(pi, {config, sdk, shell} = {}) {
  if (!object(config) || config.profile !== BRIDGE_PROFILE || !/^[a-f0-9]{64}$/.test(config.nonce ?? '') || !path.isAbsolute(config.cwd ?? '') ||
    !Number.isSafeInteger(config.deadline) || !sdk || typeof shell?.getShellConfig !== 'function' || typeof shell?.getShellEnv !== 'function' ||
    typeof pi?.on !== 'function' || typeof pi?.registerTool !== 'function' || typeof pi?.getActiveTools !== 'function' || typeof pi?.setActiveTools !== 'function')
    throw Error('pi_bridge_invalid_configuration');
  const wrapped = new Map(), seen = new Set(), calls = new Map(), truncated = new Map(); let ready = false;
  const validContext = ctx => ready && ctx?.cwd === config.cwd && Date.now() < config.deadline;
  const envelope = (type, details = {}) => ({profile: BRIDGE_PROFILE, nonce: config.nonce, cwd: config.cwd, deadline: config.deadline, type, ...details});
  const notify = (ctx, type, details) => ctx.ui.notify(JSON.stringify(envelope(type, details)), 'info');
  const questions = config.runtimeQuestions;
  if (questions !== undefined && (!object(questions) || questions.profile !== QUESTION_PROFILE ||
      !/^sha256:[a-f0-9]{64}$/.test(questions.policyDigest ?? '') || !Number.isSafeInteger(questions.maxWaitMs) ||
      questions.maxWaitMs < 1 || questions.maxWaitMs > 120000)) throw Error('pi_bridge_invalid_questions');
  const askDefinition = () => ({name: QUESTION_TOOL, label: 'Ask user',
    description: '仅在已批准业务缺少必要输入时向用户提问，等待明确答案后继续原任务；不得借此请求工具权限或改变验收。',
    parameters: {type: 'object', properties: {prompt: {type: 'string', minLength: 1, maxLength: 2048},
      kind: {type: 'string', enum: ['input', 'select']}, options: {type: 'array', items: {type: 'string'}, maxItems: 16}},
      required: ['prompt', 'kind', 'options'], additionalProperties: false},
    async execute(callId, params, signal, _update, ctx) {
      if (!questions || !validContext(ctx) || signal?.aborted || !text(params.prompt, 2048) || !params.prompt.trim() ||
        !['input', 'select'].includes(params.kind) || !Array.isArray(params.options) || params.options.length > 16 ||
        params.options.some(option => !text(option, 4096) || !option.trim()) || new Set(params.options).size !== params.options.length ||
        params.kind === 'input' && params.options.length !== 0 || params.kind === 'select' && params.options.length === 0)
        throw Error('pi_business_question_invalid');
      const questionNonce = randomBytes(32).toString('hex'), sessionId = ctx.sessionManager.getSessionId();
      const binding = {toolName: QUESTION_TOOL, toolCallId: callId, sessionId, questionNonce, policyDigest: questions.policyDigest};
      const title = JSON.stringify(envelope('business-question', {...binding, ...structuredClone(params)}));
      const timeout = Math.max(1, Math.min(questions.maxWaitMs, config.deadline - Date.now())), options = {signal, timeout};
      const answer = params.kind === 'input' ? await ctx.ui.input(title, '', options) : await ctx.ui.select(title, params.options, options);
      if (signal?.aborted || !validContext(ctx) || !text(answer, 4096) || !answer.trim() || params.kind === 'select' && !params.options.includes(answer))
        throw Error('pi_business_answer_missing');
      const answerDigest = 'sha256:' + createHash('sha256').update(answer).digest('hex');
      const acknowledged = await ctx.ui.confirm(BRIDGE_TITLE, JSON.stringify(envelope('business-answer-ack', {...binding, answerDigest})),
        {signal, timeout: Math.max(1, Math.min(5000, config.deadline - Date.now()))});
      if (acknowledged !== true || signal?.aborted || !validContext(ctx)) throw Error('pi_business_answer_unacknowledged');
      return {content: [{type: 'text', text: answer}], details: {businessAnswer: true}};
    }});
  pi.on('session_start', async (_event, ctx) => {
    if (ready || ctx?.cwd !== config.cwd || Date.now() >= config.deadline) throw Error('pi_bridge_invalid_session');
    const active = pi.getActiveTools();
    if (!Array.isArray(active) || active.length > 128 || new Set(active).size !== active.length || active.some(name => !text(name, 128)))
      throw Error('pi_bridge_invalid_tools');
    const operations = createInheritedShellOperations({cwd: config.cwd, deadline: config.deadline,
      resolveShell: () => shell.getShellConfig(config.shellPath), environment: () => shell.getShellEnv()});
    if (active.includes(QUESTION_TOOL)) throw Error('pi_bridge_question_tool_collision');
    const selected = questions ? [...active, QUESTION_TOOL] : active;
    for (const name of selected) {
      const factory = TOOL_FACTORIES[name]; if (!factory && name !== QUESTION_TOOL) continue;
      if (factory && typeof sdk[factory] !== 'function') throw Error('pi_bridge_sdk_incompatible');
      const original = name === QUESTION_TOOL ? askDefinition() : sdk[factory](config.cwd, name === 'bash' ? {operations} : undefined);
      if (original?.name !== name || typeof original.execute !== 'function') throw Error('pi_bridge_sdk_incompatible');
      const tool = {...original, prepareArguments(params) {
        // Agent-core has selected this exact Tool object, but has not validated
        // its arguments or entered execute yet. Match the original in-process
        // args reference, not model prose or an isError/error-string heuristic.
        const candidates = [...calls.values()].filter(call => call.name === name && call.args === params && !call.selected && !call.ended);
        if (candidates.length !== 1) throw Error('pi_bridge_unmatched_preparation');
        const call = candidates[0]; call.selected = true;
        notify(call.ctx, 'definition-selected', {toolCallId: call.id, toolName: name});
        delete call.args; delete call.ctx;
        return original.prepareArguments ? original.prepareArguments(params) : params;
      }, async execute(callId, params, signal, update, toolContext) {
        if (!validContext(toolContext) || signal?.aborted || !text(callId, 128) || !callId || seen.has(callId) || seen.size >= 4096)
          throw Error('pi_bridge_invalid_call');
        seen.add(callId);
        const input = structuredClone(params);
        if (!object(input) || Buffer.byteLength(JSON.stringify(input)) > 64 * 1024) throw Error('pi_bridge_invalid_arguments');
        const sessionId = toolContext.sessionManager.getSessionId();
        if (!text(sessionId, 256) || !sessionId) throw Error('pi_bridge_invalid_session');
        if (name === QUESTION_TOOL) return original.execute(callId, input, signal, update, toolContext);
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
    pi.setActiveTools(selected); ready = true;
    notify(ctx, 'ready', {tools: [...wrapped.keys()].sort(), scope: 'inherited-process-group'});
  });
  pi.on('message_end', event => {
    const message = event.message;
    if (message?.role !== 'assistant') return;
    truncated.clear();
    if (message.stopReason !== 'length' || !Array.isArray(message.content)) return;
    for (const item of message.content) if (item.type === 'toolCall' && text(item.id, 128) && item.id && text(item.name, 128)) {
      if (truncated.has(item.id) || truncated.size >= 4096) throw Error('pi_bridge_invalid_truncated_calls');
      truncated.set(item.id, item.name);
    }
  });
  pi.on('tool_execution_start', (event, ctx) => {
    if (!ready || !text(event.toolCallId, 128) || !event.toolCallId || !text(event.toolName, 128) || calls.has(event.toolCallId) || calls.size >= 4096)
      throw Error('pi_bridge_invalid_call');
    calls.set(event.toolCallId, {id: event.toolCallId, name: event.toolName, args: event.args, ctx, selected: false, ended: false});
  });
  pi.on('tool_execution_end', (event, ctx) => {
    const call = calls.get(event.toolCallId);
    if (!call || call.name !== event.toolName || call.ended) throw Error('pi_bridge_unmatched_end');
    call.ended = true;
    // Native agent-core never selects/executes any tool in a length-truncated
    // assistant response. Both original producer events and exact IDs are
    // needed; an arbitrary error from a replacement definition proves nothing.
    if (!call.selected && !seen.has(call.id) && truncated.get(call.id) === call.name && event.isError === true)
      notify(ctx, 'not-executed', {toolCallId: call.id, toolName: call.name, disposition: 'truncated-assistant'});
    delete call.args; delete call.ctx;
    truncated.delete(call.id);
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
