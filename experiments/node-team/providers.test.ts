import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { makeCommand, parseCandidate } from './providers.mjs';
import { MAX_STDOUT_BYTES } from './limits.mjs';
import { Supervisor } from './supervisor.mjs';
import { artifactHash, digest } from './store.mjs';

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
  for (const bad of [{ ...config, provider: 'opencode' }, { ...config, executable: 'pi' }, { ...config, token: 'fixture' }]) {
    assert.throws(() => makeCommand(bad, node, 'prompt'), /provider_config_invalid/);
  }
  assert.throws(() => makeCommand(config, { file: '../normalize.mjs' }, 'prompt'));
  assert.throws(() => makeCommand(config, node, 'x'.repeat(33 * 1024)));
});

const qwen = { ...config, provider: 'qwen' };
function qwenEvents(value = candidate) {
  const session_id = 'session-fixture';
  const content = JSON.stringify(value);
  return [
    { type: 'system', subtype: 'init', uuid: session_id, session_id, cwd: '/fixture/owned',
      tools: ['read_file', 'run_shell_command'], mcp_servers: [], model: 'native-fixture',
      permission_mode: 'default', slash_commands: ['help'], qwen_code_version: '0.23.0', agents: [] },
    { type: 'assistant', uuid: 'assistant-fixture', session_id, parent_tool_use_id: null,
      message: { id: 'assistant-fixture', type: 'message', role: 'assistant', model: 'native-fixture',
        content: [{ type: 'text', text: content }], stop_reason: null,
        usage: { input_tokens: 11, output_tokens: 12 } } },
    { type: 'result', subtype: 'success', uuid: 'result-fixture', session_id, is_error: false,
      duration_ms: 100, duration_api_ms: 80, num_turns: 1, result: content,
      usage: { input_tokens: 11, output_tokens: 12 }, permission_denials: [] },
  ];
}
const parseQwen = events => parseCandidate(qwen, node, stream(...events));

test('Qwen native settings plus safe mode and zero tool budget use stdin, never argv', () => {
  const result = makeCommand(qwen, node, 'private fixture prompt');
  assert.equal(result.command, qwen.executable); // No binary basename assumption.
  assert.deepEqual(result.args, ['--safe-mode', '--input-format', 'text', '--output-format', 'stream-json',
    '--approval-mode', 'default', '--max-tool-calls', '0', '--max-wall-time', '5m',
    '--no-chat-recording', '--no-openai-logging', '--no-telemetry']);
  assert.ok(!result.args.join(' ').includes('private fixture prompt'));
  assert.deepEqual(result.env, makeCommand(config, node, 'prompt').env);
  for (const key of ['QWEN_API_KEY', 'OPENAI_API_KEY', 'QWEN_CODE_SETTINGS', 'NODE_OPTIONS', 'SSH_AUTH_SOCK']) {
    assert.equal(result.env[key], undefined);
  }
  assert.equal(Object.hasOwn(makeCommand(config, node, 'prompt'), 'prompt'), false);
});

test('Qwen transport reversibly removes every literal at mention and leading slash command', () => {
  for (const original of ['/help', '@file.txt', '/shell @/private/secret', '中文任务 😀\n@x\\@y',
    'literal \\u0040 and @ with "quotes"\n\r\t', ' @../relative\n@{session} @mcp:server']) {
    const { prompt } = makeCommand(qwen, node, original);
    assert.ok(!prompt.includes('@'));
    assert.ok(!prompt.trimStart().startsWith('/'));
    assert.equal(JSON.parse(prompt.slice(prompt.indexOf('\n') + 1)), original);
    assert.equal(makeCommand(qwen, node, original).prompt, prompt);
  }
  for (const original of ['', '  ', '\ud800', '\0', 'x'.repeat(32769), '\u0001'.repeat(20000)]) {
    assert.throws(() => makeCommand(qwen, node, original), /provider_prompt_invalid/);
  }
});

test('Qwen full native init, optional separate thinking, one text and matching final result', () => {
  const events = qwenEvents();
  const thinking = structuredClone(events[1]);
  thinking.uuid = thinking.message.id = 'thinking-fixture';
  thinking.message.content = [{ type: 'thinking', thinking: 'private thought, not a candidate' }];
  events.splice(1, 0, thinking);
  assert.deepEqual(parseQwen(events), candidate);
  assert.deepEqual(parseCandidate(qwen, node, Buffer.from(stream(...qwenEvents()))), candidate);
  const compatible = qwenEvents();
  compatible[0].qwen_code_version = '0.24.0';
  assert.deepEqual(parseQwen(compatible), candidate); // Schema check, not an untested compatibility claim.
});

