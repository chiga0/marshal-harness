import fs from 'node:fs';
import path from 'node:path';
import {digest, encode} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';

export const PROFILE = 'task-local-json-report/v1';
export const MAX_REPORT = 1024 * 1024;
export const INPUT_NAME = 'publication-input.json';
export const hash = digest;
export const isDigest = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/u.test(value);
export const text = (value, max = 128) => typeof value === 'string' && value.length > 0 && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
export const id = value => text(value) && /^[a-zA-Z0-9][a-zA-Z0-9_-]*$/u.test(value);
export const closed = (value, keys) => value !== null && typeof value === 'object' && !Array.isArray(value) &&
  [Object.prototype, null].includes(Object.getPrototypeOf(value)) && Object.keys(value).sort().join(',') === [...keys].sort().join(',');
export function check(value, code = 'publication_invalid_input') { if (!value) throw new Error(code); }
export function time(deadline, signal) { check(Number.isSafeInteger(deadline) && Date.now() < deadline && !signal?.aborted, 'publication_stopped'); }
export function nameFor({taskId, artifactDigest} = {}) {
  check(id(taskId) && isDigest(artifactDigest)); return `${taskId}-${artifactDigest.slice(7)}.json`;
}
export function binding(value) {
  check(closed(value, ['actionId', 'targetId', 'name', 'artifactDigest', 'bytes', 'authorizationDigest']) && id(value.actionId) && id(value.targetId) &&
    isDigest(value.artifactDigest) && isDigest(value.authorizationDigest) && Number.isSafeInteger(value.bytes) && value.bytes > 0 && value.bytes <= MAX_REPORT &&
    text(value.name, 198) && /^[a-zA-Z0-9][a-zA-Z0-9_-]*-[a-f0-9]{64}\.json$/u.test(value.name) && value.name.endsWith('-' + value.artifactDigest.slice(7) + '.json'));
  return JSON.parse(encode(value));
}
export function json(bytes) {
  check(bytes instanceof Uint8Array && bytes.length > 0 && bytes.length <= MAX_REPORT, 'publication_report_limit');
  check(!(bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf), 'publication_invalid_json');
  const value = parseJson(bytes);
  const visit = item => {
    if (typeof item === 'string') check(item.isWellFormed() && !item.includes('\0'), 'publication_invalid_json');
    if (item && typeof item === 'object') for (const [key, child] of Object.entries(item)) {
      check(key.isWellFormed() && !key.includes('\0'), 'publication_invalid_json'); visit(child);
    }
  };
  visit(value); encode(value); return value;
}
export const same = (a, b) => a.dev === b.dev && a.ino === b.ino;
export const identity = stat => ({dev: stat.dev, ino: stat.ino, mode: stat.mode, uid: stat.uid});
export function canonicalPath(value) {
  check(text(value, 8192) && path.isAbsolute(value) && path.normalize(value) === value && value !== path.parse(value).root,
    'publication_invalid_path'); return value;
}
function directory(stat) { check(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o700, 'publication_directory_unavailable'); }
export class HeldRoot {
  constructor(root, expected) {
    this.root = canonicalPath(root); this.parent = path.dirname(root); this.fds = [];
    try {
      check(fs.realpathSync(root) === root && fs.realpathSync(this.parent) === this.parent, 'publication_directory_unavailable');
      this.parentFd = fs.openSync(this.parent, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW); this.fds.push(this.parentFd);
      this.fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW); this.fds.push(this.fd);
      this.check(); this.original = identity(fs.fstatSync(this.fd)); this.parentOriginal = identity(fs.fstatSync(this.parentFd));
      if (expected) check(encode(this.original).equals(encode(expected.root)) && encode(this.parentOriginal).equals(encode(expected.parent)), 'publication_directory_unavailable');
    } catch (error) {this.close(); throw error;}
  }
  descriptor() {this.check(); return {root: this.original, parent: this.parentOriginal};}
  check() {
    check(this.fds.length === 2, 'publication_closed');
    for (const [name, fd] of [[this.parent, this.parentFd], [this.root, this.fd]]) {
      const stat = fs.fstatSync(fd); directory(stat);
      check(fs.realpathSync(name) === name && same(stat, fs.lstatSync(name)), 'publication_directory_unavailable');
    }
  }
  sync() {this.check(); fs.fsyncSync(this.fd); this.check(); fs.fsyncSync(this.parentFd); this.check();}
  close() {for (const fd of this.fds.splice(0).reverse()) fs.closeSync(fd);}
}
export function readHeld(filename, {deadline, signal, expected, oneLink = false, allowEmpty = false} = {}) {
  time(deadline, signal);
  const fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const initial = fs.fstatSync(fd);
    const valid = stat => check(stat.isFile() && stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o600 &&
      stat.nlink >= 1 && stat.nlink <= (oneLink ? 1 : 2) && stat.size >= (allowEmpty ? 0 : 1) && stat.size <= MAX_REPORT, 'publication_file_unavailable');
    valid(initial); check(same(initial, fs.lstatSync(filename)), 'publication_file_unavailable');
    const fingerprint = stat => ({...identity(stat), size: stat.size, mtimeMs: stat.mtimeMs, ctimeMs: stat.ctimeMs, nlink: stat.nlink});
    const descriptor = fingerprint(initial);
    if (expected) check(encode(descriptor).equals(encode(expected)), 'publication_input_changed');
    const bytes = Buffer.alloc(initial.size + 1); let offset = 0;
    while (offset < bytes.length) {time(deadline, signal); const n = fs.readSync(fd, bytes, offset, Math.min(65536, bytes.length - offset), offset); if (!n) break; offset += n;}
    const recheck = () => {
      time(deadline, signal); const now = fs.fstatSync(fd); valid(now);
      check(encode(fingerprint(now)).equals(encode(descriptor)) && same(now, fs.lstatSync(filename)), 'publication_input_changed');
    };
    recheck(); check(offset === initial.size, 'publication_input_changed');
    return {fd, bytes: bytes.subarray(0, offset), descriptor, recheck, close: () => fs.closeSync(fd)};
  } catch (error) {fs.closeSync(fd); throw error;}
}
export function observe(root, value, deadline, signal) {
  root.check(); time(deadline, signal);
  let input;
  try {
    input = readHeld(path.join(root.root, value.name), {deadline, signal, allowEmpty: true});
    const actual = {bytes: input.bytes.length, artifactDigest: hash(input.bytes)};
    const matched = actual.bytes === value.bytes && actual.artifactDigest === value.artifactDigest;
    if (matched) json(input.bytes);
    root.check(); return {status: matched ? 'matched' : 'conflict', actual};
  } catch (error) {
    if (error.code === 'ENOENT') {root.check(); time(deadline, signal); return {status: 'absent', actual: null};}
    throw error;
  } finally {input?.close();}
}
export function evidence(name, value) {return {name, mediaType: 'application/json', content: encode(value)};}
export function baseURL(value) {
  check(text(value, 8192), 'publication_invalid_origin'); const url = new URL(value);
  check(url.protocol === 'http:' && ['127.0.0.1', '[::1]'].includes(url.hostname) && !url.username && !url.password && !url.search && !url.hash &&
    url.pathname.endsWith('/') && !/%|\/\//u.test(url.pathname) && url.href === value, 'publication_invalid_origin'); return url.href;
}
