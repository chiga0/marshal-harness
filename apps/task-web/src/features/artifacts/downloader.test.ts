import {afterEach, describe, expect, it, vi} from 'vitest';
import type {Transport} from '@/lib/transport/types';
import {downloadArtifact, DownloadRejection, parseSha256, sanitizeFileName, sha256Hex} from './downloader';

function stubSaving() {
  const clicked: string[] = [];
  Object.defineProperty(URL, 'createObjectURL', {configurable: true, value: vi.fn(() => 'blob:fake-url')});
  Object.defineProperty(URL, 'revokeObjectURL', {configurable: true, value: vi.fn()});
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push(this.download);
  });
  return clicked;
}

afterEach(() => {
  delete (URL as unknown as {createObjectURL?: unknown}).createObjectURL;
  delete (URL as unknown as {revokeObjectURL?: unknown}).revokeObjectURL;
  vi.restoreAllMocks();
});

async function digestOf(content: string): Promise<string> {
  return `sha256:${await sha256Hex(new Blob([content]))}`;
}

describe('下载保护（E16/E17 本机落盘与防假冒）', () => {
  it('sanitizeFileName 去掉目录穿越与危险字符', () => {
    expect(sanitizeFileName('../../etc/passwd')).toBe('passwd');
    expect(sanitizeFileName('a/b/c.txt')).toBe('c.txt');
    expect(sanitizeFileName('..\\..\\win.txt')).toBe('win.txt');
    expect(sanitizeFileName('')).toBe('artifact-download');
    expect(sanitizeFileName('报告 最终.md')).toBe('报告 最终.md');
  });

  it('parseSha256 只接受 sha256: 64hex', () => {
    expect(parseSha256(`sha256:${'a'.repeat(64)}`)).toBe('a'.repeat(64));
    expect(parseSha256('md5:abc')).toBeNull();
    expect(parseSha256('sha256:xyz')).toBeNull();
  });

  it('摘要复验通过才保存', async () => {
    const clicked = stubSaving();
    const content = 'hello delivery';
    const transport = {getArtifactBearer: vi.fn(async () => new Blob([content]))} as unknown as Transport;
    const result = await downloadArtifact(transport, {
      ref: 'sha256:ignored-ref',
      fileName: 'result/final.txt',
      expectedBytes: content.length,
      expectedDigest: await digestOf(content),
    });
    expect(result.savedName).toBe('final.txt');
    expect(clicked).toEqual(['final.txt']);
  });

  it('摘要不一致拒绝保存，不落成可信成果', async () => {
    const clicked = stubSaving();
    const transport = {getArtifactBearer: vi.fn(async () => new Blob(['tampered']))} as unknown as Transport;
    await expect(downloadArtifact(transport, {
      ref: 'ref', fileName: 'a.txt', expectedBytes: null, expectedDigest: `sha256:${'0'.repeat(64)}`,
    })).rejects.toMatchObject({name: 'DownloadRejection', reason: 'digest_mismatch'});
    expect(clicked).toEqual([]);
  });

  it('大小不一致拒绝保存', async () => {
    const clicked = stubSaving();
    const content = 'abcd';
    const transport = {getArtifactBearer: vi.fn(async () => new Blob([content]))} as unknown as Transport;
    await expect(downloadArtifact(transport, {
      ref: 'ref', fileName: 'a.txt', expectedBytes: 999, expectedDigest: await digestOf(content),
    })).rejects.toMatchObject({reason: 'size_mismatch'});
    expect(clicked).toEqual([]);
  });

  it('摘要格式不可复验时拒绝保存（不冒充成果）', async () => {
    const clicked = stubSaving();
    const transport = {getArtifactBearer: vi.fn(async () => new Blob(['x']))} as unknown as Transport;
    await expect(downloadArtifact(transport, {
      ref: 'ref', fileName: 'a.txt', expectedBytes: null, expectedDigest: 'not-a-digest',
    })).rejects.toBeInstanceOf(DownloadRejection);
    expect(clicked).toEqual([]);
  });
});
