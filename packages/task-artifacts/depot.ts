import fs from 'node:fs';
import path from 'node:path';
import {createHash, randomUUID} from 'node:crypto';

export const MAX_ARTIFACT_BYTES = 8 * 1024 * 1024;
const FORMAT = Buffer.from('{"format":"marshal-task-artifacts/v1"}\n');
const NOFOLLOW = fs.constants.O_NOFOLLOW;
const same = (a, b) => a.dev === b.dev && a.ino === b.ino;
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
export class ArtifactDepotError extends Error {
  constructor(code) { super(code); this.name = 'ArtifactDepotError'; this.code = code; }
}
function requireValue(value, code = 'artifact_depot_unavailable') { if (!value) throw new ArtifactDepotError(code); }
function regular(stat) {
  requireValue(stat.isFile() && stat.uid === process.getuid() && (stat.mode & 0o777) === 0o600 && stat.nlink === 1);
}
function directory(stat, privateRoot = false) {
  requireValue(stat.isDirectory() && stat.uid === process.getuid());
  if (privateRoot) requireValue((stat.mode & 0o777) === 0o700);
}
function reference(value) {
  requireValue(value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).length === 2 && /^sha256:[0-9a-f]{64}$/.test(value.digest) &&
    Number.isSafeInteger(value.bytes) && value.bytes >= 0 && value.bytes <= MAX_ARTIFACT_BYTES, 'invalid_artifact_reference');
  return value.digest.slice(7);
}

