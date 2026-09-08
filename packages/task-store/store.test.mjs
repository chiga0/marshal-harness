import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { DatabaseSync } from 'node:sqlite';
import { Store, StoreError, FORMAT, LIMITS, encode, digest, makeEvent } from './store.mjs';

const NOW = 1_800_000_000_000;
const code = expected => error => error instanceof StoreError && error.code === expected;
const empty = () => ({ sequence: 0n, digest: '' });
const ref = (stream, event) => ({ stream, sequence: event.sequence, digest: event.digest });
const receiptKey = { scope: 'task-1', operation: 'approve', keyDigest: digest(encode('key')) };
const requestDigest = digest(encode({ revision: 1 }));
function directory(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-node-store-')));
  t.after(() => fs.rmSync(parent, { recursive: true, force: true }));
  return path.join(parent, 'data');
}
function fixture(t, options = {}) {
  const root = directory(t), store = Store.create(root, { clock: () => NOW, ...options });
  t.after(() => store.close());
  const owner = store.claimOwner(0n, 'instance-1', NOW + 60000);
  return { root, store, owner };
}
function allFacets(tx, failAfter = 0) {
  let step = 0;
  const next = () => { if (++step === failAfter) throw new Error('injected storage failure'); };
  const event = makeEvent('task-1', 1, { type: 'approved', fixtureOnly: true });
  tx.append('task-1', empty(), [event]); next();
  const source = ref('task-1', event);
  tx.putProjection('task', 'task-1', 0, source, encode({ status: 'approved' })); next();
  tx.putProjection('budget', 'task-1', 0, source, encode({ attemptsReserved: 1 })); next();
  tx.putReceipt(receiptKey, requestDigest, source, encode({ acceptedRevision: 1 })); next();
  tx.enqueue({ id: 'start-1', taskId: 'task-1', nodeId: 'node-1', attemptId: 'attempt-1', kind: 'start', inputDigest: digest(encode('input')), payload: encode({ frozen: true }), source }); next();
  return event;
}
function assertEmpty(store, owner) {
  store.read(owner, tx => {
    assert.deepEqual(tx.head('task-1'), empty());
    assert.deepEqual(tx.events('task-1'), []);
    assert.equal(tx.projection('task', 'task-1'), null);
    assert.equal(tx.projection('budget', 'task-1'), null);
    assert.equal(tx.receipt(receiptKey.scope, receiptKey.operation, receiptKey.keyDigest, requestDigest), null);
    assert.deepEqual(tx.commands(), []);
  });
}

test('core: one transaction commits every facet and reopens original bytes', t => {
  const { root, store, owner } = fixture(t);
  const event = store.write(owner, allFacets);
  const info = store.info(); store.close();
  const next = Store.openExisting(root, { clock: () => NOW }); t.after(() => next.close());
  assert.deepEqual(next.info(), info);
  assert.throws(() => next.read(owner, () => {}), code('owner'));
  assert.throws(() => next.renewOwner(owner, NOW + 90000), code('owner'));
  const nextOwner = next.claimOwner(owner.generation, 'instance-2', NOW + 60000);
  assert.equal(nextOwner.generation, 2n);
  next.read(nextOwner, tx => {
    assert.deepEqual(tx.events('task-1'), [event]);
    assert.deepEqual(tx.projections('task').map(p => p.id), ['task-1']);
    assert.deepEqual(tx.projection('budget', 'task-1').bytes, encode({ attemptsReserved: 1 }));
    assert.deepEqual(tx.receipt(receiptKey.scope, receiptKey.operation, receiptKey.keyDigest, requestDigest).bytes, encode({ acceptedRevision: 1 }));
    assert.equal(tx.command('start-1').generation, 1n);
    assert.equal(tx.commands()[0].status, 'pending');
  });
});

test('core: every failure point rolls back all facets, including across reopen', async t => {
  for (let n = 1; n <= 5; n++) await t.test(`after write ${n}`, t => {
    const { root, store, owner } = fixture(t);
    assert.throws(() => store.write(owner, tx => allFacets(tx, n)), code('unavailable'));
    assertEmpty(store, owner); store.close();
    const next = Store.openExisting(root, { clock: () => NOW }); t.after(() => next.close());
    assertEmpty(next, next.claimOwner(1, 'reopened', NOW + 60000));
  });
});

