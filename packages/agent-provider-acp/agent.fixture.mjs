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
      if (mode.startsWith('failed-only-')) {
        update({sessionUpdate: 'tool_call_update', toolCallId: 'tool-one', kind: mode.slice('failed-only-'.length), status: 'failed'});
        update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: 'failed fixture tool'}});
        respond(message, {stopReason: 'end_turn'}); continue;
      }
      if (mode === 'permission' || mode.startsWith('permission-execute')) {
        if (mode.startsWith('permission-execute')) update({sessionUpdate: 'tool_call', toolCallId: 'tool-one', kind: 'execute', status: 'pending'});
        send({jsonrpc: '2.0', id: 'permission-fixture', method: 'session/request_permission', params: {
          sessionId: 'session-fixture', _meta: {private: 'PRIVATE_META'},
          toolCall: {toolCallId: 'tool-one', title: 'Read approved fixture', kind: mode.startsWith('permission-execute') ? 'execute' : 'read', rawInput: {path: 'fixture.txt'}, _meta: {private: 'PRIVATE_META'}},
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
    else if (['permission-fixture', 'permission-fixture-reused'].includes(message.id)) {
      if (mode === 'permission-execute-reused-permission' && message.id === 'permission-fixture') {
        send({jsonrpc: '2.0', id: 'permission-fixture-reused', method: 'session/request_permission', params: {
          sessionId: 'session-fixture', toolCall: {toolCallId: 'tool-one', kind: 'execute', rawInput: {path: 'fixture.txt'}},
          options: [{optionId: 'deny', name: 'Deny', kind: 'reject_once'}]}}); continue;
      }
      if (mode.startsWith('permission-execute-')) {
        if (mode === 'permission-execute-reused-call') update({sessionUpdate: 'tool_call', toolCallId: 'tool-one', kind: 'execute', status: 'pending'});
        if (mode === 'permission-execute-started') update({sessionUpdate: 'tool_call_update', toolCallId: 'tool-one', kind: 'execute', status: 'in_progress'});
        const failed = {sessionUpdate: 'tool_call_update', toolCallId: mode === 'permission-execute-foreign' ? 'tool-other' : 'tool-one',
          kind: mode === 'permission-execute-kind-drift' ? 'fetch' : 'execute', status: 'failed'};
        update(failed);
        if (mode === 'permission-execute-repeated-failed') update(failed);
      }
      update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(message.result)}});
      respond(pending, {stopReason: 'end_turn'});
    }
  }
}