test('Qwen requires one supported init and exact session, unique event identity and main assistant', () => {
  for (const change of [
    events => events.shift(),
    events => { events[0].qwen_code_version = ''; },
    events => { events[0].cwd = 123; },
    events => { events[0].permission_mode = 'yolo'; },
    events => { events[0].mcp_servers.push({ name: 'unexpected', status: 'connected' }); },
    events => { events[0].uuid = 'other'; },
    events => { events[1].session_id = 'other'; },
    events => { events[2].session_id = 'other'; },
    events => { events[2].uuid = events[1].uuid; },
    events => { events[1].parent_tool_use_id = 'subagent'; },
    events => { events[1].message.id = 'other'; },
    events => { events[1].message.model = 'fallback'; },
    events => { events[1].message.role = 'user'; },
    events => events.splice(1, 0, { ...events[0], uuid: 'second-init' }),
    events => events.push({ ...events[1], uuid: 'late-assistant' }),
  ]) {
    const events = qwenEvents(); change(events); assert.throws(() => parseQwen(events));
  }
});

test('Qwen tool, control, retry, mixed blocks and explicit failure never become success', () => {
  for (const type of ['tool_use', 'tool_result', 'user', 'control_request', 'control_response',
    'stream_event', 'error', 'retry']) {
    const events = qwenEvents();
    events.splice(1, 0, { type, uuid: 'unexpected', session_id: events[0].session_id });
    assert.throws(() => parseQwen(events));
  }
  for (const change of [
    events => { events[1].message.content = [{ type: 'tool_use', id: 'tool', name: 'read_file', input: {} }]; },
    events => { events[1].message.content.push({ type: 'thinking', thinking: 'mixed' }); },
    events => { events[1].message.stop_reason = 'length'; },
    events => { events[1].message.stop_reason = 'end_turn'; },
    events => { events[1].error = { message: 'PRIVATE_PROVIDER_ERROR' }; },
    events => { events[1].message.errorMessage = 'PRIVATE_PROVIDER_ERROR'; },
    events => { events[1].willRetry = true; },
    events => { events[1].message.content = []; },
    events => { events[2].subtype = 'error_max_turns'; },
    events => { events[2].is_error = true; events[2].error = { message: 'PRIVATE_PROVIDER_ERROR' }; },
    events => { events[2].permission_denials = [{ tool_name: 'read_file' }]; },
    events => { events[2].structured_result = candidate; },
    events => { events[2].parent_tool_use_id = 'subagent'; },
  ]) {
    const events = qwenEvents(); change(events);
    assert.throws(() => parseQwen(events), error => !error.message.includes('PRIVATE_PROVIDER_ERROR'));
  }
});

test('Qwen final result cannot replace the candidate or excuse incomplete/duplicate streams', () => {
  for (const change of [
    events => events.pop(),
    events => events.splice(1, 1),
    events => { events[2].result = JSON.stringify({ ...candidate, content: 'different' }); },
    events => events.push({ ...events[2], uuid: 'second-result' }),
    events => { const extra = structuredClone(events[1]); extra.uuid = extra.message.id = 'second-assistant'; events.splice(2, 0, extra); },
    events => { delete events[2].is_error; },
    events => { delete events[2].permission_denials; },
    events => { events[2].num_turns = 0; },
    events => { events[2].duration_ms = -1; },
  ]) {
    const events = qwenEvents(); change(events); assert.throws(() => parseQwen(events));
  }
  assert.throws(() => parseCandidate(qwen, node, stream(...qwenEvents()).slice(0, -5)));
  assert.throws(() => parseCandidate(qwen, node, Buffer.from([0xff])));
  assert.throws(() => parseCandidate(qwen, node, 'x'.repeat(MAX_STDOUT_BYTES + 1)));
  assert.throws(() => parseCandidate(qwen, node, '\ud800'));
});

test('Qwen candidate has the same exact assigned-file and 64 KiB constraints as Pi', () => {
  for (const bad of [{ ...candidate, name: 'report.mjs' }, { ...candidate, name: '../normalize.mjs' },
    { ...candidate, files: [] }, [candidate], { ...candidate, content: 'x'.repeat(65537) },
    { ...candidate, content: '\ud800' }, { ...candidate, content: '' }, { ...candidate, content: '\0' }]) {
    assert.throws(() => parseQwen(qwenEvents(bad)), /candidate_invalid/);
  }
  const events = qwenEvents();
  events[1].message.content[0].text = events[2].result = '```json\n' + JSON.stringify(candidate) + '\n```';
  assert.throws(() => parseQwen(events), /candidate_invalid/);
});

async function fixtureOwner(t, ports) {
  const directory = await fs.mkdtemp(path.join(process.platform === 'darwin' ? '/private/tmp' : '/tmp', 'mnt-provider-'));
  const owner = await Supervisor.open(directory, qwen, ports);
  t.after(async () => { await owner.shutdown(); await fs.rm(directory, { recursive: true, force: true }); });
  return { owner, directory };
}
const fixturePlan = intent => ({ version: 'provider-unit-fixture/v1', intent, timeoutMs: 300000,
  nodes: ['normalize', 'report'].map(id => ({ id, role: 'author', file: id + '.mjs', prompt: '/fixture @private ' + id })) });
