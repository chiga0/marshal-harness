import {spawn} from 'node:child_process';
import path from 'node:path';
import {text, object} from './bridge-contract.mjs';

/** Pi's native tool keeps parsing, session env and output formatting. Only its
 * operations backend changes: the shell inherits the Runtime guard's live
 * group, instead of Pi's default detached group. No PID registry or cleanup
 * claim is created here; final cleanup is exclusively Runtime.stop(). */
export function createInheritedShellOperations({cwd, deadline, resolveShell, environment} = {}) {
  if (!path.isAbsolute(cwd ?? '') || !Number.isSafeInteger(deadline) || typeof resolveShell !== 'function' || typeof environment !== 'function')
    throw Error('pi_bridge_invalid_shell');
  const children = new Set();
  return Object.freeze({async exec(command, executionCwd, {onData, signal, timeout, env} = {}) {
    if (executionCwd !== cwd || !text(command, 65536) || !command.trim() || typeof onData !== 'function' || signal?.aborted ||
      Date.now() >= deadline || children.size >= 4 || timeout !== undefined && (!Number.isFinite(timeout) || timeout <= 0)) throw Error('pi_bridge_shell_denied');
    const config = resolveShell(), childEnv = env ?? environment();
    if (!object(config) || !text(config.shell, 8192) || !path.isAbsolute(config.shell) || !Array.isArray(config.args) || config.args.length > 16 ||
      config.args.some(arg => !text(arg, 4096)) || config.commandTransport !== undefined && config.commandTransport !== 'stdin' || !object(childEnv))
      throw Error('pi_bridge_invalid_shell');
    const duration = Math.min(deadline - Date.now(), timeout === undefined ? Infinity : timeout * 1000);
    return await new Promise((resolve, reject) => {
      let child, timer, killTimer, idleTimer, settled = false, exited = false, exitCode, failure, bytes = 0;
      const stop = reason => { failure ??= reason; if (settled || killTimer) return; child?.kill('SIGTERM');
        killTimer = setTimeout(() => { if (!settled) child?.kill('SIGKILL'); }, 100); };
      const abort = () => stop('aborted');
      function finish(code) {
        if (settled) return; settled = true; clearTimeout(timer); clearTimeout(killTimer); clearTimeout(idleTimer); children.delete(child); signal?.removeEventListener('abort', abort);
        child?.stdout?.destroy(); child?.stderr?.destroy();
        if (failure) reject(Error(failure)); else resolve({exitCode: code});
      }
      try {
        const stdin = config.commandTransport === 'stdin';
        child = spawn(config.shell, stdin ? config.args : [...config.args, command], {
          cwd, env: childEnv, detached: false, stdio: [stdin ? 'pipe' : 'ignore', 'pipe', 'pipe']});
        children.add(child);
        child.once('error', () => { failure = 'pi_bridge_shell_spawn_failed'; finish(null); });
        // Retain native Pi's output-idle grace: exit is not stdio close. Every
        // tail chunk restarts the 100ms grace, bounded by the original deadline.
        const idle = () => { clearTimeout(idleTimer); idleTimer = setTimeout(() => finish(exitCode), 100); };
        child.once('exit', code => { exited = true; exitCode = code; idle(); });
        child.once('close', code => finish(code));
        for (const stream of [child.stdout, child.stderr]) {
          stream.on('error', () => stop('pi_bridge_shell_stream_failed'));
          stream.on('data', chunk => { if (exited) idle(); bytes += chunk.length;
            if (bytes > 4 * 1024 * 1024) { stop('pi_bridge_shell_output_limit'); return; }
            try { onData(chunk); } catch { stop('pi_bridge_shell_callback_failed'); }
          });
        }
        child.stdin?.on('error', () => stop('pi_bridge_shell_input_failed'));
        if (stdin) child.stdin.end(command);
        timer = setTimeout(() => { stop('timeout:' + (timeout ?? 'execution')); if (exited) finish(exitCode); }, Math.max(1, duration));
        signal?.addEventListener('abort', abort, {once: true}); if (signal?.aborted) abort();
      } catch { failure = 'pi_bridge_shell_spawn_failed'; finish(null); }
    });
  }});
}
