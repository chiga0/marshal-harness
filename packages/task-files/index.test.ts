import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {createExecutionDirectory, collect, TaskFilesError, MAX_BYTES} from './index.mjs';

function setup(t) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-task-files-')));
  const parent = path.join(root, 'executions'); fs.mkdirSync(parent, {mode: 0o700});
  const depot = ArtifactDepot.create(path.join(root, 'depot'));
  t.after(() => { depot.close(); fs.rmSync(root, {recursive: true, force: true}); });
  return {root, parent, depot};
}
function prepare(t, fixture, inputs = [], extra = {}) {
  const handle = createExecutionDirectory({...fixture, workerId: 'worker-one', inputs, ...extra});
  t.after(() => handle.close()); return handle;
}
function write(handle, name, bytes) {
  fs.mkdirSync(path.dirname(path.join(handle.cwd, name)), {recursive: true});
  fs.writeFileSync(path.join(handle.cwd, name), bytes, {mode: 0o644});
}
const rejected = failure => failure instanceof TaskFilesError;

test('real depot input materialization, native-file-style outputs, frozen exact downstream reconstruction', t => {
  const f = setup(t), input = {path: 'inputs/data.csv', ...f.depot.put(Buffer.from('name,value\na,3\n'))};
  const handle = prepare(t, f, [input]);
  assert.equal(fs.readFileSync(path.join(handle.cwd, input.path), 'utf8'), 'name,value\na,3\n');
  assert.equal(fs.statSync(handle.cwd).mode & 0o777, 0o700);
  assert.equal(fs.statSync(path.join(handle.cwd, input.path)).mode & 0o777, 0o400);
  write(handle, 'result/summary.json', '{"total":3}'); write(handle, 'README.md', 'Read the supplied data.');
  const result = collect(handle, {allowedPaths: ['result/summary.json', 'README.md']});
  assert.deepEqual(result.files.map(file => file.path), ['README.md', 'result/summary.json']);
  assert.equal(result.inputDigest, handle.inputDigest); assert.match(result.manifestDigest, /^sha256:[0-9a-f]{64}$/);
  assert.equal(Object.isFrozen(result.files[0]), true);
  assert.deepEqual(Object.keys(result).sort(), ['files', 'inputDigest', 'manifestDigest']);
  const next = prepare(t, f, result.files, {workerId: 'worker-two'});
  assert.equal(fs.readFileSync(path.join(next.cwd, 'result/summary.json'), 'utf8'), '{"total":3}');
  assert.throws(() => collect(handle, {allowedPaths: ['README.md']}), {code: 'task_files_already_collected'});
  handle.close(); assert.equal(fs.existsSync(handle.cwd), true);
});

test('new execution directories never adopt old content; malformed handles and parents are rejected', t => {
  const f = setup(t), handle = prepare(t, f);
  assert.throws(() => createExecutionDirectory({...f, workerId: 'worker-one'}), {code: 'task_files_directory_exists'});
  assert.throws(() => collect({cwd: handle.cwd}, {allowedPaths: []}), {code: 'task_files_invalid_handle'});
  const alias = path.join(f.root, 'alias'); fs.symlinkSync(f.parent, alias);
  assert.throws(() => createExecutionDirectory({...f, parent: alias, workerId: 'worker-next'}), rejected);
  fs.chmodSync(f.parent, 0o755);
  assert.throws(() => createExecutionDirectory({...f, workerId: 'worker-next'}), rejected);
  assert.equal(fs.existsSync(handle.cwd), true);
});

test('path escape, hidden/control paths, duplicates, aliases and file/directory conflict fail before writes', t => {
  const f = setup(t), ref = f.depot.put(Buffer.from('x'));
  for (const names of [['../escape'], ['/absolute'], ['a\\b'], ['a/./b'], ['.git/config'], ['.marshal/state'], ['.env'], ['a\0b'],
    ['same', 'same'], ['A/x', 'a/y'], ['a', 'a/b'], ['e\u0301.txt']]) {
    assert.throws(() => createExecutionDirectory({...f, workerId: 'never-created', inputs: names.map(path => ({path, ...ref}))}), rejected);
    assert.equal(fs.existsSync(path.join(f.parent, 'never-created')), false);
  }
});

test('input bytes, input identity and permissions cannot drift before collection', t => {
  for (const change of ['bytes', 'identity', 'permissions', 'missing']) {
    const f = setup(t), input = {path: 'input.txt', ...f.depot.put(Buffer.from('original'))};
    const handle = prepare(t, f, [input]), target = path.join(handle.cwd, input.path);
    if (change === 'bytes') { fs.chmodSync(target, 0o600); fs.writeFileSync(target, 'modified'); fs.chmodSync(target, 0o400); }
    if (change === 'identity') { fs.unlinkSync(target); fs.writeFileSync(target, 'original', {mode: 0o400}); }
    if (change === 'permissions') fs.chmodSync(target, 0o600);
    if (change === 'missing') fs.unlinkSync(target);
    write(handle, 'out.txt', 'candidate');
    assert.throws(() => collect(handle, {allowedPaths: ['out.txt']}), rejected, change);
    assert.equal(fs.readFileSync(path.join(handle.cwd, 'out.txt'), 'utf8'), 'candidate');
  }
});

