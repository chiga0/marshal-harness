// Trusted independent data checker. It never imports/evaluates author code.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode} from '../task-store/store.mjs';
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  const actual = ['east', 'west'].map(region => JSON.parse(fs.readFileSync(region + '.json', 'utf8')));
  process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'regions', actual}]}).toString() + '\n', () => process.exit(0));
  break;
}
