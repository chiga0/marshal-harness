import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {ArtifactDepot, ArtifactDepotError, MAX_ARTIFACT_BYTES} from './depot.mjs';

function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-artifact-test-')));
  t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  return {parent, root: path.join(parent, 'artifacts')};
}
const denied = error => error instanceof ArtifactDepotError;

test('real bytes are immutable-addressed, private, idempotent and cold-reopen readable', t => {
  const {root} = fixture(t); let depot = ArtifactDepot.create(root); t.after(() => depot.close());
  for (const source of [Buffer.alloc(0), Buffer.from('真实业务制品'), new Uint8Array([0, 255, 1])]) {
    const value = depot.put(source), original = Buffer.from(source);
    assert.match(value.digest, /^sha256:[0-9a-f]{64}$/); assert.equal(value.bytes, source.length);
    assert.deepEqual(depot.put(source), value);
    const before = fs.statSync(path.join(root, value.digest.slice(7)));
    source.fill(42); assert.deepEqual(depot.get(value), original);
    const read = depot.get(value); read.fill(7); assert.deepEqual(depot.get(value), original);
    assert.equal(before.mode & 0o777, 0o600); assert.equal(before.nlink, 1);
    depot.close(); depot = ArtifactDepot.openExisting(root);
    assert.deepEqual(depot.get(value), original);
    assert.equal(fs.statSync(path.join(root, value.digest.slice(7))).ino, before.ino);
  }
  assert.equal(fs.statSync(root).mode & 0o777, 0o700);
  depot.close(); depot.close(); assert.throws(() => depot.get({digest: 'sha256:' + '0'.repeat(64), bytes: 0}), denied);
});

test('new root and exact format are explicit; existing/partial/foreign roots never adopted', t => {
  const {parent, root} = fixture(t);
  assert.throws(() => ArtifactDepot.openExisting(root), denied);
  fs.mkdirSync(root, {mode: 0o700}); assert.throws(() => ArtifactDepot.create(root), denied);
  assert.throws(() => ArtifactDepot.openExisting(root), denied);
  fs.writeFileSync(path.join(root, 'format.json'), '{}', {mode: 0o600});
  assert.throws(() => ArtifactDepot.openExisting(root), denied);
  for (const invalid of ['relative', '/', parent + '/../wrong']) assert.throws(() => ArtifactDepot.create(invalid), denied);
});

test('held root and parent reject rename replacement, symlinks and permission drift', t => {
  for (const replaceParent of [false, true]) {
    const {parent, root} = fixture(t), depot = ArtifactDepot.create(root); t.after(() => depot.close());
    const value = depot.put(Buffer.from('persisted'));
    if (replaceParent) {
      fs.renameSync(parent, parent + '-moved'); t.after(() => fs.rmSync(parent + '-moved', {recursive: true, force: true}));
      fs.mkdirSync(parent, {mode: 0o700}); fs.mkdirSync(root, {mode: 0o700});
    } else { fs.renameSync(root, root + '-moved'); fs.symlinkSync(root + '-moved', root); }
    assert.throws(() => depot.get(value), denied); assert.throws(() => depot.put(Buffer.from('next')), denied);
  }
  const {root} = fixture(t), depot = ArtifactDepot.create(root); t.after(() => depot.close());
  fs.chmodSync(root, 0o755); assert.throws(() => depot.put(Buffer.from('next')), denied);
});

test('bad digest, truncated/missing files, links and changed bytes fail closed without repair', t => {
  for (const corruption of ['truncated', 'changed', 'missing', 'symlink', 'hardlink', 'permissions']) {
    const {root, parent} = fixture(t), depot = ArtifactDepot.create(root); t.after(() => depot.close());
    const source = Buffer.from('original'), ref = depot.put(source), file = path.join(root, ref.digest.slice(7));
    if (corruption === 'truncated') fs.truncateSync(file, 1);
    if (corruption === 'changed') fs.writeFileSync(file, 'modified');
    if (corruption === 'missing') fs.unlinkSync(file);
    if (corruption === 'symlink') { fs.renameSync(file, path.join(parent, 'elsewhere')); fs.symlinkSync(path.join(parent, 'elsewhere'), file); }
    if (corruption === 'hardlink') fs.linkSync(file, path.join(parent, 'alias'));
    if (corruption === 'permissions') fs.chmodSync(file, 0o644);
    assert.throws(() => depot.get(ref), denied, corruption);
    if (corruption !== 'missing') assert.throws(() => depot.put(source), denied, corruption);
  }
  const {root} = fixture(t), depot = ArtifactDepot.create(root); t.after(() => depot.close());
  for (const ref of [{digest: '../escape', bytes: 0}, {digest: 'sha256:' + 'a'.repeat(64), bytes: -1},
    {digest: 'sha256:' + 'a'.repeat(64), bytes: MAX_ARTIFACT_BYTES + 1}, {digest: 'sha256:' + 'a'.repeat(64), bytes: 0, path: '/tmp'}]) {
    assert.throws(() => depot.get(ref), denied);
  }
});

test('8 MiB cap is exact and file length is checked before reading', t => {
  const {root} = fixture(t), depot = ArtifactDepot.create(root); t.after(() => depot.close());
  const ref = depot.put(Buffer.alloc(MAX_ARTIFACT_BYTES, 7)); assert.equal(depot.get(ref).length, MAX_ARTIFACT_BYTES);
  assert.throws(() => depot.put(Buffer.alloc(MAX_ARTIFACT_BYTES + 1)), denied);
  assert.throws(() => depot.put('text'), denied);
  fs.truncateSync(path.join(root, ref.digest.slice(7)), MAX_ARTIFACT_BYTES + 1);
  assert.throws(() => depot.get(ref), denied);
});

test('failed write keeps orphan evidence but reopening still reads committed bytes and permits new put', t => {
  const {root} = fixture(t); let depot = ArtifactDepot.create(root); t.after(() => depot.close());
  const ref = depot.put(Buffer.from('old committed bytes'));
  const original = fs.writeFileSync;
  const mock = t.mock.method(fs, 'writeFileSync', (...args) => { original(...args); throw new Error('injected disk failure'); });
  assert.throws(() => depot.put(Buffer.from('incomplete install')), denied); mock.mock.restore();
  const orphans = fs.readdirSync(root).filter(name => name.startsWith('.pending-')); assert.equal(orphans.length, 1);
  assert.throws(() => depot.get(ref), denied); depot.close(); depot = ArtifactDepot.openExisting(root);
  assert.equal(depot.get(ref).toString(), 'old committed bytes');
  const next = depot.put(Buffer.from('new valid bytes')); assert.equal(depot.get(next).toString(), 'new valid bytes');
  assert.ok(fs.existsSync(path.join(root, orphans[0])));
});

test('directory sync and atomic install failures never acknowledge success or overwrite existing bytes', t => {
  for (const failingMethod of ['linkSync', 'fsyncSync']) {
    const {root} = fixture(t); const depot = ArtifactDepot.create(root); t.after(() => depot.close());
    const ref = depot.put(Buffer.from('old'));
    const original = fs[failingMethod];
    const mock = t.mock.method(fs, failingMethod, (...args) => {
      if (failingMethod === 'linkSync' || fs.fstatSync(args[0]).isDirectory()) throw new Error('injected install/sync failure');
      return original(...args);
    });
    assert.throws(() => depot.put(Buffer.from('new')), denied); mock.mock.restore(); depot.close();
    const reopened = ArtifactDepot.openExisting(root); t.after(() => reopened.close());
    assert.equal(reopened.get(ref).toString(), 'old');
  }
});
