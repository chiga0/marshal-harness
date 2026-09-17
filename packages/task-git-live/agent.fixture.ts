// Deterministic protocol-only peer, never imports native Pi/Qwen or credentials.
// Pi uses the original production bridge + checked-in mock SDK file operations.
import fs from 'node:fs/promises';
import {pathToFileURL} from 'node:url';
const protocol = process.argv[2], mode = process.env.GIT_MIXED_FIXTURE;
if (!['pi', 'qwen'].includes(protocol) || !['good', 'wrong', 'bad-plan'].includes(mode)) process.exit(1);
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const piReply = (request, data) => send({type: 'response', id: request.id, command: request.type, success: true, data});
const qwenReply = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
const update = value => send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: 'git-mixed-fixture', update: value}});
const handlers = new Map(), tools = new Map(), replies = new Map();
let sequence = 0, controller = new AbortController(), pending;
const api = {on: (name, callback) => handlers.set(name, callback), registerTool: tool => tools.set(tool.name, tool),
  getActiveTools: () => ['read', 'write', 'edit'], setActiveTools: names => {if (names.join(',') !== 'read,write,edit') throw Error('fixture active tool drift');}};
const context = {cwd: process.cwd(), sessionManager: {getSessionId: () => 'git-mixed-fixture', getSessionFile: () => undefined},
  ui: {notify(message) {send({type: 'extension_ui_request', id: 'notice-' + ++sequence, method: 'notify', message});},
    confirm(title, message, {signal} = {}) {
      return new Promise(resolve => {
        const id = 'permission-' + ++sequence, abort = () => {replies.delete(id); resolve(false);};
        replies.set(id, answer => {signal?.removeEventListener('abort', abort); resolve(answer === true);});
        signal?.addEventListener('abort', abort, {once: true});
        send({type: 'extension_ui_request', id, method: 'confirm', title, message});
      });
    }}};