test('core: exact receipt replay precedes CAS and does not debit or enqueue again', t => {
  const { store, owner } = fixture(t);
  const original = store.write(owner, allFacets);
  const replay = store.write(owner, tx => {
    const receipt = tx.receipt(receiptKey.scope, receiptKey.operation, receiptKey.keyDigest, requestDigest);
    if (receipt) return receipt.bytes;
    assert.fail('must not evaluate stale CAS or repeat the reducer');
  });
  assert.deepEqual(replay, encode({ acceptedRevision: 1 }));
  store.read(owner, tx => {
    assert.deepEqual(tx.events('task-1'), [original]);
    assert.equal(tx.projection('budget', 'task-1').revision, 1n);
    assert.equal(tx.commands().length, 1);
  });
  assert.throws(() => store.write(owner, tx => tx.receipt(receiptKey.scope, receiptKey.operation, receiptKey.keyDigest, digest(encode('other')))), code('conflict'));
  assert.throws(() => store.write(owner, tx => tx.putReceipt(receiptKey, requestDigest, ref('task-1', original), encode({ forged: true }))), code('conflict'));
});

test('core: caught method errors poison the transaction and preserve no prefix', t => {
  const { store, owner } = fixture(t);
  assert.throws(() => store.write(owner, tx => {
    allFacets(tx);
    try { tx.putProjection('task', 'task-1', 0, ref('task-1', makeEvent('task-1', 1, { type: 'approved', fixtureOnly: true })), encode({ bad: true })); } catch {}
  }), code('conflict'));
  assertEmpty(store, owner);
});

test('core: nested, async, read-only and escaped transactions fail closed', async t => {
  const { store, owner } = fixture(t);
  let called = false;
  assert.throws(() => store.write(owner, async () => { called = true; }), code('async-transaction'));
  assert.equal(called, false);
  let retained;
  assert.throws(() => store.write(owner, tx => { retained = tx; allFacets(tx); return Promise.resolve(); }), code('async-transaction'));
  await Promise.resolve();
  assert.throws(() => retained.head('task-1'), code('closed'));
  assertEmpty(store, owner);
  for (const inner of [() => store.read(owner, () => {}), () => store.claimOwner(1, 'nested', NOW + 70000), () => store.close()]) {
    assert.throws(() => store.write(owner, tx => { allFacets(tx); try { inner(); } catch {} }), code('nested-transaction'));
    assertEmpty(store, owner);
  }
  assert.throws(() => store.read(owner, tx => { try { tx.append('task-1', empty(), [makeEvent('task-1', 1, {})]); } catch {} }), code('read-only'));
  store.write(owner, tx => { retained = tx; });
  assert.throws(() => retained.append('task-1', empty(), [makeEvent('task-1', 1, {})]), code('closed'));
});

test('core: owner generation, renewal and commit-time expiry fence writes', t => {
  let now = NOW;
  const { store, owner } = fixture(t, { clock: () => now });
  const renewed = store.renewOwner(owner, NOW + 120000);
  assert.throws(() => store.read(owner, () => {}), code('owner'));
  assert.throws(() => store.claimOwner(0, 'stale', NOW + 60000), code('owner'));
  assert.throws(() => store.write(renewed, tx => { allFacets(tx); now = NOW + 120001; }), code('owner'));
  now = NOW;
  assertEmpty(store, renewed);
  const next = store.claimOwner(1, 'new-instance', NOW + 60000);
  assert.equal(next.generation, 2n);
  assert.throws(() => store.write(renewed, () => {}), code('owner'));
});

test('core: unknown outbox is not reset or dispatched on replay/reopen', t => {
  const { root, store, owner } = fixture(t);
  store.write(owner, allFacets);
  let original;
  store.write(owner, tx => {
    original = tx.command('start-1');
    const event = makeEvent('task-1', 2, { type: 'effect-unknown' });
    tx.append('task-1', tx.head('task-1'), [event]);
    tx.observeCommand('start-1', 1, 'unknown', ref('task-1', event));
  });
  store.close();
  const next = Store.openExisting(root, { clock: () => NOW }); t.after(() => next.close());
  const nextOwner = next.claimOwner(1, 'next', NOW + 60000);
  next.write(nextOwner, tx => {
    const { generation, revision, status, observation, ...enqueue } = original;
    assert.equal(tx.enqueue(enqueue).status, 'unknown');
  });
  next.read(nextOwner, tx => { assert.equal(tx.command('start-1').revision, 2n); assert.equal(tx.commands().length, 1); });
  assert.throws(() => next.write(nextOwner, tx => tx.observeCommand('start-1', 2, 'pending', original.source)), code('invalid'));
  assert.throws(() => next.write(nextOwner, tx => tx.enqueue({ id: 'new-command', taskId: 'task-1', kind: 'start', inputDigest: original.inputDigest, payload: original.payload, source: original.source })), code('owner'));
});

