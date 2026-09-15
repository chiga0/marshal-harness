import { isAbsolute, dirname } from 'node:path';
import { TextDecoder } from 'node:util';
import { MAX_STDOUT_BYTES } from './limits.mjs';

const MAX_STDOUT = MAX_STDOUT_BYTES;
const MAX_FILE = 64 * 1024;
const NAMES = new Set(['normalize.mjs', 'report.mjs']);
const ENV_NAMES = ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TZ', 'TMPDIR'];
const QWEN_PREFIX = 'Decode the following JSON string as the task instructions. Perform that task and return only its requested JSON candidate.\n';

function fail(reason) { throw new Error(reason); }
function validConfig(config) {
  if (!config || Object.keys(config).sort().join(',') !== 'executable,provider' ||
      !['pi', 'qwen'].includes(config.provider) || typeof config.executable !== 'string' ||
      !isAbsolute(config.executable) || /[\0\r\n]/u.test(config.executable)) {
    fail('provider_config_invalid');
  }
}
function validNode(node) {
  if (!node || !NAMES.has(node.file)) fail('provider_node_invalid');
}

// Qwen 0.23.0 preprocesses slash commands and literal @ mentions even with
// stream-json input, before the model tool budget. Transport a reversible JSON
// string with no literal @ and a non-slash prefix, never an @ path or command.
// Original plan.prompt remains persisted; source/config + that original prompt
// deterministically reconstruct this input. No persisted wire-text claim.
function qwenPrompt(prompt) {
  const encoded = QWEN_PREFIX + JSON.stringify(prompt).replaceAll('@', '\\u0040');
  if (Buffer.byteLength(encoded) > 65536) fail('provider_prompt_invalid');
  return encoded;
}

// The controller owns the process and writes the optional encoded prompt to
// stdin. Native model/login settings stay native; no credential is copied.
export function makeCommand(config, node, prompt) {
  validConfig(config);
  validNode(node);
  if (typeof prompt !== 'string' || !prompt.trim() || !prompt.isWellFormed() ||
      prompt.includes('\0') || Buffer.byteLength(prompt) > 32 * 1024) fail('provider_prompt_invalid');
  const env = {};
  for (const key of ENV_NAMES) {
    if (typeof process.env[key] === 'string') env[key] = process.env[key];
  }
  if (!env.HOME || !isAbsolute(env.HOME)) fail('provider_home_unavailable');
  env.PATH ||= `${dirname(process.execPath)}:/usr/bin:/bin`;
  if (config.provider === 'qwen') {
    return {
      command: config.executable,
      // safe-mode removes implicit context/extensions/hooks/MCP, but built-in
      // tools remain advertised. A zero call budget aborts BEFORE execution.
      // Never add --json-schema: its structured_output tool bypasses that budget.
      // --bare loses native auth/model selection; empty core-tools is NOT deny-all.
      args: ['--safe-mode', '--input-format', 'text', '--output-format', 'stream-json',
        '--approval-mode', 'default', '--max-tool-calls', '0', '--max-wall-time', '5m',
        '--no-chat-recording', '--no-openai-logging', '--no-telemetry'],
      env, prompt: qwenPrompt(prompt),
    };
  }
  return {
    command: config.executable,
    args: ['--print', '--mode', 'json', '--no-session', '--no-tools', '--no-extensions', '--no-context-files'],
    env,
  };
}

function candidateText(message) {
  if (!message || message.role !== 'assistant' || message.stopReason !== 'stop' ||
      message.errorMessage || !Array.isArray(message.content)) fail('provider_did_not_stop');
  let text = '';
  for (const part of message.content) {
    if (part?.type === 'text' && typeof part.text === 'string') text += part.text;
    else if (part?.type !== 'thinking') fail('provider_content_invalid');
  }
  if (!text.trim()) fail('provider_content_invalid');
  return text;
}

function outputEvents(rawStdout) {
  let raw;
  try {
    if (Buffer.isBuffer(rawStdout) || rawStdout instanceof Uint8Array) {
      if (rawStdout.byteLength > MAX_STDOUT) fail('provider_output_limit');
      raw = new TextDecoder('utf-8', { fatal: true }).decode(rawStdout);
    } else if (typeof rawStdout === 'string' && rawStdout.isWellFormed()) raw = rawStdout;
    else fail('provider_output_invalid');
  } catch { fail('provider_output_invalid'); }
  if (!raw.trim() || Buffer.byteLength(raw) > MAX_STDOUT) fail('provider_output_limit');
  const lines = raw.trim().split(/\r?\n/u);
  if (lines.length > 8192) fail('provider_output_limit');
  return lines.map(line => {
    let event;
    try { event = JSON.parse(line); } catch { fail('provider_output_invalid'); }
    if (!event || typeof event.type !== 'string' || Array.isArray(event)) fail('provider_output_invalid');
    return event;
  });
}

