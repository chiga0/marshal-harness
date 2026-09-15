// UI-1 e2e 共享夹具：真实 main.mjs 子进程 + 无 URL 归一化的 raw HTTP。
// 无模型、无浏览器驱动；服务只从真实 stdout 公告地址与私有连接文件取得事实。
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {spawn, spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';

export const WORKTREE = path.resolve(fileURLToPath(new URL('.', import.meta.url)), '../../..');
export const CLI = path.join(WORKTREE, 'packages/task-service/main.mjs');
export const DIST = path.join(WORKTREE, 'apps/task-web/dist');
export const FIXTURE_MINIMAL = path.join(WORKTREE, 'packages/task-service/service.fixture.mjs');
export const FIXTURE_LEADER = path.join(WORKTREE, 'packages/task-service/leader-recovery.fixture.mjs');

export function ensureDist() {
  if (fs.existsSync(path.join(DIST, 'index.html'))) return;
  const result = spawnSync('npm', ['run', 'build'], {cwd: path.join(WORKTREE, 'apps/task-web'), encoding: 'utf8', timeout: 240000});
  if (result.status !== 0 || !fs.existsSync(path.join(DIST, 'index.html'))) throw new Error('dist build unavailable: ' + result.stderr);
}

export function tempParent(prefix = 'marshal-ui-e2e-') {
  return fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), prefix)));
}

/** Launch the real fixed CLI. `ui` is an absolute build dir or null (API-only). */
export function spawnService({root, mode = 'create', config = FIXTURE_MINIMAL, ui = DIST, env = {}}) {
  const args = [CLI, '--root', root, '--mode', mode, '--port', '0', '--config', config];
  if (ui) args.push('--ui', ui);
  const child = spawn(process.execPath, args, {env: {...process.env, ...env}, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '', exit = null;
  child.stdout.on('data', value => { stdout += value; if (stdout.length > 65536) child.kill('SIGKILL'); });
  child.stderr.on('data', value => { stderr += value; if (stderr.length > 65536) child.kill('SIGKILL'); });
  const closed = new Promise(resolve => { child.once('close', (code, signal) => { exit = {code, signal}; resolve(exit); }); });
  const ready = new Promise((resolve, reject) => {
    const deadline = setTimeout(() => reject(new Error('service start timeout: ' + stderr)), 30000);
    const poll = setInterval(() => {
      if (exit) { clearInterval(poll); clearTimeout(deadline); reject(new Error('service exited: ' + JSON.stringify(exit) + ' ' + stderr)); return; }
      if (!stdout.includes('\n')) return;
      clearInterval(poll); clearTimeout(deadline);
      const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n')));
      const connection = JSON.parse(fs.readFileSync(first.connectionFile));
      resolve({address: first.address, connectionFile: first.connectionFile, token: connection.token, innerURL: connection.url});
    }, 20);
  });
  const originFrom = address => address; // stdout 公告地址即 ADR0098 唯一同源；
  return {
    child, ready, originFrom,
    get stdout() { return stdout; }, get stderr() { return stderr; }, get exit() { return exit; },
    async stop() {
      if (!exit) child.kill('SIGTERM');
      const deadline = setTimeout(() => { if (!exit) child.kill('SIGKILL'); }, 10000);
      try { return await closed; } finally { clearTimeout(deadline); }
    },
  };
}

/** Raw HTTP/1.1 over net with verbatim header lines (duplicates allowed). No
 * client-side URL normalization, so traversal/Origin/Host 反例如构造所见。 */
export function rawRequest({host, port, method = 'GET', path = '/', headers = [], body = null, origin = null, hostHeader = undefined}) {
  return new Promise((resolve, reject) => {
    const socket = net.connect(port, host);
    const chunks = [];
    socket.once('error', reject);
    socket.on('data', value => chunks.push(value));
    socket.once('close', () => {
      const buffer = Buffer.concat(chunks);
      const text = buffer.toString('latin1');
      const splitAt = text.indexOf('\r\n\r\n');
      const lines = (splitAt < 0 ? text : text.slice(0, splitAt)).split('\r\n');
      const statusMatch = /^HTTP\/1\.1 (\d{3})/.exec(lines[0] ?? '');
      const received = [];
      for (const line of lines.slice(1)) {
        const at = line.indexOf(':');
        if (at > 0) received.push([line.slice(0, at).toLowerCase(), line.slice(at + 1).trim()]);
      }
      const get = name => { const hit = received.find(([key]) => key === name); return hit ? hit[1] : null; };
      const count = name => received.filter(([key]) => key === name).length;
      resolve({status: statusMatch ? Number(statusMatch[1]) : 0, headers: received, get, count,
        body: splitAt < 0 ? Buffer.alloc(0) : buffer.subarray(splitAt + 4), bodyText: splitAt < 0 ? '' : buffer.subarray(splitAt + 4).toString('latin1')});
    });
    const parts = [`${method} ${path} HTTP/1.1`];
    if (hostHeader !== null) parts.push(`Host: ${hostHeader === undefined ? `${host}:${port}` : hostHeader}`);
    for (const [name, value] of headers) parts.push(`${name}: ${value}`);
    if (origin !== null) parts.push(`Origin: ${origin}`);
    const bytes = body === null ? null : Buffer.isBuffer(body) ? body : Buffer.from(body);
    if (bytes) parts.push(`Content-Length: ${bytes.length}`);
    parts.push('Connection: close', '', '');
    socket.write(parts.join('\r\n'));
    if (bytes) socket.write(bytes);
  });
}
