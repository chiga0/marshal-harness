// Trusted data-only checker. Never import or evaluate any author-produced code.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {rowsFrom, range} from './policy.mjs';
const fail = () => {throw Error('window_check_failed');};
try {
  for await (const line of createInterface({input: process.stdin})) {
    if (Buffer.byteLength(line) > 262144) fail();
    const request = parseJson(Buffer.from(line)), {startDate, endDate, sourceDigest} = request.input;
    range({startDate, endDate});
    const original = Buffer.from(request.input.sourceBase64, 'base64');
    if (original.toString('base64') !== request.input.sourceBase64 || digest(original) !== sourceDigest) fail();
    const rows = rowsFrom(original);
    const actual = ['east', 'west'].map(region => {
      const fd = fs.openSync(region + '.json', fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
      try {const stat = fs.fstatSync(fd); if (!stat.isFile() || stat.nlink !== 1 || stat.size < 1 || stat.size > 4096) fail();
        return parseJson(fs.readFileSync(fd));} finally {fs.closeSync(fd);}
    });
    // This process recomputes from original Depot bytes in the bound frame,
    // never from a source file supplied/rewritten by an author. Parent validators
    // independently recheck actual against the final ticket as well.
    const expected = ['east', 'west'].map(region => {
      let count = 0, sum = 0n;
      for (const row of rows) if (row.region === region && row.status === 'paid' && row.date >= startDate && row.date <= endDate) {count++; sum += BigInt(row.cents);}
      return {region, startDate, endDate, count, netCents: Number(sum)};
    });
    if (!encode(actual).equals(encode(expected))) fail();
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'input-window', actual: {startDate, endDate, sourceDigest}}, {name: 'regions', actual}]}).toString() + '\n', () => process.exit(0));
    break;
  }
} catch {process.exit(1);}