/** Bytes only: Task ownership, readiness, acceptance and references belong to SQLite. */
export class ArtifactDepot {
  #root; #parent; #rootFd; #parentFd; #closed = false; #failed = false;
  static create(root) { return ArtifactDepot.#open(root, true); }
  static openExisting(root) { return ArtifactDepot.#open(root, false); }
  static #open(root, create) {
    requireValue(typeof root === 'string' && path.isAbsolute(root) && path.normalize(root) === root && root !== path.parse(root).root,
      'invalid_artifact_root');
    const depot = new ArtifactDepot(); depot.#root = root; depot.#parent = path.dirname(root);
    try {
      requireValue(fs.realpathSync(depot.#parent) === depot.#parent);
      depot.#parentFd = fs.openSync(depot.#parent, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | NOFOLLOW);
      directory(fs.fstatSync(depot.#parentFd));
      if (create) fs.mkdirSync(root, {mode: 0o700}); // Never adopt an existing empty/legacy root.
      depot.#rootFd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | NOFOLLOW);
      depot.#check();
      if (create) {
        const fd = fs.openSync(path.join(root, 'format.json'), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | NOFOLLOW, 0o600);
        try { fs.writeFileSync(fd, FORMAT); } finally { fs.closeSync(fd); }
      }
      // Unreferenced pending bytes are not authority and do not block unrelated
      // committed artifacts. Preserve them, never adopt/delete them on reopen.
      for (const entry of fs.readdirSync(root)) {
        if (entry === 'format.json' || /^[0-9a-f]{64}$/.test(entry)) continue;
        requireValue(/^\.pending-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(entry));
        const stat = fs.lstatSync(path.join(root, entry));
        requireValue(stat.isFile() && stat.uid === process.getuid() && (stat.mode & 0o777) === 0o600 && stat.size <= MAX_ARTIFACT_BYTES);
      }
      // Existing format bytes can be visible after a failed bootstrap sync.
      // Both paths must close that obligation before returning a usable depot.
      depot.#syncBootstrap(); return depot;
    } catch (error) {
      depot.close();
      if (error instanceof ArtifactDepotError) throw error;
      throw new ArtifactDepotError(create && error.code === 'EEXIST' ? 'artifact_root_exists' : 'artifact_depot_unavailable');
    }
  }
  #check() {
    requireValue(!this.#closed && !this.#failed);
    const parent = fs.fstatSync(this.#parentFd), root = fs.fstatSync(this.#rootFd);
    directory(parent); directory(root, true);
    requireValue(fs.realpathSync(this.#parent) === this.#parent && same(parent, fs.lstatSync(this.#parent)) &&
      fs.realpathSync(this.#root) === this.#root && same(root, fs.lstatSync(this.#root)));
  }
  #read(name, bytes) {
    this.#check();
    const target = path.join(this.#root, name);
    const fd = fs.openSync(target, fs.constants.O_RDONLY | NOFOLLOW);
    try { return this.#readHeld(name, bytes, fd); } finally { fs.closeSync(fd); }
  }
  #readHeld(name, bytes, fd) {
    this.#check();
    const target = path.join(this.#root, name);
    const before = fs.fstatSync(fd); regular(before);
    requireValue(before.size === bytes && same(before, fs.lstatSync(target)));
    const result = Buffer.alloc(bytes); let offset = 0;
    while (offset < bytes) {
      const count = fs.readSync(fd, result, offset, bytes - offset, offset);
      requireValue(count > 0); offset += count;
    }
    const after = fs.fstatSync(fd); regular(after);
    requireValue(same(before, after) && after.size === bytes && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs &&
      same(after, fs.lstatSync(target)));
    this.#check(); return result;
  }
  #syncBootstrap() {
    this.#check();
    const fd = fs.openSync(path.join(this.#root, 'format.json'), fs.constants.O_RDONLY | NOFOLLOW);
    try {
      const check = () => requireValue(this.#readHeld('format.json', FORMAT.length, fd).equals(FORMAT));
      check();
      for (const held of [fd, this.#rootFd, this.#parentFd]) {
        fs.fsyncSync(held);
        check(); // Recheck exact held format, named identities and permissions.
      }
    } finally { fs.closeSync(fd); }
  }
  get(value) {
    const name = reference(value);
    try {
      const bytes = this.#read(name, value.bytes);
      requireValue(hash(bytes) === value.digest, 'artifact_integrity_failure'); return bytes;
    } catch (error) {
      if (error instanceof ArtifactDepotError) throw error;
      throw new ArtifactDepotError(error.code === 'ENOENT' ? 'artifact_missing' : 'artifact_depot_unavailable');
    }
  }
  put(value) {
    requireValue(value instanceof Uint8Array && value.byteLength <= MAX_ARTIFACT_BYTES, 'invalid_artifact_bytes');
    const bytes = Buffer.from(value); // Freeze caller-owned memory before hashing/writing.
    const result = {digest: hash(bytes), bytes: bytes.length}, name = result.digest.slice(7);
    try {
      this.#check();
      try {
        const existing = this.get(result); requireValue(existing.equals(bytes), 'artifact_integrity_failure');
        this.#syncInstalled(name); return result;
      } catch (error) { if (!(error instanceof ArtifactDepotError) || error.code !== 'artifact_missing') throw error; }
      const temporary = path.join(this.#root, '.pending-' + randomUUID());
      const target = path.join(this.#root, name);
      const fd = fs.openSync(temporary, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | NOFOLLOW, 0o600);
      try {
        fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);
        const held = fs.fstatSync(fd); regular(held);
        requireValue(held.size === bytes.length && same(held, fs.lstatSync(temporary)));
        this.#check();
        // link is atomic and never overwrites an existing digest path.
        fs.linkSync(temporary, target);
        requireValue(same(held, fs.lstatSync(target)) && same(held, fs.lstatSync(temporary)));
        fs.unlinkSync(temporary); fs.fsyncSync(this.#rootFd);
      } finally { fs.closeSync(fd); }
      requireValue(this.get(result).equals(bytes), 'artifact_integrity_failure');
      return result;
    } catch (error) {
      this.#failed = true;
      if (error instanceof ArtifactDepotError) throw error;
      throw new ArtifactDepotError('artifact_depot_unavailable');
    }
  }
  #syncInstalled(name) {
    this.#check(); const target = path.join(this.#root, name);
    const fd = fs.openSync(target, fs.constants.O_RDONLY | NOFOLLOW);
    try { regular(fs.fstatSync(fd)); fs.fsyncSync(fd); this.#check(); fs.fsyncSync(this.#rootFd); }
    finally { fs.closeSync(fd); }
  }
  close() {
    if (this.#closed) return; this.#closed = true;
    for (const fd of [this.#rootFd, this.#parentFd]) if (fd !== undefined) fs.closeSync(fd);
  }
}