if (protocol === 'pi') {
  const entry = process.argv[process.argv.indexOf('--extension') + 1];
  await (await import(pathToFileURL(entry).href)).default(api); await handlers.get('session_start')({}, context);
}
const source = {
  library: `export function net(cents, discount) {
  if (!Number.isSafeInteger(cents) || cents < 0 || !Number.isSafeInteger(discount) || discount < 0 || discount > cents) throw Error('invalid');
  return cents - discount;
}\n`,
  client: `export function invoice(rows, net) {
  if (!Array.isArray(rows)) throw Error('invalid');
  const lines = []; let total = 0;
  for (const row of rows) {
    if (!row || typeof row.sku !== 'string' || !row.sku.trim()) throw Error('invalid');
    const amount = net(row.cents, row.discount);
    if (!Number.isSafeInteger(amount) || amount < 0) throw Error('invalid');
    total += amount; if (!Number.isSafeInteger(total)) throw Error('overflow');
    lines.push({sku: row.sku, amount});
  }
  return {lines, total};
}\n`,
};
async function piTool(name, input) {
  const id = 'tool-' + ++sequence, event = {toolName: name, toolCallId: id, args: input};
  await handlers.get('tool_execution_start')?.(event, context); send({type: 'tool_execution_start', ...event});
  let isError = false;
  try {
    const block = await handlers.get('tool_call')?.({toolName: name, toolCallId: id, input}, context);
    if (block?.block) throw Error('blocked');
    const tool = tools.get(name), prepared = tool.prepareArguments(input);
    await tool.execute(id, prepared, controller.signal, () => {}, context);
  } catch {isError = true;}
  await handlers.get('tool_execution_end')?.({toolName: name, toolCallId: id, isError}, context);
  send({type: 'tool_execution_end', toolName: name, toolCallId: id, isError}); return !isError;
}
async function qwenTool(kind, input) {
  const id = 'tool-' + ++sequence, requestId = 'permission-' + ++sequence;
  update({sessionUpdate: 'tool_call', toolCallId: id, kind, status: 'pending'});
  const selected = await new Promise(resolve => {
    replies.set(requestId, result => resolve(result?.outcome?.outcome === 'selected' && result.outcome.optionId === 'proceed_once'));
    send({jsonrpc: '2.0', id: requestId, method: 'session/request_permission', params: {sessionId: 'git-mixed-fixture',
      toolCall: {toolCallId: id, kind, rawInput: input}, options: [
        {optionId: 'proceed_once', name: 'Allow once', kind: 'allow_once'}, {optionId: 'cancel', name: 'Deny', kind: 'reject_once'}]}});
  });
  if (!selected) {update({sessionUpdate: 'tool_call_update', toolCallId: id, kind, status: 'failed'}); return false;}
  update({sessionUpdate: 'tool_call_update', toolCallId: id, kind, status: 'in_progress'});
  if (kind === 'read') await fs.readFile(input.file_path); else await fs.writeFile(input.file_path, input.content);
  update({sessionUpdate: 'tool_call_update', toolCallId: id, kind, status: 'completed'}); return true;
}
function finish(value, failed = false) {
  if (protocol === 'pi') {
    send({type: 'message_end', message: {role: 'assistant', stopReason: failed ? 'error' : 'stop', content: [{type: 'text', text: JSON.stringify(value)}]}});
    send({type: 'agent_end'}); send({type: 'agent_settled'});
  } else {
    update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(value)}});
    qwenReply(pending, {stopReason: failed ? 'refusal' : 'end_turn'});
  }
}
async function prompt(raw) {
  controller = new AbortController();
  const marker = 'GIT_MIXED_DECLARED_PLAN_V1\n', end = '\nGIT_MIXED_DECLARED_PLAN_END\n';
  if (raw.includes(marker)) {
    const plan = JSON.parse(raw.slice(raw.indexOf(marker) + marker.length, raw.indexOf(end)));
    if (mode === 'bad-plan') plan.nodes[1].providerId = 'pi-rpc';
    finish(plan); return; // No private imported proposal or implementation oracle.
  }
  const input = JSON.parse(raw.slice(raw.indexOf('{"task":'))), node = input.node.id;
  if (node !== (protocol === 'pi' ? 'library' : 'client')) throw Error('wrong native assignment');
  const filename = input.git.writePaths[0], tool = protocol === 'pi' ? piTool : qwenTool;
  // A real refused request must never read an unrelated path.
  const denied = await tool(protocol === 'pi' ? 'read' : 'execute', protocol === 'pi' ? {path: '.git'} : {command: 'never execute this fixture command'});
  if (denied) throw Error('unexpected permission');
  if (!await tool('read', protocol === 'pi' ? {path: filename} : {file_path: filename})) throw Error('read denied');
  await new Promise(resolve => setTimeout(resolve, 500)); // Fixture timing only; NEVER used by the real path.
  let content = source[node]; if (mode === 'wrong' && node === 'library') content = content.replace('cents - discount', 'cents');
  if (!await tool(protocol === 'pi' ? 'write' : 'edit', protocol === 'pi' ? {path: filename, content} : {file_path: filename, content})) throw Error('write denied');
  finish({message: '真实文件代码候选已写，等待独立验证'});
}
let buffer = '';
process.stdin.on('data', bytes => {
  buffer += bytes.toString(); if (buffer.length > 512 * 1024) process.exit(1);
  let at;
  while ((at = buffer.indexOf('\n')) >= 0) {
    const request = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
    if (protocol === 'pi') {
      if (request.type === 'get_state') piReply(request, {sessionId: 'git-mixed-fixture', isStreaming: false, isCompacting: false, pendingMessageCount: 0, messageCount: 0});
      else if (request.type === 'prompt') {piReply(request); send({type: 'agent_start'}); void prompt(request.message).catch(() => finish({}, true));}
      else if (request.type === 'extension_ui_response') {const callback = replies.get(request.id); replies.delete(request.id); callback?.(request.confirmed);}
      else if (request.type === 'clear_queue') piReply(request);
      else if (request.type === 'abort') {controller.abort(); piReply(request);}
    } else {
      if (request.method === 'initialize') qwenReply(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
      else if (request.method === 'session/new') qwenReply(request, {sessionId: 'git-mixed-fixture'});
      else if (request.method === 'session/prompt') {pending = request; void prompt(request.params.prompt[0].text).catch(() => finish({}, true));}
      else if (request.method === 'session/cancel') {controller.abort(); if (pending) qwenReply(pending, {stopReason: 'cancelled'});}
      else {const callback = replies.get(request.id); replies.delete(request.id); callback?.(request.result);}
    }
  }
});
