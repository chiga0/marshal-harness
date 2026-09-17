// Data-only trusted checker: never import or execute candidate contents.
import fs from 'node:fs';
import path from 'node:path';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.ts';
import {check, utf8} from './policy.ts';
try {
  for await (const line of createInterface({input: process.stdin})) {
    check(Buffer.byteLength(line) <= 262144);
    const request = JSON.parse(line);
    check(Array.isArray(request.input.files) && request.input.files.length >= 1 && request.input.files.length <= 8);
    const actual = request.input.files.map(ref => {
      check(/^results\/[A-Za-z0-9][A-Za-z0-9_-]{0,127}\.md$/.test(ref.path));
      const filename = path.resolve(ref.path);
      check(fs.realpathSync(filename) === filename);
      const fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
      try {
        const stat = fs.fstatSync(fd); check(stat.isFile() && stat.nlink === 1 && stat.size === ref.bytes && stat.size <= 8192);
        const bytes = fs.readFileSync(fd); utf8(bytes);
        check(digest(bytes) === ref.digest);
        return {path: ref.path, digest: digest(bytes), bytes: bytes.length};
      } finally {fs.closeSync(fd);}
    });
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'exact-files', actual}]}).toString() + '\n', () => process.exit(0));
    break;
  }
} catch {process.exitCode = 1; process.stdin.destroy();}
