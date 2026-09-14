import {blobBytes, fetchVerifiedArtifact, parseSha256, sha256Hex} from './downloader';
import type {DownloadSpec} from './downloader';
import type {Transport} from '@/lib/transport/types';

export interface DeliveryFile {path: string; bytes: number; digest: string; content: string}
export class DeliveryFilesError extends Error {
  constructor() {super('不支持或无法核验此文件包。请保留原包下载；未提取或保存包内文件。');}
}
const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const closed = (value: Record<string, unknown>, fields: string[]) => Object.keys(value).length === fields.length && fields.every(key => Object.hasOwn(value, key));

/** 只解释已核验的通用文件包；不解析HTML、不猜扩展名、不授予业务验收。 */
export async function readDeliveryFiles(transport: Transport, spec: DownloadSpec): Promise<DeliveryFile[]> {
  const {blob} = await fetchVerifiedArtifact(transport, spec);
  let bundle: unknown;
  try {
    const buffer = await blobBytes(blob);
    bundle = JSON.parse(new TextDecoder('utf-8', {fatal: true}).decode(buffer));
  } catch {throw new DeliveryFilesError();}
  if (!object(bundle) || !closed(bundle, ['profile', 'scope', 'files']) || bundle.profile !== 'generic-files-delivery/v1' ||
      bundle.scope !== 'independently-reviewed-files-not-external-effects' || !Array.isArray(bundle.files) || bundle.files.length < 1 || bundle.files.length > 8) throw new DeliveryFilesError();
  const files: DeliveryFile[] = [], seen = new Set<string>();
  for (const value of bundle.files) {
    if (!object(value) || !closed(value, ['path', 'bytes', 'digest', 'content']) || typeof value.path !== 'string' ||
        !/^results\/[A-Za-z0-9][A-Za-z0-9_-]{0,127}\.md$/.test(value.path) || seen.has(value.path.toLowerCase()) ||
        !Number.isSafeInteger(value.bytes) || Number(value.bytes) < 1 || Number(value.bytes) > 8192 ||
        typeof value.digest !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(value.digest) || typeof value.content !== 'string') throw new DeliveryFilesError();
    const bytes = new TextEncoder().encode(value.content);
    if (bytes.length !== value.bytes || value.content.includes('\0') || !value.content.trim() ||
        new TextDecoder('utf-8', {fatal: true}).decode(bytes) !== value.content ||
        await sha256Hex(new Blob([bytes])) !== parseSha256(value.digest)) throw new DeliveryFilesError();
    seen.add(value.path.toLowerCase());
    files.push({path: value.path, bytes: Number(value.bytes), digest: value.digest, content: value.content});
  }
  return files;
}

/** 显式用户下载名只影响本地保存；拒绝路径及控制字符，不改内容或原引用。 */
export function validSaveName(name: string): boolean {
  return name.length > 0 && name.length <= 128 && name.trim() === name &&
    !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(name) &&
    !/[\x00-\x1f\x7f<>:"/\\|?*]/.test(name) && !name.startsWith('.') && !name.endsWith('.') &&
    new TextDecoder().decode(new TextEncoder().encode(name)) === name;
}
