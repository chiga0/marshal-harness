// 覆盖率门禁（随 2026-09 TS 迁移引入）:NODE_V8_COVERAGE 原始目录 → 剔损(SIGKILL 子进程截断)→
// 合并(同字节区间计数相加)→ 行覆盖率(covered 行/非空行)→ 与 toolchain/coverage-baseline.json
// 比较,只能升不能降。noh 内置 --experimental-test-coverage 在含 SIGKILL fixture 的套件下
// 会因截断 JSON 中止全部报告,故解耦为"原始采集 + 受控合并"。
// 用法:NODE_V8_COVERAGE=.cov-raw node --test … && node scripts/coverage-gate.ts [--update]
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const rawDir = path.join(root, '.cov-raw');
const baselinePath = path.join(root, 'toolchain', 'coverage-baseline.json');
const INCLUDE_PREFIXES = ['packages/', 'experiments/', 'scripts/'];
const includePath = rel => INCLUDE_PREFIXES.some(prefix => rel.startsWith(prefix));

function loadRanges(dir) {
  const names = fs.readdirSync(dir).filter(name => name.endsWith('.json'));
  if (names.length === 0) throw new Error(`no coverage files in ${dir}`);
  const hits = new Map();
  let corrupt = 0;
  for (const name of names) {
    let data;
    try { data = JSON.parse(fs.readFileSync(path.join(dir, name), 'utf8')); }
    catch { corrupt++; continue; }
    for (const script of data.result ?? []) {
      const rel = script.url?.startsWith('file://') ? path.relative(root, fileURLToPath(script.url)) : null;
      if (rel === null || rel.startsWith('..') || !includePath(rel)) continue;
      if (!script.url.endsWith('.ts')) continue;
      let ranges = [];
      for (const fn of script.functions ?? []) {
        const rs = (fn.ranges ?? []).filter(range => range.count > 0);
        // 只保留叶子区间,避免控制流区间重复计数父区间造成的透支
        for (const leaf of rs.filter(a => !rs.some(b => a !== b && b.startOffset >= a.startOffset && b.endOffset <= a.endOffset)))
          ranges.push([leaf.startOffset, leaf.endOffset]);
      }
      if (ranges.length === 0) continue;
      const list = hits.get(rel) ?? [];
      hits.set(rel, list.concat(ranges));
    }
  }
  return {hits, corrupt, files: names.length};
}

function lineCoverage(source, ranges) {
  const starts = [0];
  for (let i = 0; i < source.length; i++) if (source[i] === '\n') starts.push(i + 1);
  let covered = 0, total = 0;
  for (let line = 0; line < starts.length; line++) {
    const begin = starts[line], end = line + 1 < starts.length ? starts[line + 1] : source.length;
    if (source.slice(begin, end).trim() === '') continue;
    total++;
    if (ranges.some(([s, e]) => s < end && e > begin)) covered++;
  }
  return {covered, total};
}

const {hits, corrupt, files} = loadRanges(rawDir);
let covered = 0, total = 0;
const perFile = [];
for (const [rel, ranges] of [...hits.entries()].sort()) {
  const text = fs.readFileSync(path.join(root, rel), 'utf8');
  const result = lineCoverage(text, ranges);
  covered += result.covered; total += result.total;
  perFile.push({file: rel, ...result});
}
if (total === 0) throw new Error('no included source lines observed');
const lines = Math.round((covered / total) * 10000) / 100;

if (process.argv.includes('--update')) {
  fs.writeFileSync(baselinePath, JSON.stringify({lines}, null, 2) + '\n');
  console.log(`coverage baseline updated: ${lines}% lines (${covered}/${total}, files=${perFile.length}, corruptSkipped=${corrupt}/${files})`);
  process.exit(0);
}

const baseline = JSON.parse(fs.readFileSync(baselinePath, 'utf8'));
console.log(`coverage: ${lines}% lines (baseline ${baseline.lines}%, files=${perFile.length}, corruptSkipped=${corrupt}/${files})`);
if (lines < baseline.lines) {
  console.error('coverage gate failed: 覆盖率低于基线;确认无回归后以 --update 更新,下调须在 PR 中说明理由。');
  process.exit(1);
}
console.log('coverage gate passed' + (lines > baseline.lines ? ' (improved — run with --update to ratchet up)' : ''));
