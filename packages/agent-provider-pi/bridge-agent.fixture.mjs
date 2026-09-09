// Native-RPC-shaped peer loads the ACTUAL checked-in extension and operations.
// Its SDK seam is deterministic; no Pi/model/login is imported by this fixture.
import {pathToFileURL} from 'node:url';
const mode = process.argv[2], extensionPath = process.argv[process.argv.indexOf('--extension') + 1];
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const handlers = new Map(), registered = new Map(), questions = new Map();
let active = ['read', 'write', 'edit', 'bash', 'custom'], sequence = 0, controller, last = null;
const api = {on: (name, fn) => handlers.set(name, fn), registerTool: tool => registered.set(tool.name, tool),
  getActiveTools: () => [...active], setActiveTools: names => { active = [...names]; }};
const context = {cwd: process.cwd(), sessionManager: {getSessionId: () => 'native-session', getSessionFile: () => undefined},
  ui: {notify(message) {
      if (mode === 'bad-nonce') { const value = JSON.parse(message); value.nonce = 'a'.repeat(64); message = JSON.stringify(value); }
      if (mode === 'custom-missing-block' && JSON.parse(message).type === 'blocked') return;
      if (mode === 'custom-foreign-block' && JSON.parse(message).type === 'blocked') {
        const value = JSON.parse(message); value.toolCallId = 'foreign-call'; message = JSON.stringify(value);
      }
      send({type: 'extension_ui_request', id: 'ui-' + ++sequence, method: 'notify', message}); },
    confirm(title, message, {signal} = {}) {
      if (mode === 'business-ack-missing' && JSON.parse(message).type === 'business-answer-ack') return new Promise(() => {});
      if (mode === 'business-ack-foreign' && JSON.parse(message).type === 'business-answer-ack') {
        const value = JSON.parse(message); value.questionNonce = '0'.repeat(64); message = JSON.stringify(value);
      }
      const id = 'ui-' + ++sequence;
      return new Promise(resolve => { const abort = () => { questions.delete(id); resolve(false); };
        questions.set(id, value => { signal?.removeEventListener('abort', abort); resolve(value === true); });
        signal?.addEventListener('abort', abort, {once: true});
        send({type: 'extension_ui_request', id, method: 'confirm', title, message});
      });
    },
    input(title, _placeholder, {signal} = {}) {return dialog('input', title, [], signal);},
    select(title, options, {signal} = {}) {return dialog('select', title, options, signal);},
  }};
