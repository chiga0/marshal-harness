import fs from 'node:fs';
import {run} from './command.mjs';
import {json} from './io.mjs';
// Test-only interception of the ORIGINAL call, scoped to this owned child.
const point = process.argv[2], unlink = fs.unlinkSync, sync = fs.fsyncSync;
fs.unlinkSync = filename => {
  if (point === 'stop-before-unlink') process.kill(process.pid, 'SIGSTOP');
  if (point === 'before-unlink') process.kill(process.pid, 'SIGKILL');
  if (point === 'unlink-error') throw new Error('fixture-unlink-failure');
  const result = unlink(filename);
  if (point === 'after-unlink') process.kill(process.pid, 'SIGKILL');
  return result;
};
let syncs = 0;
fs.fsyncSync = fd => {if (++syncs === 2 && point === 'directory-sync-error') throw new Error('fixture-sync-failure'); return sync(fd);};
let parts = [], length = 0;
process.stdin.on('data', chunk => {
  parts.push(chunk); length += chunk.length; if (length > 192 * 1024) process.exit(1);
  const bytes = Buffer.concat(parts); if (bytes.at(-1) !== 10) return;
  process.stdin.pause(); run(json(bytes.subarray(0, -1))).then(() => process.exit(0), () => process.exit(1));
});
