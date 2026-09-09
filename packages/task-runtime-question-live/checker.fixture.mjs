// Versioned independent checker; parses data only, never executes author output.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {verifyBusiness} from './scenario.fixture.mjs';
const read = name => {const stat = fs.lstatSync(name); if (!stat.isFile() || stat.nlink !== 1 || stat.size > 16384) throw Error('input_boundary');
  return fs.readFileSync(name, 'utf8');};
try {
  for await (const line of createInterface({input: process.stdin})) {
    if (Buffer.byteLength(line) > 262144) throw Error('input_limit');
    const request = parseJson(Buffer.from(line));
    const actual = verifyBusiness({sales: encode(request.input.sales), source: request.input.source,
      files: ['east.json', 'west.json'].map(path => ({path, content: read(path)})),
      refs: request.input.interactionRefs, verification: request.input.verification, planDigest: request.binding.planDigest});
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'answered-regions', actual}]}).toString() + '\n', () => process.exit(0)); break;
  }
} catch {process.exitCode = 1;}
