// Deterministic ACP hold point, not a model. The ORIGINAL runtime owns this
// process and its cleanup. No Store writes, receipts or external process kills.
import assert from 'node:assert/strict';
import {createInterface} from 'node:readline';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  const reply = result => send({jsonrpc: '2.0', id: request.id, result});
  if (request.method === 'initialize') reply({protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') reply({sessionId: 'held-original-leader'});
  else if (request.method === 'session/prompt') {
    const input = JSON.parse(request.params.prompt[0].text.split('\n完整冻结输入：').at(-1));
    assert.equal(input.profile, 'task-managed-leader/v1');
    assert.ok(input.snapshot.plan); assert.equal(input.snapshot.selection.length, 2);
    assert.equal(input.snapshot.interactions.replies.length, 1);
    send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
      update: {sessionUpdate: 'tool_call', toolCallId: 'leader-recovery-hold', kind: 'think', status: 'in_progress'}}});
    // Deliberately no decision/end_turn. Service death must cause original
    // custody cleanup; the test has a finite outer watchdog and owns only CLI.
  }
}
