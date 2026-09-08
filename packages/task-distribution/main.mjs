import {pack, verify, DistributionError} from './index.mjs';

try {
  const [command, ...argv] = process.argv.slice(2);
  const permitted = command === 'pack' ? ['--source', '--target', '--source-head'] : command === 'verify' ? ['--root', '--manifest-digest'] : [];
  const args = {};
  for (let i = 0; i < argv.length; i += 2) {
    if (!permitted.includes(argv[i]) || !argv[i + 1] || Object.hasOwn(args, argv[i])) throw new DistributionError('invalid_arguments');
    args[argv[i]] = argv[i + 1];
  }
  if (!permitted.length || Object.keys(args).length !== permitted.length) throw new DistributionError('invalid_arguments');
  const result = command === 'pack' ? pack({sourceRoot: args['--source'], target: args['--target'], sourceHead: args['--source-head']})
    : verify({root: args['--root'], manifestDigest: args['--manifest-digest']});
  process.stdout.write(JSON.stringify(result) + '\n');
} catch (error) {
  process.stderr.write(JSON.stringify({code: error instanceof DistributionError ? error.code : 'package_failed'}) + '\n');
  process.exitCode = 1;
}