test('core: real multi-event batch roundtrip, sequence/digest and cross-stream rejection', t => {
  const { store, owner } = fixture(t);
  const events = [1, 2, 3].map(n => makeEvent('task-1', n, { n }));
  store.write(owner, tx => assert.deepEqual(tx.append('task-1', empty(), events), { sequence: 3n, digest: events[2].digest }));
  store.read(owner, tx => assert.deepEqual(tx.events('task-1'), events));
  assert.throws(() => store.write(owner, tx => tx.append('task-2', empty(), [events[0]])), code('invalid'));
  assert.throws(() => store.write(owner, tx => tx.append('task-1', empty(), [events[0]])), code('conflict'));
  assert.throws(() => store.write(owner, tx => tx.append('task-2', empty(), [{ ...makeEvent('task-2', 1, {}), digest: events[0].digest }])), code('invalid'));
  assert.throws(() => store.write(owner, tx => tx.putProjection('task', 'task-2', 0, { stream: 'task-2', sequence: 1n, digest: events[0].digest }, encode({}))), code('conflict'));
});

test('core: codec closes malformed bytes, duplicate keys and unsafe integers', () => {
  assert.deepEqual(encode({ z: 1, a: [null, true, 'x'] }), Buffer.from('{"a":[null,true,"x"],"z":1}'));
  for (const value of [undefined, NaN, Infinity, 1n, '\ud800', [undefined], new Date(), { x: undefined }]) assert.throws(() => encode(value));
  const cycle = {}; cycle.x = cycle; assert.throws(() => encode(cycle));
  assert.throws(() => makeEvent('task-1', Number.MAX_SAFE_INTEGER + 1, {}), code('invalid'));
  assert.equal(makeEvent('task-1', 9007199254740993n, {}).sequence, 9007199254740993n);
});

