// Test-only ACP peer. Explicit release files control ordering, never cleanup.
import fs from 'node:fs';
import path from 'node:path';
import {createInterface} from 'node:readline';
import {setTimeout as pause} from 'node:timers/promises';
import {proposal} from '../task-team-integration/scenario.fixture.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const respond = (message, result) => send({jsonrpc: '2.0', id: message.id, result});
async function prompt(message) {
  const text = message.params.prompt[0].text, data = JSON.parse(text.slice(text.indexOf('{"task":')));
  const held = data.task.intent === 'hold-planner' && data.node.role === 'planner' || data.task.intent === 'hold-authors' && data.node.role === 'author';
  if (held) {
    const until = Date.now() + 30000, release = path.join(process.env.WORKER_RELEASE_PARENT, path.basename(process.cwd()));
    while (!fs.existsSync(release)) {if (Date.now() >= until) throw Error('fixture release deadline'); await pause(10);}
  }
  let result;
  if (data.node.role === 'planner') result = proposal();
  else {
    const rows = JSON.parse(fs.readFileSync('sales.json', 'utf8')).rows.filter(row => row.region === data.node.id && row.status === 'paid');
    fs.writeFileSync(data.node.id + '.json', JSON.stringify({region: data.node.id, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)}), {flag: 'wx', mode: 0o600});
    result = {message: '原受管进程写入候选，不宣称验收'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: message.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(result)}}}});
  respond(message, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const message = JSON.parse(line);
  if (message.method === 'initialize') respond(message, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (message.method === 'session/new') respond(message, {sessionId: 'worker-cancel-fixture'});
  else if (message.method === 'session/prompt') void prompt(message).catch(() => process.exitCode = 1);
}
