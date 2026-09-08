import test from 'node:test';
import assert from 'node:assert/strict';
import { makeCommand, parseCandidate } from './providers.mjs';

const config = { provider: 'pi', executable: '/operator/agent-entry.mjs' };
const node = { id: 'normalize', role: 'author', file: 'normalize.mjs' };
const candidate = { name: node.file, content: 'export function normalize(rows) { return rows; }\n' };
const message = (value = candidate, stopReason = 'stop') => ({
  role: 'assistant', stopReason, content: [{ type: 'text', text: JSON.stringify(value) }],
});
const stream = (...events) => events.map((event) => JSON.stringify(event)).join('\n') + '\n';
const ended = (value = candidate) => ({ type: 'message_end', message: message(value) });

test('Pi command uses explicit executable, native configuration and stdin-only prompt', () => {
  const result = makeCommand(config, node, 'private test prompt');
  assert.equal(result.command, config.executable);
  assert.deepEqual(result.args, ['--print', '--mode', 'json', '--no-session', '--no-tools', '--no-extensions', '--no-context-files']);
  assert.ok(!result.args.join(' ').includes('private test prompt'));
  assert.ok(Object.keys(result.env).every((key) => ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TZ', 'TMPDIR'].includes(key)));
  for (const key of ['NODE_OPTIONS', 'NODE_PATH', 'GITHUB_TOKEN', 'AWS_SECRET_ACCESS_KEY', 'SSH_AUTH_SOCK', 'ANTHROPIC_API_KEY']) {
    assert.equal(result.env[key], undefined);
  }
});

test('provider config, unsupported adapter and node paths fail closed', () => {
  for (const bad of [{ ...config, provider: 'qwen' }, { ...config, executable: 'pi' }, { ...config, token: 'fixture' }]) {
    assert.throws(() => makeCommand(bad, node, 'prompt'), /provider_config_invalid/);
  }
  assert.throws(() => makeCommand(config, { file: '../normalize.mjs' }, 'prompt'));
  assert.throws(() => makeCommand(config, node, 'x'.repeat(33 * 1024)));
});

test('native user message, assistant message_end and repeated agent_end yield one candidate', () => {
  const assistant = message();
  assistant.content.unshift({ type: 'thinking', thinking: 'not a candidate' });
  const raw = stream(
    { type: 'agent_start' }, { type: 'message_end', message: { role: 'user', content: 'task' } },
    { type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: '{' } },
    { type: 'message_end', message: assistant },
    { type: 'agent_end', messages: [{ role: 'user', content: 'task' }, assistant], willRetry: false },
  );
  assert.deepEqual(parseCandidate(config, node, Buffer.from(raw)), candidate);
  assert.throws(() => parseCandidate(config, node, stream(ended())), /candidate_missing/);
});

test('agent_end alone and duplicated message_end are not authoritative candidates', () => {
  assert.throws(() => parseCandidate(config, node, stream({ type: 'agent_end', messages: [message()] })));
  assert.throws(() => parseCandidate(config, node, stream(ended(), ended())), /multiple_candidates/);
  assert.throws(() => parseCandidate(config, node, stream(ended(), { type: 'agent_end', messages: [message({ ...candidate, content: 'different' })] })));
});

test('native error, truncation, length, retry and tool use are not success', () => {
  for (const stopReason of ['error', 'length', 'aborted', 'toolUse', undefined]) {
    const assistant = message();
    assistant.stopReason = stopReason;
    assert.throws(() => parseCandidate(config, node, stream({ type: 'message_end', message: assistant })));
  }
  for (const event of [{ type: 'error', error: 'PRIVATE_TEST_ERROR' }, { type: 'auto_retry_start' },
    { type: 'tool_execution_start' }, { type: 'agent_end', messages: [message()], willRetry: true }]) {
    assert.throws(() => parseCandidate(config, node, stream(ended(), event)), (error) => !error.message.includes('PRIVATE_TEST_ERROR'));
  }
  assert.throws(() => parseCandidate(config, node, stream(ended()).slice(0, -4)));
  assert.throws(() => parseCandidate(config, node, Buffer.from([0xff, 0xfe])));
  assert.throws(() => parseCandidate(config, node, 'x'.repeat(1024 * 1024 + 1)));
});

test('only assigned filename and bounded exact candidate fields are accepted', () => {
  for (const bad of [{ ...candidate, name: 'report.mjs' }, { ...candidate, name: '../normalize.mjs' },
    { ...candidate, files: [] }, [candidate], { ...candidate, content: 'x'.repeat(65537) },
    { ...candidate, content: '\ud800' }, { ...candidate, content: '' }, { ...candidate, content: '\0' }]) {
    assert.throws(() => parseCandidate(config, node, stream(ended(bad), { type: 'agent_end', messages: [message(bad)] })), /candidate_invalid/);
  }
  const fenced = message();
  fenced.content[0].text = '```json\n' + JSON.stringify(candidate) + '\n```';
  assert.throws(() => parseCandidate(config, node, stream({ type: 'message_end', message: fenced }, { type: 'agent_end', messages: [fenced] })), /candidate_invalid/);
});
