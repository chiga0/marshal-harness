// Typecheck 基线门禁（Phase A，随 2026-09 TS 迁移引入）:tsc 全量运行,与 toolchain/typecheck-baseline.json 的
// 每文件错误数基线比较——任何文件错误数上升或出现基线外新错误文件即失败;只降不升。
// 收紧方式:修复错误后执行 `node scripts/typecheck-gate.ts --update` 重新生成基线并提交。
import {spawnSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const baselinePath = path.join(root, 'toolchain', 'typecheck-baseline.json');
const tsc = path.join(root, 'node_modules', 'typescript', 'bin', 'tsc');

const run = spawnSync(process.execPath, [tsc, '--noEmit'], {cwd: root, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024});
const output = (run.stdout ?? '') + (run.stderr ?? '');
const linePattern = /^(.+?)\(\d+,\d+\): error (TS\d+):/gm;

const counts = new Map();
let match;
while ((match = linePattern.exec(output)) !== null) {
  const file = match[1];
  counts.set(file, (counts.get(file) ?? 0) + 1);
}

const baseline = JSON.parse(fs.readFileSync(baselinePath, 'utf8'));
const update = process.argv.includes('--update');

if (update) {
  const next = Object.fromEntries([...counts.entries()].sort(([a], [b]) => a.localeCompare(b)));
  fs.writeFileSync(baselinePath, JSON.stringify(next, null, 2) + '\n');
  console.log(`baseline updated: ${counts.size} files, ${[...counts.values()].reduce((a, b) => a + b, 0)} errors`);
  process.exit(0);
}

const regressions = [];
let currentTotal = 0;
for (const [file, count] of counts) {
  currentTotal += count;
  const base = baseline[file] ?? 0;
  if (count > base) regressions.push(`${file}: ${base} -> ${count}`);
}
const baselineTotal = Object.values(baseline).reduce((a, b) => a + b, 0);

if (regressions.length > 0) {
  console.error(`typecheck gate failed: ${regressions.length} file(s) regressed (baseline ${baselineTotal}, current ${currentTotal})`);
  for (const line of regressions) console.error('  ' + line);
  console.error('修复错误或确认无回归后以 --update 下调基线;上调基线须在 PR 中说明理由。');
  process.exit(1);
}

console.log(`typecheck gate passed: ${currentTotal}/${baselineTotal} baseline errors, ${counts.size} files` +
  (currentTotal < baselineTotal ? ' (improved — run with --update to ratchet down)' : ''));
