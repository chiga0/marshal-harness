import { isAbsolute, dirname } from 'node:path';
import { TextDecoder } from 'node:util';

const MAX_STDOUT = 1024 * 1024;
const MAX_FILE = 64 * 1024;
const NAMES = new Set(['normalize.mjs', 'report.mjs']);
const ENV_NAMES = ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TZ', 'TMPDIR'];

function fail(reason) { throw new Error(reason); }
function validConfig(config) {
  if (!config || Object.keys(config).sort().join(',') !== 'executable,provider' ||
      config.provider !== 'pi' || typeof config.executable !== 'string' ||
      !isAbsolute(config.executable) || /[\0\r\n]/u.test(config.executable)) {
    fail('provider_config_invalid');
  }
}
function validNode(node) {
  if (!node || !NAMES.has(node.file)) fail('provider_node_invalid');
}

// The controller owns the process and writes prompt to stdin. No model,
// credential copy, shell, extension or execution tool is introduced here.
export function makeCommand(config, node, prompt) {
  validConfig(config);
  validNode(node);
  if (typeof prompt !== 'string' || !prompt.trim() || !prompt.isWellFormed() ||
      Buffer.byteLength(prompt) > 32 * 1024) fail('provider_prompt_invalid');
  const env = {};
  for (const key of ENV_NAMES) {
    if (typeof process.env[key] === 'string') env[key] = process.env[key];
  }
  if (!env.HOME || !isAbsolute(env.HOME)) fail('provider_home_unavailable');
  env.PATH ||= `${dirname(process.execPath)}:/usr/bin:/bin`;
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

// Only message_end creates a candidate. agent_end repeats the same message in
// native Pi JSON; it is checked for failures, never counted as another output.
// The caller must independently require a clean process exit and no stream cap.
export function parseCandidate(config, node, rawStdout) {
  validConfig(config);
  validNode(node);
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
  let selected = null;
  let ended = false;
  for (const line of lines) {
    let event;
    try { event = JSON.parse(line); } catch { fail('provider_output_invalid'); }
    if (!event || typeof event.type !== 'string' || Array.isArray(event)) fail('provider_output_invalid');
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
  let value;
  try { value = JSON.parse(selected); } catch { fail('provider_candidate_invalid'); }
  if (!value || Array.isArray(value) || Object.keys(value).sort().join(',') !== 'content,name' ||
      value.name !== node.file || typeof value.content !== 'string' || !value.content.trim() ||
      !value.content.isWellFormed() || value.content.includes('\0') || Buffer.byteLength(value.content) > MAX_FILE) {
    fail('provider_candidate_invalid');
  }
  return { name: value.name, content: value.content };
}
