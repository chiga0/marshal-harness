// Test-only observer and child launcher. No synthetic I/O error, transaction
// outcome, cleanup receipt or production recovery callback is substituted.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {until} from './backup-restore.fixture.mjs';

export const fileLimit = 65536;
const python = '/usr/bin/python3'; // Installed system interpreter, no shell/PATH lookup.
const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const configurationPath = fileURLToPath(import.meta.url);
// Only the new child (then exec'd into the original CLI) and its descendants
// inherit this limit. Parent, host disks, existing processes and user data do not.
const limitAndExec = `import os,resource,signal,sys,json
limit=int(sys.argv[1])
assert limit == 65536 and os.path.isabs(sys.argv[2])
signal.signal(signal.SIGXFSZ,signal.SIG_IGN)
resource.setrlimit(resource.RLIMIT_FSIZE,(limit,limit))
assert resource.getrlimit(resource.RLIMIT_FSIZE)==(limit,limit)
os.write(3,(json.dumps({"type":"limit","bytes":limit,"signalIgnored":True})+"\\n").encode())
os.execve(sys.argv[2],sys.argv[2:],os.environ)
`;

let configuration;
if (process.env.MARSHAL_STORAGE_LIMIT_OBSERVE === '1') {
  const originalWrite = fs.writeSync, record = value => originalWrite(3, JSON.stringify(value) + '\n');
  let count = 0;
  const observe = (source, error, extra = {}) => {
    if (count++ >= 32) return;
    try {record({type: 'io-error', source,
      code: typeof error.code === 'string' && /^[A-Z0-9_]{1,64}$/.test(error.code) ? error.code : null,
      errcode: Number.isSafeInteger(error.errcode) ? error.errcode : null,
      errstr: ['disk I/O error', 'database or disk is full'].includes(error.errstr) ? error.errstr : null, ...extra});}
    catch {} // Missing diagnostic makes the test fail, never replaces native error.
  };
  const writeFile = fs.writeFileSync;
  fs.writeFileSync = function(file, bytes, ...args) {
    try {return writeFile.call(this, file, bytes, ...args);}
    catch (error) {
      let stat; try {if (typeof file === 'number') stat = fs.fstatSync(file);} catch {}
      observe('fs.writeFileSync', error, {requestedBytes: typeof bytes === 'string' ? Buffer.byteLength(bytes) : bytes?.byteLength ?? null,
        regularFile: stat?.isFile() === true, resultingBytes: stat?.size ?? null});
      throw error; // Same native error object; never return success/fake EFBIG.
    }
  };
  // Record the ORIGINAL SQLite error before Store normalizes it to unavailable.
  // Original callbacks, arguments, COMMIT and SQLite's own rollback all run.
  const probe = new DatabaseSync(':memory:');
  const statementPrototype = Object.getPrototypeOf(probe.prepare('SELECT 1')); probe.close();
  for (const [prototype, methods] of [[DatabaseSync.prototype, ['exec', 'prepare', 'close']],
    [statementPrototype, ['run', 'get', 'all']]]) {
    for (const method of methods) {
      const original = prototype[method];
      prototype[method] = function(...args) {
        try {return original.apply(this, args);}
        catch (error) {
          const action = method === 'exec' && typeof args[0] === 'string' && ['BEGIN', 'COMMIT', 'ROLLBACK'].includes(args[0].split(' ')[0]) ? args[0].split(' ')[0] : null;
          observe('sqlite.' + method, error, {action}); throw error;
        }
      };
    }
  }
  configuration = (await import('./custody-recovery.fixture.mjs')).default;
}
export default configuration;

export async function launchLimited(f, root) {
  f.offline(root); assert.ok(fs.statSync(python).isFile());
  const observationFile = path.join(f.parent, 'storage-limit-' + f.services.length + '.json');
  const child = spawn(python, ['-I', '-B', '-c', limitAndExec, String(fileLimit), process.execPath, cli,
    '--root', root, '--mode', 'open', '--config', configurationPath], {cwd: f.parent,
    env: {MARSHAL_CUSTODY_FIXTURE: '1', MARSHAL_CUSTODY_SCENARIO: 'authors', MARSHAL_STORAGE_LIMIT_OBSERVE: '1'},
    stdio: ['ignore', 'pipe', 'pipe', 'pipe']});
  let stdout = '', stderr = '', raw = '', closed = false, overflow = false, token, saved = false;
  const done = new Promise(resolve => {
    child.once('error', () => {closed = true; resolve({code: null, signal: 'spawn-error'});});
    child.once('close', (code, signal) => {closed = true; resolve({code, signal});});
  });
  const take = (stream, bytes) => {
    if (stream === 'stdout') stdout += bytes; else if (stream === 'stderr') stderr += bytes; else raw += bytes;
    if (Buffer.byteLength(stdout) + Buffer.byteLength(stderr) + Buffer.byteLength(raw) > 65536) {overflow = true; child.kill('SIGKILL');}
  };
  child.stdout.on('data', bytes => take('stdout', bytes)); child.stderr.on('data', bytes => take('stderr', bytes));
  child.stdio[3].on('data', bytes => take('raw', bytes));
  const rows = value => value.slice(0, value.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);
  const service = {root, child, done, get closed() {return closed;}, observations: () => rows(raw),
    checkOutput() {assert.equal(overflow, false); if (token) assert.equal((stdout + stderr + raw).includes(token), false);},
    async stop() {
      if (!closed) child.kill('SIGTERM'); // Only original ChildProcess, never stored PID.
      const watchdog = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 10000);
      try {
        await until(() => closed, 15000); const result = await done; service.checkOutput();
        assert.equal(result.signal, null, 'SIGXFSZ/SIGKILL is not a clean I/O failure return'); assert.ok([0, 1].includes(result.code));
        const last = rows(stdout).at(-1);
        if (!saved) {
          fs.writeFileSync(observationFile, JSON.stringify({errors: rows(raw), exit: result, shutdown: last ?? null}) + '\n', {flag: 'wx', mode: 0o600}); saved = true;
        }
        assert.equal(last?.state, 'closed');
        // An I/O error may be reported by shutdown. Do not rewrite it to clean.
        return {...result, shutdown: last};
      } finally {clearTimeout(watchdog);}
    }};
  f.services.push(service);
  await until(() => {assert.equal(closed, false, 'limited original CLI startup failed'); return rows(stdout).length > 0;}, 15000);
  assert.deepEqual(service.observations()[0], {type: 'limit', bytes: fileLimit, signalIgnored: true});
  const ready = rows(stdout)[0]; assert.equal(ready.profile, 'node-task-service/v1');
  assert.ok(ready.connectionFile.startsWith(path.join(root, 'connections') + path.sep));
  const connection = JSON.parse(fs.readFileSync(ready.connectionFile)); token = connection.token;
  service.client = new TaskClient({baseURL: connection.url, token, timeoutMs: 3000});
  return service;
}
