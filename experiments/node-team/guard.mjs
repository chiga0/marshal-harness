import { spawn } from 'node:child_process';
import { TextDecoder } from 'node:util';

// Spawned detached by the supervisor. This Node process remains the living
// session/group leader after its Agent exits. Only authenticated inherited IPC
// controls it; no PID restored from disk is ever used to signal a process.
let child, agentExitedAt, launched = false, cleanup = false, overflow = false;
let stdout = [], stderrBytes = 0, stdoutBytes = 0;
const LIMIT = 1024 * 1024;
const keepAlive = setInterval(() => {}, 60_000);
process.on('SIGTERM', () => {});
process.on('SIGINT', () => {});
function send(message) { if (process.connected) process.send(message); }

function clean(nonce) {
  if (cleanup) return;
  cleanup = true;
  // This process itself still owns process.pid and is the group leader.
  // SIGTERM leaves the leader alive until the final group-wide SIGKILL.
  send({ type: 'cleaning', nonce });
  try { process.kill(-process.pid, 'SIGTERM'); } catch { send({ type: 'cleanup-error', nonce }); }
  setTimeout(() => {
    try { process.kill(-process.pid, 'SIGKILL'); }
    catch { clearInterval(keepAlive); process.exitCode = 1; process.disconnect?.(); }
  }, 300);
}

process.on('message', message => {
  if (message?.type === 'cleanup' && typeof message.nonce === 'string') return clean(message.nonce);
  if (message?.type !== 'launch' || launched || cleanup) return;
  launched = true;
  const { command, args, env, cwd, prompt } = message;
  if (typeof command !== 'string' || !command.startsWith('/') || !Array.isArray(args) || args.some(v => typeof v !== 'string') || typeof prompt !== 'string' || Buffer.byteLength(prompt) > 65536 || !cwd?.startsWith('/') || env === null || typeof env !== 'object' || Object.values(env).some(v => typeof v !== 'string')) {
    send({ type: 'terminal', reason: 'invalid-command', code: null, stdout: '', stdoutBytes: 0, stderrBytes: 0 }); return;
  }
  try { child = spawn(command, args, { cwd, env, detached: false, stdio: ['pipe', 'pipe', 'pipe'] }); }
  catch { send({ type: 'terminal', reason: 'spawn-failed', code: null, stdout: '', stdoutBytes: 0, stderrBytes: 0 }); return; }
  child.on('spawn', () => send({ type: 'started', pid: child.pid, at: new Date().toISOString() }));
  let lastProgress = 0;
  function progress() {
    if (Date.now() - lastProgress > 500) { lastProgress = Date.now(); send({ type: 'progress', stdoutBytes, stderrBytes, at: new Date().toISOString() }); }
  }
  function excess() {
    if (overflow) return;
    overflow = true; send({ type: 'overflow', stdoutBytes, stderrBytes });
    // The supervisor persists the failure fence and asks this live guard to
    // clean. If it is already gone, IPC disconnect executes the same cleanup.
  }
  child.stdout.on('data', chunk => {
    stdoutBytes += chunk.length;
    if (stdoutBytes <= LIMIT) stdout.push(chunk); else excess();
    progress();
  });
  child.stderr.on('data', chunk => { stderrBytes += chunk.length; if (stderrBytes > LIMIT) excess(); progress(); });
  child.stdin.on('error', () => {});
  child.on('error', () => {});
  // stdio close can be delayed by inherited descendant pipes. Freeze the
  // actual direct Agent lifetime separately from transport/group cleanup.
  child.once('exit', () => { agentExitedAt = new Date().toISOString(); });
  child.on('close', (code, signal) => {
    let raw = '', reason;
    try { raw = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(stdout)); }
    catch { reason = 'invalid-utf8-output'; }
    stdout = [];
    send({ type: 'terminal', code, signal, reason: overflow ? 'output-limit' : reason, stdout: raw, stdoutBytes, stderrBytes, agentExitedAt, at: new Date().toISOString() });
    // Deliberately do not exit: descendants remain in a group whose live
    // leader cannot be reused until supervisor-owned cleanup has completed.
  });
  child.stdin.end(prompt);
});
process.on('disconnect', () => clean('parent-disconnected'));
send({ type: 'ready', pid: process.pid });
