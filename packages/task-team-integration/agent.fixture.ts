// Deterministic ACP process fixture, NOT a real model and never production registration.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {proposal} from './scenario.fixture.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const respond = (message, result) => send({jsonrpc: '2.0', id: message.id, result});
for await (const line of createInterface({input: process.stdin})) {
  const message = JSON.parse(line);
  if (message.method === 'initialize') respond(message, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (message.method === 'session/new') respond(message, {sessionId: 'fixture-session'});
  else if (message.method === 'session/prompt') {
    const text = message.params.prompt[0].text, data = JSON.parse(text.slice(text.indexOf('{"task":')));
    const publish = report => {
      send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: message.params.sessionId,
        update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(report)}}}});
      respond(message, {stopReason: 'end_turn'});
    };
    if (data.node.role === 'planner') publish(proposal());
    else if (process.env.TEAM_FIXTURE_MODE !== 'hang') setTimeout(() => {
      const rows = JSON.parse(fs.readFileSync('sales.json', 'utf8')).rows;
      const selected = rows.filter(row => row.region === data.node.id && row.status === 'paid');
      const result = {region: data.node.id, count: selected.length, netCents: selected.reduce((sum, row) => sum + row.cents, 0)};
      if (process.env.TEAM_FIXTURE_MODE === 'corrupt' && data.node.id === 'west') result.netCents++;
      fs.writeFileSync(data.node.id + '.json', JSON.stringify(result), {flag: 'wx', mode: 0o600});
      publish({message: '候选已写入；不声明验收通过'});
    }, 1000);
  }
}
