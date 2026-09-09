import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { performance } from 'node:perf_hooks';
import { DatabaseSync } from 'node:sqlite';

// Internal storage, not a Task reducer, execution supervisor, or public SQL API.
export const FORMAT = 'marshal-node-task-sqlite/v1';
export const CUSTODY_FORMAT = 'marshal-node-task-sqlite/v2-custody';
export const INTERACTION_FORMAT = 'marshal-node-task-sqlite/v3-interaction';
// A maximum page of observed commands needs three validated accesses per row.
// Leave room for its enclosing read/CAS while keeping aggregate work bounded.
export const LIMITS = Object.freeze({ recordBytes: 1 << 20, transactionBytes: 8 << 20, records: 512, page: 100, transactionMs: 5000 });
const DATABASE = 'authority.sqlite';
const APP_ID = 1297305934;
const MAX_INT = (1n << 63n) - 1n;
const KINDS = new Set(['task', 'node', 'attempt', 'budget', 'interaction', 'artifact', 'provider', 'operation']);
const COMMANDS = new Set(['start', 'stop', 'answer', 'verify']);
const opening = new Set();
const INTERNAL = Symbol('internal-store');

export class StoreError extends Error {
  constructor(code) { super(`task store: ${code}`); this.name = 'StoreError'; this.code = code; }
}
function fail(code) { throw new StoreError(code); }
function normalize(error) {
  if (error instanceof StoreError) return error;
  return new StoreError(error?.errcode === 5 || error?.errcode === 6 ? 'busy' : 'unavailable');
}
function check(condition, code = 'invalid') { if (!condition) fail(code); }
function id(value) { return typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,255}$/.test(value); }
function hash(value) { return typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value); }
function integer(value, zero = false) {
  if (typeof value === 'number') { check(Number.isSafeInteger(value)); value = BigInt(value); }
  check(typeof value === 'bigint' && value >= (zero ? 0n : 1n) && value <= MAX_INT);
  return value;
}
function timestamp(value) { check(Number.isSafeInteger(value) && value > 0); return value; }
function plain(value) { return value !== null && typeof value === 'object' && [null, Object.prototype].includes(Object.getPrototypeOf(value)); }
function closed(value, keys) { return plain(value) && Object.keys(value).every(key => keys.includes(key)); }
function validString(value) { return !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value); }

