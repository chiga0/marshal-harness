import fs from 'node:fs/promises';
import { constants } from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';

export const FORMAT = 'marshal-node-team-experiment/v1';
export const MAX_STATE = 32 * 1024 * 1024;
export const digest = value => 'sha256:' + crypto.createHash('sha256').update(typeof value === 'string' || Buffer.isBuffer(value) ? value : JSON.stringify(value)).digest('hex');
export const artifactHash = value => crypto.createHash('sha256').update(value).digest('hex');
// Native Agent credentials remain in their native HOME files. Only these
// non-secret environment names cross the launcher boundary, never API tokens.
export function nativeEnvironment(source = process.env) {
  const env = {};
  for (const name of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (typeof source[name] === 'string' && !source[name].includes('\0')) env[name] = source[name];
  env.PATH = path.dirname(process.execPath) + path.delimiter + (env.PATH || '/usr/bin:/bin');
  env.LANG ||= 'en_US.UTF-8';
  return env;
}
export class Fault extends Error {
  constructor(code, status = 409) { super(code); this.code = code; this.status = status; }
}
export const fail = (code, status = 409) => { throw new Fault(code, status); };
export function closedObject(value, keys) {
  return value !== null && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(k => keys.includes(k));
}
export function text(value, maximum) {
  return typeof value === 'string' && value.trim().length > 0 && !value.includes('\0') && Buffer.byteLength(value) <= maximum && !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value);
}
export function id(value) { return typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$/.test(value); }
export function sameSecret(a, b) {
  if (typeof a !== 'string' || typeof b !== 'string') return false;
  const left = Buffer.from(a), right = Buffer.from(b);
  return left.length === right.length && crypto.timingSafeEqual(left, right);
}

// These files belong only to this explicitly selected experiment root. Never
// adopt a Marshal .marshal root, follow symlinks, or repair unknown ownership.
export async function privateRoot(directory) {
  if (!path.isAbsolute(directory) || path.normalize(directory) !== directory || directory === '/' || directory.split(path.sep).includes('.marshal')) fail('invalid-data-dir', 400);
  let current = path.parse(directory).root;
  const parts = directory.slice(current.length).split(path.sep);
  for (let n = 0; n < parts.length; n++) {
    current = path.join(current, parts[n]);
    let stat;
    try { stat = await fs.lstat(current); }
    catch (err) {
      if (err.code !== 'ENOENT' || n !== parts.length - 1) throw new Fault('data-dir-unavailable', 503);
      await fs.mkdir(current, { mode: 0o700 }); stat = await fs.lstat(current);
    }
    if (!stat.isDirectory() || stat.isSymbolicLink()) fail('unsafe-data-dir', 400);
    if (n === parts.length - 1 && (stat.uid !== process.getuid() || (stat.mode & 0o777) !== 0o700)) fail('private-data-dir-required', 400);
  }
  if (Buffer.byteLength(path.join(directory, 's.sock')) > 95) fail('data-dir-socket-path-too-long', 400);
  return directory;
}

export async function readPrivate(file, maximum = MAX_STATE) {
  const handle = await fs.open(file, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.uid !== process.getuid() || (stat.mode & 0o777) !== 0o600 || stat.nlink !== 1 || stat.size > maximum) fail('unsafe-private-file', 503);
    const bytes = await handle.readFile();
    if (bytes.length > maximum) fail('private-file-too-large', 503);
    return bytes;
  } finally { await handle.close(); }
}

export async function atomicPrivate(file, value) {
  const bytes = Buffer.from(JSON.stringify(value));
  if (bytes.length > MAX_STATE) fail('state-capacity-exceeded', 503);
  const temporary = path.join(path.dirname(file), '.write-' + crypto.randomUUID());
  let handle;
  try {
    handle = await fs.open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    await handle.writeFile(bytes); await handle.sync(); await handle.close(); handle = undefined;
    await fs.rename(temporary, file);
    const directory = await fs.open(path.dirname(file), constants.O_RDONLY);
    try { await directory.sync(); } finally { await directory.close(); }
  } finally {
    await handle?.close();
    // Only the unique temporary file created by this call is recoverable here.
    await fs.unlink(temporary).catch(err => { if (err.code !== 'ENOENT') throw err; });
  }
}

export function validateState(state) {
  if (!closedObject(state, ['format', 'generation', 'tasks', 'createKeys']) || state.format !== FORMAT || !Number.isSafeInteger(state.generation) || state.generation < 0 || !Array.isArray(state.tasks) || state.tasks.length > 100 || !closedObject(state.createKeys, Object.keys(state.createKeys ?? {}))) fail('invalid-state', 503);
  const statuses = ['awaiting-approval', 'approved', 'running', 'verifying', 'completed', 'failed', 'cancelling', 'cancelled', 'intervention'];
  const seen = new Set();
  for (const task of state.tasks) {
    if (!id(task.id) || seen.has(task.id) || !statuses.includes(task.status) || !Number.isSafeInteger(task.revision) || task.revision < 1 || !text(task.intent, 4096) || !Array.isArray(task.workers) || task.workers.length !== 2 || !task.plan || task.previewDigest !== digest(task.plan) || !Array.isArray(task.controls) || task.controls.length > 3 || !Number.isInteger(task.attempts) || task.attempts < 0 || task.attempts > 1) fail('invalid-state', 503);
    seen.add(task.id);
    if (task.approvedAt && (!Number.isFinite(Date.parse(task.deadline)) || Date.parse(task.deadline) - Date.parse(task.approvedAt) !== task.plan.timeoutMs)) fail('invalid-state', 503);
    if (task.status === 'completed' && (!task.delivery || task.delivery.taskId !== task.id || task.delivery.files?.length !== 2 || task.delivery.files.some(f => !text(f.content, 65536) || f.sha256 !== artifactHash(f.content)))) fail('invalid-state', 503);
  }
  for (const receipt of Object.values(state.createKeys)) if (!seen.has(receipt.taskId) || typeof receipt.digest !== 'string') fail('invalid-state', 503);
  return state;
}

export class Store {
  constructor(directory, state, fingerprint) { this.directory = directory; this.file = path.join(directory, 'state.json'); this.state = state; this.fingerprint = fingerprint; }
  static async open(directory) {
    await privateRoot(directory);
    let state, fingerprint = null;
    try {
      const raw = await readPrivate(path.join(directory, 'state.json'));
      fingerprint = digest(raw); state = validateState(JSON.parse(raw));
    } catch (err) {
      if (err.code !== 'ENOENT') throw err instanceof Fault ? err : new Fault('invalid-state', 503);
      const entries = await fs.readdir(directory);
      if (entries.some(name => !['supervisor.lock'].includes(name))) fail('unknown-data-root', 503);
      state = { format: FORMAT, generation: 0, tasks: [], createKeys: {} };
    }
    return new Store(directory, state, fingerprint);
  }
  async save(next) {
    validateState(next);
    if (this.fingerprint !== null) {
      if (digest(await readPrivate(this.file)) !== this.fingerprint) fail('state-drift', 503);
    } else {
      try { await fs.lstat(this.file); fail('state-drift', 503); } catch (err) { if (err.code !== 'ENOENT') throw err; }
    }
    next.generation = this.state.generation + 1;
    await atomicPrivate(this.file, next);
    this.state = next; this.fingerprint = digest(JSON.stringify(next));
  }
}
