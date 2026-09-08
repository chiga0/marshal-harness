import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import http from 'node:http';
import { fork } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { FORMAT, Fault, fail, digest, nativeEnvironment, closedObject, privateRoot, readPrivate, atomicPrivate, id } from './store.mjs';
import { createTaskHandler } from './http-handler.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export async function sourceDigest() {
  const files = (await fs.readdir(HERE)).filter(name => name.endsWith('.mjs') && !name.includes('test')).sort();
  const chunks = [];
  for (const name of files) { chunks.push(name + '\0'); chunks.push(await fs.readFile(path.join(HERE, name))); }
  return digest(Buffer.concat(chunks.map(v => Buffer.isBuffer(v) ? v : Buffer.from(v))));
}
export async function configFromFile(file) {
  if (!path.isAbsolute(file ?? '')) fail('absolute-config-required', 400);
  let config;
  try { config = JSON.parse(await readPrivate(file, 8192)); } catch { fail('invalid-private-config', 400); }
  if (!closedObject(config, ['provider', 'executable']) || !/^[a-z][a-z0-9-]{0,31}$/.test(config.provider) || !path.isAbsolute(config.executable ?? '') || config.executable.includes('\0')) fail('invalid-config', 400);
  const stat = await fs.stat(config.executable).catch(() => fail('executable-unavailable', 400));
  if (!stat.isFile() || !(stat.mode & 0o111)) fail('executable-unavailable', 400);
  return config;
}
export async function metadataFromRoot(directory) {
  const data = JSON.parse(await readPrivate(path.join(directory, 'supervisor.lock'), 8192));
  if (!closedObject(data, ['format', 'instance', 'token', 'socket', 'sourceDigest', 'configDigest']) || data.format !== FORMAT || !id(data.instance) || !/^[0-9a-f]{64}$/.test(data.token) || data.socket !== path.join(directory, 's.sock') || !/^sha256:[0-9a-f]{64}$/.test(data.sourceDigest) || !/^sha256:[0-9a-f]{64}$/.test(data.configDigest)) fail('invalid-supervisor-lock', 503);
  return data;
}
export function rpc(metadata, input, timeout = 10000) {
  return new Promise((resolve, reject) => {
    const raw = JSON.stringify(input);
    const request = http.request({ socketPath: metadata.socket, path: '/rpc', method: 'POST', headers: { Authorization: 'Bearer ' + metadata.token, 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(raw) } }, res => {
      const chunks = []; let size = 0;
      res.on('data', chunk => { size += chunk.length; if (size > 16 * 1024 * 1024) { request.destroy(); reject(new Fault('supervisor-response-too-large', 503)); } else chunks.push(chunk); });
      res.on('end', () => {
        try { const value = JSON.parse(Buffer.concat(chunks).toString('utf8')); if (res.statusCode !== 200) reject(new Fault(typeof value.error === 'string' ? value.error : 'supervisor-unavailable', res.statusCode)); else resolve(value); }
        catch { reject(new Fault('invalid-supervisor-response', 503)); }
      });
      res.on('error', () => reject(new Fault('supervisor-unavailable', 503)));
    });
    request.setTimeout(timeout, () => request.destroy());
    request.on('error', () => reject(new Fault('supervisor-unavailable', 503)));
    request.end(raw);
  });
}
async function checkMetadata(metadata, expectedSource, expectedConfig) {
  if (metadata.sourceDigest !== expectedSource || metadata.configDigest !== expectedConfig) fail('supervisor-identity-mismatch', 409);
  const identity = await rpc(metadata, { operation: 'ping' });
  if (identity.instance !== metadata.instance || identity.sourceDigest !== expectedSource || identity.configDigest !== expectedConfig) fail('supervisor-identity-mismatch', 409);
  return metadata;
}
export async function connectSupervisor(directory, config) {
  await privateRoot(directory);
  const source = await sourceDigest(), configHash = digest(config);
  try { return await checkMetadata(await metadataFromRoot(directory), source, configHash); }
  catch (err) { if (err.code !== 'ENOENT') throw err instanceof Fault ? err : new Fault('supervisor-needs-intervention', 503); }
  const child = fork(path.join(HERE, 'supervisor.mjs'), [], { detached: true, stdio: ['ignore', 'ignore', 'ignore', 'ipc'], env: nativeEnvironment() });
  const ready = new Promise(resolve => {
    const timer = setTimeout(() => resolve(false), 8000);
    const finish = value => { clearTimeout(timer); resolve(value); };
    child.once('message', message => finish(message?.ready === true)); child.once('error', () => finish(false)); child.once('exit', () => finish(false));
  });
  child.send({ directory, config, sourceDigest: source, configDigest: configHash }, () => {});
  await ready;
  if (child.connected) child.disconnect(); child.unref();
  // If another frontend won startup, connect only to the same locked instance.
  // Failed startup never removes an unknown lock/socket or kills a disk PID.
  try { return await checkMetadata(await metadataFromRoot(directory), source, configHash); }
  catch (err) { throw err instanceof Fault ? err : new Fault('supervisor-needs-intervention', 503); }
}

