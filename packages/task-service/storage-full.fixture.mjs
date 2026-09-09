// Test-only page limit on the original CLI's SQLite connection. No synthetic
// SQLite error, replacement transaction, OS ENOSPC or disk exhaustion.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {until} from './backup-restore.fixture.mjs';

let configuration;
if (process.env.MARSHAL_SQLITE_FULL_FIXTURE === '1') {
  const root = process.argv[process.argv.indexOf('--root') + 1];
  assert.ok(path.isAbsolute(root)); assert.equal(fs.realpathSync(root), root);
  const database = path.join(root, 'store', 'authority.sqlite');
  const marker = path.join(path.dirname(root), 'arm-sqlite-full');
  const record = value => fs.writeSync(3, JSON.stringify(value) + '\n');
  const exec = DatabaseSync.prototype.exec, prepare = DatabaseSync.prototype.prepare;
  let armed = true, errors = 0;
  const observe = (source, error) => {
    if (++errors > 32) return;
    try {
      record({type: 'sqlite-error', source, code: error.code ?? null,
        errcode: Number.isSafeInteger(error.errcode) ? error.errcode : null,
        errstr: error.errstr === 'database or disk is full' ? error.errstr : null});
    } catch {} // Missing diagnostics fail the test, never replace the native error.
  };
  DatabaseSync.prototype.exec = function(sql) {
    if (armed && sql === 'BEGIN IMMEDIATE' && fs.existsSync(marker)) {
      const target = prepare.call(this, 'PRAGMA database_list').all().find(row => row.name === 'main');
      assert.equal(target.file, database, 'limit only the exact disposable authority database');
      const markerStat = fs.lstatSync(marker);
      assert.ok(markerStat.isFile()); assert.equal(markerStat.uid, process.getuid());
      assert.equal(markerStat.nlink, 1); assert.equal(markerStat.mode & 0o777, 0o600);
      const pages = Number(prepare.call(this, 'PRAGMA page_count').get().page_count);
      const previous = Number(prepare.call(this, 'PRAGMA max_page_count').get().max_page_count);
      assert.ok(pages > 0 && pages < 16384 && previous > pages);
      exec.call(this, 'PRAGMA max_page_count=' + pages);
      const actual = Number(prepare.call(this, 'PRAGMA max_page_count').get().max_page_count);
      assert.equal(actual, pages); armed = false;
      record({type: 'page-limit', pages, previous, actual});
    }
    try {return exec.call(this, sql);} catch (error) {observe('exec', error); throw error;}
  };
  const probe = new DatabaseSync(':memory:');
  const prototype = Object.getPrototypeOf(probe.prepare('SELECT 1')); probe.close();
  for (const method of ['run', 'get', 'all']) {
    const original = prototype[method];
    prototype[method] = function(...args) {
      try {return original.apply(this, args);} catch (error) {observe(method, error); throw error;}
    };
  }
  configuration = (await import('./custody-recovery.fixture.mjs')).default;
}
export default configuration;

export async function launchFull(f, root) {
  f.offline(root);
  const child = spawn(process.execPath, [fileURLToPath(new URL('./main.mjs', import.meta.url)),
    '--root', root, '--mode', 'open', '--config', fileURLToPath(import.meta.url)], {cwd: f.parent,
    env: {MARSHAL_CUSTODY_FIXTURE: '1', MARSHAL_CUSTODY_SCENARIO: 'authors', MARSHAL_SQLITE_FULL_FIXTURE: '1'},
    stdio: ['ignore', 'pipe', 'pipe', 'pipe']});
  let stdout = '', stderr = '', raw = '', closed = false, overflow = false, token;
  const done = new Promise(resolve => {
    child.once('error', () => {closed = true; resolve({code: null, signal: 'spawn-error'});});
    child.once('close', (code, signal) => {closed = true; resolve({code, signal});});
  });
  const take = (stream, bytes) => {
    if (stream === 'stdout') stdout += bytes; else if (stream === 'stderr') stderr += bytes; else raw += bytes;
    if (Buffer.byteLength(stdout + stderr + raw) > 65536) {overflow = true; child.kill('SIGKILL');}
  };
  child.stdout.on('data', bytes => take('stdout', bytes)); child.stderr.on('data', bytes => take('stderr', bytes));
  child.stdio[3].on('data', bytes => take('raw', bytes));
  const rows = text => text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);
  const service = {root, child, done, get closed() {return closed;}, observations: () => rows(raw),
    checkOutput() {assert.equal(overflow, false); if (token) assert.equal((stdout + stderr + raw).includes(token), false);},
    arm() {fs.writeFileSync(path.join(f.parent, 'arm-sqlite-full'), '', {flag: 'wx', mode: 0o600});},
    async stop() {
      if (!closed) child.kill('SIGTERM');
      const watchdog = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 10000);
      try {
        await until(() => closed); const result = await done; service.checkOutput();
        assert.equal(result.signal, null); assert.ok([0, 1].includes(result.code));
        assert.equal(rows(stdout).at(-1)?.state, 'closed');
        return {...result, shutdown: rows(stdout).at(-1)};
      } finally {clearTimeout(watchdog);}
    }};
  f.services.push(service);
  await until(() => {assert.equal(closed, false, 'page-limit CLI failed before arming'); return rows(stdout).length > 0;});
  const ready = rows(stdout)[0]; assert.equal(ready.profile, 'node-task-service/v1');
  assert.ok(ready.connectionFile.startsWith(path.join(root, 'connections') + path.sep));
  const connection = JSON.parse(fs.readFileSync(ready.connectionFile)); token = connection.token;
  service.client = new TaskClient({baseURL: connection.url, token, timeoutMs: 3000});
  return service;
}