async function approvedFixture(owner, key) {
  const task = await owner.command('create', undefined, { intent: 'fixture only' }, key);
  await owner.command('approve', task.id, { expectedRevision: task.revision, previewDigest: task.previewDigest }, key + '-approve');
  const end = Date.now() + 8000;
  for (;;) {
    const current = await owner.command('get', task.id);
    if (['completed', 'failed', 'intervention'].includes(current.status)) return current;
    assert.ok(Date.now() < end, 'bounded fixture did not terminalize');
    await new Promise(resolve => setTimeout(resolve, 15));
  }
}

test('generic guard receives exact encoded stdin while original approved plan survives cold read', { timeout: 15000 }, async t => {
  // Deterministic Node fixture, not a Qwen process or Provider-team proof.
  const program = `
    let input = ''; for await (const chunk of process.stdin) input += chunk;
    if (input.includes('@') || input.startsWith('/')) process.exit(7);
    const original = JSON.parse(input.slice(input.indexOf('\\n') + 1));
    const name = original.endsWith('normalize') ? 'normalize.mjs' : 'report.mjs';
    const events = (${qwenEvents.toString()})({ name, content: JSON.stringify({ input, original }) });
    for (const event of events) process.stdout.write(JSON.stringify(event) + '\\n');
  `;
  const ports = { plan: fixturePlan, makeCommand(config, node, prompt) {
    const prepared = makeCommand(config, node, prompt);
    return { ...prepared, command: process.execPath, args: ['--input-type=module', '-e', program], env: {} };
  }, parseCandidate, async verifyFiles(files) {
    return { passed: true, checks: files.length * 2, files: files.map(file => ({ ...file, sha256: artifactHash(file.content) })) };
  } };
  const { owner, directory } = await fixtureOwner(t, ports);
  const terminal = await approvedFixture(owner, 'encoded');
  assert.equal(terminal.status, 'completed');
  assert.equal(terminal.previewDigest, digest(terminal.plan));
  const delivery = await owner.command('delivery', terminal.id);
  for (const file of delivery.files) {
    const { input, original } = JSON.parse(file.content);
    const node = terminal.plan.nodes.find(n => n.file === file.name);
    assert.equal(original, node.prompt);
    assert.equal(input, makeCommand(qwen, node, node.prompt).prompt);
  }
  const saved = JSON.parse(await fs.readFile(path.join(directory, 'state.json'), 'utf8'));
  assert.deepEqual(saved.tasks[0].plan, terminal.plan);
  assert.ok(saved.tasks[0].workers.every(worker => worker.cleaned));
  await owner.shutdown();
  const reloaded = await Supervisor.open(directory, qwen, ports);
  try {
    assert.deepEqual((await reloaded.command('get', terminal.id)).plan, terminal.plan);
    assert.deepEqual(await reloaded.command('delivery', terminal.id), delivery);
    assert.equal((await reloaded.command('approve', terminal.id, { expectedRevision: 1,
      previewDigest: terminal.previewDigest }, 'encoded-approve')).attempts, 1);
    assert.equal(reloaded.guards.size, 0);
  } finally { await reloaded.shutdown(); }
});

test('generic encoded prompt rejects empty, non-string, malformed UTF-8 and oversized values before spawn', { timeout: 15000 }, async t => {
  let override;
  const { owner } = await fixtureOwner(t, { plan: fixturePlan,
    makeCommand: () => ({ command: process.execPath, args: [], env: {}, prompt: override }),
    parseCandidate, verifyFiles: async () => { throw new Error('must not verify'); } });
  let n = 0;
  for (override of ['', ' ', undefined, null, {}, '\ud800', '\0', 'x'.repeat(65537)]) {
    const terminal = await approvedFixture(owner, 'invalid-' + n++);
    assert.equal(terminal.status, 'failed');
    assert.ok(terminal.workers.every(worker => !worker.pid && worker.cleaned));
    assert.equal(owner.guards.size, 0);
  }
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
  assert.throws(() => parseCandidate(config, node, 'x'.repeat(MAX_STDOUT_BYTES + 1)), /output_limit/);
});

test('bounded native progress amplification is separate from final candidate limits', () => {
  const progress = {type: 'message_update', assistantMessageEvent: {type: 'text_delta', partial: 'x'.repeat(16384)}};
  const raw = stream(...Array(80).fill(progress), ended(), {type: 'agent_end', messages: [message()]});
  assert.ok(Buffer.byteLength(raw) > 1024 * 1024);
  assert.deepEqual(parseCandidate(config, node, raw), candidate);
  assert.throws(() => parseCandidate(config, node, stream(...Array(520).fill(progress), ended(),
    {type: 'agent_end', messages: [message()]})), /output_limit/);
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
