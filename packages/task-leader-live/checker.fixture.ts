// Checked-in fixed Node oracle. Candidate JSON is data, never imported/executed.
import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {verifyRegions, data} from './scenario.fixture.mjs';
import {businessReply, check, equal} from './proof.fixture.mjs';

export function checkRequest(request, read) {
  const input = request.input, bytes = encode(data), reply = businessReply(input.taskId, input);
  check(input.source?.kind === 'input' && input.source.name === 'sales.json' && input.source.digest === digest(bytes) &&
    input.source.bytes === bytes.length && equal(input.reply, reply) && equal(input.sales, data), 'original_input_mismatch');
  const report = verifyRegions({sales: encode(input.sales), answer: reply.answer,
    files: ['east.json', 'west.json'].map(path => ({path, content: read(path)}))});
  return {profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'leader-regions', actual: {report, reply}}]};
}
function read(name) {
  const fd = fs.openSync(name, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const before = fs.fstatSync(fd); check(before.isFile() && before.nlink === 1 && before.size > 0 && before.size <= 4096, 'candidate_boundary');
    const bytes = Buffer.alloc(before.size + 1), count = fs.readSync(fd, bytes, 0, bytes.length, 0), after = fs.fstatSync(fd);
    check(count === before.size && before.size === after.size && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs, 'candidate_changed');
    return bytes.subarray(0, count);
  } finally {fs.closeSync(fd);}
}
export async function main(input = process.stdin) {
  const chunks = []; let total = 0;
  // Runtime keeps stdin open for custody. A single canonical LF frame, not
  // EOF, completes this request; breaking also closes the owned read iterator.
  for await (const chunk of input) {
    total += chunk.length; check(total <= 262144, 'checker_input_limit'); chunks.push(chunk);
    if (chunk.includes(10)) break;
  }
  const frame = Buffer.concat(chunks); check(frame.length > 1 && frame.at(-1) === 10, 'checker_frame');
  const request = parseJson(frame); check(encode(request).toString() + '\n' === frame.toString(), 'checker_frame');
  return encode(checkRequest(request, read)).toString() + '\n';
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {process.stdout.write(await main());} catch {process.exitCode = 1;}
}