function child(mode, root) {
  return new Promise((resolve, reject) => {
    const processHandle = spawn(process.execPath, [fileURLToPath(new URL('./fixtures/process.mjs', import.meta.url)), mode, root], { stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
    let reply, expired = false;
    const timer = setTimeout(() => { expired = true; processHandle.kill('SIGKILL'); }, 5000);
    processHandle.on('message', value => { reply = value; });
    processHandle.once('error', error => { clearTimeout(timer); reject(error); });
    processHandle.once('exit', (exitCode, signal) => { clearTimeout(timer); if (expired) reject(new Error('fixed storage fixture timed out')); else resolve({ exitCode, signal, reply }); });
  });
}

test('recovery: SQLite lifetime lock rejects second process; close permits a new owner', async t => {
  const { root, store, owner } = fixture(t);
  assert.throws(() => Store.openExisting(root), code('busy'));
  const busy = await child('probe', root);
  assert.equal(busy.exitCode, 1); assert.deepEqual(busy.reply, { code: 'busy' });
  store.close();
  const free = await child('probe', root);
  assert.equal(free.exitCode, 0); assert.deepEqual(free.reply, { code: 'opened' });
  const next = Store.openExisting(root, { clock: () => NOW }); t.after(() => next.close());
  assert.throws(() => next.read(owner, () => assert.fail('prior owner reused')), code('owner'));
  assert.equal(next.claimOwner(1, 'new-owner', NOW + 60000).generation, 2n);
});

test('recovery: abrupt process exit before/after commit preserves atomic facts, never sends outbox', async t => {
  for (const mode of ['before-commit', 'after-commit']) await t.test(mode, async t => {
    const { root, store } = fixture(t); const storeId = store.info().storeId; store.close();
    const result = await child(mode, root);
    assert.equal(result.exitCode, mode === 'before-commit' ? 23 : 24); assert.equal(result.signal, null);
    const next = Store.openExisting(root); t.after(() => next.close());
    assert.equal(next.info().storeId, storeId);
    assert.equal(next.info().generation, 2n);
    const owner = next.claimOwner(2, 'recovered', Date.now() + 60000);
    if (mode === 'before-commit') assertEmpty(next, owner);
    else next.read(owner, tx => {
      assert.equal(tx.events('task-1').length, 1);
      assert.deepEqual(tx.projection('task', 'task-1').bytes, encode({ status: 'approved' }));
      assert.deepEqual(tx.projection('budget', 'task-1').bytes, encode({ attemptsReserved: 1 }));
      assert.deepEqual(tx.receipt(receiptKey.scope, receiptKey.operation, receiptKey.keyDigest, requestDigest).bytes, encode({ acceptedRevision: 1 }));
      const [command] = tx.commands(); assert.equal(command.status, 'pending'); assert.equal(command.generation, 2n);
    });
  });
});

test('recovery: create/open never initialize missing, partial, old or malformed roots', async t => {
  for (const mutation of ['missing-file', 'missing-format', 'corrupt', 'wrong-format', 'future-version', 'missing-trigger', 'missing-table']) await t.test(mutation, t => {
    const { root, store } = fixture(t); store.close();
    const database = path.join(root, 'authority.sqlite');
    if (mutation === 'missing-file') fs.unlinkSync(database);
    else if (mutation === 'missing-format') fs.unlinkSync(path.join(root, 'format'));
    else if (mutation === 'corrupt') fs.writeFileSync(database, 'PRIVATE_FIXTURE_NOT_SQLITE');
    else {
      const db = new DatabaseSync(database);
      try {
        if (mutation === 'wrong-format') db.prepare('UPDATE metadata SET format=?').run('marshal-node-team-experiment/v1');
        if (mutation === 'future-version') db.exec('PRAGMA user_version=999');
        if (mutation === 'missing-trigger') db.exec('DROP TRIGGER events_no_update');
        if (mutation === 'missing-table') db.exec('DROP TABLE outbox');
      } finally { db.close(); }
    }
    const before = fs.existsSync(database) ? fs.readFileSync(database) : null;
    assert.throws(() => Store.openExisting(root), code('unavailable'));
    assert.throws(() => Store.create(root), code('unavailable'));
    if (before === null) assert.equal(fs.existsSync(database), false);
    else assert.deepEqual(fs.readFileSync(database), before);
  });
  const root = directory(t); fs.mkdirSync(root, { mode: 0o700 });
  fs.writeFileSync(path.join(root, 'authority.sqlite'), '', { mode: 0o600 });
  assert.throws(() => Store.openExisting(root), code('unavailable'));
  assert.equal(fs.statSync(path.join(root, 'authority.sqlite')).size, 0);
});

test('recovery: child-parent sync failure must be resolved on open before claiming', t => {
  const root = directory(t); let calls = 0;
  const syncDirectory = fd => { calls++; if (calls % 2 === 0) throw new Error('injected parent sync failure'); fs.fsyncSync(fd); };
  assert.throws(() => Store.create(root, { syncDirectory }), code('unavailable')); assert.equal(calls, 2);
  const original = fs.readFileSync(path.join(root, 'authority.sqlite'));
  assert.throws(() => Store.openExisting(root, { syncDirectory }), code('unavailable')); assert.equal(calls, 4);
  assert.deepEqual(fs.readFileSync(path.join(root, 'authority.sqlite')), original);
  const synced = [];
  const store = Store.openExisting(root, { clock: () => NOW, syncDirectory: fd => { synced.push(fs.fstatSync(fd).ino); fs.fsyncSync(fd); } }); t.after(() => store.close());
  assert.deepEqual(synced, [fs.statSync(root).ino, fs.statSync(path.dirname(root)).ino]);
  const owner = store.claimOwner(0, 'after-sync', NOW + 60000); store.write(owner, allFacets);
  const storeId = owner.storeId; store.close();
  const next = Store.openExisting(root, { clock: () => NOW }); t.after(() => next.close());
  assert.equal(next.claimOwner(1, 'after-restart', NOW + 60000).storeId, storeId);
});

test('recovery: unsafe files, symlinks and directory replacement reject without adoption', async t => {
  for (const mutation of ['permissions', 'hardlink', 'symlink', 'root-replaced']) await t.test(mutation, t => {
    const { root, store, owner } = fixture(t), database = path.join(root, 'authority.sqlite');
    if (mutation === 'permissions') fs.chmodSync(database, 0o644);
    if (mutation === 'hardlink') fs.linkSync(database, path.join(root, 'alias'));
    if (mutation === 'symlink') { fs.renameSync(database, path.join(root, 'original')); fs.symlinkSync('original', database); }
    if (mutation === 'root-replaced') { fs.renameSync(root, root + '-original'); fs.mkdirSync(root, { mode: 0o700 }); }
    assert.throws(() => store.read(owner, () => assert.fail('unsafe root admitted')), code('unavailable'));
  });
  const root = directory(t), parent = path.dirname(root); let synced = false;
  assert.throws(() => Store.create(root, { syncDirectory: fd => {
    fs.fsyncSync(fd);
    if (!synced) { synced = true; fs.renameSync(root, path.join(parent, 'original')); fs.mkdirSync(root, { mode: 0o700 }); }
  } }), code('unavailable'));
  assert.equal(fs.readdirSync(root).length, 0);
  assert.equal(fs.existsSync(path.join(parent, 'original', 'authority.sqlite')), true);
});

test('bounds: exact record-count and page limits; ignored overflow rolls back real batch', t => {
  const { store, owner } = fixture(t);
  const events = Array.from({ length: LIMITS.records }, (_, n) => makeEvent('task-1', n + 1, { n }));
  assert.throws(() => store.write(owner, tx => {
    tx.append('task-1', empty(), events);
    try { tx.head('task-1'); } catch (error) { assert.equal(error.code, 'limit'); }
  }), code('limit'));
  assertEmpty(store, owner);
  store.write(owner, tx => tx.append('task-1', empty(), events));
  store.read(owner, tx => assert.deepEqual(tx.events('task-1', 0, LIMITS.page), events.slice(0, LIMITS.page)));
  store.read(owner, tx => assert.deepEqual(tx.events('task-1', LIMITS.page, LIMITS.page), events.slice(LIMITS.page)));
  assert.throws(() => store.read(owner, tx => tx.events('task-1', 0, LIMITS.page + 1)), code('invalid'));
});

test('bounds: aggregate bytes are enforced independently of count; oversize poisons prefix', t => {
  const { store, owner } = fixture(t);
  // Prebuild one 128 KiB canonical record. Repeated genuine reference reads
  // test accounting, not an 8 MiB write throughput or elapsed-time guarantee.
  const size = 128 << 10;
  const overhead = makeEvent('task-1', 1, '').bytes.length;
  const event = makeEvent('task-1', 1, 'x'.repeat(size - overhead));
  assert.equal(event.bytes.length, size);
  store.write(owner, tx => tx.append('task-1', empty(), [event]));
  const count = LIMITS.transactionBytes / size;
  store.read(owner, tx => { for (let n = 0; n < count; n++) tx.head('task-1'); });
  assert.throws(() => store.read(owner, tx => { for (let n = 0; n <= count; n++) tx.head('task-1'); }), code('limit'));
  const prefix = makeEvent('task-2', 1, {});
  const bytes = Buffer.from('{"payload":"' + 'x'.repeat(LIMITS.recordBytes) + '","sequence":"2","stream":"task-2"}');
  assert.throws(() => store.write(owner, tx => {
    tx.append('task-2', empty(), [prefix]);
    try { tx.append('task-2', { sequence: 1n, digest: prefix.digest }, [{ sequence: 2n, digest: digest(bytes), bytes }]); } catch (error) { assert.equal(error.code, 'limit'); }
  }), code('limit'));
  store.read(owner, tx => assert.deepEqual(tx.events('task-2'), []));
});

test('bounds: deterministic transaction deadline, malformed bytes and owned copy boundaries', t => {
  let monotonic = 0;
  const { store, owner } = fixture(t, { monotonic: () => monotonic });
  assert.throws(() => store.write(owner, tx => { allFacets(tx); monotonic = LIMITS.transactionMs + 1; }), code('deadline'));
  monotonic = 0; assertEmpty(store, owner);
  for (const bytes of [Buffer.from('{"payload":{},"payload":{},"sequence":"1","stream":"task-1"}'), Buffer.from('{ "payload":{},"sequence":"1","stream":"task-1"}')]) {
    assert.throws(() => store.write(owner, tx => tx.append('task-1', empty(), [{ sequence: 1n, digest: digest(bytes), bytes }])), code('invalid'));
  }
  const event = store.write(owner, allFacets); event.bytes.fill(0);
  store.read(owner, tx => { const read = tx.events('task-1')[0]; assert.notEqual(read.bytes[0], 0); read.bytes.fill(0); });
  store.read(owner, tx => assert.notEqual(tx.events('task-1')[0].bytes[0], 0));
});
