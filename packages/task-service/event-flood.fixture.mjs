// 仅无模型故障验证：异常字节来自原受管 ACP 子进程，不替换 Core reducer。
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createInterface} from 'node:readline';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';

let configuration;
if (process.argv.includes('--agent')) {
  const send = value => process.stdout.write(JSON.stringify(value) + '\n');
  for await (const line of createInterface({input: process.stdin})) {
    const message = JSON.parse(line);
    if (message.method === 'initialize') send({jsonrpc: '2.0', id: message.id, result: {protocolVersion: 1, agentCapabilities: {loadSession: false}}});
    else if (message.method === 'session/new') send({jsonrpc: '2.0', id: message.id, result: {sessionId: 'flood-session'}});
    else if (message.method === 'session/prompt') {
      const update = value => send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: 'flood-session', update: value}});
      if (process.env.FLOOD_CASE === 'updates') {
        // 未归一化 thought 仍计入原 Provider 的4096条预算，不持久化正文。
        for (let index = 0; index < 4100; index++) update({sessionUpdate: 'agent_thought_chunk', content: {type: 'text', text: 'private-flood-canary'}});
      } else if (process.env.FLOOD_CASE === 'frame') process.stdout.write('private-flood-canary' + 'x'.repeat(1024 * 1024));
      else update({sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: 'private-flood-canary' + 'x'.repeat(65536)}});
      // 预算越界后的正常终态不能挽救原失败，也不能变成有效成果。
      send({jsonrpc: '2.0', id: message.id, result: {stopReason: 'end_turn'}});
    }
  }
} else {
  if (process.env.MARSHAL_EVENT_FLOOD_FIXTURE !== '1') throw Error('test-only configuration');
  const base = (await import('./soak.fixture.mjs')).default;
  const original = base.providers.get('fixture-acp'), tickets = new Map();
  const root = process.argv[process.argv.indexOf('--root') + 1];
  const journal = path.join(path.dirname(root), 'flood-observations.jsonl');
  const record = value => {
    const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
    try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  };
  const providers = Object.fromEntries(['updates', 'output', 'frame'].map(mode => [mode, createAcpProvider({id: original.id,
    executable: process.execPath, args: [fileURLToPath(import.meta.url), '--agent'], env: {FLOOD_CASE: mode}, custodyProfile: original.custodyProfile})]));
  configuration = {...base, providers: new Map([[original.id, {...original, start(input) {
    const ticket = tickets.get(input.cwd);
    const mode = ['updates', 'output', 'frame'].find(value => ticket?.input.task.intent === 'flood ' + value);
    if (!mode || ticket.role !== 'author' || ticket.nodeId !== 'east') return original.start(input);
    const handle = providers[mode].start(input), identity = {taskId: ticket.taskId, workerId: ticket.workerId, mode};
    return {...handle, started: handle.started.then(started => {record({type: 'started', ...identity, started}); return started;}),
      completion: handle.completion.then(result => {
        record({type: 'completion', ...identity, status: result.status, reason: result.reason, cleanup: result.cleanup,
          outputBytes: Buffer.byteLength(result.outputText), usage: result.usage}); return result;
      })};
  }}]]), businessFactory(context) {
    const business = base.businessFactory(context);
    return {...business, async prepare(ticket, options) {
      const value = await business.prepare(ticket, options); tickets.set(value.cwd, ticket); return value;
    }, release(ticket) {
      for (const [cwd, value] of tickets) if (value.workerId === ticket.workerId) tickets.delete(cwd);
      return business.release(ticket);
    }};
  }};
}
export default configuration;
