// Deterministic protocol peer, never imports Pi or calls a model.
import fs from 'node:fs';
import path from 'node:path';
const mode = process.argv[2] ?? 'normal';
let buffer = '', active = false, cleared = false;
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const reply = (request, data) => send({type: 'response', id: request.id, command: request.type, success: true, ...(data ? {data} : {})});
const message = (text = 'public output', stopReason = 'stop') => ({type: 'message_end', message: {role: 'assistant', stopReason,
  content: [{type: 'thinking', thinking: 'PRIVATE_THINKING'}, {type: 'text', text}]}});
function finish(stopReason = 'stop') {
  send(message(mode === 'overflow' ? 'x'.repeat(65537) : 'public output', stopReason));
  send({type: 'agent_end', messages: [], willRetry: false});
  if (mode !== 'no-settled') { send({type: 'agent_settled'}); active = false; }
}
function handle(request) {
  if (request.type === 'get_state') {
    if (mode === 'hang-state') return;
    reply(request, {sessionId: 'pi-session', isStreaming: false, isCompacting: false, pendingMessageCount: 0,
      messageCount: mode === 'existing-session' ? 1 : 0, sessionFile: 'PRIVATE_SESSION_FILE', model: {private: 'PRIVATE_MODEL'}}); return;
  }
  if (request.type === 'prompt') {
    active = true; reply(request); send({type: 'agent_start'});
    if (mode === 'hang' || mode === 'cancel-order') return;
    if (mode === 'interaction') { send({type: 'extension_ui_request', id: 'question-1', method: 'confirm', title: 'PRIVATE_QUESTION'}); return; }
    if (mode === 'shell' || mode === 'custom-tool') { send({type: 'tool_execution_start', toolCallId: 'call-shell', toolName: mode === 'shell' ? 'bash' : 'custom', args: {command: 'PRIVATE_NOT_EXECUTED'}}); return; }
    if (mode === 'retry') {
      send(message('initial failed output', 'error')); send({type: 'agent_end', messages: [], willRetry: true});
      setTimeout(() => { send({type: 'auto_retry_start', attempt: 1, errorMessage: 'PRIVATE_RETRY'});
        send({type: 'agent_start'}); finish(); }, 40); return;
    }
    send({type: 'tool_execution_start', toolCallId: 'read-one', toolName: 'read', args: {path: 'PRIVATE_INPUT'}});
    send({type: 'tool_execution_end', toolCallId: 'read-one', toolName: 'read', isError: false, result: {private: 'PRIVATE_OUTPUT'}});
    finish(mode === 'error' ? 'error' : mode === 'length' ? 'length' : 'stop'); return;
  }
  if (request.type === 'extension_ui_response') { if (request.cancelled === true) finish(); return; }
  if (request.type === 'clear_queue') { cleared = true; reply(request, {steering: [], followUp: []}); return; }
  if (request.type === 'abort') {
    if (mode === 'cancel-order') fs.writeFileSync(path.join(process.cwd(), 'cancel-order.json'), JSON.stringify({cleared}));
    reply(request); if (active) finish('aborted'); return;
  }
}
process.stdin.on('data', bytes => { buffer += bytes.toString('utf8'); let index;
  while ((index = buffer.indexOf('\n')) >= 0) { const line = buffer.slice(0, index); buffer = buffer.slice(index + 1); handle(JSON.parse(line)); }
});
