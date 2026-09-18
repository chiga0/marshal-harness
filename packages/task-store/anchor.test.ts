import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {Store, makeEvent} from './store.ts';
import {ANCHOR_FORMAT, exportAnchor, verifyAnchor, verifyAnchorPayload} from './anchor.ts';

function rootDir(t) { const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'anchor-test-')); t.after(() => fs.rmSync(dir, { recursive: true, force: true })); return fs.realpathSync(dir); }

async function seed(root, from, to) {
  const authority = path.join(root, 'authority.sqlite');
  const store = fs.existsSync(authority) ? Store.openExisting(root) : Store.create(root);
  const owner = await store.claimOwner(store.info().generation, 'i', Date.now() + 60000);
  await store.write(owner, tx => {
    let head = tx.head('task-1');
    for (let n = from; n <= to; n++) head = tx.append('task-1', head, [makeEvent('task-1', BigInt(n), { kind: 'fact', n })]);
  });
  const info = store.info();
  store.close();
  return info;
}

test('anchor: 导出包含稳定 storeId 的 tenantScope 与链头摘要;自摘要可复算', async t => {
  const root = rootDir(t), info = await seed(root, 1, 3);
  const anchor = await exportAnchor(root, {now: 1758000000000});
  assert.equal(anchor.format, ANCHOR_FORMAT);
  assert.equal(anchor.tenantScope, info.storeId);
  assert.equal(anchor.storeId, info.storeId);
  assert.equal(anchor.generatedAt, '2025-09-16T05:20:00.000Z');
  assert.equal(anchor.heads.length, 1);
  assert.deepEqual(verifyAnchorPayload(root, anchor, {expectedTenantScope: anchor.tenantScope}).heads, 1);
});

test('anchor: 同根验证通过;变更任意链后必如实偏差,不自动调和', async t => {
  const root = rootDir(t);
  await seed(root, 1, 3);
  const anchor = await exportAnchor(root);
  let verdict = await verifyAnchor(root, anchor);
  assert.equal(verdict.ok, true, JSON.stringify(verdict.mismatches));
  await seed(root, 4, 6); // 在新打开链上再追加事件(同一 stream 头前进)
  verdict = await verifyAnchor(root, anchor);
  assert.equal(verdict.ok, false);
  assert.ok(verdict.mismatches.some(item => item.code === 'head_digest_diverged' && item.stream === 'task-1'));
});

test('anchor: 跨根验证按 tenantScope 直接拒,不允许误读为他根证据', async t => {
  const a = rootDir(t), b = rootDir(t);
  await seed(a, 1, 2); await seed(b, 1, 2);
  const anchor = await exportAnchor(a);
  await assert.rejects(verifyAnchor(b, anchor), /anchor_tenant_mismatch/);
});

test('anchor: 形状破坏或自摘要受损的锚直接被拒', async t => {
  const root = rootDir(t);
  await seed(root, 1, 1);
  const anchor = await exportAnchor(root);
  assert.throws(() => verifyAnchorPayload(root, {...anchor, format: 'other'}), /anchor_shape_invalid/);
  assert.throws(() => verifyAnchorPayload(root, {...anchor, headsDigest: '0'.repeat(64)}), /anchor_self_digest_mismatch/);
});
