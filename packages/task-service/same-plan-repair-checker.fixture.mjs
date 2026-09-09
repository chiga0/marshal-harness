// Independent fixed data checker: never imports or executes author code. Read
// one bounded LF frame; the managed command intentionally keeps stdin open.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
for await (const line of createInterface({input: process.stdin})) {
  if (Buffer.byteLength(line) > 262144) throw Error('frame_limit');
  const request = JSON.parse(line), {sales, source} = request.input;
  if (source?.digest !== digest(encode(sales)) || source.bytes !== encode(sales).length || !Array.isArray(sales.rows)) throw Error('input_mismatch');
  const assertions = ['east', 'west'].map(region => {
    const name = region + '.json', stat = fs.lstatSync(name);
    if (!stat.isFile() || stat.nlink !== 1 || stat.size > 4096) throw Error('candidate_boundary');
    const actual = JSON.parse(fs.readFileSync(name, 'utf8'));
    const selected = sales.rows.filter(row => row.region === region && row.status === 'paid');
    return {name: region + '-content', actual: {actual, expected: {region, count: selected.length,
      netCents: selected.reduce((sum, row) => sum + row.cents, 0)}}};
  });
  assertions.push({name: 'report-structure', actual: {complete: true}});
  const frame = {profile: request.profile, nonce: request.nonce, binding: request.binding, assertions};
  if (process.env.MARSHAL_REPAIR_CHECKER_MODE === 'bad-frame') frame.nonce = 'foreign-nonce';
  process.stdout.write(encode(frame).toString() + '\n', () => process.exit(0));
  break;
}
