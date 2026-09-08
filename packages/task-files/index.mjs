import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';

export const MAX_FILES = 64, MAX_BYTES = 8 * 1024 * 1024;
const states = new WeakMap(), NOFOLLOW = fs.constants.O_NOFOLLOW;
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const same = (a, b) => a.dev === b.dev && a.ino === b.ino;
const freeze = value => { if (value && typeof value === 'object') { Object.values(value).forEach(freeze); Object.freeze(value); } return value; };
export class TaskFilesError extends Error {
  constructor(code) { super(code); this.name = 'TaskFilesError'; this.code = code; }
}
function check(value, code = 'task_files_unavailable') { if (!value) throw new TaskFilesError(code); }
function relative(value) {
  check(typeof value === 'string' && value.isWellFormed() && Buffer.byteLength(value) <= 1024 &&
    value.normalize('NFC') === value && !/[\\:\x00-\x1f\x7f]/.test(value), 'task_files_invalid_path');
  const parts = value.split('/');
  check(parts.length <= 8 && parts.every(part => part && !part.startsWith('.') && !/[. ]$/.test(part)), 'task_files_invalid_path');
  return value;
}
function pathSet(names) {
  check(Array.isArray(names) && names.length <= MAX_FILES, 'task_files_limit');
  const leaves = new Set(), prefixes = new Set(), aliases = new Map();
  for (const name of names) {
    relative(name); check(!leaves.has(name), 'task_files_duplicate_path'); leaves.add(name);
    const parts = name.split('/');
    for (let index = 1; index <= parts.length; index++) {
      const part = parts.slice(0, index).join('/'), key = part.toLowerCase();
      check(!aliases.has(key) || aliases.get(key) === part, 'task_files_duplicate_path'); aliases.set(key, part);
      if (index < parts.length) prefixes.add(part);
    }
  }
  check([...leaves].every(name => !prefixes.has(name)), 'task_files_path_conflict');
  return {leaves, prefixes};
}
function directory(stat, privateRoot = false) {
  check(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o7022) === 0);
  if (privateRoot) check((stat.mode & 0o777) === 0o700);
}
function file(stat) {
  check(stat.isFile() && stat.nlink === 1 && stat.uid === process.getuid() && (stat.mode & 0o7022) === 0);
  check(stat.size >= 0 && stat.size <= MAX_BYTES, 'task_files_limit');
}
function stable(before, after) {
  return same(before, after) && before.size === after.size && before.mode === after.mode && before.mtimeMs === after.mtimeMs && before.ctimeMs === after.ctimeMs;
}
function checkRoot(state) {
  check(!state.closed && !state.failed, 'task_files_closed');
  for (const [name, fd] of [[state.parent, state.parentFd], [state.cwd, state.rootFd]]) {
    const held = fs.fstatSync(fd); directory(held, true);
    check(fs.realpathSync(name) === name && same(held, fs.lstatSync(name)), 'task_files_identity_changed');
  }
}
function checkDirectories(state, directories) {
  checkRoot(state);
  for (const [name, fd] of directories) {
    const held = fs.fstatSync(fd); directory(held);
    check(same(held, fs.lstatSync(path.join(state.cwd, name))), 'task_files_identity_changed');
  }
}
function readHeld(state, name, fd, expected) {
  checkRoot(state);
  const before = fs.fstatSync(fd); file(before);
  check(same(before, fs.lstatSync(path.join(state.cwd, name))) && (!expected || stable(before, expected)), 'task_files_identity_changed');
  const bytes = Buffer.alloc(before.size); let offset = 0;
  while (offset < bytes.length) {
    const count = fs.readSync(fd, bytes, offset, bytes.length - offset, offset);
    check(count > 0, 'task_files_changed'); offset += count;
  }
  const after = fs.fstatSync(fd); file(after);
  check(stable(before, after) && same(after, fs.lstatSync(path.join(state.cwd, name))), 'task_files_changed');
  checkRoot(state); return {bytes, stat: after};
}
function closeState(state) {
  if (state.closed) return; state.closed = true;
  for (const fd of state.fds.reverse()) fs.closeSync(fd);
}

