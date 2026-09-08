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
const bootstrapOrder = ['format', 'root', 'parent'];
function bootstrapProbe(t, root, open, {failAt, afterSync} = {}) {
  const calls = [], original = fs.fsyncSync;
  const mock = t.mock.method(fs, 'fsyncSync', fd => {
    const held = fs.fstatSync(fd);
    const stage = [['format', path.join(root, 'format.json')], ['root', root], ['parent', path.dirname(root)]]
      .find(([, name]) => { const named = fs.lstatSync(name); return held.dev === named.dev && held.ino === named.ino; })?.[0];
    assert.ok(stage, 'sync must use the held, named bootstrap object');
    calls.push(stage);
    if (stage === failAt) throw new Error('injected bootstrap sync failure');
    original(fd); afterSync?.(stage);
  });
  let depot;
  try {
    if (failAt || afterSync) {
      assert.throws(() => { depot = open(root); }, denied);
      assert.equal(depot, undefined, 'failed bootstrap must not return a usable instance');
    } else depot = open(root);
  } finally { mock.mock.restore(); }
  assert.deepEqual(calls, failAt ? bootstrapOrder.slice(0, bootstrapOrder.indexOf(failAt) + 1) : bootstrapOrder);
  return depot;
}

test('create and every cold reopen sync the held format, root and parent in order', t => {
  const {root} = fixture(t);
  let depot = bootstrapProbe(t, root, ArtifactDepot.create); t.after(() => depot.close());
  const ref = depot.put(Buffer.from('committed before reopen')); depot.close();
  depot = bootstrapProbe(t, root, ArtifactDepot.openExisting);
  assert.equal(depot.get(ref).toString(), 'committed before reopen');
  const next = depot.put(Buffer.from('committed after reopen')); depot.close();
  depot = bootstrapProbe(t, root, ArtifactDepot.openExisting);
  assert.equal(depot.get(ref).toString(), 'committed before reopen');
  assert.equal(depot.get(next).toString(), 'committed after reopen');
});

test('each failed create sync must be completed on reopen; repeated failures preserve evidence and reject use', async t => {
  for (const failAt of bootstrapOrder) await t.test(failAt, t => {
    const {root} = fixture(t), formatPath = path.join(root, 'format.json');
    bootstrapProbe(t, root, ArtifactDepot.create, {failAt});
    const format = fs.readFileSync(formatPath), initial = fs.statSync(formatPath), rootInitial = fs.statSync(root);
    assert.ok(format.length > 0); assert.deepEqual(fs.readdirSync(root), ['format.json']);
    bootstrapProbe(t, root, ArtifactDepot.openExisting, {failAt});
    assert.deepEqual(fs.readFileSync(formatPath), format);
    assert.equal(fs.statSync(formatPath).ino, initial.ino); assert.equal(fs.statSync(root).ino, rootInitial.ino);
    assert.throws(() => ArtifactDepot.create(root), denied, 'failed initialization is not an empty root to reset');
    let depot = bootstrapProbe(t, root, ArtifactDepot.openExisting); t.after(() => depot.close());
    const ref = depot.put(Buffer.from('persisted after bootstrap recovery')); depot.close();
    // Even a previously usable root must not bypass a new bootstrap sync error.
    bootstrapProbe(t, root, ArtifactDepot.openExisting, {failAt});
    assert.deepEqual(fs.readFileSync(formatPath), format);
    assert.equal(fs.statSync(formatPath).ino, initial.ino);
    depot = bootstrapProbe(t, root, ArtifactDepot.openExisting);
    assert.equal(depot.get(ref).toString(), 'persisted after bootstrap recovery');
    const next = depot.put(Buffer.from('new bytes after second reopen')); depot.close();
    depot = bootstrapProbe(t, root, ArtifactDepot.openExisting);
    assert.equal(depot.get(ref).toString(), 'persisted after bootstrap recovery');
    assert.equal(depot.get(next).toString(), 'new bytes after second reopen');
  });
});

test('bootstrap rechecks held identities, format bytes and permissions after the final sync', async t => {
  for (const mutation of ['format-replaced', 'root-replaced', 'parent-replaced', 'format-mode', 'root-mode', 'format-bytes']) {
    await t.test(mutation, t => {
      const {root, parent} = fixture(t), formatPath = path.join(root, 'format.json');
      const depot = ArtifactDepot.create(root); depot.close();
      const format = fs.readFileSync(formatPath);
      bootstrapProbe(t, root, ArtifactDepot.openExisting, {afterSync: stage => {
        if (stage !== 'parent') return;
        if (mutation === 'format-replaced') {
          fs.renameSync(formatPath, path.join(parent, 'old-format'));
          fs.writeFileSync(formatPath, format, {mode: 0o600});
        } else if (mutation === 'root-replaced' || mutation === 'parent-replaced') {
          const target = mutation === 'root-replaced' ? root : parent;
          fs.renameSync(target, target + '-moved');
          t.after(() => fs.rmSync(target + '-moved', {recursive: true, force: true}));
          fs.mkdirSync(target, {mode: 0o700});
          if (mutation === 'parent-replaced') fs.mkdirSync(root, {mode: 0o700});
          fs.writeFileSync(formatPath, format, {mode: 0o600});
        } else if (mutation === 'format-mode') fs.chmodSync(formatPath, 0o644);
        else if (mutation === 'root-mode') fs.chmodSync(root, 0o755);
        else fs.writeFileSync(formatPath, Buffer.alloc(format.length, 32));
      }});
    });
  }
});

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
