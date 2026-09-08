// Deterministic ACP peer, never selected by production service-config.mjs.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {finalValues} from './policy.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const respond = (message, result) => send({jsonrpc: '2.0', id: message.id, result});
for await (const line of createInterface({input: process.stdin})) {
  const message = JSON.parse(line);
  if (message.method === 'initialize') respond(message, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (message.method === 'session/new') respond(message, {sessionId: 'window-fixture'});
  else if (message.method === 'session/prompt') {
    const text = message.params.prompt[0].text, input = JSON.parse(text.slice(text.indexOf('{"task":')));
    const publish = value => {send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: message.params.sessionId,
      update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(value)}}}}); respond(message, {stopReason: 'end_turn'});};
    if (input.node.role === 'planner') {
      const declaration = /^REGIONAL_WINDOW_FIXED_PROPOSAL_V1\n([^\n]+)\nREGIONAL_WINDOW_FIXED_PROPOSAL_END$/m.exec(text);
      if (!declaration) throw new Error('fixture-planner-declaration-missing');
      const proposed = JSON.parse(declaration[1]);
      if (process.env.WINDOW_FIXTURE === 'alter-planner-goal') proposed.nodes[0].goal += '（同义改写）';
      publish(proposed);
    }
    else if (process.env.WINDOW_FIXTURE !== 'hang') setTimeout(() => {
      const dates = finalValues(input.task), rows = JSON.parse(fs.readFileSync('sales.json')).rows;
      const chosen = rows.filter(row => row.status === 'paid' && row.region === input.node.id &&
        (process.env.WINDOW_FIXTURE === 'wrong-window' || row.date >= dates.startDate && row.date <= dates.endDate));
      const result = {region: input.node.id, ...dates, count: chosen.length, netCents: chosen.reduce((sum, row) => sum + row.cents, 0)};
      fs.writeFileSync(input.node.id + '.json', JSON.stringify(result), {flag: 'wx', mode: 0o600}); publish({message: '候选已写入，不声称验收通过'});
    }, 1000);
  }
}