test('symlink/hardlink and unapproved or absent output cannot enter candidate manifest', t => {
  for (const change of ['symlink', 'hardlink', 'extra', 'extra-directory', 'missing', 'input-output-conflict']) {
    const f = setup(t), input = {path: 'input.txt', ...f.depot.put(Buffer.from('original'))};
    const handle = prepare(t, f, [input]); write(handle, 'out.txt', 'candidate');
    if (change === 'symlink') { fs.unlinkSync(path.join(handle.cwd, 'out.txt')); fs.symlinkSync(path.join(handle.cwd, 'input.txt'), path.join(handle.cwd, 'out.txt')); }
    if (change === 'hardlink') fs.linkSync(path.join(handle.cwd, 'out.txt'), path.join(f.root, 'alias'));
    if (change === 'extra') write(handle, 'unapproved.txt', 'extra');
    if (change === 'extra-directory') fs.mkdirSync(path.join(handle.cwd, 'unused'));
    if (change === 'missing') fs.unlinkSync(path.join(handle.cwd, 'out.txt'));
    assert.throws(() => collect(handle, {allowedPaths: change === 'input-output-conflict' ? ['input.txt'] : ['out.txt']}), rejected, change);
    assert.equal(fs.existsSync(handle.cwd), true);
  }
});

test('held root/intermediate directories reject replacements even if copied input bytes match', t => {
  for (const target of ['root', 'nested']) {
    const f = setup(t), input = {path: 'inputs/original.txt', ...f.depot.put(Buffer.from('original'))};
    const handle = prepare(t, f, [input]);
    if (target === 'root') {
      fs.renameSync(handle.cwd, handle.cwd + '-original'); fs.mkdirSync(handle.cwd, {mode: 0o700});
    } else {
      fs.renameSync(path.join(handle.cwd, 'inputs'), path.join(handle.cwd, 'moved'));
      fs.mkdirSync(path.join(handle.cwd, 'inputs'), {mode: 0o700});
      fs.writeFileSync(path.join(handle.cwd, input.path), 'original', {mode: 0o400});
    }
    assert.throws(() => collect(handle, {allowedPaths: []}), rejected);
  }
});

test('64-file and aggregate 8 MiB limits cover inputs and outputs before depot publication', t => {
  for (const mode of ['count', 'single', 'aggregate']) {
    const f = setup(t), inputs = mode === 'aggregate' ? [{path: 'input.bin', ...f.depot.put(Buffer.alloc(1))}] : [];
    const handle = prepare(t, f, inputs);
    const allowedPaths = mode === 'count' ? Array.from({length: 65}, (_, index) => 'f' + index) : ['out.bin'];
    if (mode !== 'count') { write(handle, 'out.bin', ''); fs.truncateSync(path.join(handle.cwd, 'out.bin'), MAX_BYTES + (mode === 'single' ? 1 : 0)); }
    assert.throws(() => collect(handle, {allowedPaths}), {code: 'task_files_limit'});
  }
});

test('depot failure never returns a manifest or deletes source and earlier orphan bytes', t => {
  const f = setup(t); let puts = 0;
  const depot = {get: reference => f.depot.get(reference), put: bytes => { if (++puts === 2) throw new Error('PRIVATE_FAILURE'); return f.depot.put(bytes); }};
  const handle = prepare(t, f, [], {depot}); write(handle, 'a.txt', 'a'); write(handle, 'b.txt', 'b');
  assert.throws(() => collect(handle, {allowedPaths: ['a.txt', 'b.txt']}), failure => failure.code === 'task_files_unavailable' && !failure.message.includes('PRIVATE'));
  assert.equal(fs.readFileSync(path.join(handle.cwd, 'b.txt'), 'utf8'), 'b');
  assert.equal(fs.readdirSync(path.join(f.root, 'depot')).length, 2); // format + one unreferenced blob.
  assert.throws(() => collect(handle, {allowedPaths: ['a.txt', 'b.txt']}), {code: 'task_files_closed'});
});

test('failed materialization retains partial directory and never adopts it on retry', t => {
  const f = setup(t), ref = f.depot.put(Buffer.from('first')); let reads = 0;
  const depot = {get: value => { if (++reads === 2) throw new Error('failed'); return f.depot.get(value); }, put: bytes => f.depot.put(bytes)};
  assert.throws(() => createExecutionDirectory({...f, depot, workerId: 'partial', inputs: [{path: 'a', ...ref}, {path: 'b', ...ref}]}), rejected);
  assert.equal(fs.readFileSync(path.join(f.parent, 'partial/a'), 'utf8'), 'first');
  assert.throws(() => createExecutionDirectory({...f, workerId: 'partial'}), {code: 'task_files_directory_exists'});
});