// Only message_end creates a candidate. agent_end repeats the same message in
// native Pi JSON; it is checked for failures, never counted as another output.
function piText(events) {
  let selected = null;
  let ended = false;
  for (const event of events) {
    if (event.error || event.isError || event.willRetry === true || /error|auto_retry|tool_execution/u.test(event.type)) {
      fail('provider_failed');
    }
    if (event.message?.role === 'assistant' && (event.message.errorMessage ||
        ['error', 'length', 'aborted'].includes(event.message.stopReason))) fail('provider_failed');
    if (event.type === 'message_end') {
      if (event.message?.role === 'assistant') {
        if (ended || selected !== null) fail('provider_multiple_candidates');
        selected = candidateText(event.message);
      } else if (event.message?.role !== 'user') fail('provider_content_invalid');
    }
    if (event.type === 'agent_end') {
      if (ended || selected === null || !Array.isArray(event.messages)) fail('provider_output_invalid');
      ended = true;
      const messages = event.messages.filter((message) => message?.role === 'assistant');
      if (messages.length !== 1 || candidateText(messages[0]) !== selected) fail('provider_terminal_mismatch');
    }
  }
  if (selected === null || !ended) fail('provider_candidate_missing');
  return selected;
}

const wireId = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,128}$/u.test(value);
const wireString = value => typeof value === 'string' && value.trim().length > 0 && value.isWellFormed() && !value.includes('\0');
const stringList = value => Array.isArray(value) && value.every(wireString);

// Validate the full-message schema observed in native 0.23.0, not a Pi/Claude
// protocol. Text stop_reason is normally null: native output does not expose the
// backend finishReason, so this does NOT prove absence of backend length stops.
// A unique final successful result matching the sole candidate is mandatory.
function qwenText(events) {
  let init, selected = null, ended = false;
  const seen = new Set();
  for (const event of events) {
    if (ended || !wireId(event.uuid) || seen.has(event.uuid) || !wireId(event.session_id)) fail('provider_output_invalid');
    seen.add(event.uuid);
    if (Object.hasOwn(event, 'error') || Object.hasOwn(event, 'errors') || event.is_error === true ||
        event.isError === true || event.willRetry === true) fail('provider_failed');
    if (!init) {
      if (event.type !== 'system' || event.subtype !== 'init' || event.uuid !== event.session_id ||
          !wireString(event.qwen_code_version) || event.qwen_code_version.length > 128 || !wireString(event.model) ||
          !wireString(event.cwd) || !isAbsolute(event.cwd) || event.permission_mode !== 'default' ||
          !stringList(event.tools) || !stringList(event.slash_commands) || !stringList(event.agents) ||
          !Array.isArray(event.mcp_servers) || event.mcp_servers.length !== 0) fail('provider_init_invalid');
      init = event;
      continue;
    }
    if (event.session_id !== init.session_id) fail('provider_session_mismatch');
    if (event.type === 'assistant') {
      const message = event.message;
      if (selected !== null) fail('provider_multiple_candidates');
      if (event.parent_tool_use_id !== null || !message || message.id !== event.uuid ||
          message.type !== 'message' || message.role !== 'assistant' || message.model !== init.model ||
          message.stop_reason !== null || Object.hasOwn(message, 'error') || message.errorMessage ||
          !Array.isArray(message.content) || message.content.length === 0) fail('provider_content_invalid');
      if (message.content.every(part => part?.type === 'thinking' && typeof part.thinking === 'string')) continue;
      if (!message.content.every(part => part?.type === 'text' && typeof part.text === 'string')) fail('provider_content_invalid');
      selected = message.content.map(part => part.text).join('');
      if (!selected.trim()) fail('provider_content_invalid');
    } else if (event.type === 'result') {
      if (selected === null || event.subtype !== 'success' || event.is_error !== false ||
          event.result !== selected || Object.hasOwn(event, 'structured_result') ||
          Object.hasOwn(event, 'parent_tool_use_id') ||
          !Array.isArray(event.permission_denials) || event.permission_denials.length !== 0 ||
          !Number.isSafeInteger(event.num_turns) || event.num_turns < 1 ||
          !Number.isFinite(event.duration_ms) || event.duration_ms < 0 ||
          !Number.isFinite(event.duration_api_ms) || event.duration_api_ms < 0) fail('provider_terminal_mismatch');
      ended = true;
    } else {
      // Includes tool/result, user/control/partial streams, fallback/retry,
      // background/subagent events and duplicate init. None are this profile.
      fail('provider_event_invalid');
    }
  }
  if (!ended) fail('provider_candidate_missing');
  return selected;
}

// The caller independently requires clean process exit, complete owned cleanup
// and no stream cap. Provider text is a candidate, never verification evidence.
export function parseCandidate(config, node, rawStdout) {
  validConfig(config);
  validNode(node);
  const events = outputEvents(rawStdout);
  const selected = config.provider === 'qwen' ? qwenText(events) : piText(events);
  let value;
  try { value = JSON.parse(selected); } catch { fail('provider_candidate_invalid'); }
  if (!value || Array.isArray(value) || Object.keys(value).sort().join(',') !== 'content,name' ||
      value.name !== node.file || typeof value.content !== 'string' || !value.content.trim() ||
      !value.content.isWellFormed() || value.content.includes('\0') || Buffer.byteLength(value.content) > MAX_FILE) {
    fail('provider_candidate_invalid');
  }
  return { name: value.name, content: value.content };
}
