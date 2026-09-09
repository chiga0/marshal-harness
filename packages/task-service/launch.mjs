// Local CLI path selection only. ServiceRoot/Store remain the sole format,
// ownership, recovery and token authorities; no fallback or state migration.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export class LaunchError extends Error {
  constructor(code) {super(code); this.code = code;}
}
const requireValue = (value, code = 'invalid_launch_configuration') => {if (!value) throw new LaunchError(code);};
const absolute = value => typeof value === 'string' && value.isWellFormed() && !/[\x00-\x1f\x7f]/.test(value) &&
  Buffer.byteLength(value) <= 4096 && path.isAbsolute(value) && path.normalize(value) === value && value !== path.parse(value).root;
const same = (a, b) => a.dev === b.dev && a.ino === b.ino;

export function parseLaunchArguments(argv, {home = os.homedir()} = {}) {
  requireValue(Array.isArray(argv));
  if (argv.length === 1 && argv[0] === '--help') return {help: true};
  const args = {};
  for (let at = 0; at < argv.length; at += 2) {
    const name = argv[at], value = argv[at + 1];
    requireValue(['--root', '--data-dir', '--mode', '--config', '--port'].includes(name) &&
      typeof value === 'string' && value.length > 0 && !Object.hasOwn(args, name));
    args[name] = value;
  }
  requireValue(!Object.hasOwn(args, '--root') || !Object.hasOwn(args, '--data-dir'));
  // Config is still explicit trusted deployment code, never a guessed Provider.
  requireValue(typeof args['--config'] === 'string' && path.isAbsolute(args['--config']) && !args['--config'].includes('\0'));
  const mode = args['--mode'] ?? 'auto';
  requireValue(['auto', 'create', 'open'].includes(mode));
  requireValue(args['--port'] === undefined || /^(0|[1-9][0-9]{0,4})$/.test(args['--port']) && Number(args['--port']) <= 65535);
  const useDefault = args['--root'] === undefined && args['--data-dir'] === undefined;
  if (useDefault) requireValue(absolute(home), 'default_home_unavailable');
  const root = args['--root'] ?? args['--data-dir'] ?? path.join(home, '.marshal-node', 'task-service');
  requireValue(absolute(root));
  return {root, mode, config: args['--config'], port: Number(args['--port'] ?? 0), homeAnchor: useDefault ? home : null};
}

/** Retain and sync the named graph until composition owns its original FDs.
 * Existing directories are never chmod'ed, removed, reset or silently adopted
 * after a racing mkdir. A failed fsync leaves evidence and returns no target.
 * This is ordinary-user path hygiene, not a hostile same-UID sandbox. */
export function prepareLaunch(options) {
  requireValue(options && absolute(options.root) && ['auto', 'create', 'open'].includes(options.mode));
  const uid = process.getuid(), held = []; let closed = false;
  const close = () => {
    if (closed) return; closed = true;
    for (const entry of held.splice(0).reverse()) fs.closeSync(entry.fd);
  };
  const inspect = name => {
    try {return fs.lstatSync(name);} catch (error) {if (error.code === 'ENOENT') return null; throw error;}
  };
  const valid = (stat, privateDirectory) => stat.isDirectory() && stat.uid === uid &&
    (privateDirectory ? (stat.mode & 0o7777) === 0o700 : (stat.mode & 0o7022) === 0);
  const check = () => {
    requireValue(!closed, 'launch_target_closed');
    for (const entry of held) {
      const actual = fs.fstatSync(entry.fd), named = inspect(entry.name);
      requireValue(named && valid(actual, entry.privateDirectory) && valid(named, entry.privateDirectory) &&
        same(actual, entry.stat) && same(actual, named) && fs.realpathSync(entry.name) === entry.name, 'unsafe_data_parent');
    }
  };
  const hold = (name, privateDirectory = true) => {
    const named = inspect(name);
    requireValue(named && valid(named, privateDirectory) && fs.realpathSync(name) === name, 'unsafe_data_parent');
    const fd = fs.openSync(name, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW);
    held.push({name, fd, privateDirectory, stat: named}); check();
  };
  try {
    const existing = inspect(options.root);
    if (existing) requireValue(valid(existing, true), 'unsafe_data_root');
    requireValue(options.mode !== 'create' || !existing, 'data_root_exists');
    requireValue(options.mode !== 'open' || existing, 'data_root_missing');
    const mode = options.mode === 'auto' ? existing ? 'open' : 'create' : options.mode;
    const parent = path.dirname(options.root), missing = []; let anchor = parent;
    if (options.homeAnchor !== null && options.homeAnchor !== undefined) {
      requireValue(absolute(options.homeAnchor) && options.root === path.join(options.homeAnchor, '.marshal-node', 'task-service'));
      hold(options.homeAnchor, false); anchor = options.homeAnchor;
      const appParent = path.join(anchor, '.marshal-node');
      if (!inspect(appParent)) missing.push(appParent); else hold(appParent);
    } else {
      while (!inspect(anchor)) {
        requireValue(mode === 'create' && missing.length < 32 && anchor !== path.parse(anchor).root, 'data_parent_missing');
        missing.unshift(anchor); anchor = path.dirname(anchor);
      }
      // Explicit paths require an existing private owned anchor. In particular
      // /tmp, another user's directory or a shared 0755 parent is not adopted.
      requireValue(valid(inspect(anchor), true), 'unsafe_data_parent');
      // A previous failed mkdir bootstrap may have left every child present.
      // Rebuild the contiguous private ancestor chain, rather than treating the
      // deepest existing child as evidence that its parents are already durable.
      // The first non-private boundary is only inspected, never held or adopted.
      const privateParents = []; let ancestor = anchor;
      for (;;) {
        requireValue(privateParents.length < 64, 'data_parent_limit');
        privateParents.unshift(ancestor);
        const above = path.dirname(ancestor), stat = inspect(above);
        if (above === ancestor || !stat || !valid(stat, true)) break;
        ancestor = above;
      }
      // Use one limit before and after mkdir, so a successfully prepared chain
      // is not rejected on its next call merely because missing became present.
      requireValue(privateParents.length + missing.length <= 64, 'data_parent_limit');
      for (const name of privateParents) hold(name);
    }
    for (const name of missing) {
      requireValue(mode === 'create', 'data_parent_missing'); check();
      fs.mkdirSync(name, {mode: 0o700}); // EEXIST is a conflict, not a retry/adoption.
      hold(name);
    }
    requireValue(held.some(entry => entry.name === parent), 'data_parent_missing');
    if (existing) hold(options.root);
    // Always repeat the durability barrier on reopen, including a previous
    // failed bootstrap whose private directories happen to be present.
    check(); for (const entry of [...held].reverse()) fs.fsyncSync(entry.fd); check();
    return Object.freeze({root: options.root, mode, check, close});
  } catch (error) {close(); throw error;}
}
