import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {AcpClient} from '../agent-acp/client.mjs';
import {BOOT_WAIT_MS, CLEANUP_WAIT_MS} from './protocol.mjs';
import {CUSTODY_PROFILE, custodyDigest, validBinding, validDescriptor, verifyObservation} from './custody-contract.mjs';
import {CustodyFiles} from './custody-files.mjs';

const ENTRY = fileURLToPath(new URL('./custody-process.mjs', import.meta.url));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
const fail = (code, completion) => Object.assign(new Error(code), {code, completion});

/** Original creation IPC owns launch/stop. Recovery has read/ACK only, never PID
 * attach, replacement launch, prompt replay or a new observer signing old facts. */
export function createExecutionCustody({root} = {}) {
  const files = new CustodyFiles(root), handles = new Set(); let closed = false;
  return Object.freeze({
    async prepare(binding) {
      if (closed || !validBinding(binding) || handles.size >= 64 || binding.deadline <= Date.now()) throw fail('custody_unavailable');
      files.check();
      const peer = spawn(process.execPath, [ENTRY], {cwd: root, env: {}, detached: true, stdio: ['pipe', 'pipe', 'ignore', 'ipc']});
      const prepared = deferred(), began = deferred(), exited = deferred(), completed = deferred();
      let descriptor, authorized = false, launched = false, stopped = false, ended = false, start = null, timer, client, observation, outputEnded = false;
      const unknown = () => ({executionId: descriptor?.executionId ?? null, started: start, cleaned: false,
        scope: 'unconfirmed', reason: 'cleanup_unconfirmed'});
      const send = value => { if (!peer.connected) return false; try { peer.send({profile: CUSTODY_PROFILE, ...value}, error => { if (error) stop(); }); return true; } catch { return false; } };
      function finish(value) {
        if (ended) return; ended = true; clearTimeout(timer);
        try { client?.close(); } catch {}
        began.resolve(undefined); exited.resolve(value.agentExit ?? {observed: false, at: null}); completed.resolve(value);
        prepared.resolve(undefined);
      }
      function stop() {
        if (stopped || ended) return completed.promise; stopped = true;
        if (descriptor) send({type: 'stop', custodyId: descriptor.custodyId});
        else if (peer.exitCode === null && peer.signalCode === null) peer.kill('SIGTERM');
        clearTimeout(timer); timer = setTimeout(() => finish(unknown()), BOOT_WAIT_MS + CLEANUP_WAIT_MS);
        return completed.promise;
      }
      function drain() {
        if (observation && outputEnded) finish(observation.payload.cleanup);
        else if (observation && !timer) timer = setTimeout(() => finish(unknown()), CLEANUP_WAIT_MS);
      }
      peer.stdin.on('error', () => void stop()); peer.stdout.on('error', () => void stop());
      peer.stdout.on('end', () => { outputEnded = true; drain(); });
      peer.on('error', () => finish(unknown()));
      peer.on('exit', () => {
        if (!ended) {
          if (!observation && descriptor) try {
            const saved = files.read(descriptor.custodyId);
            if (saved && verifyObservation(descriptor, saved)) observation = saved;
          } catch {}
          if (observation) drain(); else finish(unknown());
        }
        handles.delete(handle);
      });
      peer.on('message', message => {
        if (ended || message?.profile !== CUSTODY_PROFILE) return;
        if (message.type === 'prepared' && !descriptor && validDescriptor(message.descriptor, binding)) {
          descriptor = Object.freeze(structuredClone(message.descriptor)); clearTimeout(timer); timer = undefined;
          if (stopped) send({type: 'stop', custodyId: descriptor.custodyId});
          prepared.resolve(descriptor);
        } else if (message.type === 'started' && descriptor && !start && message.started?.executionId === descriptor.executionId) {
          start = Object.freeze(message.started); began.resolve(start);
        } else if (message.type === 'agent-exit' && descriptor) exited.resolve(message.exit);
        else if (message.type === 'complete' && descriptor && verifyObservation(descriptor, message.observation)) {
          observation = message.observation; if (!launched) peer.stdout.resume(); drain();
        } else void stop();
      });
      const handle = Object.freeze({
        get descriptor() { return descriptor; },
        permit() { if (!descriptor || stopped || ended || authorized) throw fail('custody_invalid_permit'); authorized = true; },
        stop,
        async launch(options, {createClient, onUpdate, onPermission, input = null} = {}) {
          if (!authorized || launched || stopped || ended || options.deadline !== binding.deadline) throw fail('custody_launch_denied');
          launched = true; let chunks = [], size = 0;
          if (input === null) {
            const connection = {readable: peer.stdout, writable: peer.stdin, onClose: () => void stop()};
            try {
              client = createClient ? createClient(connection) : new AcpClient({...connection, onUpdate, onPermission});
              if (!client || typeof client.close !== 'function' || typeof client.then === 'function') throw fail('custody_client_invalid');
            } catch { const cleanup = await stop(); throw fail('custody_client_invalid', cleanup); }
          } else peer.stdout.on('data', chunk => {
            size += chunk.length;
            if (size > options.limits.outputBytes) { void stop(); return; }
            chunks.push(Buffer.from(chunk));
          });
          if (!send({type: 'launch', custodyId: descriptor.custodyId, bindingDigest: descriptor.bindingDigest, options})) void stop();
          const started = await began.promise;
          if (!started) throw fail('custody_launch_failed', await completed.promise);
          if (input !== null && !stopped && input.length) peer.stdin.write(input);
          const result = input === null ? completed.promise : completed.promise.then(cleanup => ({cleanup,
            stdout: Buffer.concat(chunks), outputComplete: size === cleanup.outputBytes && size <= options.limits.outputBytes}));
          return Object.freeze({client, started, exited: exited.promise, completion: result, stop: async () => { await stop(); return result; }});
        },
      });
      handles.add(handle); timer = setTimeout(() => void stop(), Math.min(BOOT_WAIT_MS, binding.deadline - Date.now()));
      if (!send({type: 'prepare', root, binding})) void stop();
      if (!await prepared.promise) throw fail('custody_prepare_failed', await completed.promise);
      return handle;
    },
    read(descriptor) { if (closed || !validDescriptor(descriptor)) throw fail('custody_invalid_descriptor'); return files.read(descriptor.custodyId); },
    acknowledge(descriptor, digest) {
      if (closed || !validDescriptor(descriptor)) throw fail('custody_invalid_descriptor');
      const observation = files.read(descriptor.custodyId);
      if (!observation || !verifyObservation(descriptor, observation) || custodyDigest(observation) !== digest) throw fail('custody_ack_conflict');
      files.write(descriptor.custodyId, {profile: CUSTODY_PROFILE, observationDigest: digest, bindingDigest: descriptor.bindingDigest}, 'ack');
    },
    async close() { if (closed) return; closed = true; await Promise.all([...handles].map(handle => handle.stop())); files.close(); },
  });
}