// This profile's canonical JSON: finite JSON values, UTF-16 sorted object keys,
// JSON.stringify number/string encoding, no holes, accessors, cycles or extras.
export function encode(value) {
  let nodes = 0;
  const seen = new Set();
  function visit(item, depth) {
    check(++nodes <= 100000 && depth <= 64, 'limit');
    if (item === null || typeof item === 'boolean') return JSON.stringify(item);
    if (typeof item === 'number') { check(Number.isFinite(item)); return JSON.stringify(item); }
    if (typeof item === 'string') { check(validString(item)); return JSON.stringify(item); }
    check(Array.isArray(item) || plain(item));
    check(!seen.has(item) && Object.getOwnPropertySymbols(item).length === 0);
    seen.add(item);
    let result;
    if (Array.isArray(item)) {
      check(Object.keys(item).length === item.length && item.length <= 100000, 'limit');
      const values = [];
      for (let n = 0; n < item.length; n++) {
        const descriptor = Object.getOwnPropertyDescriptor(item, String(n));
        check(descriptor && Object.hasOwn(descriptor, 'value'));
        values.push(visit(descriptor.value, depth + 1));
      }
      result = '[' + values.join(',') + ']';
    } else {
      const values = [];
      for (const key of Object.keys(item).sort()) {
        const descriptor = Object.getOwnPropertyDescriptor(item, key);
        check(validString(key) && Object.hasOwn(descriptor, 'value'));
        values.push(JSON.stringify(key) + ':' + visit(descriptor.value, depth + 1));
      }
      result = '{' + values.join(',') + '}';
    }
    seen.delete(item);
    check(Buffer.byteLength(result) <= LIMITS.recordBytes, 'limit');
    return result;
  }
  const bytes = Buffer.from(visit(value, 0));
  check(bytes.length > 0 && bytes.length <= LIMITS.recordBytes, 'limit');
  return bytes;
}
export function digest(bytes) {
  check(bytes instanceof Uint8Array);
  return 'sha256:' + crypto.createHash('sha256').update(bytes).digest('hex');
}
function canonical(bytes) {
  check(bytes instanceof Uint8Array && bytes.length > 0 && bytes.length <= LIMITS.recordBytes, 'limit');
  const copy = Buffer.from(bytes);
  let parsed;
  try { parsed = JSON.parse(copy.toString('utf8')); } catch { fail('invalid'); }
  check(encode(parsed).equals(copy));
  return copy;
}
export function makeEvent(stream, sequence, payload) {
  check(id(stream)); sequence = integer(sequence);
  const bytes = encode({ stream, sequence: sequence.toString(), payload });
  return { sequence, digest: digest(bytes), bytes };
}
function record(stream, value) {
  check(id(stream) && closed(value, ['sequence', 'digest', 'bytes']));
  const sequence = integer(value.sequence), bytes = canonical(value.bytes);
  const envelope = JSON.parse(bytes.toString('utf8'));
  check(closed(envelope, ['stream', 'sequence', 'payload']) && Object.hasOwn(envelope, 'payload') && envelope.stream === stream && envelope.sequence === sequence.toString());
  check(hash(value.digest) && digest(bytes) === value.digest);
  return { sequence, digest: value.digest, bytes };
}
function head(value) {
  check(closed(value, ['sequence', 'digest']));
  const sequence = integer(value.sequence, true);
  check(sequence === 0n ? value.digest === '' : hash(value.digest));
  return { sequence, digest: value.digest };
}
function source(value) {
  check(closed(value, ['stream', 'sequence', 'digest']) && id(value.stream));
  return { stream: value.stream, sequence: integer(value.sequence), digest: (check(hash(value.digest)), value.digest) };
}
function sameSource(a, b) { return a.stream === b.stream && a.sequence === b.sequence && a.digest === b.digest; }
function page(after, limit) { check(typeof after === 'string' && (after === '' || id(after)) && Number.isSafeInteger(limit) && limit > 0 && limit <= LIMITS.page); }

