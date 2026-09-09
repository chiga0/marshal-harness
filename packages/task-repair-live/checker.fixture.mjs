// Independent command reads data, never imports or executes author output.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
try {
  for await (const line of createInterface({input: process.stdin})) {
    if (Buffer.byteLength(line) > 262144) throw Error('input_limit');
    const request = parseJson(Buffer.from(line)), {sales, source} = request.input;
    if (source?.digest !== digest(encode(sales)) || source.bytes !== encode(sales).length) throw Error('source_mismatch');
    const reports = ['east', 'west'].map(region => {
      const name = region + '.json', stat = fs.lstatSync(name);
      if (!stat.isFile() || stat.nlink !== 1 || stat.size < 1 || stat.size > 4096) throw Error('candidate_boundary');
      return parseJson(fs.readFileSync(name));
    });
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'east-content', actual: reports[0]}, {name: 'west-content', actual: reports[1]},
        {name: 'report-structure', actual: reports}]}).toString() + '\n', () => process.exit(0));
    break;
  }
} catch {process.exitCode = 1;}
