import {encode} from '../task-store/store.mjs';
import {json, check} from './io.mjs';
import {run} from './command.mjs';

let size = 0, parts = [], processing = false;
const deadline = setTimeout(() => process.exit(1), 10000);
process.stdin.on('data', chunk => {
  if (processing) return;
  size += chunk.length;
  if (size > 192 * 1024) process.exit(1);
  parts.push(chunk); const bytes = Buffer.concat(parts), end = bytes.indexOf(10);
  if (end < 0) return;
  processing = true; clearTimeout(deadline); process.stdin.pause();
  (async () => {
    check(end === bytes.length - 1);
    const request = json(bytes.subarray(0, end));
    check(encode(request).equals(bytes.subarray(0, end)));
    const report = await run(request);
    process.stdout.write(Buffer.concat([encode(report), Buffer.from('\n')]), () => process.exit(0));
  })().catch(() => process.exit(1)); // Never print input, paths or raw errors.
});
process.stdin.on('error', () => process.exit(1));
process.stdin.on('end', () => {if (!processing) process.exit(1);});
