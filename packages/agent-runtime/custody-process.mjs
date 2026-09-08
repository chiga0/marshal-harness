import {generateKeyPairSync, randomUUID, sign} from 'node:crypto';
import {launchCustodyStreams} from './index.mjs';
import {BOOT_WAIT_MS, CLEANUP_WAIT_MS} from './protocol.mjs';
import {CUSTODY_PROFILE, canonical, custodyDigest, validBinding} from './custody-contract.mjs';
import {CustodyFiles} from './custody-files.mjs';

// No model protocol, Store, credentials, permission policy or result acceptance.
// This process retains the ORIGINAL guard handle after its service IPC dies.
let descriptor, key, files, runtime, launchPromise, sealed = false, permitted = false, finished = false, prepareTimer;
let stdoutDrained = Promise.resolve(), drainUntil, outputStream;
const originalParent = process.ppid;
const send = message => { if (process.connected) try { process.send({profile: CUSTODY_PROFILE, ...message}, () => {}); } catch {} };
async function settle(cleanup) {
  if (finished) return; finished = true; sealed = true; clearTimeout(prepareTimer);
  let drainTimer;
  try {
    await Promise.race([stdoutDrained, new Promise(resolve => {
      drainTimer = setTimeout(resolve, Math.max(0, (drainUntil ?? performance.now()) - performance.now()));
    })]);
  } finally { clearTimeout(drainTimer); outputStream?.unpipe(process.stdout); }
  const payload = {profile: CUSTODY_PROFILE, custodyId: descriptor.custodyId, executionId: descriptor.executionId,
    bindingDigest: descriptor.bindingDigest, permitReceived: permitted, cleanup, observedAt: new Date().toISOString()};
  const observation = {payload, signature: sign(null, canonical(payload), key).toString('base64')};
  try {
    files.write(descriptor.custodyId, observation);
    // stdout and IPC are different pipes. The service also requires actual EOF
    // before resolving a command; queued business bytes cannot be truncated by
    // an earlier control observation.
    await new Promise(resolve => process.stdout.end(resolve));
    send({type: 'complete', observation});
  }
  catch { send({type: 'storage-failed'}); }
  finally { files.close(); process.stdin.destroy(); process.stdout.end(); if (process.connected) process.disconnect(); }
}
async function stop() {
  if (sealed) return; sealed = true; clearTimeout(prepareTimer);
  if (launchPromise) {
    try { runtime = await launchPromise; await settle(await runtime.stop()); }
    catch (error) { await settle(error.completion ?? {executionId: descriptor.executionId, started: null, cleaned: false, scope: 'unconfirmed', reason: 'cleanup_unconfirmed'}); }
  } else if (descriptor) await settle({executionId: descriptor.executionId, started: null, cleaned: true, scope: 'none-start', reason: 'custody_not_launched'});
  else {
    process.exitCode = 1; files?.close(); process.stdin.destroy(); process.stdout.end();
    if (process.connected) process.disconnect();
  }
}
process.on('message', message => {
  if (sealed || message?.profile !== CUSTODY_PROFILE) { void stop(); return; }
  if (message.type === 'prepare' && !descriptor) {
    try {
      if (!validBinding(message.binding) || message.binding.deadline <= Date.now() || message.binding.ownerExpiresAt <= Date.now() || process.ppid !== originalParent) throw new Error('binding');
      files = new CustodyFiles(message.root);
      const pair = generateKeyPairSync('ed25519'); key = pair.privateKey;
      descriptor = {profile: CUSTODY_PROFILE, custodyId: randomUUID(), executionId: randomUUID(),
        publicKey: pair.publicKey.export({format: 'der', type: 'spki'}).toString('base64'),
        binding: message.binding, bindingDigest: custodyDigest(message.binding)};
      send({type: 'prepared', descriptor});
      clearTimeout(prepareTimer);
      prepareTimer = setTimeout(() => void stop(), Math.max(1, Math.min(BOOT_WAIT_MS, descriptor.binding.deadline - Date.now())));
    } catch { sealed = true; files?.close(); process.exitCode = 1; if (process.connected) process.disconnect(); }
  } else if (message.type === 'launch' && descriptor && !permitted && message.bindingDigest === descriptor.bindingDigest &&
      message.custodyId === descriptor.custodyId && process.connected && process.ppid === originalParent &&
      message.options?.deadline === descriptor.binding.deadline && Date.now() < Math.min(descriptor.binding.deadline, descriptor.binding.ownerExpiresAt)) {
    permitted = true; clearTimeout(prepareTimer);
    launchPromise = launchCustodyStreams({...message.options, executionId: descriptor.executionId, createClient: ({readable, writable}) => {
      outputStream = readable;
      stdoutDrained = new Promise(resolve => { readable.once('end', resolve); readable.once('close', resolve); });
      process.stdin.pipe(writable); readable.pipe(process.stdout, {end: false});
      return {close() { drainUntil ??= performance.now() + CLEANUP_WAIT_MS; process.stdin.unpipe(writable); }};
    }});
    void launchPromise.then(async handle => {
      runtime = handle;
      send({type: 'started', started: handle.started});
      void handle.exited.then(exit => send({type: 'agent-exit', exit}));
      if (sealed) await handle.stop();
      await settle(await handle.completion);
    }, async error => settle(error.completion ?? {executionId: descriptor.executionId, started: null, cleaned: false, scope: 'unconfirmed', reason: 'cleanup_unconfirmed'}));
  } else if (message.type === 'stop' && descriptor && message.custodyId === descriptor.custodyId) void stop();
  else void stop(); // No duplicate launch, reconnect or delayed permit authority.
});
process.on('disconnect', () => void stop());
process.on('SIGTERM', () => void stop()); process.on('SIGINT', () => void stop());
process.stdin.on('error', () => void stop()); process.stdout.on('error', () => void stop());
prepareTimer = setTimeout(() => { void stop(); if (!descriptor) process.exit(1); }, BOOT_WAIT_MS + CLEANUP_WAIT_MS);
