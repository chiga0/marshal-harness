// Test-only independent Node checker: authors write plain data, never code.
import {readFileSync} from 'node:fs';
import {encode} from '../task-store/store.mjs';
let input = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => {
  input += chunk; if (Buffer.byteLength(input) > 262144) process.exit(9);
  if (!input.endsWith('\n')) return;
  process.stdin.removeAllListeners('data');
  const request = JSON.parse(input);
  const code = readFileSync('code.txt', 'utf8'), docs = readFileSync('docs.txt', 'utf8');
  const report = encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'business', actual: code === 'correct'}, {name: 'structure', actual: docs === 'retained'}]});
  process.stdout.write(Buffer.concat([report, Buffer.from('\n')]), () => process.exit(0));
});
