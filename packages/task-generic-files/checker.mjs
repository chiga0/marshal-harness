// Fixed data-only checker: never evaluates candidate code or shells out.
import fs from 'node:fs';
import path from 'node:path';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {check, textBytes, MAX_FILE_BYTES} from './policy.mjs';

try {
  for await (const line of createInterface({input: process.stdin})) {
    check(Buffer.byteLength(line) <= 262144);
    const request = parseJson(Buffer.from(line)), expected = request.input.files;
    check(Array.isArray(expected) && expected.length >= 1 && expected.length <= 8 &&
      expected.every(item => /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(item.nodeId) && item.path === `results/${item.nodeId}.md`) &&
      new Set(expected.map(item => item.path.toLowerCase())).size === expected.length);
    const root = fs.realpathSync(process.cwd()), leaves = [];
    function walk(relative) {
      for (const name of fs.readdirSync(path.join(root, relative))) {
        const child = relative ? relative + '/' + name : name, absolute = path.join(root, child), stat = fs.lstatSync(absolute);
        check(!stat.isSymbolicLink() && fs.realpathSync(absolute) === absolute);
        if (stat.isDirectory()) {check(child === 'results'); walk(child);}
        else {check(stat.isFile() && stat.nlink === 1); leaves.push(child);}
      }
    }
    walk('');
    check(encode(leaves.sort()).equals(encode(expected.map(item => item.path).sort())));
    const actual = expected.map(item => {
      const fd = fs.openSync(path.join(root, item.path), fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
      try {
        const before = fs.fstatSync(fd); check(before.isFile() && before.nlink === 1 && before.size > 0 && before.size <= MAX_FILE_BYTES);
        const bytes = fs.readFileSync(fd), after = fs.fstatSync(fd);
        check(before.size === bytes.length && before.size === after.size && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs);
        textBytes(bytes);
        return {nodeId: item.nodeId, path: item.path, digest: digest(bytes), bytes: bytes.length};
      } finally {fs.closeSync(fd);}
    });
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'exact-files', actual}]}).toString() + '\n', () => process.exit(0));
    break;
  }
} catch {process.exit(1);}
