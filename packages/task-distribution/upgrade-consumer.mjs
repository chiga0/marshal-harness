// 显式双包消费者；从不回退打包当前源码，不进入默认自动test glob。
import {runUpgrade} from './upgrade-consumer.fixture.mjs';
const names = ['old-package', 'old-source', 'old-manifest', 'new-package', 'new-source', 'new-manifest', 'run-dir', 'asset-kind'];
const options = {};
try {
  const args = process.argv.slice(2);
  for (let i = 0; i < args.length; i += 2) {
    const key = args[i]?.slice(2), value = args[i + 1];
    if (!args[i]?.startsWith('--') || !names.includes(key) || Object.hasOwn(options, key) || !value || value.startsWith('--')) throw Error();
    options[key] = value;
  }
  if (Object.keys(options).length !== names.length) throw Error();
  const result = await runUpgrade({
    oldPackage: {root: options['old-package'], sourceHead: options['old-source'], manifestDigest: options['old-manifest']},
    newPackage: {root: options['new-package'], sourceHead: options['new-source'], manifestDigest: options['new-manifest']},
    runDir: options['run-dir'], assetKind: options['asset-kind'],
  });
  process.stdout.write(JSON.stringify(result) + '\n');
} catch {
  process.stderr.write('双安装包同根升级/回滚验收失败；已创建的私有证据目录保留，未自动迁移或重试。\n');
  process.exitCode = 1;
}