function dialog(method, title, options, signal) {
  const id = 'ui-' + ++sequence;
  return new Promise(resolve => {const abort = () => {questions.delete(id); resolve(undefined);};
    questions.set(id, value => {signal?.removeEventListener('abort', abort); resolve(value);});
    signal?.addEventListener('abort', abort, {once: true});
    send({type: 'extension_ui_request', id, method, title, ...(method === 'select' ? {options} : {})});
  });
}
if (mode !== 'no-bridge') {
  await (await import(pathToFileURL(extensionPath).href)).default(api);
  await handlers.get('session_start')({}, context);
}
const response = (request, data) => send({type: 'response', id: request.id, command: request.type, success: true, data});
function finish(stopReason = 'stop', output = 'native fixture candidate') {
  send({type: 'message_end', message: {role: 'assistant', stopReason, content: [{type: 'text', text: output}]}});
  send({type: 'agent_end'}); send({type: 'agent_settled'});
}
async function tool(name, input, callId) {
  last = {name, callId};
  await handlers.get('tool_execution_start')?.({toolName: name, toolCallId: callId, args: input}, context);
  send({type: 'tool_execution_start', toolName: name, toolCallId: callId, args: input});
  if (mode === 'custom-await-block') await new Promise(resolve => controller.signal.addEventListener('abort', resolve, {once: true}));
  if (mode === 'bypass' || mode === 'bypass-error') {
    await handlers.get('tool_execution_end')?.({toolName: name, toolCallId: callId, isError: mode === 'bypass-error'}, context);
    send({type: 'tool_execution_end', toolName: name, toolCallId: callId, isError: mode === 'bypass-error'}); return;
  }
  const event = {toolName: name, toolCallId: callId, input}, blocked = await handlers.get('tool_call')(event, context);
  let failed = blocked?.block === true;
  if (mode === 'custom-success-end') {
    await handlers.get('tool_execution_end')?.({toolName: name, toolCallId: callId, isError: false}, context);
    send({type: 'tool_execution_end', toolName: name, toolCallId: callId, isError: false}); return;
  }
  let result;
  if (!failed) {
    try { const tool = registered.get(name); input = tool.prepareArguments(input);
      if (mode === 'invalid-arguments') throw Error('fixture schema rejects missing required content');
      if (mode === 'cancel-before-execute') controller.abort();
      result = await tool.execute(callId, input, controller.signal, () => {}, context); }
    catch { failed = true; }
  }
  await handlers.get('tool_execution_end')?.({toolName: name, toolCallId: callId, isError: failed}, context);
  send({type: 'tool_execution_end', toolName: name, toolCallId: callId, isError: failed});
  return result;
}
async function prompt(request) {
  response(request); controller = new AbortController(); send({type: 'agent_start'});
  if (mode === 'business-plan') {
    finish('stop', JSON.stringify({summary: '两个独立文件分支及业务区域问答', nodes: ['code', 'docs', 'verify'].map(id => ({id,
      role: id === 'verify' ? 'verifier' : 'author', goal: '完成' + id, scope: [id], providerId: id === 'verify' ? null : 'pi-' + id})),
      edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}], deliverables: ['两个完整文件'], acceptance: ['原区域答案与独立文件全部验证']}));
    return;
  }
  if (mode.startsWith('custom')) await tool('custom', {}, 'custom-one');
  else if (mode.startsWith('business')) {
    const reply = await tool('marshal_ask_user', {prompt: '请确定本次业务区域', kind: 'select', options: ['north', 'south']}, 'question-one');
    if (reply) await tool('write', {path: 'output.txt', content: reply.content[0].text}, 'write-answer');
  }
  else if (mode === 'shell' || mode === 'shell-child' || mode === 'shell-timeout' || mode === 'shell-hang') {
    const command = mode === 'shell' ? "printf 'actual-shell-output' > output.txt" :
      mode === 'shell-hang' ? "printf '%s' \"$$\" > shell.pid; trap '' TERM; while :; do sleep 1; done" :
      mode === 'shell-timeout' ? "trap '' TERM; while :; do sleep 1; done" :
      // Inherited background child intentionally outlives shell. Only the
      // original Runtime guard cleans it after the peer's settled event.
      `"${process.execPath}" -e 'setInterval(()=>{},1000)' & printf '%s' "$!" > child.pid`;
    await tool('bash', {command, ...(mode === 'shell-timeout' ? {timeout: 0.05} : {})}, 'shell-one');
  } else {
    if (mode === 'file-business') await tool('read', {path: 'input.txt'}, 'read-one');
    await tool('write', {path: 'output.txt', content: 'independent native candidate'}, 'write-one');
  }
  finish(controller.signal.aborted ? 'aborted' : 'stop');
}
let buffer = '';
process.stdin.on('data', chunk => { buffer += chunk.toString(); let at;
  while ((at = buffer.indexOf('\n')) >= 0) {
    const request = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
    if (request.type === 'get_state') response(request, {sessionId: 'native-session', isStreaming: false, isCompacting: false, pendingMessageCount: 0, messageCount: 0});
    else if (request.type === 'prompt') void prompt(request).catch(() => finish('error'));
    else if (request.type === 'extension_ui_response') { const answer = questions.get(request.id); questions.delete(request.id);
      answer?.(Object.hasOwn(request, 'value') ? request.value : request.confirmed); }
    else if (request.type === 'clear_queue') response(request);
    else if (request.type === 'abort') { controller?.abort(); response(request); }
  }
});
