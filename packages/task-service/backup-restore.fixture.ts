// Test-only offline copying, not a product backup API or a cloned-root fence.
// Only disposable roots owned by this fixture are opened, copied or damaged.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {validate} from '../task-api/contract.mjs';
import {encode} from '../task-store/store.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
// The existing versioned fixture preserves the real CLI, custody, ACP guard,
// FileBusiness, SQLite and independent command verifier. Healthy intents do
// not enable any of its crash/hold branches; no model process is involved.
const config = fileURLToPath(new URL('./custody-recovery.fixture.mjs', import.meta.url));
export const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
export const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
export async function until(observe, ms = 15000) {
  const deadline = Date.now() + ms;
  for (;;) {
    const value = await observe(); if (value) return value;
    assert.ok(Date.now() < deadline, 'bounded backup/restore observation timed out'); await pause(10);
  }
}
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const sameInode = (before, after) => before.dev === after.dev && before.ino === after.ino;
const directory = file => fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);

// Whole-tree manifest remains in test memory, never inside a product root.
// Connection tokens are included only by hash; their bodies are never logged.
export function manifest(root) {
  assert.equal(fs.realpathSync(root), root); assert.equal(fs.statSync(root).mode & 0o777, 0o700);
  const entries = []; let total = 0;
  function visit(relative) {
    assert.ok(entries.length < 4096, 'bounded fixture tree');
    const file = path.join(root, relative), stat = fs.lstatSync(file);
    assert.equal(stat.uid, process.getuid()); assert.equal(stat.isSymbolicLink(), false);
    if (stat.isDirectory()) {
      entries.push({path: relative, kind: 'directory', mode: stat.mode & 0o777});
      for (const name of fs.readdirSync(file).sort()) visit(path.join(relative, name));
      return;
    }
    assert.ok(stat.isFile()); assert.equal(stat.nlink, 1);
    total += stat.size; assert.ok(total <= 64 * 1024 * 1024, 'bounded snapshot bytes');
    const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
    try {
      assert.ok(sameInode(stat, fs.fstatSync(fd)));
      const bytes = fs.readFileSync(fd); assert.equal(bytes.length, stat.size);
      entries.push({path: relative, kind: 'file', mode: stat.mode & 0o777, bytes: bytes.length, sha256: hash(bytes)});
    } finally {fs.closeSync(fd);}
  }
  visit(''); return entries;
}

