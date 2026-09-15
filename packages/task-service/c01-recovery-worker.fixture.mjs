// Test-only subprocess used to create a genuine process crash boundary.
import fs from 'node:fs';
import process from 'node:process';
import {runImport, C01Crash, C01RecoveryError} from './c01-recovery.mjs';

const arg = name => { const at = process.argv.indexOf(name); return at >= 0 ? process.argv[at + 1] : null; };
const root = arg('--root'), sourcePath = arg('--source'), crashAfter = Number(arg('--crash-after') ?? '-1'), skipMode = arg('--skip-mode') ?? 'complete-and-consistent';
if (!root || !sourcePath || !Number.isSafeInteger(crashAfter)) process.exit(64);
try {
  const source = JSON.parse(fs.readFileSync(sourcePath, 'utf8'));
  const result = runImport({root, source, crashAfter, skipMode});
  process.stdout.write(JSON.stringify(result) + '\n'); process.exitCode = 0;
} catch (error) {
  if (error instanceof C01Crash) { process.stdout.write(JSON.stringify({status: 'crashed', step: error.step}) + '\n'); process.exitCode = 42; }
  else if (error instanceof C01RecoveryError) { process.stdout.write(JSON.stringify({status: 'error', code: error.code}) + '\n'); process.exitCode = 2; }
  else { process.stdout.write(JSON.stringify({status: 'error', code: 'unexpected_fixture_failure'}) + '\n'); process.exitCode = 1; }
}
