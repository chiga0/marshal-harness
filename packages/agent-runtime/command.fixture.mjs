import {createInterface} from 'node:readline';
import {spawn} from 'node:child_process';

// Checked-in test fixture. No generated executable, shell or ambient config.
const mode = process.argv[2];
if (mode === 'hang') {
  process.on('SIGTERM', () => {}); setInterval(() => {}, 1000);
} else if (mode === 'output-limit') {
  process.stdout.write('x'.repeat(8192)); setInterval(() => {}, 1000);
} else if (mode === 'stderr-limit') {
  process.stderr.write('PRIVATE_COMMAND_FIXTURE'.repeat(1000)); setInterval(() => {}, 1000);
} else if (mode === 'descendant') {
  const child = spawn(process.execPath, [new URL(import.meta.url).pathname, 'hang'], {stdio: 'ignore', env: {}});
  child.once('spawn', () => process.stdout.write(JSON.stringify({pid: child.pid}) + '\n', () => process.exit(7)));
} else {
  const lines = createInterface({input: process.stdin});
  lines.once('line', line => {
    const request = JSON.parse(line);
    const result = {nonce: request.nonce, sum: request.values.reduce((a, b) => a + b, 0),
      leaked: Object.hasOwn(process.env, 'PRIVATE_COMMAND_ENV')};
    process.stdout.write(JSON.stringify(result) + '\n', () => process.exit(0));
  });
}