export function copyOffline(source, destination, assertOffline) {
  assertOffline(); assert.equal(fs.existsSync(destination), false);
  assert.equal(fs.realpathSync(path.dirname(destination)), path.dirname(destination));
  assert.equal(fs.statSync(path.dirname(destination)).mode & 0o777, 0o700);
  const original = manifest(source);
  // No hardlinks, no reflink requirement, no symlink following, no omissions
  // for WAL/SHM/journal or custody observations, and no overwrite of a root.
  for (const entry of original) {
    const target = path.join(destination, entry.path);
    if (entry.kind === 'directory') {fs.mkdirSync(target, {mode: 0o700}); continue;}
    const bytes = fs.readFileSync(path.join(source, entry.path)); assert.equal(hash(bytes), entry.sha256);
    const fd = fs.openSync(target, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
    try {fs.writeFileSync(fd, bytes); fs.fchmodSync(fd, entry.mode); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  }
  // Persist bytes + permissions, then directory entries bottom-up and parent.
  // This demonstrates an offline POSIX copy recipe, not a power-cut proof.
  for (const entry of original.filter(value => value.kind === 'directory').reverse()) {
    const fd = directory(path.join(destination, entry.path));
    try {fs.fchmodSync(fd, entry.mode); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  }
  const parent = directory(path.dirname(destination)); try {fs.fsyncSync(parent);} finally {fs.closeSync(parent);}
  assertOffline(); assert.deepEqual(manifest(source), original); assert.deepEqual(manifest(destination), original);
  return original;
}

// Called only after the corresponding original CLI handles have exited.
// Read-only checks do not claim an owner or alter any lifecycle rows.
export function durable(root, assertOffline) {
  assertOffline();
  const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {metadata: {...db.prepare('SELECT format,version,store_id,generation FROM metadata').get()},
      events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally {db.close();}
}

export async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-backup-restore-')));
  const f = {parent, services: [], completed: false};
  f.offline = root => assert.ok(f.services.filter(service => service.root === root).every(service => service.closed), 'original service must be closed');
  f.observations = () => {
    const file = path.join(parent, 'custody-observations.jsonl'); if (!fs.existsSync(file)) return [];
    const bytes = fs.readFileSync(file); assert.ok(bytes.length <= 262144);
    const text = bytes.toString('utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);
  };
  f.launch = async (root, mode, available = true) => {
    const child = spawn(process.execPath, [cli, '--root', root, '--mode', mode, '--config', config], {cwd: parent,
      env: {MARSHAL_CUSTODY_FIXTURE: '1', MARSHAL_CUSTODY_SCENARIO: 'authors'}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', closed = false, overflow = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => {closed = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('close', (code, signal) => {closed = true; resolve({code, signal});});
    });
    const receive = (name, bytes) => {
      if (name === 'stdout') stdout += bytes; else stderr += bytes;
      if (stdout.length + stderr.length > 16384) {overflow = true; child.kill('SIGKILL');}
    };
    child.stdout.on('data', bytes => receive('stdout', bytes)); child.stderr.on('data', bytes => receive('stderr', bytes));
    const output = () => stdout.split('\n').filter(Boolean).map(JSON.parse);
    const service = {root, child, done, get closed() {return closed;}, checkOutput() {
      assert.equal(overflow, false); if (token) assert.equal((stdout + stderr).includes(token), false);
    }, async stop(clean = true) {
      if (!closed) child.kill('SIGTERM');
      const watchdog = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 10000);
      try {
        await until(() => closed); const result = await done; service.checkOutput();
        if (clean) {assert.deepEqual(result, {code: 0, signal: null}); assert.deepEqual(output().at(-1), {state: 'closed', clean: true, code: null});}
        return result;
      } finally {clearTimeout(watchdog);}
    }};
    f.services.push(service);
    if (!available) {
      await until(() => closed); assert.deepEqual(await done, {code: 1, signal: null});
      assert.ok(stderr.includes('"code":"service_start_unavailable"')); assert.ok(output().every(row => !row.address));
      service.checkOutput(); return service;
    }
    await until(() => {assert.equal(closed, false, 'original CLI startup failed'); return stdout.includes('\n');}, 20000);
    const ready = output()[0], connection = JSON.parse(fs.readFileSync(ready.connectionFile));
    assert.equal(ready.profile, 'node-task-service/v1'); assert.ok(ready.connectionFile.startsWith(path.join(root, 'connections') + path.sep));
    token = connection.token; service.connectionFile = ready.connectionFile;
    service.client = new TaskClient({baseURL: connection.url, token});
    // TaskClient intentionally checks metadata before download. This single
    // raw HTTP probe also exercises the content route when metadata is broken.
    service.contentFailure = async artifactId => {
      assert.equal(validate(artifactId, 'Id'), true);
      const response = await fetch(connection.url + '/v1/artifacts/' + artifactId + '/content', {
        headers: {Host: new URL(connection.url).host, Authorization: 'Bearer ' + token}, signal: AbortSignal.timeout(3000), redirect: 'error'});
      const bytes = Buffer.from(await response.arrayBuffer()); assert.ok(bytes.length <= 4096); assert.equal(bytes.includes(Buffer.from(token)), false);
      return {status: response.status, code: JSON.parse(bytes).code};
    };
    return service;
  };
  t.after(async () => {
    if (!f.completed) t.diagnostic('Preserved failed private backup fixture: ' + parent);
    for (const service of f.services) {if (!service.closed) await service.stop(false); service.checkOutput();}
    // No persisted PID is signalled. Successful cleanup must come from each
    // original runtime completion and the original service shutdown receipt.
    const completions = f.observations().filter(value => value.type === 'completion');
    if (f.completed) {assert.ok(completions.every(value => value.cleanup?.cleaned === true)); fs.rmSync(parent, {recursive: true, force: true});}
  });
  return f;
}

export async function completeTeam(client, inputId, key) {
  const body = {intent: 'healthy offline backup team ' + key, context: {inputRefs: [inputId]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, key + '-create');
  const preview = await until(async () => {const task = await client.getTask(created.id); return task.status === 'awaiting-approval' && task;});
  const plan = await client.request('task.plan', {path: {taskId: created.id}});
  assert.equal(plan.nodes.filter(node => node.role === 'author').length, 2); assert.equal(plan.nodes.filter(node => node.role === 'verifier').length, 1);
  const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
  const receipt = await client.approveTask(created.id, approval, key + '-approve');
  const completed = await until(async () => {const task = await client.getTask(created.id); return task.status === 'completed' && task;});
  await until(async () => (await client.request('operation.get', {path: {operationId: receipt.id}})).status === 'succeeded');
  const downloads = await Promise.all(completed.artifactIds.map(id => client.downloadArtifact(id)));
  assert.equal(downloads.length, 2); const delivery = downloads.find(value => value.artifact.kind === 'delivery'); assert.ok(delivery);
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  const audit = await client.getAudit(created.id); assert.equal(audit.attempts, 4); assert.equal(audit.acceptance.status, 'passed');
  assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
  return {key, body, created, plan, approval, receipt, completed, downloads, delivery, audit};
}
export async function events(client, taskId, cursor = '') {
  const items = []; let pages = 0;
  for (;;) {
    assert.ok(pages++ < 100); const page = await client.request('task.events', {path: {taskId}, query: {limit: 3, ...(cursor ? {cursor} : {})}});
    items.push(...page.items); if (page.nextCursor === null) return items;
    assert.notEqual(page.nextCursor, cursor); cursor = page.nextCursor;
  }
}
export async function upload(client) {
  return client.request('input.create', {idempotencyKey: 'original-sales-input', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
}
