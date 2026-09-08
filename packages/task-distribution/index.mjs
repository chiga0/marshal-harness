import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';

export const NODE_VERSION = '24.15.0';
export const SOURCE_FILES = Object.freeze([
  'packages/agent-acp/client.mjs',
  'packages/agent-pi-rpc/client.mjs',
  'packages/agent-provider-acp/index.mjs',
  'packages/agent-provider-pi/bridge-contract.mjs',
  'packages/agent-provider-pi/index.mjs',
  'packages/agent-provider-pi/native-bridge.mjs',
  'packages/agent-provider-pi/shell-operations.mjs',
  'packages/agent-runtime/custody-contract.mjs',
  'packages/agent-runtime/custody-files.mjs',
  'packages/agent-runtime/custody-process.mjs',
  'packages/agent-runtime/custody.mjs',
  'packages/agent-runtime/guard.mjs',
  'packages/agent-runtime/index.mjs',
  'packages/agent-runtime/protocol.mjs',
  'packages/task-api/contract.mjs',
  'packages/task-api/http-boundary.mjs',
  'packages/task-api/http-handler.mjs',
  'packages/task-api/openapi.json',
  'packages/task-application/application.mjs',
  'packages/task-application/artifacts.mjs',
  'packages/task-application/clarification.mjs',
  'packages/task-application/cleanup.mjs',
  'packages/task-application/execution.mjs',
  'packages/task-application/graph.mjs',
  'packages/task-application/model.mjs',
  'packages/task-application/verification.mjs',
  'packages/task-artifacts/depot.mjs',
  'packages/task-business/index.mjs',
  'packages/task-client/index.mjs',
  'packages/task-files/index.mjs',
  'packages/task-regional-window/checker.mjs',
  'packages/task-regional-window/consumer.mjs',
  'packages/task-regional-window/driver.mjs',
  'packages/task-regional-window/index.mjs',
  'packages/task-regional-window/policy.mjs',
  'packages/task-regional-window/service-config.mjs',
  'packages/task-service/composition.mjs',
  'packages/task-service/main.mjs',
  'packages/task-store/store.mjs',
  'packages/task-supervisor/controller.mjs',
  'packages/task-verification-command/index.mjs',
]);
const ENTRYPOINT = 'packages/task-service/main.mjs';
const MANIFEST = 'manifest.json';
const MAX_FILE = 2 * 1024 * 1024;
const MAX_TOTAL = 16 * 1024 * 1024;
const hash = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
const encode = value => Buffer.from(JSON.stringify(value, null, 2) + '\n');
const fail = code => { throw new DistributionError(code); };
export class DistributionError extends Error {
  constructor(code) { super(code); this.name = 'DistributionError'; this.code = code; }
}
function canonicalDirectory(root) {
  if (typeof root !== 'string' || !path.isAbsolute(root) || path.resolve(root) !== root) fail('invalid_directory');
  let current = path.parse(root).root;
  for (const segment of root.slice(current.length).split('/').filter(Boolean)) {
    current = path.join(current, segment);
    if (!fs.lstatSync(current).isDirectory()) fail('unsafe_directory');
  }
  if (fs.realpathSync(root) !== root) fail('unsafe_directory');
}
function read(root, relative, limit = MAX_FILE) {
  if (!/^[a-zA-Z0-9_.-]+(?:\/[a-zA-Z0-9_.-]+)*$/.test(relative) || relative.split('/').some(s => s === '.' || s === '..')) fail('unsafe_path');
  canonicalDirectory(path.dirname(path.join(root, relative)));
  const fd = fs.openSync(path.join(root, relative), fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const before = fs.fstatSync(fd);
    if (!before.isFile() || before.nlink !== 1 || before.size > limit) fail('unsafe_file');
    const buffer = Buffer.alloc(before.size + 1);
    let length = 0;
    while (length < buffer.length) {
      const count = fs.readSync(fd, buffer, length, buffer.length - length, null);
      if (count === 0) break;
      length += count;
    }
    const bytes = buffer.subarray(0, length);
    const after = fs.fstatSync(fd);
    if (bytes.length !== before.size || bytes.length > limit || before.size !== after.size || before.mtimeMs !== after.mtimeMs || before.ctimeMs !== after.ctimeMs) fail('source_drift');
    return bytes;
  } finally { fs.closeSync(fd); }
}
function git(root, args, limit = MAX_TOTAL) {
  return execFileSync('git', ['-C', root, ...args], {encoding: null, maxBuffer: limit, timeout: 10000, stdio: ['ignore', 'pipe', 'pipe']});
}
function runtimePath(file) {
  // These are non-runtime development inputs, not files to publish.
  return !file.startsWith('packages/task-distribution/') && !file.includes('/fixtures/') &&
    !file.endsWith('/README.md') && !/\.(test|fixture)\.mjs$/.test(file);
}
function inventory(root, sourceHead) {
  const names = git(root, ['ls-tree', '-r', '--name-only', '-z', sourceHead, '--', 'packages']).toString('utf8').split('\0').filter(Boolean).filter(runtimePath).sort();
  if (JSON.stringify(names) !== JSON.stringify(SOURCE_FILES)) fail('source_inventory_changed');
}
function manifestFor(sourceHead, files) {
  return {format: 'marshal-node-script-package/v1', sourceHead, node: NODE_VERSION,
    platforms: ['darwin-arm64', 'linux-x64'], entrypoint: ENTRYPOINT, files};
}
function writeNew(file, bytes) {
  const fd = fs.openSync(file, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, bytes); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
function syncDirectory(root) {
  const fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
  try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
function wrap(work) {
  try { return work(); } catch (error) {
    if (error instanceof DistributionError) throw error;
    // Never propagate Git stderr, file contents, configuration, or arbitrary paths.
    throw new DistributionError('package_io_failed');
  }
}

/** New private directory only. Failed targets remain evidence, never automatically removed. */
export function pack({sourceRoot, target, sourceHead}) {
  return wrap(() => {
    if (!/^[a-f0-9]{40}$/.test(sourceHead ?? '')) fail('invalid_source_head');
    canonicalDirectory(sourceRoot);
    if (typeof target !== 'string' || !path.isAbsolute(target) || path.resolve(target) !== target) fail('invalid_target');
    canonicalDirectory(path.dirname(target));
    if (target === sourceRoot || target.startsWith(sourceRoot + '/')) fail('target_inside_source');
    if (git(sourceRoot, ['rev-parse', `${sourceHead}^{commit}`]).toString().trim() !== sourceHead) fail('invalid_source_head');
    inventory(sourceRoot, sourceHead);
    let total = 0;
    const contents = SOURCE_FILES.map(file => {
      const bytes = read(sourceRoot, file);
      if (!bytes.equals(git(sourceRoot, ['show', `${sourceHead}:${file}`], MAX_FILE))) fail('source_drift');
      total += bytes.length;
      if (total > MAX_TOTAL) fail('package_too_large');
      return {path: file, bytes};
    });
    // This mkdir is the exclusive claim; no existing directory is reused or overwritten.
    fs.mkdirSync(target, {mode: 0o700});
    const directories = new Set([target]);
    for (const file of contents) {
      let current = target;
      for (const component of file.path.split('/').slice(0, -1)) {
        current = path.join(current, component);
        if (!directories.has(current)) { fs.mkdirSync(current, {mode: 0o700}); directories.add(current); }
      }
      writeNew(path.join(target, file.path), file.bytes);
    }
    const manifest = manifestFor(sourceHead, contents.map(file => ({path: file.path, digest: hash(file.bytes), bytes: file.bytes.length})));
    const bytes = encode(manifest);
    writeNew(path.join(target, MANIFEST), bytes);
    for (const directory of [...directories].reverse()) syncDirectory(directory);
    syncDirectory(path.dirname(target));
    const manifestDigest = hash(bytes);
    verify({root: target, manifestDigest});
    return Object.freeze({sourceHead, manifestDigest, files: manifest.files.length, bytes: total});
  });
}

/** manifestDigest must come from trusted release evidence, never from this directory. */
export function verify({root, manifestDigest}) {
  return wrap(() => {
    if (!/^sha256:[a-f0-9]{64}$/.test(manifestDigest ?? '')) fail('invalid_manifest_digest');
    canonicalDirectory(root);
    const bytes = read(root, MANIFEST, 65536);
    if (hash(bytes) !== manifestDigest) fail('manifest_digest_mismatch');
    let manifest;
    try { manifest = JSON.parse(bytes.toString('utf8')); } catch { fail('invalid_manifest'); }
    if (!manifest || !/^[a-f0-9]{40}$/.test(manifest.sourceHead ?? '') || !Array.isArray(manifest.files) || manifest.files.length !== SOURCE_FILES.length) fail('invalid_manifest');
    for (let i = 0; i < SOURCE_FILES.length; i++) {
      const file = manifest.files[i];
      if (!file || file.path !== SOURCE_FILES[i] || !/^sha256:[a-f0-9]{64}$/.test(file.digest ?? '') || !Number.isSafeInteger(file.bytes) || file.bytes < 0 || file.bytes > MAX_FILE || Object.keys(file).join(',') !== 'path,digest,bytes') fail('invalid_manifest');
    }
    if (!encode(manifestFor(manifest.sourceHead, manifest.files)).equals(bytes)) fail('invalid_manifest');
    const wantedFiles = new Set([MANIFEST, ...SOURCE_FILES]);
    const wantedDirs = new Set(SOURCE_FILES.flatMap(file => {
      const parts = file.split('/'); return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join('/'));
    }));
    function walk(directory, prefix = '') {
      if ((fs.lstatSync(directory).mode & 0o777) !== 0o700) fail('unsafe_permissions');
      for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
        const relative = prefix + entry.name;
        if (entry.isDirectory() && wantedDirs.delete(relative)) walk(path.join(directory, entry.name), relative + '/');
        else if (!entry.isFile() || !wantedFiles.delete(relative)) fail('unexpected_entry');
        else if ((fs.lstatSync(path.join(directory, entry.name)).mode & 0o777) !== 0o600) fail('unsafe_permissions');
      }
    }
    walk(root);
    if (wantedFiles.size || wantedDirs.size) fail('missing_entry');
    let total = 0;
    for (const file of manifest.files) {
      const content = read(root, file.path);
      if (content.length !== file.bytes || hash(content) !== file.digest) fail('file_digest_mismatch');
      total += content.length;
      if (total > MAX_TOTAL) fail('package_too_large');
    }
    return Object.freeze({sourceHead: manifest.sourceHead, manifestDigest, node: NODE_VERSION, entrypoint: ENTRYPOINT, files: manifest.files.length, bytes: total});
  });
}
