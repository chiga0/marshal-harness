// External fixed checker: observe real files, preserve the original command
// challenge. Only the installed parent's independent assertions may accept it.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
const canonical = value => Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  value && typeof value === 'object' ? '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}' : JSON.stringify(value);
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  const actual = ['east', 'west'].map(id => JSON.parse(fs.readFileSync(id + '.json', 'utf8')));
  process.stdout.write(canonical({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'both-original-requirements', actual}]}) + '\n', () => process.exit(0));
  break;
}
