import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn, spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {withoutSQLiteRuntimeNotices} from '../task-store/runtime-notices.fixture.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {parseLaunchArguments, prepareLaunch} from './launch.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./service.fixture.mjs', import.meta.url));
const base = ['--config', config];
const errorCode = code => error => error.code === code;
async function until(read, timeoutMs = 15000) {
  const end = Date.now() + timeoutMs;
  for (;;) {const value = await read(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded launch observation deadline'); await pause(10);}
}
function fixture(t) {
  const directory = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-launch-'))), home = path.join(directory, 'home');
  fs.mkdirSync(home, {mode: 0o700});
  const active = [], f = {directory, home, root: path.join(home, '.marshal-node/task-service'), active, complete: false};
  f.run = args => spawnSync(process.execPath, [cli, ...args], {cwd: directory, env: {HOME: home}, timeout: 20000, maxBuffer: 16384, encoding: 'utf8'});
  f.launch = async args => {
    const child = spawn(process.execPath, [cli, ...args], {cwd: directory, env: {HOME: home}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => {exited = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('close', (code, signal) => {exited = true; resolve({code, signal});});
    });
    child.stdout.on('data', value => {stdout += value; if (stdout.length > 16384) child.kill('SIGKILL');});
    child.stderr.on('data', value => {stderr += value; if (stderr.length > 16384) child.kill('SIGKILL');});
    const service = {child, get exited() {return exited;}, get stdout() {return stdout;}, get stderr() {return stderr;}, async stop() {
      if (!exited) child.kill('SIGTERM');
      const timer = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 5000);
      try {await until(() => exited, 10000); return await done;} finally {clearTimeout(timer);}
    }, checkOutput() {if (token) assert.equal((stdout + stderr).includes(token), false);}};
    active.push(service);
    await until(() => {assert.equal(exited, false, 'CLI failed before ready: ' + stderr); return stdout.includes('\n');});
    const first = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(first.connectionFile));
    token = connection.token; service.token = token; service.connectionFile = first.connectionFile;
    service.client = new TaskClient({baseURL: connection.url, token});
    assert.deepEqual(Object.keys(first).sort(), ['address', 'connectionFile', 'profile']);
    assert.equal((await service.client.request('ready.get')).ready, true); return service;
  };
  t.after(async () => {
    for (const service of active) {await service.stop(); service.checkOutput();}
    if (f.complete) fs.rmSync(directory, {recursive: true, force: true}); else t.diagnostic('Preserved launch evidence: ' + directory);
  });
  return f;
}
function rejectCLI(f, args) {
  const result = f.run(args); assert.equal(result.error, undefined); assert.equal(result.status, 1);
  assert.equal(result.stdout, ''); assert.equal(withoutSQLiteRuntimeNotices(result.stderr), '{"code":"service_start_unavailable"}\n'); return result;
}
// Own temporary SQLite only, after every original service process has exited.
function durable(f, root = f.root) {
  assert.ok(f.active.every(service => service.exited));
  const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally {db.close();}
}
test('closed launch parsing keeps explicit legacy options, adds mutually exclusive data-dir and safe default', () => {
  const home = '/private/owned/home';
  assert.deepEqual(parseLaunchArguments(base, {home}), {root: home + '/.marshal-node/task-service', homeAnchor: home, mode: 'auto', config, port: 0});
  const old = parseLaunchArguments([...base, '--root', '/private/owned/data', '--mode', 'open', '--port', '1234']);
  assert.equal(old.root, '/private/owned/data'); assert.equal(old.mode, 'open'); assert.equal(old.homeAnchor, null);
  assert.deepEqual(parseLaunchArguments(['--help']), {help: true});
  for (const args of [[], [...base, '--root', '/one', '--data-dir', '/two'], [...base, '--mode', 'reset'], [...base, '--data-dir', '.'],
    [...base, '--data-dir', '/'], [...base, '--data-dir', '/private/../data'], [...base, '--data-dir', '/private/x\0y'],
    [...base, '--port', '65536'], [...base, '--port', '01'], [...base, '--config', config], [...base, '--init', 'true'],
    ['--config', 'relative.mjs'], [...base, '--mode']]) assert.throws(() => parseLaunchArguments(args, {home}));
});
test('default root: real first CLI, owned private data, original input/Task receipts, SIGTERM and second CLI exact cold facts', {timeout: 30000}, async t => {
  const f = fixture(t); fs.chmodSync(f.home, 0o755); // HOME is an owned non-writable-by-others anchor, not our private Store.
  const first = await f.launch(base);
  assert.equal(fs.statSync(f.home).mode & 0o777, 0o755); // Never chmod existing HOME.
  for (const name of [path.dirname(f.root), f.root]) assert.equal(fs.statSync(name).mode & 0o7777, 0o700);
  assert.equal(fs.statSync(first.connectionFile).mode & 0o7777, 0o600);
  const content = Buffer.from('original input retained through normal CLI restart');
  const inputRequest = {idempotencyKey: 'input-original', body: {name: 'input.txt', mediaType: 'text/plain', contentBase64: content.toString('base64')}};
  const input = await first.client.request('input.create', inputRequest);
  const taskRequest = {idempotencyKey: 'task-original', body: {intent: '明确无模型启动夹具：准备回调拒绝，不启动 Provider', context: {inputRefs: [input.id]}}};
  const receipt = await first.client.request('task.create', taskRequest);
  // The checked-in test config rejects prepare before any process start. This
  // is a real failed Task/receipt, not a forged cleanup or successful business.
  const failed = await until(async () => {const task = await first.client.getTask(receipt.id); return task.status === 'failed' && task;});
  const audit = await first.client.request('task.audit', {path: {taskId: receipt.id}}); assert.equal(audit.attempts, 1);
  assert.deepEqual(await first.stop(), {code: 0, signal: null}); first.checkOutput();
  const before = durable(f), second = await f.launch(base);
  assert.notEqual(second.child.pid, first.child.pid); assert.notEqual(second.token, first.token); assert.notEqual(second.connectionFile, first.connectionFile);
  assert.deepEqual(await second.client.request('input.create', inputRequest), input);
  assert.deepEqual(await second.client.request('task.create', taskRequest), receipt);
  assert.deepEqual(await second.client.getTask(receipt.id), failed);
  assert.deepEqual(await second.client.request('task.audit', {path: {taskId: receipt.id}}), audit);
  assert.deepEqual((await second.client.downloadArtifact(input.id)).content, content);
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
  assert.deepEqual(await second.stop(), {code: 0, signal: null}); second.checkOutput(); assert.deepEqual(durable(f), before);
  assert.equal(fs.readdirSync(path.join(f.root, 'connections')).length, 2); f.complete = true;
});
test('explicit data-dir creates only missing private parents and old root/create/open CLI calls remain usable', {timeout: 30000}, async t => {
  const f = fixture(t), root = path.join(f.directory, 'private/child/data');
  const first = await f.launch([...base, '--data-dir', root]);
  for (const name of ['private', 'private/child']) assert.equal(fs.statSync(path.join(f.directory, name)).mode & 0o7777, 0o700);
  assert.deepEqual(await first.stop(), {code: 0, signal: null});
  const second = await f.launch([...base, '--root', root, '--mode', 'open']); assert.deepEqual(await second.stop(), {code: 0, signal: null});
  const before = durable(f, root); rejectCLI(f, [...base, '--root', root, '--mode', 'create']); assert.deepEqual(durable(f, root), before);
  const legacy = path.join(f.directory, 'legacy');
  const third = await f.launch([...base, '--root', legacy, '--mode', 'create']); assert.deepEqual(await third.stop(), {code: 0, signal: null}); f.complete = true;
});
test('a second live owner is rejected without replacing connection/token or interrupting the original service', {timeout: 25000}, async t => {
  const f = fixture(t), first = await f.launch(base), before = fs.readdirSync(path.join(f.root, 'connections'));
  rejectCLI(f, base); assert.equal((await first.client.request('health.get')).status, 'ok'); assert.equal((await first.client.request('ready.get')).ready, true);
  assert.deepEqual(fs.readdirSync(path.join(f.root, 'connections')), before); assert.equal(JSON.parse(fs.readFileSync(first.connectionFile)).token, first.token);
  assert.deepEqual(await first.stop(), {code: 0, signal: null}); f.complete = true;
});
test('empty, partial, unknown and corrupted existing roots never fall back to creation or replace original bytes', {timeout: 30000}, async t => {
  const f = fixture(t);
  for (const mode of ['empty', 'partial', 'unknown']) {
    const root = path.join(f.directory, mode); fs.mkdirSync(root, {mode: 0o700});
    if (mode !== 'empty') fs.writeFileSync(path.join(root, 'profile.json'), JSON.stringify({profile: 'node-task-service/v1', layout: mode === 'partial' ? 1 : 99}) + '\n', {mode: 0o600});
    const before = fs.readdirSync(root).map(name => [name, fs.readFileSync(path.join(root, name)).toString('hex')]);
    rejectCLI(f, [...base, '--data-dir', root]); assert.deepEqual(fs.readdirSync(root).map(name => [name, fs.readFileSync(path.join(root, name)).toString('hex')]), before);
  }
  const first = await f.launch(base); assert.deepEqual(await first.stop(), {code: 0, signal: null});
  const database = path.join(f.root, 'store/authority.sqlite'); fs.writeFileSync(database, 'deliberately corrupt only this closed fixture database');
  const bytes = fs.readFileSync(database), connections = fs.readdirSync(path.join(f.root, 'connections'));
  rejectCLI(f, base); assert.deepEqual(fs.readFileSync(database), bytes); assert.deepEqual(fs.readdirSync(path.join(f.root, 'connections')), connections); f.complete = true;
});
test('symlink, wide private directory, writable HOME and missing open reject without chmod or new state', {timeout: 30000}, async t => {
  const f = fixture(t), wide = path.join(f.directory, 'wide'); fs.mkdirSync(wide, {mode: 0o755});
  rejectCLI(f, [...base, '--data-dir', path.join(wide, 'data')]); assert.deepEqual(fs.readdirSync(wide), []); assert.equal(fs.statSync(wide).mode & 0o777, 0o755);
  rejectCLI(f, [...base, '--data-dir', wide]); assert.equal(fs.statSync(wide).mode & 0o777, 0o755);
  const link = path.join(f.directory, 'link'); fs.symlinkSync(f.home, link);
  rejectCLI(f, [...base, '--data-dir', link]); rejectCLI(f, [...base, '--data-dir', path.join(link, 'data')]); assert.deepEqual(fs.readdirSync(f.home), []);
  fs.chmodSync(f.home, 0o770); rejectCLI(f, base); assert.deepEqual(fs.readdirSync(f.home), []); assert.equal(fs.statSync(f.home).mode & 0o777, 0o770);
  fs.chmodSync(f.home, 0o700); rejectCLI(f, [...base, '--mode', 'open']); assert.deepEqual(fs.readdirSync(f.home), []);
  const absent = path.join(f.directory, 'absent/deep/data'); rejectCLI(f, [...base, '--data-dir', absent, '--mode', 'open']); assert.equal(fs.existsSync(path.join(f.directory, 'absent')), false);
  const systemTarget = path.join(path.parse(f.directory).root, 'marshal-launch-never-create');
  assert.throws(() => prepareLaunch({root: systemTarget, mode: 'auto', homeAnchor: null}), errorCode('unsafe_data_parent'));
  f.complete = true;
});
test('parent fsync failure retains new private directory, and a later attempt must repeat child-parent durability before returning', t => {
  const f = fixture(t), options = parseLaunchArguments(base, {home: f.home}), original = fs.fsyncSync;
  const calls = []; fs.fsyncSync = fd => {calls.push(fs.fstatSync(fd).ino); throw new Error('fixture_sync_failure');};
  try {assert.throws(() => prepareLaunch(options), /fixture_sync_failure/);} finally {fs.fsyncSync = original;}
  const parent = path.dirname(f.root), inode = fs.statSync(parent).ino;
  assert.equal(fs.existsSync(f.root), false); assert.equal(fs.statSync(parent).mode & 0o7777, 0o700); assert.deepEqual(calls, [inode]);
  fs.fsyncSync = fd => {calls.push(fs.fstatSync(fd).ino); throw new Error('second_fixture_sync_failure');};
  try {assert.throws(() => prepareLaunch(options), /second_fixture_sync_failure/);} finally {fs.fsyncSync = original;}
  assert.equal(fs.statSync(parent).ino, inode); assert.equal(fs.existsSync(f.root), false);
  const parentFailure = []; fs.fsyncSync = fd => {
    parentFailure.push(fs.fstatSync(fd).ino);
    if (parentFailure.length === 2) throw new Error('fixture_home_sync_failure');
    original(fd);
  };
  try {assert.throws(() => prepareLaunch(options), /fixture_home_sync_failure/);} finally {fs.fsyncSync = original;}
  assert.deepEqual(parentFailure, [inode, fs.statSync(f.home).ino]);
  assert.equal(fs.statSync(parent).ino, inode); assert.equal(fs.existsSync(f.root), false);
  const successful = []; fs.fsyncSync = fd => {successful.push(fs.fstatSync(fd).ino); original(fd);};
  let target;
  try {target = prepareLaunch(options); assert.equal(target.mode, 'create'); target.check();} finally {fs.fsyncSync = original; target?.close();}
  assert.deepEqual(successful, [inode, fs.statSync(f.home).ino]); f.complete = true;
});
test('explicit multi-parent bootstrap repeats every failed ancestor barrier even after all child directories exist', t => {
  const f = fixture(t), original = fs.fsyncSync;
  for (const failureAt of [0, 1, 2]) {
    const anchor = path.join(f.directory, `anchor-${failureAt}`); fs.mkdirSync(anchor, {mode: 0o700});
    const first = path.join(anchor, 'first'), second = path.join(first, 'second'), root = path.join(second, 'data');
    const options = {root, mode: 'auto', homeAnchor: null}, required = [second, first, anchor];
    let inodes;
    for (const attempt of ['initial', 'retry']) {
      const called = []; fs.fsyncSync = fd => {
        const observed = required.map(name => fs.statSync(name).ino);
        inodes ??= observed; assert.deepEqual(observed, inodes);
        const inode = fs.fstatSync(fd).ino; called.push(inode);
        if (inode === inodes[failureAt]) throw new Error(`fixture_${attempt}_ancestor_sync_failure`);
        original(fd);
      };
      try {assert.throws(() => prepareLaunch(options), new RegExp(`fixture_${attempt}_ancestor_sync_failure`));}
      finally {fs.fsyncSync = original;}
      assert.deepEqual(called, inodes.slice(0, failureAt + 1));
      assert.equal(fs.existsSync(root), false);
      for (let at = 0; at < required.length; at++) {
        const stat = fs.statSync(required[at]); assert.equal(stat.ino, inodes[at]); assert.equal(stat.mode & 0o7777, 0o700);
      }
    }
    const called = []; fs.fsyncSync = fd => {called.push(fs.fstatSync(fd).ino); original(fd);};
    let target;
    try {target = prepareLaunch(options); assert.equal(target.mode, 'create'); target.check();}
    finally {fs.fsyncSync = original; target?.close();}
    assert.deepEqual(called.slice(0, 3), inodes); // second -> first -> original private anchor, never only second.
    assert.equal(called[3], fs.statSync(f.directory).ino); // Continue the original private chain, not just the former anchor.
    assert.equal(fs.existsSync(root), false); // Only original composition may create the service root.
  }
  f.complete = true;
});
test('private chain limit counts new and existing parents identically before mkdir and on retry', t => {
  const f = fixture(t); let count = 0, current = f.directory;
  for (;;) {
    const stat = fs.lstatSync(current);
    if (!stat.isDirectory() || stat.uid !== process.getuid() || (stat.mode & 0o7777) !== 0o700) break;
    count++; const parent = path.dirname(current); if (parent === current) break; current = parent;
  }
  let anchor = f.directory;
  while (count < 33) {anchor = path.join(anchor, 'existing'); fs.mkdirSync(anchor, {mode: 0o700}); count++;}
  const missing = Array.from({length: 31}, (_, at) => `new-${at}`), root = path.join(anchor, ...missing, 'data');
  assert.throws(() => prepareLaunch({root: path.join(anchor, ...missing, 'one-too-many', 'data'), mode: 'auto'}), errorCode('data_parent_limit'));
  assert.equal(fs.existsSync(path.join(anchor, missing[0])), false);
  for (const attempt of ['initial', 'retry']) {
    const target = prepareLaunch({root, mode: 'auto'});
    try {assert.equal(target.mode, 'create', attempt); target.check();} finally {target.close();}
    assert.equal(fs.existsSync(root), false);
  }
  f.complete = true;
});
test('held parent replacement is rejected before handoff and closed targets cannot be reused', t => {
  const f = fixture(t), target = prepareLaunch(parseLaunchArguments(base, {home: f.home})), parent = path.dirname(f.root);
  fs.renameSync(parent, path.join(f.home, 'preserved-old-parent')); fs.mkdirSync(parent, {mode: 0o700});
  assert.throws(target.check, errorCode('unsafe_data_parent')); target.close(); target.close(); assert.throws(target.check, errorCode('launch_target_closed'));
  assert.equal(fs.existsSync(f.root), false); f.complete = true;
});
test('CLI errors keep the old safe envelope and do not create a default root without explicit trusted config', t => {
  const f = fixture(t);
  for (const args of [[], ['--root', '/secret-do-not-echo'], [...base, '--data-dir', '/secret-do-not-echo', '--root', '/other'],
    ['--config', '/secret-do-not-echo/missing.mjs'], [...base, '--port', '65536']]) rejectCLI(f, args);
  assert.deepEqual(fs.readdirSync(f.home), []);
  const help = f.run(['--help']); assert.equal(help.status, 0); assert.match(help.stdout, /HOME\/\.marshal-node\/task-service/); assert.equal(withoutSQLiteRuntimeNotices(help.stderr), ''); f.complete = true;
});
