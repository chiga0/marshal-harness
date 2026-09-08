// Checked-in deterministic test Agent. No model, network or native helper.
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const mode = process.env.AGENT_RUNTIME_FIXTURE_MODE ?? 'normal';
const HERE = fileURLToPath(import.meta.url);
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const response = (request, result) => send({ jsonrpc: '2.0', id: request.id, result });
const update = (sessionId, value) => send({ jsonrpc: '2.0', method: 'session/update', params: {
  sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: JSON.stringify(value) } } } });
const pending = new Map(), permissions = new Map();
let sessions = 0, peers = 0;
if (mode === 'descendant' || mode === 'ignore-term') process.on('SIGTERM', () => {});
if (mode === 'exit-immediate') process.exit(7);
if (mode === 'stderr-overflow') process.stderr.write('PRIVATE_STDERR_FIXTURE'.repeat(512));
if (mode === 'stdout-overflow') process.stdout.write('x'.repeat(8192));
if (mode === 'descendant' || mode === 'ignore-term') {
  process.stdin.resume(); setInterval(() => {}, 1000);
} else {
  let raw = '';
  for await (const chunk of process.stdin) {
    raw += chunk.toString('utf8');
    if (raw.length > 2 * 1024 * 1024) process.exit(9);
    for (;;) {
      const index = raw.indexOf('\n'); if (index < 0) break;
      const message = JSON.parse(raw.slice(0, index)); raw = raw.slice(index + 1);
      if (message.method === 'initialize') response(message, { protocolVersion: 1,
        agentInfo: { name: 'checked-in-node-fixture', version: '1' }, agentCapabilities: { loadSession: false } });
      else if (message.method === 'session/new') response(message, { sessionId: 'fixture-session-' + ++sessions });
      else if (message.method === 'session/prompt') {
        const sessionId = message.params.sessionId, text = message.params.prompt[0].text;
        if (text === 'hang') pending.set(sessionId, message);
        else if (text === 'permission') {
          const peer = 'permission-' + ++peers;
          pending.set(sessionId, message); permissions.set(peer, { sessionId, message });
          send({ jsonrpc: '2.0', id: peer, method: 'session/request_permission', params: { sessionId,
            toolCall: { toolCallId: 'fixture-tool', status: 'pending', title: 'Fixture permission', rawInput: { action: 'fixture' } },
            options: [{ optionId: 'once', name: 'Allow once', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }] } });
        } else if (text === 'descendant' || text === 'descendant-and-exit') {
          const child = spawn(process.execPath, [HERE], { env: { AGENT_RUNTIME_FIXTURE_MODE: 'descendant' }, stdio: 'ignore', detached: false });
          update(sessionId, { descendantPid: child.pid });
          response(message, { stopReason: 'end_turn' });
          if (text === 'descendant-and-exit') setTimeout(() => process.exit(7), 25);
        } else {
          update(sessionId, { echo: text, envLeaked: process.env.AGENT_RUNTIME_PARENT_ONLY !== undefined });
          response(message, { stopReason: 'end_turn' });
        }
      } else if (message.method === 'session/cancel') {
        const request = pending.get(message.params.sessionId);
        if (request) {
          pending.delete(message.params.sessionId);
          update(message.params.sessionId, { cancellationObserved: true });
          setTimeout(() => response(request, { stopReason: 'cancelled' }), 30);
        }
      } else if (permissions.has(message.id)) {
        const { sessionId, message: request } = permissions.get(message.id);
        permissions.delete(message.id);
        // A denied/approved tool interaction is a fixture observation, never a
        // business verification. The real Task application decides authority.
        update(sessionId, { permission: message.result?.outcome?.outcome,
          optionId: message.result?.outcome?.optionId ?? null });
        if (pending.has(sessionId)) { pending.delete(sessionId); response(request, { stopReason: 'end_turn' }); }
      } else process.exit(8);
    }
  }
}
