import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';

export const MAX_BYTES = 8 * 1024 * 1024;
const sha = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const same = (a, b) => a.dev === b.dev && a.ino === b.ino;
const stable = (a, b) => same(a, b) && a.size === b.size && a.mode === b.mode && a.mtimeMs === b.mtimeMs && a.ctimeMs === b.ctimeMs;
export class GitBusinessError extends Error { constructor(code) {super(code); this.code = code; this.name = 'GitBusinessError';} }
export function check(ok, code = 'git_business_invalid') {if (!ok) throw new GitBusinessError(code);}
export function gitPath(value) {
  check(typeof value === 'string' && value.isWellFormed() && Buffer.byteLength(value) <= 1024 &&
    value.normalize('NFC') === value && !/[\\:\x00-\x1f\x7f]/.test(value), 'git_business_path');
  const parts = value.split('/');
  check(parts.length <= 8 && parts.every(part => part && !['.', '..', '.git', '.marshal'].includes(part.toLowerCase()) &&
    !/[. ]$/.test(part)), 'git_business_path');
  return value;
}
function directory(name, privateRoot = false) {
  const stat = fs.lstatSync(name);
  check(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7022) === 0 &&
    (!privateRoot || (stat.mode & 0o777) === 0o700) && fs.realpathSync(name) === name, 'git_business_directory');
  return stat;
}
export function privateDirectory(name) {
  try {fs.mkdirSync(name, {mode: 0o700});} catch (error) {if (error.code !== 'EEXIST') throw error;}
  directory(name, true); return name;
}
function bytes(name) {
  const fd = fs.openSync(name, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const before = fs.fstatSync(fd);
    check(before.isFile() && before.nlink === 1 && before.uid === process.getuid() &&
      (before.mode & 0o7022) === 0 && before.size <= MAX_BYTES, 'git_business_file');
    const buffer = Buffer.alloc(before.size + 1); let offset = 0, count;
    while (offset < buffer.length && (count = fs.readSync(fd, buffer, offset, buffer.length - offset, null)) > 0) offset += count;
    const value = buffer.subarray(0, offset), after = fs.fstatSync(fd);
    check(stable(before, after) && same(after, fs.lstatSync(name)) && value.length === before.size, 'git_business_changed');
    return {bytes: value, digest: sha(value), mode: before.mode & 0o111 ? '100755' : '100644'};
  } finally {fs.closeSync(fd);}
}

/** Fixed Git plumbing only. No shell, ambient configuration, hooks, credential
 * helpers, pager, replacement objects, network transport or filter execution.
 * The caller owns this exact ChildProcess until close, including on abort. */
