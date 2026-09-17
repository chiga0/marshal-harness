// 审计锚定:把"此刻每个流的事实链头"固定成一份可带走的轻证据载体。
// 不变量:
// - 内容零推断:仅由唯一权威 Store 的只读快照组装,tenantScope 即 root 的 storeId;
// - 一份锚只可锚当时那一刻:旧 anchor 与新事实不匹配是期望信号,不自动调和;
// - 不携带签名签发权:外部存放/签名是部署环境义务,本文件只保证字节可复算可比对。
import {digest, encode, Store} from './store.ts';

export const ANCHOR_FORMAT = 'marshal-audit-anchor/v1';

function fail(reason: string, detail: Record<string, unknown> = {}) { const error = new Error('audit anchor: ' + reason) as any; error.name = 'AuditAnchorError'; error.code = reason; error.detail = detail; return error; }

export async function exportAnchor(root: string, options: {now?: number | (() => number)} = {}) {
  const store = Store.openExisting(root);
  try {
    const info = store.info();
    const heads: {stream: string, sequence: bigint, digest: string}[] = await store.headsList();
    const canonical = heads.map(entry => ({stream: entry.stream, sequence: entry.sequence.toString(), digest: entry.digest}));
    const nowValue: number | (() => number) = options.now ?? Date.now();
    const at = typeof nowValue === 'function' ? nowValue() : nowValue;
    return Object.freeze({format: ANCHOR_FORMAT, tenantScope: info.tenantScope, storeId: info.storeId,
      generatedAt: new Date(at).toISOString(), heads: canonical, headsDigest: digest(encode(canonical))});
  } finally { store.close(); }
}

function shape(anchor: any) {
  if (anchor?.format !== ANCHOR_FORMAT || typeof anchor.tenantScope !== 'string' || typeof anchor.storeId !== 'string'
      || !Array.isArray(anchor.heads) || typeof anchor.headsDigest !== 'string' || typeof anchor.generatedAt !== 'string')
    throw fail('anchor_shape_invalid');
  for (const entry of anchor.heads)
    if (typeof entry?.stream !== 'string' || !/^[0-9]+$/.test(entry.sequence ?? '') || typeof entry?.digest !== 'string') throw fail('anchor_shape_invalid', {entry});
}

export function verifyAnchorPayload(root: string, anchor: any, options: {expectedTenantScope?: string} = {}) {
  shape(anchor);
  if (digest(encode(anchor.heads)) !== anchor.headsDigest) throw fail('anchor_self_digest_mismatch');
  if (options.expectedTenantScope !== undefined && anchor.tenantScope !== options.expectedTenantScope) throw fail('anchor_tenant_mismatch');
  return {tenantScope: anchor.tenantScope, generatedAt: anchor.generatedAt, heads: anchor.heads.length};
}

export async function verifyAnchor(root: string, anchor: any, options: {expectedTenantScope?: string} = {}) {
  const summary = verifyAnchorPayload(root, anchor, options);
  const store = Store.openExisting(root);
  try {
    const info = store.info();
    if (info.tenantScope !== anchor.tenantScope) throw fail('anchor_tenant_mismatch', {expected: anchor.tenantScope, actual: info.tenantScope});
    const currentHeads: {stream: string, sequence: bigint, digest: string}[] = await store.headsList();
    const current = new Map(currentHeads.map(entry => [entry.stream, {sequence: entry.sequence.toString(), digest: entry.digest}]));
    const baseline = new Map<string, {stream: string, sequence: string, digest: string}>((anchor.heads as any[]).map((entry: any) => [entry.stream, entry]));
    const mismatches = [];
    for (const [stream, now] of current) {
      const then = baseline.get(stream);
      if (!then) mismatches.push({stream, code: 'stream_added_since_anchor'});
      else if (then.digest !== now.digest) mismatches.push({stream, code: 'head_digest_diverged', anchor: then.digest, current: now.digest});
      else if (BigInt(then.sequence) > BigInt(now.sequence)) mismatches.push({stream, code: 'head_sequence_regressed', anchor: then.sequence, current: now.sequence});
      baseline.delete(stream);
    }
    for (const [stream, then] of baseline) mismatches.push({stream, code: 'stream_missing_since_anchor', anchor: then.digest});
    return {ok: mismatches.length === 0, ...summary, mismatches};
  } finally { store.close(); }
}
