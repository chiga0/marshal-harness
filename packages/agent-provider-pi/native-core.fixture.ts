// Explicit installed pi-agent-core + original SDK, deterministic stream only.
// No Pi CLI/session/login/model transport is constructed by this test peer.
import {pathToFileURL} from 'node:url';
import fs from 'node:fs';
const [mode, coreEntry] = process.argv.slice(2);
const extension = process.argv[process.argv.indexOf('--extension') + 1];
const {runAgentLoop} = await import(pathToFileURL(coreEntry).href);
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const handlers = new Map(), tools = new Map(), questions = new Map(); let sequence = 0, controller;
const proof = {definitionSelected: 0, notExecuted: 0, permission: 0, executeEntered: 0, ends: []};
const ctx = {cwd: process.cwd(), sessionManager: {getSessionId: () => 'native-core-session', getSessionFile: () => undefined}, ui: {
  notify: message => { const value = JSON.parse(message);
    if (value.type === 'definition-selected') proof.definitionSelected++;
    if (value.type === 'not-executed') proof.notExecuted++;
    send({type: 'extension_ui_request', id: 'ui-' + ++sequence, method: 'notify', message}); },
  confirm(title, message) { const id = 'ui-' + ++sequence; return new Promise(resolve => {
    proof.permission++;
    questions.set(id, resolve); send({type: 'extension_ui_request', id, method: 'confirm', title, message}); }); }
}};
await (await import(pathToFileURL(extension).href)).default({on: (name, fn) => handlers.set(name, fn),
  registerTool: tool => tools.set(tool.name, tool), getActiveTools: () => ['write'], setActiveTools() {}});
await handlers.get('session_start')({}, ctx);
const response = (request, data) => send({type: 'response', id: request.id, command: request.type, success: true, data});
async function prompt(request) {
  response(request); controller = new AbortController(); let turns = 0;
  const original = tools.get('write');
  const selected = mode === 'bypass-error' ? {...original, prepareArguments: undefined, execute: async () => { proof.executeEntered++; throw Error('Operation aborted'); }} :
    {...original, execute: (id, args, signal, update) => { proof.executeEntered++; return original.execute(id, args, signal, update, ctx); }};
  const emit = async event => { await handlers.get(event.type)?.(event, ctx);
    if (event.type === 'tool_execution_end') { proof.ends.push({id: event.toolCallId, isError: event.isError});
      fs.writeFileSync('core-proof.json', JSON.stringify(proof), {mode: 0o600}); }
    send(event); };
  const stream = () => {
    const call = {type: 'toolCall', id: 'write-one', name: 'write', arguments: mode === 'invalid-arguments' ? {path: 'output.txt'} : {path: 'output.txt', content: 'not executed'}};
    const first = turns++ === 0, message = {role: 'assistant', content: first ? [call] : [{type: 'text', text: 'original loop finished'}],
      stopReason: first ? mode === 'truncated' ? 'length' : 'toolUse' : controller.signal.aborted ? 'aborted' : 'stop'};
    return {async *[Symbol.asyncIterator]() { yield {type: 'done'}; }, result: async () => message};
  };
  await runAgentLoop([{role: 'user', content: request.message}], {systemPrompt: '', messages: [], tools: [selected]},
    {model: {provider: 'deterministic-test'}, convertToLlm: value => value, beforeToolCall: async event => {
      const result = await handlers.get('tool_call')?.({toolName: event.toolCall.name, toolCallId: event.toolCall.id, input: event.args}, ctx);
      if (mode === 'cancel-before-execute') controller.abort(); return result;
    }}, emit, controller.signal, stream);
  send({type: 'agent_settled'});
}
let buffer = '';
process.stdin.on('data', chunk => { buffer += chunk.toString(); let at;
  while ((at = buffer.indexOf('\n')) >= 0) {
    const request = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
    if (request.type === 'get_state') response(request, {sessionId: 'native-core-session', isStreaming: false, isCompacting: false, pendingMessageCount: 0, messageCount: 0});
    else if (request.type === 'prompt') void prompt(request).catch(() => { send({type: 'extension_error'}); });
    else if (request.type === 'clear_queue') response(request);
    else if (request.type === 'abort') { controller?.abort(); response(request); }
    else if (request.type === 'extension_ui_response') { questions.get(request.id)?.(request.confirmed === true); questions.delete(request.id); }
  }
});