export function runGit(executable, cwd, args, {input, deadline = Date.now() + 10000, signal, index} = {}) {
  check(typeof executable === 'string' && path.isAbsolute(executable) && path.isAbsolute(cwd) &&
    Array.isArray(args) && Number.isSafeInteger(deadline), 'git_business_configuration');
  if (signal?.aborted || deadline <= Date.now()) return Promise.reject(new GitBusinessError('git_business_stopped'));
  return new Promise((resolve, reject) => {
    const env = {PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
      GIT_TERMINAL_PROMPT: '0', GIT_OPTIONAL_LOCKS: '0', GIT_NO_REPLACE_OBJECTS: '1', ...(index ? {GIT_INDEX_FILE: index} : {})};
    const child = spawn(executable, ['--no-pager', '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false',
      '-c', 'core.untrackedCache=false', '-c', 'core.autocrlf=false', '-c', 'core.attributesFile=/dev/null', '-c', 'credential.helper=',
      '-c', 'protocol.allow=never', ...args], {cwd, env, stdio: ['pipe', 'pipe', 'pipe']});
    let failure, size = 0, errors = 0; const chunks = [];
    const stop = code => {failure ??= code; if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');};
    const abort = () => stop('git_business_stopped');
    const timer = setTimeout(() => stop('git_business_git_timeout'), Math.min(10000, deadline - Date.now()));
    signal?.addEventListener('abort', abort, {once: true});
    child.on('error', () => {failure ??= 'git_business_git_failed';});
    child.stdin.on('error', () => stop('git_business_git_failed'));
    child.stdout.on('data', chunk => {size += chunk.length; if (size > MAX_BYTES) stop('git_business_limit'); else chunks.push(chunk);});
    child.stderr.on('data', chunk => {errors += chunk.length; if (errors > 65536) stop('git_business_limit');});
    child.once('close', (code, exitSignal) => {
      clearTimeout(timer); signal?.removeEventListener('abort', abort);
      if (failure || code !== 0 || exitSignal) reject(new GitBusinessError(failure ?? 'git_business_git_failed'));
      else resolve(Buffer.concat(chunks));
    });
    child.stdin.end(input); // Git plumbing consumes a bounded input, unlike ACP.
  });
}
function utf8(value) {try {return new TextDecoder('utf-8', {fatal: true}).decode(value);} catch {throw new GitBusinessError('git_business_encoding');}}

/** New detached locked worktree; never adopts, removes, unlocks or reuses one.
 * Initial scope: <=64 ordinary tracked files, edits to existing files only.
 * Materialization uses raw objects, not checkout filters or repository hooks. */
export async function allocateWorktree({gitExecutable, repository, base, parent, indexParent, workerId, writePaths, deadline, signal}) {
  check(/^[a-f0-9]{40}$/.test(base) && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(workerId), 'git_business_binding');
  const repoStat = directory(repository), common = path.join(repository, '.git');
  const commonStat = directory(common); directory(parent, true); directory(indexParent, true);
  const wait = {deadline, signal}, git = (cwd, args, extra = {}) => runGit(gitExecutable, cwd, args, {...wait, ...extra});
  check(utf8(await git(repository, ['rev-parse', '--show-object-format'])).trim() === 'sha1', 'git_business_object_format');
  check(utf8(await git(repository, ['cat-file', '-t', base])).trim() === 'commit', 'git_business_base');
  const tree = utf8(await git(repository, ['rev-parse', base + '^{tree}'])).trim();
  const raw = utf8(await git(repository, ['ls-tree', '-rz', '--full-tree', base]));
  const rows = raw.split('\0').filter(Boolean).map(line => {
    const match = /^(100644|100755) blob ([a-f0-9]{40})\t(.+)$/u.exec(line);
    check(match, 'git_business_tree_unsupported'); return {mode: match[1], oid: match[2], path: gitPath(match[3])};
  });
  check(rows.length > 0 && rows.length <= 64 && Array.isArray(writePaths) && writePaths.length > 0 && writePaths.length <= 32 &&
    new Set(writePaths).size === writePaths.length && writePaths.every(name => rows.some(row => row.path === gitPath(name))), 'git_business_write_scope');
  const aliases = new Map(); let total = 0;
  for (const row of rows) {
    for (const [i] of row.path.split('/').entries()) {
      const prefix = row.path.split('/').slice(0, i + 1).join('/');
      check(!aliases.has(prefix.toLowerCase()) || aliases.get(prefix.toLowerCase()) === prefix, 'git_business_path');
      aliases.set(prefix.toLowerCase(), prefix);
      check(!rows.some(other => other.path !== row.path && other.path.toLowerCase() === prefix.toLowerCase()), 'git_business_path');
    }
    row.content = await git(repository, ['cat-file', 'blob', row.oid]); total += row.content.length;
    check(total <= MAX_BYTES, 'git_business_limit'); row.digest = sha(row.content);
  }
  const cwd = path.join(parent, workerId), index = path.join(indexParent, workerId);
  fs.mkdirSync(cwd, {mode: 0o700}); // EEXIST fails, including a prior unknown attempt.
  await git(repository, ['worktree', 'add', '--no-checkout', '--detach', '--lock', '--reason', 'marshal-owned-' + workerId, cwd, base]);
  await git(cwd, ['read-tree', base]);
  for (const row of rows) {
    fs.mkdirSync(path.dirname(path.join(cwd, row.path)), {recursive: true, mode: 0o700});
    fs.writeFileSync(path.join(cwd, row.path), row.content, {flag: 'wx', mode: row.mode === '100755' ? 0o755 : 0o644});
    delete row.content;
  }
  const rootStat = directory(cwd, true), gitFile = bytes(path.join(cwd, '.git'));
  const metadata = fs.realpathSync(utf8(await git(cwd, ['rev-parse', '--absolute-git-dir'])).trim());
  check(metadata.startsWith(common + path.sep + 'worktrees' + path.sep), 'git_business_binding');
  const metadataStat = directory(metadata), lock = bytes(path.join(metadata, 'locked'));
  const rootFd = fs.openSync(cwd, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
  if (!same(rootStat, fs.fstatSync(rootFd))) {fs.closeSync(rootFd); throw new GitBusinessError('git_business_identity_changed');}
  let closed = false;
  function assertIdentity() {
    check(!closed && same(rootStat, fs.fstatSync(rootFd)) && same(rootStat, directory(cwd, true)) &&
      same(repoStat, directory(repository)) && same(commonStat, directory(common)) && same(metadataStat, directory(metadata)) &&
      bytes(path.join(cwd, '.git')).digest === gitFile.digest && bytes(path.join(metadata, 'locked')).digest === lock.digest,
    'git_business_identity_changed');
  }
  function snapshot() {
    assertIdentity(); const found = new Map(); let size = 0, entries = 0;
    function visit(relative = '') {
      const iterator = fs.opendirSync(path.join(cwd, relative));
      try {for (let entry; (entry = iterator.readSync()) !== null;) {
        check(++entries <= 512, 'git_business_limit');
        if (relative === '' && entry.name === '.git') continue;
        const name = gitPath(relative ? relative + '/' + entry.name : entry.name);
        if (entry.isDirectory()) {
          check(rows.some(row => row.path.startsWith(name + '/')), 'git_business_unallowed_change');
          directory(path.join(cwd, name)); visit(name);
        } else {
          check(rows.some(row => row.path === name) && found.size < 64, 'git_business_unallowed_change');
          const value = bytes(path.join(cwd, name)); size += value.bytes.length; check(size <= MAX_BYTES, 'git_business_limit'); found.set(name, value);
        }
      }} finally {iterator.closeSync();}
    }
    visit(); check(found.size === rows.length, 'git_business_unallowed_change');
    for (const row of rows) {
      const value = found.get(row.path); check(value.mode === row.mode, 'git_business_unallowed_change');
      check(value.digest === row.digest || writePaths.includes(row.path), 'git_business_unallowed_change');
    }
    assertIdentity(); return found;
  }
  async function collectPatch(context) {
    const currentWait = {deadline: context.deadline, signal: context.signal}; assertIdentity();
    check(utf8(await runGit(gitExecutable, cwd, ['rev-parse', 'HEAD'], currentWait)).trim() === base, 'git_business_base_changed');
    const before = snapshot();
    const changed = rows.filter(row => before.get(row.path).digest !== row.digest);
    check(changed.length > 0, 'git_business_no_change_unsupported');
    check(!fs.existsSync(index), 'git_business_index_exists');
    await runGit(gitExecutable, cwd, ['read-tree', base], {...currentWait, index});
    for (const row of changed) {
      const oid = utf8(await runGit(gitExecutable, cwd, ['hash-object', '-w', '--no-filters', '--stdin'], {...currentWait, input: before.get(row.path).bytes})).trim();
      check(/^[a-f0-9]{40}$/.test(oid), 'git_business_git_failed');
      await runGit(gitExecutable, cwd, ['update-index', '--add', '--cacheinfo', row.mode, oid, row.path], {...currentWait, index});
    }
    const patch = await runGit(gitExecutable, cwd, ['diff', '--cached', '--binary', '--full-index', '--no-renames', '--no-ext-diff', '--no-textconv', base, '--'], {...currentWait, index});
    const after = snapshot();
    check(patch.length > 0 && [...before].every(([name, value]) => after.get(name).digest === value.digest), 'git_business_changed');
    check(utf8(await runGit(gitExecutable, cwd, ['rev-parse', 'HEAD'], currentWait)).trim() === base, 'git_business_base_changed');
    assertIdentity();
    return {patch, baseTree: tree, files: changed.map(row => ({path: row.path, before: row.digest, after: after.get(row.path).digest}))};
  }
  return Object.freeze({cwd, base, baseTree: tree, collectPatch, close() {if (!closed) {closed = true; fs.closeSync(rootFd);}}});
}
