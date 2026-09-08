// An actual ACP child process that edits a real Git worktree. No model or
// authoritative acceptance is simulated; only candidate implementation is fixed.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {execFileSync} from 'node:child_process';
import {proposal} from './scenario.fixture.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const reply = (message, result) => send({jsonrpc: '2.0', id: message.id, result});
const source = {
  library: `export function net(cents, discount) {
  if (!Number.isSafeInteger(cents) || cents < 0 || !Number.isSafeInteger(discount) || discount < 0 || discount > cents) throw Error('invalid');
  return cents - discount;
}\n`,
  client: `export function invoice(rows, net) {
  if (!Array.isArray(rows)) throw Error('invalid');
  const lines = []; let total = 0;
  for (const row of rows) {
    if (!row || typeof row.sku !== 'string' || !row.sku.trim()) throw Error('invalid');
    const amount = net(row.cents, row.discount); total += amount;
    if (!Number.isSafeInteger(amount) || !Number.isSafeInteger(total)) throw Error('overflow');
    lines.push({sku: row.sku, amount});
  }
  return {lines, total};
}\n`,
};
for await (const line of createInterface({input: process.stdin})) {
  const message = JSON.parse(line);
  if (message.method === 'initialize') reply(message, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (message.method === 'session/new') reply(message, {sessionId: 'git-fixture'});
  else if (message.method === 'session/prompt') {
    const raw = message.params.prompt[0].text, data = JSON.parse(raw.slice(raw.indexOf('{"task":')));
    const finish = value => {
      send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: message.params.sessionId,
        update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(value)}}}});
      reply(message, {stopReason: 'end_turn'});
    };
    if (data.node.role === 'planner') {finish(proposal()); continue;}
    const base = execFileSync('/usr/bin/git', ['rev-parse', 'HEAD'], {encoding: 'utf8', timeout: 3000, env: {PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null'}}).trim();
    if (base !== data.git.base || !fs.statSync('.git').isFile()) throw Error('not_the_bound_worktree');
    if (process.env.GIT_BUSINESS_FIXTURE_MODE === 'hang') continue;
    setTimeout(() => {
      let content = source[data.node.id];
      if (process.env.GIT_BUSINESS_FIXTURE_MODE === 'wrong' && data.node.id === 'library') content = content.replace('cents - discount', 'cents');
      fs.writeFileSync(data.git.writePaths[0], content);
      if (process.env.GIT_BUSINESS_FIXTURE_MODE === 'extra' && data.node.id === 'library') fs.writeFileSync('untouched.txt', 'not authorized\n');
      finish({message: '实际代码候选已写入，等待独立验收'});
    }, 500); // Deterministic fixture overlap, not real-model/CPU evidence.
  }
}