/** Create only; no directory adoption, execution, credentials or Task truth. */
export function createExecutionDirectory({parent, workerId, inputs = [], depot} = {}) {
  check(typeof parent === 'string' && path.isAbsolute(parent) && path.normalize(parent) === parent &&
    typeof workerId === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(workerId) &&
    depot && typeof depot.get === 'function' && typeof depot.put === 'function', 'task_files_invalid_configuration');
  check(Array.isArray(inputs) && inputs.every(value => value && Object.keys(value).sort().join(',') === 'bytes,digest,path' &&
    typeof value.digest === 'string' && /^sha256:[0-9a-f]{64}$/.test(value.digest) &&
    Number.isSafeInteger(value.bytes) && value.bytes >= 0 && value.bytes <= MAX_BYTES), 'task_files_invalid_input');
  pathSet(inputs.map(value => value.path));
  check(inputs.reduce((sum, value) => sum + value.bytes, 0) <= MAX_BYTES, 'task_files_limit');
  const manifest = inputs.map(({path, digest, bytes}) => ({path, digest, bytes})).sort((a, b) => a.path < b.path ? -1 : 1);
  const state = {parent, cwd: path.join(parent, workerId), depot, fds: [], directories: new Map(), inputs: new Map(),
    inputDigest: hash(Buffer.from(JSON.stringify(manifest))), closed: false, failed: false, collected: false};
  const open = (name, flags, mode) => { const fd = fs.openSync(name, flags | NOFOLLOW, mode); state.fds.push(fd); return fd; };
  try {
    check(fs.realpathSync(parent) === parent, 'task_files_identity_changed');
    state.parentFd = open(parent, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); directory(fs.fstatSync(state.parentFd), true);
    fs.mkdirSync(state.cwd, {mode: 0o700}); // EEXIST is never a reusable reservation.
    state.rootFd = open(state.cwd, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY); checkRoot(state);
    for (const ref of manifest) {
      const bytes = depot.get({digest: ref.digest, bytes: ref.bytes});
      check(bytes instanceof Uint8Array && bytes.length === ref.bytes && hash(bytes) === ref.digest, 'task_files_input_integrity');
      const parts = ref.path.split('/');
      for (let index = 1; index < parts.length; index++) {
        const name = parts.slice(0, index).join('/'); if (state.directories.has(name)) continue;
        fs.mkdirSync(path.join(state.cwd, name), {mode: 0o700});
        state.directories.set(name, open(path.join(state.cwd, name), fs.constants.O_RDONLY | fs.constants.O_DIRECTORY));
      }
      checkDirectories(state, state.directories);
      const fd = open(path.join(state.cwd, ref.path), fs.constants.O_RDWR | fs.constants.O_CREAT | fs.constants.O_EXCL, 0o600);
      fs.writeFileSync(fd, bytes); fs.fchmodSync(fd, 0o400); fs.fsyncSync(fd);
      const stat = fs.fstatSync(fd); file(stat);
      state.inputs.set(ref.path, {ref, fd, stat});
    }
    for (const fd of [...state.directories.values()].reverse()) fs.fsyncSync(fd);
    fs.fsyncSync(state.rootFd); fs.fsyncSync(state.parentFd); checkDirectories(state, state.directories);
    const handle = Object.freeze({cwd: state.cwd, inputDigest: state.inputDigest, close: () => closeState(state)});
    states.set(handle, state); return handle;
  } catch (failure) {
    closeState(state);
    if (failure instanceof TaskFilesError) throw failure;
    throw new TaskFilesError(failure.code === 'EEXIST' ? 'task_files_directory_exists' : 'task_files_unavailable');
  }
}

/** Caller must establish actual Runtime cleanup BEFORE collecting this handle. */
export function collect(handle, {allowedPaths} = {}) {
  const state = states.get(handle); check(state, 'task_files_invalid_handle');
  checkRoot(state); check(!state.collected, 'task_files_already_collected');
  const all = pathSet([...state.inputs.keys(), ...pathSet(allowedPaths).leaves]);
  const outputNames = [...allowedPaths].sort(), directories = new Map(state.directories), heldOutputs = [], output = [];
  const extraFds = []; let entries = 0, total = 0;
  const inspectInputs = () => {
    checkDirectories(state, directories);
    for (const [name, input] of state.inputs) {
      const {bytes} = readHeld(state, name, input.fd, input.stat);
      check(hash(bytes) === input.ref.digest && bytes.length === input.ref.bytes, 'task_files_input_changed');
    }
  };
  try {
    inspectInputs(); const found = new Set();
    function visit(prefix) {
      const iterator = fs.opendirSync(path.join(state.cwd, prefix));
      try {
        for (let entry; (entry = iterator.readSync()) !== null;) {
          check(++entries <= MAX_FILES * 8, 'task_files_limit');
          const name = prefix ? prefix + '/' + entry.name : entry.name; relative(name);
          const target = path.join(state.cwd, name), stat = fs.lstatSync(target);
          if (stat.isDirectory()) {
            check(all.prefixes.has(name), 'task_files_unallowed_output'); directory(stat);
            if (!directories.has(name)) {
              const fd = fs.openSync(target, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | NOFOLLOW);
              extraFds.push(fd); directories.set(name, fd);
              check(same(stat, fs.fstatSync(fd)), 'task_files_identity_changed');
            }
            checkDirectories(state, directories); visit(name);
          } else {
            file(stat); check(all.leaves.has(name) && !found.has(name), 'task_files_unallowed_output');
            found.add(name); check(found.size <= MAX_FILES, 'task_files_limit');
            total += stat.size; check(total <= MAX_BYTES, 'task_files_limit');
          }
        }
      } finally { iterator.closeSync(); }
    }
    visit(''); check(found.size === all.leaves.size, 'task_files_missing_output');
    for (const name of outputNames) {
      checkDirectories(state, directories);
      const fd = fs.openSync(path.join(state.cwd, name), fs.constants.O_RDONLY | NOFOLLOW); extraFds.push(fd);
      const snapshot = readHeld(state, name, fd); heldOutputs.push({name, fd, stat: snapshot.stat});
      output.push({path: name, content: snapshot.bytes});
    }
    inspectInputs();
    const files = output.map(({path, content}) => {
      const ref = state.depot.put(content);
      check(ref?.digest === hash(content) && ref.bytes === content.length, 'task_files_depot_integrity');
      return {path, digest: ref.digest, bytes: ref.bytes};
    });
    inspectInputs();
    for (const held of heldOutputs) {
      const after = fs.fstatSync(held.fd); file(after);
      check(stable(held.stat, after) && same(after, fs.lstatSync(path.join(state.cwd, held.name))), 'task_files_changed');
    }
    const result = {files, inputDigest: state.inputDigest, manifestDigest: hash(Buffer.from(JSON.stringify(files)))};
    state.collected = true; return freeze(result);
  } catch (failure) {
    state.failed = true;
    if (failure instanceof TaskFilesError) throw failure;
    throw new TaskFilesError('task_files_unavailable');
  } finally { for (const fd of extraFds.reverse()) fs.closeSync(fd); }
}
