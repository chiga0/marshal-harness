// 审计锚定 CLI(node scripts/audit-anchor.ts export|verify):
//   export <root>                 —— 输出当前根的锚(load-only,JSON 落屏)
//   verify <root> <anchor.json>   —— 复算与锚逐流比对,逐条列出偏差;不一致进程退出码 1
// 用法纪律:
// - 导出与核证都不看 reports/语义,只按链头摘要字节比对;
// - 锚是时间点证据,不是当前签出;不匹配本身即"自锚定后发生了变化"的信号。
import fs from 'node:fs';
import path from 'node:path';
import {exportAnchor, verifyAnchor} from '../packages/task-store/anchor.ts';

function usage(exitCode) {
  console.error('usage: node scripts/audit-anchor.ts export <root>');
  console.error('       node scripts/audit-anchor.ts verify <root> <anchor.json>');
  process.exit(exitCode);
}

const [mode, root, anchorPath] = process.argv.slice(2);
if (!mode || !root || (mode === 'verify' && !anchorPath)) usage(2);

try {
  const realRoot = fs.realpathSync(root);
  if (mode === 'export') {
    process.stdout.write(JSON.stringify(await exportAnchor(realRoot), null, 2) + '\n');
  } else if (mode === 'verify') {
    const anchor = JSON.parse(fs.readFileSync(path.resolve(anchorPath), 'utf8'));
    const verdict = await verifyAnchor(realRoot, anchor);
    if (verdict.ok) {
      console.log(`OK: 根 ${realRoot} 与锚 tenantScope=${verdict.tenantScope} (heads=${verdict.heads}) 一致`);
    } else {
      console.log(`DIVERGED: ${verdict.mismatches.length} 处偏差`);
      for (const item of verdict.mismatches) console.log(` - ${item.stream}: ${item.code}` + (item.anchor ? ` (anchor ${item.anchor} -> current ${item.current ?? 'missing'})` : ''));
      process.exit(1);
    }
  } else usage(2);
} catch (error) {
  console.error('审计锚定失败(不掩饰,不推断): ' + (error?.message ?? error));
  process.exit(1);
}
