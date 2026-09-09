// Test-only versioned checker. Never registered as a production business policy.
import {readFileSync} from 'node:fs';
function canonical(value) {
  if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
  if (value !== null && typeof value === 'object') return '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}';
  return JSON.stringify(value);
}
let input = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => {
  input += chunk;
  if (input.length > 262144) process.exit(9);
  if (!input.endsWith('\n')) return;
  process.stdin.removeAllListeners('data');
  const request = JSON.parse(input), mode = request.input.mode;
  if (mode === 'hang') { setInterval(() => {}, 1000); return; }
  // This fixture kills only its OWN test guard and exits itself immediately.
  // It demonstrates that observed exit without an original cleanup receipt is
  // not accepted, rather than fabricating a cleanup DTO in a fake runtime.
  if (mode === 'unconfirmed') { process.kill(process.ppid, 'SIGKILL'); process.exit(0); }
  if (mode === 'limit') { process.stdout.write('x'.repeat(300000)); setInterval(() => {}, 1000); return; }
  const data = JSON.parse(readFileSync('candidate.json', 'utf8'));
  const report = {profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'sum', actual: data.values.reduce((sum, n) => sum + n, 0)},
      {name: 'count', actual: data.values.length}]};
  if (mode === 'env' && (process.env.PRIVATE_VERIFIER_TEST || process.env.CHANGED_AFTER_FACTORY)) report.assertions[1].actual = 999;
  if (mode === 'nonce') report.nonce = 'another-execution';
  if (mode === 'binding') report.binding.planDigest = 'sha256:' + '0'.repeat(64);
  if (mode === 'missing') report.assertions.pop();
  if (mode === 'duplicate') report.assertions[1] = report.assertions[0];
  if (mode === 'unknown') report.assertions[1].name = 'passed';
  if (mode === 'body') report.assertions[0].actual = 'process.env.PRIVATE_VERIFIER_TOKEN; passed=true';
  if (mode === 'extra') report.status = 'passed';
  let output = canonical(report) + '\n';
  if (mode === 'bom') output = '\uFEFF' + output;
  if (mode === 'duplicate-key') output = output.replace('"nonce":', '"nonce":"fake","nonce":');
  if (mode === 'trailing') output += '{}\n';
  if (mode === 'utf8') { process.stdout.write(Buffer.from([0xff, 0xfe])); process.exit(0); }
  const finish = () => process.stdout.write(output, () => process.exit(mode === 'nonzero' ? 7 : 0));
  if (mode === 'delay') setTimeout(finish, 400); else finish();
});
