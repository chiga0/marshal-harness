// Deterministic ACP peer with one real inherited descendant. No model or login.
import fs from 'node:fs';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
const entry = fileURLToPath(import.meta.url);
process.on('SIGTERM', () => {}); // The original guard must finish group cleanup.
if (process.argv[2] === 'descendant') setInterval(() => {}, 1000);
else {
  const send = value => process.stdout.write(JSON.stringify(value) + '\n');
  const answer = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
  let buffer = '', started = false;
  process.stdin.on('data', bytes => {
    buffer += bytes.toString();
    if (buffer.length > 512 * 1024) process.exit(2);
    let at;
    while ((at = buffer.indexOf('\n')) >= 0) {
      const request = JSON.parse(buffer.slice(0, at)); buffer = buffer.slice(at + 1);
      if (request.method === 'initialize') answer(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
      else if (request.method === 'session/new') answer(request, {sessionId: 'crash-session'});
      else if (request.method === 'session/prompt' && !started) {
        started = true;
        const child = spawn(process.execPath, [entry, 'descendant'], {env: {}, stdio: 'ignore', detached: false});
        child.once('spawn', () => {
          fs.writeFileSync('fixture-processes.json', JSON.stringify({agentPid: process.pid, descendantPid: child.pid}), {flag: 'wx', mode: 0o600});
          send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: 'crash-session',
            update: {sessionUpdate: 'tool_call', toolCallId: 'fixture-hold', kind: 'think', status: 'in_progress'}}});
        });
      }
      // No prompt completion or fake cleanup. Parent process loss is the fault.
    }
  });
  setInterval(() => {}, 1000);
}
