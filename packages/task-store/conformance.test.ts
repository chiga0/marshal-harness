// Store Port 合同测试套件(行为级,后端无关):任何权威存储实现只要声称实现
// Task Store Port,就必须通过同一组行为契约。SQLite 实现在此处先行通过;
// 后续后端(PostgreSQL 等)只需提供同形 factory 挂入即可复跑全部用例。
// 契约来源:docs/extension-contracts.md Store/Depot 端口义务。
// - 生命周期方法为 Promise 边界;事务回调严格同步(async 回调/返回 Promise 即拒 'async-transaction')
// - 单一写者:同一 root 同时只允许一个打开句柄('busy'),owner 代际/实例/有效期三重围栏('owner')
// - 事件仅追加、序列连续、摘要链一致('conflict');事实/回执/outbox 与事件同事务
// - 投影与 outbox 为 CAS('conflict');回执同 key 同 digest 幂等,同 key 异 digest 拒绝('conflict')
// - outbox 仅 pending→unknown/observed 单向,observe 计代际围栏('owner'/'conflict')
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {Store, encode, digest, makeEvent} from './store.ts';

const code = value => error => error instanceof Error && error.name === 'StoreError' && error.code === value;

export function runStoreConformance(label, factory) {
  function setup(t) {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), `store-conformance-${label}-`));
    t.after(() => fs.rmSync(root, { recursive: true, force: true }));
    return fs.realpathSync(root);
  }
  const event = n => makeEvent('task-1', n, { kind: 'fact', n });
  const src = e => ({ stream: 'task-1', sequence: e.sequence, digest: e.digest });

  test(`${label}: 生命周期方法为 Promise 边界;创建-认领-写-重开-读取往返(含冷开事实重放)`, async t => {
    const root = setup(t), store = factory.create(root);
    assert.equal(typeof store.info().format, 'string');
    assert.equal(store.info().generation, 0n);
    const claim = store.claimOwner(store.info().generation, 'instance-1', Date.now() + 60000);
    assert.ok(claim instanceof Promise, 'claimOwner must return a Promise');
    const owner = await claim;
    assert.equal(owner.generation, 1n);
    const e1 = event(1);
    const written = store.write(owner, tx => {
      tx.append('task-1', { sequence: 0n, digest: '' }, [e1]);
      tx.putProjection('task', 'task-1', 0, src(e1), encode({ status: 'queued' }));
      tx.putReceipt({ scope: 'task-1', operation: 'approve', keyDigest: digest(encode('k1')) }, digest(encode({ rev: 1 })), src(e1), encode({ ok: 1 }));
      return 'done';
    });
    assert.ok(written instanceof Promise, 'write must return a Promise');
    assert.equal(await written, 'done');
    store.close();
    const reopened = factory.open(root);
    const owner2 = await reopened.claimOwner(reopened.info().generation, 'instance-2', Date.now() + 60000);
    await reopened.read(owner2, tx => {
      assert.deepEqual(tx.head('task-1'), { sequence: 1n, digest: e1.digest });
      assert.equal(JSON.parse(tx.projection('task', 'task-1').bytes.toString('utf8')).status, 'queued');
      assert.equal(tx.receipt('task-1', 'approve', digest(encode('k1')), digest(encode({ rev: 1 }))) !== null, true);
    });
    reopened.close();
  });

  test(`${label}: 同一 root 不允许第二个并发打开句柄`, async t => {
    const root = setup(t), first = factory.create(root);
    t.after(() => first.close());
    assert.throws(() => factory.open(root), code('busy'));
    first.close();
    const second = factory.open(root);
    t.after(() => second.close());
  });

  test(`${label}: owner 围栏——旧代际读写拒 owner,认领幂等按代际+实例+有效期`, async t => {
    const root = setup(t), store = factory.create(root);
    t.after(() => store.close());
    const first = await store.claimOwner(0, 'a', Date.now() + 60000);
    await store.write(first, () => {});
    const stale = first;
    const next = await store.claimOwner(first.generation, 'b', Date.now() + 60000);
    assert.equal(next.generation, 2n);
    await assert.rejects(store.read(stale, () => {}), code('owner'));
    await assert.rejects(store.write(stale, () => {}), code('owner'));
    await assert.rejects(store.renewOwner(stale, Date.now() + 120000), code('owner'));
    const renewed = await store.renewOwner(next, Date.now() + 120000);
    assert.equal(renewed.generation, 2n);
    await store.write(renewed, () => {});
  });

  test(`${label}: 事务回调必须同步;async 回调与 Promise 返回均拒 async-transaction`, async t => {
    const store = factory.create(setup(t));
    t.after(() => store.close());
    const owner = await store.claimOwner(0, 'i', Date.now() + 60000);
    await assert.rejects(store.write(owner, async () => {}), code('async-transaction'));
    await assert.rejects(store.write(owner, () => Promise.resolve(1)), code('async-transaction'));
    await assert.rejects(store.read(owner, async () => {}), code('async-transaction'));
  });

  test(`${label}: 事件仅追加且摘要链一致;陈旧 head CAS 拒 conflict;仅追加表不可改`, async t => {
    const store = factory.create(setup(t));
    t.after(() => store.close());
    const owner = await store.claimOwner(0, 'i', Date.now() + 60000);
    const events = [event(1), event(2), event(3)];
    await store.write(owner, tx => {
      let head = { sequence: 0n, digest: '' };
      for (const e of events) head = tx.append('task-1', head, [e]);
    });
    await store.read(owner, tx => {
      assert.deepEqual(tx.head('task-1'), { sequence: 3n, digest: events[2].digest });
      assert.deepEqual(tx.events('task-1').map(e => e.digest), events.map(e => e.digest));
    });
    await assert.rejects(store.write(owner, tx => tx.append('task-1', { sequence: 0n, digest: '' }, [event(1)])), code('conflict'));
    await assert.rejects(store.write(owner, tx => tx.append('task-1', tx.head('task-1'), [event(5)])), code('conflict'));
  });

  test(`${label}: 投影响 CAS;回执幂等与冲突;outbox 单向生命周期`, async t => {
    const store = factory.create(setup(t));
    t.after(() => store.close());
    const owner = await store.claimOwner(0, 'i', Date.now() + 60000);
    const e1 = event(1);
    await store.write(owner, tx => {
      tx.append('task-1', { sequence: 0n, digest: '' }, [e1]);
      tx.putProjection('task', 'task-1', 0, src(e1), encode({ v: 1 }));
    });
    await assert.rejects(store.write(owner, tx => tx.putProjection('task', 'task-1', 0, src(e1), encode({ v: 2 }))), code('conflict'));
    const key = { scope: 'task-1', operation: 'approve', keyDigest: digest(encode('k')) };
    const reqDigest = digest(encode({ a: 1 })), otherReqDigest = digest(encode({ a: 2 }));
    await store.write(owner, tx => tx.putReceipt(key, reqDigest, src(e1), encode({ accepted: 1 })));
    await store.write(owner, tx => tx.putReceipt(key, reqDigest, src(e1), encode({ accepted: 1 }))); // 同 key 同 digest 幂等
    await assert.rejects(store.write(owner, tx => tx.receipt(key.scope, key.operation, key.keyDigest, otherReqDigest)), code('conflict')); // 同 key 异 digest 不得被误读为未提交
    await store.write(owner, tx => {
      const cmd = tx.enqueue({ id: 'cmd-1', taskId: 'task-1', kind: 'start', inputDigest: digest(encode('in')), payload: encode({}), source: src(e1) });
      assert.equal(cmd.status, 'pending');
      tx.observeCommand('cmd-1', 1n, 'unknown', src(e1));
      assert.equal(tx.command('cmd-1').status, 'unknown');
      tx.observeCommand('cmd-1', 2n, 'observed', src(e1));
      assert.equal(tx.command('cmd-1').status, 'observed');
    });
    await assert.rejects(store.write(owner, tx => tx.observeCommand('cmd-1', 3n, 'unknown', src(e1))), code('conflict'));
  });

  test(`${label}: inspectRecovery 仅限受信恢复格式且未认领前;认领后拒 owner`, async t => {
    const createWithFormat = factory.createWithFormat ?? factory.create;
    const root = setup(t), store = createWithFormat(root, 'marshal-node-task-sqlite/v2-custody');
    t.after(() => store.close());
    const peek = store.inspectRecovery(tx => tx.head('task-1'));
    assert.ok(peek instanceof Promise, 'inspectRecovery must return a Promise');
    assert.deepEqual(await peek, { sequence: 0n, digest: '' });
    await store.claimOwner(0, 'i', Date.now() + 60000);
    await assert.rejects(store.inspectRecovery(() => {}), code('owner'));
  });
}

// ---- SQLite 实现挂入 ----
runStoreConformance('sqlite', {
  create: root => Store.create(root),
  open: root => Store.openExisting(root),
  createWithFormat: (root, format) => Store.create(root, {format}),
});