function sameFile(a, b) { return a.dev === b.dev && a.ino === b.ino && a.mode === b.mode && a.uid === b.uid && a.nlink === b.nlink; }
class PrivateFiles {
  constructor(root, create, format) {
    this.formatBytes = Buffer.from(format + '\n');
    this.root = root; this.fds = []; this.registered = false;
    try {
      check(typeof root === 'string' && path.isAbsolute(root) && path.normalize(root) === root && root !== path.parse(root).root && !root.split(path.sep).includes('.marshal'));
      this.parent = path.dirname(root);
      check(fs.realpathSync(this.parent) === this.parent, 'unavailable');
      this.parentFd = this.open(this.parent, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
      this.checkParent();
      if (create) {
        try { fs.mkdirSync(root, { mode: 0o700 }); } catch (error) { if (error.code !== 'EEXIST') throw error; }
      }
      check(fs.realpathSync(root) === root, 'unavailable');
      this.rootFd = this.open(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
      const stat = fs.fstatSync(this.rootFd);
      check(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o700, 'unavailable');
      this.key = `${stat.dev}:${stat.ino}`;
      check(!opening.has(this.key), 'busy'); opening.add(this.key); this.registered = true;
      if (create) check(fs.readdirSync(root).length === 0, 'unavailable');
      // Bootstrap format sentinel only, never a Task/owner authority. If the
      // database is lost, Create must not mistake its directory for a new root.
      this.format = path.join(root, 'format');
      this.formatFd = this.open(this.format, fs.constants.O_RDWR | fs.constants.O_NOFOLLOW | (create ? fs.constants.O_CREAT | fs.constants.O_EXCL : 0), 0o600);
      if (create) { fs.writeFileSync(this.formatFd, this.formatBytes); fs.fsyncSync(this.formatFd); }
      // Reject a different reader profile before opening SQLite or claiming an
      // owner. No automatic upgrade or reconstruction of old custody bindings.
      const sentinel = Buffer.alloc(this.formatBytes.length + 1);
      check(fs.readSync(this.formatFd, sentinel, 0, sentinel.length, 0) === this.formatBytes.length &&
        sentinel.subarray(0, this.formatBytes.length).equals(this.formatBytes), 'unavailable');
      this.database = path.join(root, DATABASE);
      this.dbFd = this.open(this.database, fs.constants.O_RDWR | fs.constants.O_NOFOLLOW | (create ? fs.constants.O_CREAT | fs.constants.O_EXCL : 0), 0o600);
      this.check();
    } catch (error) { this.close(); throw normalize(error); }
  }
  open(file, flags, mode) { const fd = fs.openSync(file, flags, mode); this.fds.push(fd); return fd; }
  checkParent() {
    check(sameFile(fs.fstatSync(this.parentFd), fs.lstatSync(this.parent)) && fs.realpathSync(this.parent) === this.parent, 'unavailable');
  }
  check() {
    this.checkParent();
    check(sameFile(fs.fstatSync(this.rootFd), fs.lstatSync(this.root)) && fs.realpathSync(this.root) === this.root, 'unavailable');
    const format = fs.fstatSync(this.formatFd), marker = Buffer.alloc(this.formatBytes.length);
    check(sameFile(format, fs.lstatSync(this.format)) && format.isFile() && format.uid === process.getuid() && (format.mode & 0o7777) === 0o600 && format.nlink === 1 && format.size === marker.length, 'unavailable');
    check(fs.readSync(this.formatFd, marker, 0, marker.length, 0) === marker.length && marker.equals(this.formatBytes), 'unavailable');
    const db = fs.fstatSync(this.dbFd);
    check(sameFile(db, fs.lstatSync(this.database)), 'unavailable');
    for (const suffix of ['', '-wal', '-shm', '-journal']) {
      let stat;
      try { stat = fs.lstatSync(this.database + suffix); } catch (error) { if (suffix && error.code === 'ENOENT') continue; throw error; }
      check(stat.isFile() && !stat.isSymbolicLink() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o600 && stat.nlink === 1, 'unavailable');
    }
  }
  sync(syncDirectory = fs.fsyncSync) {
    this.check(); syncDirectory(this.rootFd); this.check(); syncDirectory(this.parentFd); this.check();
  }
  close() {
    for (const fd of this.fds.reverse()) { try { fs.closeSync(fd); } catch {} }
    this.fds = [];
    if (this.registered) opening.delete(this.key);
    this.registered = false;
  }
}

const SCHEMA = `
CREATE TABLE metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1),format TEXT NOT NULL,version INTEGER NOT NULL,store_id TEXT NOT NULL,generation INTEGER NOT NULL,instance_id TEXT NOT NULL,expires_at INTEGER NOT NULL) STRICT;
CREATE TABLE owner_history(sequence INTEGER PRIMARY KEY,generation INTEGER NOT NULL,instance_id TEXT NOT NULL,expires_at INTEGER NOT NULL,kind TEXT NOT NULL) STRICT;
CREATE TABLE events(stream TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>0),digest TEXT NOT NULL,bytes BLOB NOT NULL,generation INTEGER NOT NULL,PRIMARY KEY(stream,sequence),UNIQUE(stream,sequence,digest)) STRICT;
CREATE TABLE heads(stream TEXT PRIMARY KEY,sequence INTEGER NOT NULL,digest TEXT NOT NULL,FOREIGN KEY(stream,sequence,digest) REFERENCES events(stream,sequence,digest)) STRICT;
CREATE TABLE projections(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),source_stream TEXT NOT NULL,source_sequence INTEGER NOT NULL,source_digest TEXT NOT NULL,bytes BLOB NOT NULL,PRIMARY KEY(kind,id),FOREIGN KEY(source_stream,source_sequence,source_digest) REFERENCES events(stream,sequence,digest)) STRICT;
CREATE TABLE receipts(scope TEXT NOT NULL,operation TEXT NOT NULL,key_digest TEXT NOT NULL,request_digest TEXT NOT NULL,source_stream TEXT NOT NULL,source_sequence INTEGER NOT NULL,source_digest TEXT NOT NULL,bytes BLOB NOT NULL,PRIMARY KEY(scope,operation,key_digest),FOREIGN KEY(source_stream,source_sequence,source_digest) REFERENCES events(stream,sequence,digest)) STRICT;
CREATE TABLE outbox(id TEXT PRIMARY KEY,task_id TEXT NOT NULL,node_id TEXT NOT NULL,attempt_id TEXT NOT NULL,generation INTEGER NOT NULL,kind TEXT NOT NULL,input_digest TEXT NOT NULL,payload BLOB NOT NULL,source_stream TEXT NOT NULL,source_sequence INTEGER NOT NULL,source_digest TEXT NOT NULL,revision INTEGER NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','unknown','observed')),observation_stream TEXT,observation_sequence INTEGER,observation_digest TEXT,FOREIGN KEY(source_stream,source_sequence,source_digest) REFERENCES events(stream,sequence,digest),FOREIGN KEY(observation_stream,observation_sequence,observation_digest) REFERENCES events(stream,sequence,digest)) STRICT;
${['events', 'receipts', 'owner_history'].flatMap(table => ['UPDATE', 'DELETE'].map(action => `CREATE TRIGGER ${table}_no_${action.toLowerCase()} BEFORE ${action} ON ${table} BEGIN SELECT RAISE(ABORT,'immutable'); END;`)).join('\n')}
PRAGMA application_id=${APP_ID}; PRAGMA user_version=1;
`;
const EXPECTED_TABLES = ['events', 'heads', 'metadata', 'outbox', 'owner_history', 'projections', 'receipts'];
const EXPECTED_SCHEMA = [...SCHEMA.matchAll(/CREATE (TABLE|TRIGGER) (\w+)[^\n]*/g)].map(match => ({ type: match[1].toLowerCase(), name: match[2], sql: match[0].replace(/;$/, '') })).sort((a, b) => a.type.localeCompare(b.type) || a.name.localeCompare(b.name));

export class Store {
  #db; #files; #active = null; #closed = false; #transaction = false; #currentTx = null; #clock; #monotonic;
  constructor(token, db, files, options) {
    check(token === INTERNAL); this.#db = db; this.#files = files;
    this.#clock = options.clock ?? Date.now; this.#monotonic = options.monotonic ?? (() => performance.now());
  }
  static create(root, options = {}) { return Store.#open(root, options, true); }
  static openExisting(root, options = {}) { return Store.#open(root, options, false); }
  static #open(root, options, create) {
    const [major, minor] = process.versions.node.split('.').map(Number);
    check(['darwin', 'linux'].includes(process.platform) && major === 24 && minor >= 15, 'unsupported');
    check(closed(options, ['format', 'clock', 'monotonic', 'syncDirectory']) && (options.format === undefined || [FORMAT, CUSTODY_FORMAT, INTERACTION_FORMAT].includes(options.format)));
    const format = options.format ?? FORMAT, version = format === INTERACTION_FORMAT ? 3 : format === CUSTODY_FORMAT ? 2 : 1;
    for (const key of ['clock', 'monotonic', 'syncDirectory']) check(options[key] === undefined || typeof options[key] === 'function');
    let files, db;
    try {
      files = new PrivateFiles(root, create, format);
      db = new DatabaseSync(files.database, { timeout: 100, enableForeignKeyConstraints: true, allowExtension: false, defensive: true, readBigInts: true });
      // One connection holds SQLite's physical lock until close, including
      // between short transactions. This says nothing about live Workers.
      db.exec('PRAGMA locking_mode=EXCLUSIVE; PRAGMA synchronous=FULL;');
      if (create) check(db.prepare('PRAGMA journal_mode=WAL').get().journal_mode === 'wal', 'unavailable');
      db.exec('BEGIN EXCLUSIVE');
      if (create) {
        db.exec(SCHEMA);
        db.exec('PRAGMA user_version=' + version);
        db.prepare('INSERT INTO metadata VALUES(1,?,?,?,?,?,?)').run(format, version, crypto.randomUUID(), 0, '', 0);
      }
      check(db.prepare('PRAGMA application_id').get().application_id === BigInt(APP_ID) && db.prepare('PRAGMA user_version').get().user_version === BigInt(version), 'unavailable');
      check(db.prepare('PRAGMA journal_mode').get().journal_mode === 'wal' && db.prepare('PRAGMA locking_mode').get().locking_mode === 'exclusive', 'unavailable');
      check(db.prepare('PRAGMA quick_check(1)').get().quick_check === 'ok', 'unavailable');
      const tables = db.prepare("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name").all().map(row => row.name);
      check(JSON.stringify(tables) === JSON.stringify(EXPECTED_TABLES), 'unavailable');
      const schema = db.prepare("SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name").all();
      check(encode(schema).equals(encode(EXPECTED_SCHEMA)), 'unavailable');
      check(db.prepare('PRAGMA foreign_key_check').all().length === 0, 'unavailable');
      const metadata = db.prepare('SELECT * FROM metadata WHERE singleton=1').get();
      check(metadata?.format === format && metadata.version === BigInt(version) && id(metadata.store_id) && metadata.generation >= 0n && metadata.generation <= MAX_INT, 'unavailable');
      check(metadata.generation === 0n ? metadata.instance_id === '' && metadata.expires_at === 0n : id(metadata.instance_id) && metadata.expires_at > 0n && metadata.expires_at <= BigInt(Number.MAX_SAFE_INTEGER), 'unavailable');
      files.check(); db.exec('COMMIT'); files.sync(options.syncDirectory);
      return new Store(INTERNAL, db, files, options);
    } catch (error) {
      try { if (db?.isTransaction) db.exec('ROLLBACK'); } catch {}
      try { db?.close(); } finally { files?.close(); }
      throw normalize(error);
    }
  }
  #enter() {
    check(!this.#closed, 'closed');
    if (this.#transaction) { this.#currentTx?.poison('nested-transaction'); fail('nested-transaction'); }
    try { this.#files.check(); } catch (error) { throw normalize(error); }
  }
  #metadata() { return this.#db.prepare('SELECT * FROM metadata WHERE singleton=1').get(); }
  info() {
    try { this.#enter(); const row = this.#metadata(); return Object.freeze({ format: row.format, storeId: row.store_id, generation: row.generation }); }
    catch (error) { throw normalize(error); }
  }
  // Narrow startup inspection while THIS connection holds the physical writer
  // lock but before a new logical owner exists. Only the v2 recovery consumer
  // uses this read-only snapshot; no Task mutation or owner token is returned.
  inspectRecovery(callback) {
    this.#enter(); let tx;
    try {
      check(this.#active === null && [CUSTODY_FORMAT, INTERACTION_FORMAT].includes(this.#metadata().format) && typeof callback === 'function', 'owner');
      check(Object.prototype.toString.call(callback) !== '[object AsyncFunction]', 'async-transaction');
      const until = this.#monotonic() + LIMITS.transactionMs;
      this.#transaction = true; this.#db.exec('BEGIN');
      tx = new Transaction(this.#db, null, false, () => check(this.#monotonic() <= until, 'deadline')); this.#currentTx = tx;
      const value = callback(tx);
      check(!value || typeof value.then !== 'function', 'async-transaction');
      tx.finish(); this.#files.check(); this.#db.exec('COMMIT'); return value;
    } catch (error) { this.#rollback(); throw normalize(error); }
    finally { tx?.expire(); this.#currentTx = null; this.#transaction = false; }
  }
  #owner(owner) {
    check(this.#active !== null && closed(owner, ['storeId', 'generation', 'instanceId', 'expiresAt']), 'owner');
    const row = this.#metadata(), now = this.#clock();
    check(owner.storeId === row.store_id && owner.generation === row.generation && owner.generation === this.#active.generation && owner.instanceId === row.instance_id && owner.instanceId === this.#active.instanceId && owner.expiresAt === Number(row.expires_at) && owner.expiresAt === this.#active.expiresAt && owner.expiresAt > now, 'owner');
  }
  claimOwner(expectedGeneration, instanceId, expiresAt) { return this.#changeOwner(expectedGeneration, instanceId, expiresAt, null); }
  renewOwner(owner, expiresAt) { return this.#changeOwner(owner?.generation, owner?.instanceId, expiresAt, owner); }
  #changeOwner(expected, instanceId, expiresAt, previous) {
    this.#enter();
    try {
      expected = integer(expected, true); check(id(instanceId)); timestamp(expiresAt);
      check(expiresAt > this.#clock() && (!previous || expiresAt > previous.expiresAt), 'owner');
      this.#transaction = true; this.#db.exec('BEGIN IMMEDIATE');
      const row = this.#metadata(); check(row.generation === expected, 'owner');
      if (previous) this.#owner(previous);
      const generation = previous ? expected : integer(expected + 1n);
      this.#db.prepare('UPDATE metadata SET generation=?,instance_id=?,expires_at=? WHERE singleton=1').run(generation, instanceId, expiresAt);
      this.#db.prepare('INSERT INTO owner_history(generation,instance_id,expires_at,kind) VALUES(?,?,?,?)').run(generation, instanceId, expiresAt, previous ? 'renew' : 'claim');
      this.#files.check(); check(expiresAt > this.#clock(), 'owner'); this.#db.exec('COMMIT');
      this.#active = Object.freeze({ storeId: row.store_id, generation, instanceId, expiresAt });
      return this.#active;
    } catch (error) {
      this.#rollback(); throw normalize(error);
    } finally { this.#transaction = false; }
  }
  #rollback() { try { if (this.#db.isTransaction) this.#db.exec('ROLLBACK'); } catch { this.#closed = true; try { this.#db.close(); } finally { this.#files.close(); } } }
  read(owner, callback) { return this.#run(owner, callback, false); }
  write(owner, callback) { return this.#run(owner, callback, true); }
  #run(owner, callback, write) {
    this.#enter();
    let tx;
    try {
      check(typeof callback === 'function');
      check(Object.prototype.toString.call(callback) !== '[object AsyncFunction]', 'async-transaction');
      const until = this.#monotonic() + LIMITS.transactionMs;
      this.#transaction = true; this.#db.exec(write ? 'BEGIN IMMEDIATE' : 'BEGIN'); this.#owner(owner);
      tx = new Transaction(this.#db, this.#active, write, () => check(this.#monotonic() <= until, 'deadline')); this.#currentTx = tx;
      const value = callback(tx);
      if (value !== null && (typeof value === 'object' || typeof value === 'function') && typeof value.then === 'function') {
        Promise.resolve(value).catch(() => {}); fail('async-transaction');
      }
      tx.finish(); this.#files.check(); this.#owner(owner); this.#db.exec('COMMIT');
      return value;
    } catch (error) { this.#rollback(); throw normalize(error); }
    finally { tx?.expire(); this.#currentTx = null; this.#transaction = false; }
  }
  close() {
    if (this.#transaction) { this.#currentTx?.poison('nested-transaction'); fail('nested-transaction'); }
    if (this.#closed) return;
    this.#closed = true; this.#active = null;
    try { this.#db.close(); } catch (error) { throw normalize(error); } finally { this.#files.close(); }
  }
}

class Transaction {
  #db; #owner; #write; #deadline; #active = true; #error = null; #count = 0; #bytes = 0;
  constructor(db, owner, write, deadline) { this.#db = db; this.#owner = owner; this.#write = write; this.#deadline = deadline; }
  #guard(fn, write = false) {
    try {
      check(this.#active, 'closed'); if (this.#error) throw this.#error;
      this.#deadline(); check(!write || this.#write, 'read-only'); return fn();
    } catch (error) { this.#error ??= normalize(error); throw this.#error; }
  }
  #charge(size) { this.#count++; this.#bytes += size; check(this.#count <= LIMITS.records && this.#bytes <= LIMITS.transactionBytes, 'limit'); }
  finish() { this.#guard(() => {}); }
  poison(code) { this.#error ??= new StoreError(code); }
  expire() { this.#active = false; }
  #reference(value) {
    const ref = source(value);
    const row = this.#db.prepare('SELECT sequence,digest,bytes,generation FROM events WHERE stream=? AND sequence=?').get(ref.stream, ref.sequence);
    check(row && row.digest === ref.digest, 'conflict');
    const event = record(ref.stream, { sequence: row.sequence, digest: row.digest, bytes: row.bytes });
    this.#charge(event.bytes.length); return { ...ref, generation: row.generation };
  }
  head(stream) { return this.#guard(() => {
    check(id(stream)); const row = this.#db.prepare('SELECT sequence,digest FROM heads WHERE stream=?').get(stream);
    if (!row) return { sequence: 0n, digest: '' };
    this.#reference({ stream, ...row }); return head(row);
  }); }
  events(stream, after = 0n, limit = LIMITS.page) { return this.#guard(() => {
    check(id(stream)); after = integer(after, true); page('', limit);
    const rows = this.#db.prepare('SELECT sequence,digest,bytes FROM events WHERE stream=? AND sequence>? ORDER BY sequence LIMIT ?').all(stream, after, limit);
    return rows.map((row, n) => { check(row.sequence === after + BigInt(n) + 1n, 'unavailable'); const value = record(stream, row); this.#charge(value.bytes.length); return value; });
  }); }
  append(stream, expected, events) { return this.#guard(() => {
    expected = head(expected); check(Array.isArray(events) && events.length > 0 && events.length <= LIMITS.records);
    let current = this.head(stream); check(current.sequence === expected.sequence && current.digest === expected.digest, 'conflict');
    for (const candidate of events) {
      const value = record(stream, candidate); check(value.sequence === current.sequence + 1n, 'conflict'); this.#charge(value.bytes.length);
      this.#db.prepare('INSERT INTO events VALUES(?,?,?,?,?)').run(stream, value.sequence, value.digest, value.bytes, this.#owner.generation);
      current = { sequence: value.sequence, digest: value.digest };
    }
    this.#db.prepare('INSERT INTO heads VALUES(?,?,?) ON CONFLICT(stream) DO UPDATE SET sequence=excluded.sequence,digest=excluded.digest').run(stream, current.sequence, current.digest);
    return current;
  }, true); }
  projection(kind, idValue) { return this.#guard(() => {
    check(KINDS.has(kind) && id(idValue));
    const row = this.#db.prepare('SELECT * FROM projections WHERE kind=? AND id=?').get(kind, idValue);
    if (!row) return null;
    const ref = { stream: row.source_stream, sequence: row.source_sequence, digest: row.source_digest };
    this.#reference(ref); const bytes = canonical(row.bytes); this.#charge(bytes.length);
    return { kind, id: idValue, revision: integer(row.revision), source: ref, bytes };
  }); }
  projections(kind, after = '', limit = LIMITS.page) { return this.#guard(() => {
    check(KINDS.has(kind)); page(after, limit);
    return this.#db.prepare('SELECT id FROM projections WHERE kind=? AND id>? ORDER BY id LIMIT ?').all(kind, after, limit).map(row => this.projection(kind, row.id));
  }); }
  putProjection(kind, idValue, expectedRevision, refValue, bytesValue) { return this.#guard(() => {
    check(KINDS.has(kind) && id(idValue)); expectedRevision = integer(expectedRevision, true);
    const revision = integer(expectedRevision + 1n), bytes = canonical(bytesValue), ref = source(refValue);
    this.#reference(ref); const current = this.projection(kind, idValue);
    check((current?.revision ?? 0n) === expectedRevision, 'conflict'); this.#charge(bytes.length);
    this.#db.prepare('INSERT INTO projections VALUES(?,?,?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET revision=excluded.revision,source_stream=excluded.source_stream,source_sequence=excluded.source_sequence,source_digest=excluded.source_digest,bytes=excluded.bytes').run(kind, idValue, revision, ref.stream, ref.sequence, ref.digest, bytes);
    return revision;
  }, true); }
  receipt(scope, operation, keyDigest, requestDigest) { return this.#guard(() => {
    check(id(scope) && id(operation) && hash(keyDigest) && hash(requestDigest));
    const row = this.#db.prepare('SELECT * FROM receipts WHERE scope=? AND operation=? AND key_digest=?').get(scope, operation, keyDigest);
    if (!row) return null;
    check(row.request_digest === requestDigest, 'conflict');
    const ref = { stream: row.source_stream, sequence: row.source_sequence, digest: row.source_digest };
    this.#reference(ref); const bytes = canonical(row.bytes); this.#charge(bytes.length);
    return { scope, operation, keyDigest, requestDigest, source: ref, bytes };
  }); }
  putReceipt(key, requestDigest, refValue, bytesValue) { return this.#guard(() => {
    check(closed(key, ['scope', 'operation', 'keyDigest']));
    const bytes = canonical(bytesValue), ref = source(refValue);
    const current = this.receipt(key.scope, key.operation, key.keyDigest, requestDigest);
    if (current) { check(sameSource(current.source, ref) && current.bytes.equals(bytes), 'conflict'); return; }
    this.#reference(ref); this.#charge(bytes.length);
    this.#db.prepare('INSERT INTO receipts VALUES(?,?,?,?,?,?,?,?)').run(key.scope, key.operation, key.keyDigest, requestDigest, ref.stream, ref.sequence, ref.digest, bytes);
  }, true); }
  command(commandId) { return this.#guard(() => {
    check(id(commandId)); const row = this.#db.prepare('SELECT * FROM outbox WHERE id=?').get(commandId); if (!row) return null;
    check(id(row.task_id) && (row.node_id === '' || id(row.node_id)) && (row.attempt_id === '' || id(row.attempt_id)) && COMMANDS.has(row.kind) && hash(row.input_digest), 'unavailable');
    const ref = { stream: row.source_stream, sequence: row.source_sequence, digest: row.source_digest };
    const original = this.#reference(ref); check(original.generation === row.generation, 'unavailable');
    const payload = canonical(row.payload); this.#charge(payload.length);
    let observation = null;
    if (row.status === 'pending') check(row.revision === 1n && row.observation_stream === null && row.observation_sequence === null && row.observation_digest === null, 'unavailable');
    else {
      check(['unknown', 'observed'].includes(row.status) && row.revision > 1n, 'unavailable');
      observation = { stream: row.observation_stream, sequence: row.observation_sequence, digest: row.observation_digest }; this.#reference(observation);
    }
    return { id: row.id, taskId: row.task_id, nodeId: row.node_id, attemptId: row.attempt_id, generation: integer(row.generation), kind: row.kind, inputDigest: row.input_digest, payload, source: ref, revision: integer(row.revision), status: row.status, observation };
  }); }
  commands(after = '', limit = LIMITS.page) { return this.#guard(() => {
    page(after, limit); return this.#db.prepare('SELECT id FROM outbox WHERE id>? ORDER BY id LIMIT ?').all(after, limit).map(row => this.command(row.id));
  }); }
  enqueue(value) { return this.#guard(() => {
    check(closed(value, ['id', 'taskId', 'nodeId', 'attemptId', 'kind', 'inputDigest', 'payload', 'source']) && id(value.id) && id(value.taskId) && COMMANDS.has(value.kind) && hash(value.inputDigest));
    const nodeId = value.nodeId ?? '', attemptId = value.attemptId ?? '';
    check((nodeId === '' || id(nodeId)) && (attemptId === '' || id(attemptId)));
    const payload = canonical(value.payload), ref = source(value.source), current = this.command(value.id);
    if (current) {
      check(current.taskId === value.taskId && current.nodeId === nodeId && current.attemptId === attemptId && current.kind === value.kind && current.inputDigest === value.inputDigest && sameSource(current.source, ref) && current.payload.equals(payload), 'conflict'); return current;
    }
    const original = this.#reference(ref); check(original.generation === this.#owner.generation, 'owner'); this.#charge(payload.length);
    this.#db.prepare("INSERT INTO outbox VALUES(?,?,?,?,?,?,?,?,?,?,?,1,'pending',NULL,NULL,NULL)").run(value.id, value.taskId, nodeId, attemptId, this.#owner.generation, value.kind, value.inputDigest, payload, ref.stream, ref.sequence, ref.digest);
    return { ...value, nodeId, attemptId, payload, source: ref, generation: this.#owner.generation, revision: 1n, status: 'pending', observation: null };
  }, true); }
  observeCommand(commandId, expectedRevision, status, refValue) { return this.#guard(() => {
    expectedRevision = integer(expectedRevision); check(['unknown', 'observed'].includes(status));
    const ref = source(refValue), observation = this.#reference(ref); check(observation.generation === this.#owner.generation, 'owner'); const current = this.command(commandId);
    check(current && current.revision === expectedRevision && current.status !== 'observed' && (current.status !== 'unknown' || status === 'observed'), 'conflict');
    const revision = integer(expectedRevision + 1n); this.#charge(0);
    this.#db.prepare('UPDATE outbox SET revision=?,status=?,observation_stream=?,observation_sequence=?,observation_digest=? WHERE id=? AND revision=?').run(revision, status, ref.stream, ref.sequence, ref.digest, commandId, expectedRevision);
    return revision;
  }, true); }
}
