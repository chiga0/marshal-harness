// Independent fixed checker observes original files; no author/verifier boolean.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode} from '../task-store/store.mjs';
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  const actual = ['east', 'west'].map(id => JSON.parse(fs.readFileSync(id + '.json', 'utf8')));
  process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'both-original-requirements', actual}]}).toString() + '\n', () => process.exit(0));
  break;
}
