// Operator-side adapter. Its import target is the explicitly trusted matching
// source checkout, NEVER a downloaded candidate. No Core implementation here.
import {pathToFileURL} from 'node:url';
import path from 'node:path';

try {
  const [command, source, extra] = process.argv.slice(2);
  if (command !== 'inventory' || extra !== undefined || !path.isAbsolute(source ?? '') || path.resolve(source) !== source ||
      !/^\d+\.\d+\.\d+$/.test(process.versions.node) || Number(process.versions.node.split('.')[0]) < 22) throw Error();
  const {SOURCE_FILES, NODE_VERSION} = await import(pathToFileURL(path.join(source, 'packages/task-distribution/index.mjs')).href);
  process.stdout.write(JSON.stringify({files: SOURCE_FILES, node: NODE_VERSION}) + '\n');
} catch {
  process.stderr.write('{"code":"candidate_inventory_unavailable"}\n');
  process.exitCode = 1;
}
