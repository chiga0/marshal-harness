// Deterministic Pi RPC peer; loads the ORIGINAL native extension plus checked-in
// SDK fixture. No model/login. Author bytes are written through registered tools.
import {pathToFileURL} from 'node:url';
const extension = process.argv[process.argv.indexOf('--extension') + 1];
const handlers = new Map(), tools = new Map(), replies = new Map();
let active = ['read', 'write', 'edit'], sequence = 0, controller;
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const dialog = (method, title, options, signal) => {
  const id = 'ui-worker-fixture-' + ++sequence;
  return new Promise(resolve => {const abort = () => {replies.delete(id); resolve(undefined);};
    replies.set(id, value => {signal?.removeEventListener('abort', abort); resolve(value);});
    signal?.addEventListener('abort', abort, {once: true});
    send({type: 'extension_ui_request', id, method, title, ...(method === 'select' ? {options} : {})});
  });
};
const context = {cwd: process.cwd(), sessionManager: {getSessionId: () => 'worker-cancel-fixture', getSessionFile: () => undefined}, ui: {
  notify: message => send({type: 'extension_ui_request', id: 'notice-' + ++sequence, method: 'notify', message}),
  confirm(title, message, {signal} = {}) {const id = 'permission-' + ++sequence;
    return new Promise(resolve => {const abort = () => {replies.delete(id); resolve(false);};
      replies.set(id, value => {signal?.removeEventListener('abort', abort); resolve(value === true);});
      signal?.addEventListener('abort', abort, {once: true}); send({type: 'extension_ui_request', id, method: 'confirm', title, message});});},
  select: (title, options, {signal} = {}) => dialog('select', title, options, signal),
  input: (title, _placeholder, {signal} = {}) => dialog('input', title, [], signal),
}};
await (await import(pathToFileURL(extension).href)).default({on: (name, handler) => handlers.set(name, handler),
  registerTool: tool => tools.set(tool.name, tool), getActiveTools: () => [...active], setActiveTools: value => {active = [...value];}});
await handlers.get('session_start')({}, context);
const response = (request, data) => send({type: 'response', id: request.id, command: request.type, success: true, data});
function finish(stopReason = 'stop', output = 'Fixture completed original file operation') {
  send({type: 'message_end', message: {role: 'assistant', stopReason, content: [{type: 'text', text: output}]}});
  send({type: 'agent_end'}); send({type: 'agent_settled'});
}
async function call(name, input) {
  const toolCallId = 'call_worker_' + ++sequence + '|fc_fixture';
  await handlers.get('tool_execution_start')?.({toolName: name, toolCallId, args: input}, context);
  send({type: 'tool_execution_start', toolName: name, toolCallId, args: input});
  let result, isError = false;
  try {
    if ((await handlers.get('tool_call')({toolName: name, toolCallId, input}, context))?.block) throw Error('blocked');
    const tool = tools.get(name); input = tool.prepareArguments(input);
    result = await tool.execute(toolCallId, input, controller.signal, () => {}, context);
  } catch {isError = true;}
  await handlers.get('tool_execution_end')?.({toolName: name, toolCallId, isError}, context);
  send({type: 'tool_execution_end', toolName: name, toolCallId, isError});
  if (isError) throw Error('fixture_tool_failed'); return result;
}
async function prompt(request) {
  response(request); controller = new AbortController(); send({type: 'agent_start'});
  const match = /\nWORKER_CANCEL_FIXTURE=([^\n]+)$/.exec(request.message);
  if (!match) throw Error('fixture_input_missing'); const declaration = JSON.parse(match[1]);
  if (declaration.plan) {finish('stop', JSON.stringify(declaration.plan)); return;}
  if (declaration.node === 'east') {
    await call('marshal_ask_user', {kind: 'select', prompt: '请选择 east 状态', options: ['paid', 'cancelled']});
    throw Error('fixture_east_must_be_cancelled_without_answer');
  }
  if (declaration.node !== 'west') throw Error('unexpected_node');
  const source = await call('read', {path: 'sales.json'}), data = JSON.parse(source.content[0].text);
  const rows = data.rows.filter(row => row.region === 'west' && row.status === 'paid');
  await call('write', {path: 'west.json', content: JSON.stringify({region: 'west', status: 'paid', count: rows.length,
    netCents: rows.reduce((sum, row) => sum + row.cents, 0)})});
  finish();
}
let buffer = '';
process.stdin.on('data', chunk => {buffer += chunk.toString('utf8'); if (Buffer.byteLength(buffer) > 1048576) {process.exitCode = 1; process.stdin.destroy(); return;}
  let at; while ((at = buffer.indexOf('\n')) >= 0) {
    const request = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
    if (request.type === 'get_state') response(request, {sessionId: 'worker-cancel-fixture', isStreaming: false, isCompacting: false, pendingMessageCount: 0, messageCount: 0});
    else if (request.type === 'prompt') void prompt(request).catch(() => finish(controller?.signal.aborted ? 'aborted' : 'error'));
    else if (request.type === 'extension_ui_response') {const reply = replies.get(request.id); replies.delete(request.id);
      reply?.(Object.hasOwn(request, 'value') ? request.value : request.confirmed);}
    else if (request.type === 'clear_queue') response(request);
    else if (request.type === 'abort') {controller?.abort(); response(request);}
  }
});
