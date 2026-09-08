import { spawn } from 'node:child_process';
import { Transform } from 'node:stream';
import { PROTOCOL, BUFFER_BYTES, BOOT_WAIT_MS, CLEANUP_GRACE_MS, executionId, validateOptions } from './protocol.mjs';

// This checked-in Node program is launched detached by launchAcp. It stays the
// live group leader until it signals its OWN group. No replayed PID is accepted.
// Trusted ordinary-user execution only; deliberately detached children can
// escape an inherited POSIX group, so this is not a malicious-code sandbox.
let child, launched = false, cleaning = false, scopeId = null, deadlineTimer;
let inputBytes = 0, outputBytes = 0, stderrBytes = 0;
const keepAlive = setInterval(() => {}, 60000);
const bootstrap = setTimeout(() => clean('bootstrap_timeout'), BOOT_WAIT_MS);
process.on('SIGTERM', () => { if (!cleaning) clean('external_stop'); });
process.on('SIGINT', () => { if (!cleaning) clean('external_stop'); });
process.on('SIGHUP', () => { if (!cleaning) clean('external_stop'); });
const counts = () => ({ inputBytes, outputBytes, stderrBytes });
function send(message) {
  if (!process.connected) return;
  try { process.send({ protocol: PROTOCOL, executionId: scopeId, ...message }, error => { if (error && !cleaning) clean('parent_disconnected'); }); }
  catch { if (!cleaning) clean('parent_disconnected'); }
}
function clean(reason) {
  if (cleaning) return;
  cleaning = true; clearTimeout(bootstrap); clearTimeout(deadlineTimer);
  send({ type: 'cleaning', reason, at: new Date().toISOString(), ...counts() });
  // SIGTERM is graceful for the Agent; this leader handles it and remains alive
  // to prevent group-id reuse until the unconditional final group-wide SIGKILL.
  try { process.kill(-process.pid, 'SIGTERM'); }
  catch { send({ type: 'cleanup_error' }); }
  setTimeout(() => {
    try { process.kill(-process.pid, 'SIGKILL'); }
    catch {
      send({ type: 'cleanup_error' }); clearInterval(keepAlive);
      process.exitCode = 1; process.disconnect?.();
    }
  }, CLEANUP_GRACE_MS);
}
function bridge(limit, kind) {
  return new Transform({ highWaterMark: BUFFER_BYTES, transform(chunk, _encoding, callback) {
    if (kind === 'input') inputBytes += chunk.length; else outputBytes += chunk.length;
    if ((kind === 'input' ? inputBytes : outputBytes) > limit) {
      clean(kind + '_limit'); callback(new Error('runtime_byte_limit')); return;
    }
    callback(null, chunk);
  } });
}
function launch(options) {
  try { options = validateOptions(options); }
  catch { clean('invalid_launch'); return; }
  launched = true; clearTimeout(bootstrap);
  deadlineTimer = setTimeout(() => clean('deadline'), Math.max(0, options.deadline - Date.now()));
  try { child = spawn(options.executable, options.args, { cwd: options.cwd, env: options.env,
    detached: false, stdio: ['pipe', 'pipe', 'pipe'] }); }
  catch { send({ type: 'spawn_failed' }); clean('spawn_failed'); return; }
  child.once('spawn', () => send({ type: 'started', agentPid: child.pid, at: new Date().toISOString() }));
  child.once('error', () => { send({ type: 'spawn_failed' }); clean('spawn_failed'); });
  child.once('exit', (code, signal) => {
    send({ type: 'agent_exit', code, signal, at: new Date().toISOString(), ...counts() });
    // Do not wait for stdio close: a descendant can still hold those pipes.
    clean('agent_exit');
  });
  const input = bridge(options.limits.inputBytes, 'input'), output = bridge(options.limits.outputBytes, 'output');
  for (const stream of [input, output, child.stdin, child.stdout]) stream.on('error', () => clean('stream_error'));
  child.stderr.on('error', () => clean('stream_error'));
  // Raw stderr is discarded, never printed, forwarded in IPC, or logged. Only
  // its bounded byte count crosses this boundary.
  child.stderr.on('data', chunk => { stderrBytes += chunk.length; if (stderrBytes > options.limits.stderrBytes) clean('stderr_limit'); });
  // pipe() propagates byte-stream backpressure in both directions. ACP stdin
  // stays open across initialize, sessions, prompts and permission responses.
  process.stdin.pipe(input).pipe(child.stdin);
  child.stdout.pipe(output).pipe(process.stdout, { end: false });
}
process.stdin.on('error', () => clean('parent_stream_error'));
process.stdout.on('error', () => clean('parent_stream_error'));
process.stdin.on('end', () => clean('parent_input_closed'));
process.on('disconnect', () => clean('parent_disconnected'));
process.on('message', message => {
  if (message?.protocol !== PROTOCOL || !executionId(message.executionId)) { clean('invalid_control'); return; }
  if (scopeId !== null && scopeId !== message.executionId) { clean('invalid_control'); return; }
  scopeId ??= message.executionId;
  if (message.type === 'stop') { clean('owner_stop'); return; }
  if (message.type !== 'launch' || launched || cleaning) { if (!cleaning) clean('invalid_control'); return; }
  launch(message.options);
});
if (!process.send || !['darwin', 'linux'].includes(process.platform)) process.exit(1);
// A missing group whose id is our still-live pid means we were not detached.
try { process.kill(-process.pid, 0); }
catch { process.exit(1); }
send({ type: 'ready', guardPid: process.pid });
