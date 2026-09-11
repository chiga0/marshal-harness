// 成果下载：GET /v1/artifacts/{artifactId}/content 拉流 → 本机 SHA-256 复验（浏览器 Web Crypto）→ 落盘为浏览器下载。
// 摘要或大小不一致一律拒绝保存（E17 防假冒/防损坏）；文件名去掉路径穿越；下载不等于发布，也不冒充发布。
// 合同单个成果 ≤8388608 字节；下载前复核清单上限，读取后再次校验实际字节数。

import type {ArtifactId, Transport} from '@/lib/transport/types';

export const MAX_ARTIFACT_DOWNLOAD_BYTES = 8388608;

export class DownloadRejection extends Error {
  readonly reason: 'digest_mismatch' | 'size_mismatch' | 'size_limit' | 'unverifiable' | 'empty_ref';

  constructor(reason: DownloadRejection['reason'], message: string) {
    super(message);
    this.name = 'DownloadRejection';
    this.reason = reason;
  }
}

/** 只保留路径最后一段，滤掉目录穿越与控制字符；空名回退为 artifact-download。 */
export function sanitizeFileName(rawPath: string): string {
  const segments = rawPath.split(/[\\/]+/).filter(segment => segment !== '' && segment !== '.' && segment !== '..');
  const base = segments.length > 0 ? (segments[segments.length - 1] ?? '') : '';
  const cleaned = base.replace(/[\x00-\x1f<>:"|?*]/g, '_').replace(/^\.+/, '').trim();
  return cleaned === '' ? 'artifact-download' : cleaned;
}

export function parseSha256(digest: string): string | null {
  const match = /^sha256:([0-9a-fA-F]{64})$/.exec(digest);
  return match ? (match[1] ?? '').toLowerCase() : null;
}

export async function sha256Hex(blob: Blob): Promise<string> {
  // jsdom 的 Blob 无 arrayBuffer；走 Response/FileReader 两条兼容路径的浏览器 Web Crypto 复验
  const buffer = typeof blob.arrayBuffer === 'function'
    ? await blob.arrayBuffer()
    : await new Response(blob).arrayBuffer();
  const hash = await globalThis.crypto.subtle.digest('SHA-256', buffer);
  return [...new Uint8Array(hash)].map(byte => byte.toString(16).padStart(2, '0')).join('');
}

export interface DownloadSpec {
  /** 产物 ID（GET /v1/artifacts/{artifactId}/content）。 */
  artifactId: ArtifactId;
  fileName: string;
  expectedBytes: number | null;
  expectedDigest: string;
}

export interface DownloadResult {
  savedName: string;
  bytes: number;
  digestHex: string;
}

/** 拉取 → 复验（大小 + sha256）→ 触发浏览器保存。任何不一致都拒绝保存。 */
export async function downloadArtifact(transport: Transport, spec: DownloadSpec): Promise<DownloadResult> {
  if (spec.artifactId.trim() === '') {
    throw new DownloadRejection('empty_ref', '缺少产物 ID，已拒绝。');
  }
  if (spec.expectedBytes !== null && spec.expectedBytes > MAX_ARTIFACT_DOWNLOAD_BYTES) {
    throw new DownloadRejection('size_limit', '成果超过合同的 8 MiB 上限，已拒绝下载。');
  }
  const expected = parseSha256(spec.expectedDigest);
  if (expected === null) {
    throw new DownloadRejection('unverifiable', '交付摘要不是可复验的 sha256 形式，为避免冒充成果已拒绝保存。');
  }
  const blob = await transport.getArtifactContent(spec.artifactId);
  if (blob.size > MAX_ARTIFACT_DOWNLOAD_BYTES) {
    throw new DownloadRejection('size_limit', '实际下载内容超过合同的 8 MiB 上限，已拒绝保存。');
  }
  if (spec.expectedBytes !== null && blob.size !== spec.expectedBytes) {
    throw new DownloadRejection('size_mismatch', `下载大小（${blob.size} B）与产物清单（${spec.expectedBytes} B）不一致，已拒绝保存。`);
  }
  const digestHex = await sha256Hex(blob);
  if (digestHex !== expected) {
    throw new DownloadRejection('digest_mismatch', '下载内容摘要与交付摘要不一致，已拒绝保存（可能是错误或假冒成果）。');
  }
  const name = sanitizeFileName(spec.fileName);
  saveBlob(blob, name);
  return {savedName: name, bytes: blob.size, digestHex};
}

/** 浏览器落盘：objectURL + a[download]；失败由调用方展示。 */
export function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = name;
  anchor.rel = 'noopener';
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
