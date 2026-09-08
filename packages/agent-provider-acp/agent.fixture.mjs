// Deterministic ACP peer, never calls a model or network.
const mode = process.argv[2] ?? 'normal';
const send = message => process.stdout.write(JSON.stringify(message) + '\n');
const respond = (message, result) => send({jsonrpc: '2.0', id: message.id, result});
const update = value => send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: 'session-fixture', update: value}});
let buffer = '', pending;
for await (const chunk of process.stdin) {
  buffer += chunk.toString();
  for (;;) {
    const at = buffer.indexOf('\n'); if (at < 0) break;
    const message = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
    if (message.method === 'initialize') {
      if (mode === 'hang-init') continue;
      respond(message, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
    } else if (message.method === 'session/new') respond(message, {sessionId: 'session-fixture'});
    else if (message.method === 'session/prompt') {
      pending = message;
      if (mode === 'permission' || mode === 'permission-execute') {
        if (mode === 'permission-execute') update({sessionUpdate: 'tool_call', toolCallId: 'tool-one', kind: 'execute', status: 'pending'});
        send({jsonrpc: '2.0', id: 'permission-fixture', method: 'session/request_permission', params: {
          sessionId: 'session-fixture', _meta: {private: 'PRIVATE_META'},
          toolCall: {toolCallId: 'tool-one', title: 'Read approved fixture', kind: mode === 'permission-execute' ? 'execute' : 'read', rawInput: {path: 'fixture.txt'}, _meta: {private: 'PRIVATE_META'}},
          options: [{optionId: 'once', name: 'Allow once', kind: 'allow_once'}, {optionId: 'deny', name: 'Deny', kind: 'reject_once'}]}});
        continue;
      }
      update({sessionUpdate: 'tool_call', toolCallId: 'tool-one', kind: 'read', status: 'in_progress', rawInput: {secret: 'PRIVATE_INPUT'}, _meta: {private: 'PRIVATE_META'}});
      update({sessionUpdate: 'agent_thought_chunk', content: {type: 'text', text: 'PRIVATE_THOUGHT'}});
      update({sessionUpdate: 'usage_update', used: 50, size: 1000, _meta: {private: 'PRIVATE_META'}});
      update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: mode === 'overflow' ? 'x'.repeat(32769) : 'public output'}});
      if (mode === 'overflow') update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: 'y'.repeat(32769)}});
      if (mode === 'hang-prompt') continue;
      update({sessionUpdate: 'tool_call_update', toolCallId: 'tool-one', status: 'completed', rawOutput: {secret: 'PRIVATE_OUTPUT'}});
      process.stderr.write('PRIVATE_STDERR');
      respond(message, {stopReason: mode === 'refusal' ? 'refusal' : 'end_turn'});
    } else if (message.method === 'session/cancel') { if (pending) respond(pending, {stopReason: 'cancelled'}); }
    else if (message.id === 'permission-fixture') {
      update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(message.result)}});
      respond(pending, {stopReason: 'end_turn'});
    }
  }
}