export async function serve(directory, config) {
  const metadata = await connectSupervisor(directory, config);
  const token = crypto.randomBytes(32).toString('hex');
  const server = http.createServer();
  server.requestTimeout = 15000; server.headersTimeout = 5000; server.keepAliveTimeout = 1000; server.maxConnections = 32;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  const expectedHost = '127.0.0.1:' + server.address().port;
  server.on('request', createTaskHandler({ application: input => rpc(metadata, input), token, expectedHost }));
  const url = 'http://' + expectedHost, connectionFile = path.join(directory, 'connection-' + crypto.randomUUID() + '.json');
  await atomicPrivate(connectionFile, { url, token, profile: FORMAT });
  const fileIdentity = await fs.lstat(connectionFile);
  const close = async () => {
    await new Promise(resolve => { server.close(resolve); server.closeIdleConnections(); });
    try { const current = await fs.lstat(connectionFile); if (current.dev === fileIdentity.dev && current.ino === fileIdentity.ino) await fs.unlink(connectionFile); } catch (err) { if (err.code !== 'ENOENT') throw err; }
  };
  return { server, url, connectionFile, close };
}
function argumentsFrom(argv) {
  let stop = false; if (argv[0] === 'stop') { stop = true; argv = argv.slice(1); }
  const values = {};
  for (let n = 0; n < argv.length; n += 2) {
    if (!['--data-dir', '--config'].includes(argv[n]) || !argv[n + 1] || values[argv[n]]) fail('invalid-arguments', 400);
    values[argv[n]] = argv[n + 1];
  }
  if (!path.isAbsolute(values['--data-dir'] ?? '') || !stop && !path.isAbsolute(values['--config'] ?? '') || stop && values['--config']) fail('invalid-arguments', 400);
  return { stop, directory: values['--data-dir'], config: values['--config'] };
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (Number(process.versions.node.split('.')[0]) < 24) fail('node-24-required', 400);
    const options = argumentsFrom(process.argv.slice(2));
    if (options.stop) {
      await fs.lstat(options.directory); await privateRoot(options.directory);
      const metadata = await metadataFromRoot(options.directory);
      const reply = await rpc(metadata, { operation: 'shutdown' }, 12000);
      const until = Date.now() + 3000;
      for (;;) {
        try { const current = await metadataFromRoot(options.directory); if (current.instance !== metadata.instance) fail('supervisor-owner-changed', 503); }
        catch (err) { if (err.code === 'ENOENT') break; throw err; }
        if (Date.now() >= until) fail('shutdown-needs-intervention', 503);
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      process.stdout.write(JSON.stringify(reply) + '\n');
    } else {
      const front = await serve(options.directory, await configFromFile(options.config));
      process.stdout.write(JSON.stringify({ url: front.url, connectionFile: front.connectionFile }) + '\n');
      let stopping = false;
      const close = () => { if (stopping) return; stopping = true; front.close().then(() => { process.exitCode = 0; }).catch(() => { process.exitCode = 1; }); };
      process.on('SIGTERM', close); process.on('SIGINT', close);
    }
  } catch (error) {
    process.stderr.write(JSON.stringify({ error: error instanceof Fault ? error.code : 'startup-unavailable' }) + '\n'); process.exitCode = 1;
  }
}
