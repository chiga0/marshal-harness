import fs from 'node:fs';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {canonical, MAX_OBSERVATION} from './custody-contract.mjs';

const fail = () => { throw new Error('custody_storage_unavailable'); };
const same = (a, b) => a.dev === b.dev && a.ino === b.ino && a.uid === b.uid && a.mode === b.mode;
export class CustodyFiles {
  constructor(root) {
    if (typeof root !== 'string' || !path.isAbsolute(root) || fs.realpathSync(root) !== root) fail();
    this.root = root;
    this.fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
    try { this.check(); } catch (error) { this.close(); throw error; }
  }
  check() {
    const stat = fs.fstatSync(this.fd);
    if (!stat.isDirectory() || stat.uid !== process.getuid() || (stat.mode & 0o7777) !== 0o700 ||
      !same(stat, fs.lstatSync(this.root)) || fs.realpathSync(this.root) !== this.root) fail();
  }
  filename(id, suffix) {
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id) || !['observation', 'ack'].includes(suffix)) fail();
    this.check(); return path.join(this.root, id + '.' + suffix + '.json');
  }
  read(id, suffix = 'observation') {
    const filename = this.filename(id, suffix); let fd;
    try { fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW); }
    catch (error) { if (error.code === 'ENOENT') { this.check(); return null; } throw error; }
    try {
      const stat = fs.fstatSync(fd);
      if (!stat.isFile() || stat.nlink !== 1 || stat.uid !== process.getuid() || (stat.mode & 0o7777) !== 0o600 || stat.size > MAX_OBSERVATION || stat.size < 2) fail();
      const bytes = Buffer.alloc(stat.size + 1); let offset = 0, count;
      while (offset < bytes.length && (count = fs.readSync(fd, bytes, offset, bytes.length - offset, null))) offset += count;
      const after = fs.fstatSync(fd);
      if (offset !== stat.size || !same(stat, fs.lstatSync(filename)) || stat.mtimeMs !== after.mtimeMs || stat.ctimeMs !== after.ctimeMs) fail();
      const value = JSON.parse(bytes.subarray(0, offset).toString());
      if (!canonical(value).equals(bytes.subarray(0, offset))) fail();
      this.check(); return value;
    } finally { fs.closeSync(fd); }
  }
  write(id, value, suffix = 'observation') {
    const filename = this.filename(id, suffix), bytes = canonical(value), prior = this.read(id, suffix);
    if (prior) { if (!canonical(prior).equals(bytes)) fail(); return; }
    this.check();
    if (fs.readdirSync(this.root).length >= 8192) throw new Error('custody_storage_limit');
    const temporary = path.join(this.root, 'pending-' + randomUUID());
    const fd = fs.openSync(temporary, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
    try { fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
    this.check(); fs.linkSync(temporary, filename); fs.unlinkSync(temporary); fs.fsyncSync(this.fd); this.check();
  }
  close() { if (this.fd !== undefined) fs.closeSync(this.fd); this.fd = undefined; }
}
