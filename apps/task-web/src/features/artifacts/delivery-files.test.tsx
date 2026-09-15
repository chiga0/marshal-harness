import {afterEach, describe, expect, it, vi} from 'vitest';
import {act, cleanup, render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type {Transport} from '@/lib/transport/types';
import {sha256Hex} from './downloader';
import {readDeliveryFiles, validSaveName} from './delivery-files';
import {DeliveryFilesView} from './delivery-files-view';
import {makeArtifact, makeTask} from '../tasks/detail/testing/fixtures';
import {ArtifactsView} from './artifacts-view';

async function fixture(change?: (bundle: any) => void) {
  const content = '<!doctype html><html><body>真实文件</body></html>';
  const bundle = {profile: 'generic-files-delivery/v1', scope: 'independently-reviewed-files-not-external-effects',
    files: [{path: 'results/author.md', content, bytes: new TextEncoder().encode(content).length,
      digest: `sha256:${await sha256Hex(new Blob([content]))}`}]};
  change?.(bundle);
  const blob = new Blob([JSON.stringify(bundle)]);
  const spec = {artifactId: 'delivery', fileName: 'deliverables.json', expectedBytes: blob.size,
    expectedDigest: `sha256:${await sha256Hex(blob)}`};
  const transport = {getArtifactContent: vi.fn(async () => blob)} as unknown as Transport;
  return {bundle, blob, spec, transport};
}
afterEach(() => {cleanup(); vi.restoreAllMocks();});
describe('通用文件包提取（原包与逐文件双重核验）', () => {
  it('保留原名、UTF8原内容和摘要；不执行HTML', async () => {
    const f = await fixture();
    expect(await readDeliveryFiles(f.transport, f.spec)).toEqual(f.bundle.files);
    expect(document.querySelector('iframe')).toBeNull();
  });
  it.each([
    ['未知profile', (b: any) => {b.profile = 'other';}],
    ['扩大scope', (b: any) => {b.scope = 'external';}],
    ['多余字段', (b: any) => {b.files[0].url = 'https://evil';}],
    ['穿越路径', (b: any) => {b.files[0].path = 'results/../x.md';}],
    ['猜测扩展名', (b: any) => {b.files[0].path = 'results/a.html';}],
    ['重复路径', (b: any) => {b.files.push({...b.files[0], path: 'results/AUTHOR.md'});}],
    ['字节不符', (b: any) => {b.files[0].bytes++;}],
    ['摘要不符', (b: any) => {b.files[0].digest = `sha256:${'0'.repeat(64)}`;}],
    ['超限', (b: any) => {b.files[0].bytes = 8193;}],
    ['空列表', (b: any) => {b.files = [];}],
    ['文件数超限', (b: any) => {b.files = Array.from({length: 9}, (_, i) => ({...b.files[0], path: `results/a${i}.md`}));}],
    ['代理字符', (b: any) => {b.files[0].content = '\ud800'; b.files[0].bytes = 3;}],
  ])('%s拒绝且不保存', async (_name, change) => {
    const f = await fixture(change);
    await expect(readDeliveryFiles(f.transport, f.spec)).rejects.toThrow();
  });
  it('原包摘要不匹配先拒绝', async () => {
    const f = await fixture();
    await expect(readDeliveryFiles(f.transport, {...f.spec, expectedDigest: `sha256:${'0'.repeat(64)}`})).rejects.toThrow();
  });
  it.each(['../todo.html', 'a/b', 'a\\b', 'x\n.html', 'CON.html', 'nul', 'LPT1.txt', '.hidden', 'name.', ''])('拒绝保存名%j', name => {
    expect(validSaveName(name)).toBe(false);
  });
  it('支持显式普通中文/HTML名称', () => {expect(validSaveName('待办.html')).toBe(true);});
  it('点击才拉取，默认原名，显式改名下载原字节，不渲染HTML', async () => {
    const f = await fixture(); const saved: string[] = []; const blobs: Blob[] = [];
    Object.defineProperty(URL, 'createObjectURL', {configurable: true, value: vi.fn((b: Blob) => {blobs.push(b); return 'blob:test';})});
    Object.defineProperty(URL, 'revokeObjectURL', {configurable: true, value: vi.fn()});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function(this: HTMLAnchorElement) {saved.push(this.download);});
    render(<DeliveryFilesView artifact={makeArtifact({id: f.spec.artifactId, name: f.spec.fileName, bytes: f.spec.expectedBytes, digest: f.spec.expectedDigest})} transport={f.transport} />);
    expect(f.transport.getArtifactContent).not.toHaveBeenCalled();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', {name: '查看包内文件'}));
    const input = await screen.findByLabelText('另存文件名');
    expect(input).toHaveValue('author.md');
    await user.click(screen.getByText('更改保存文件名'));
    await user.clear(input); await user.type(input, '../bad');
    expect(screen.getByRole('button', {name: '下载此文件'})).toBeDisabled();
    await user.clear(input); await user.type(input, 'todo.html');
    await user.click(screen.getByRole('button', {name: '下载此文件'}));
    expect(saved).toEqual(['todo.html']);
    expect(blobs[0]!.type).toBe('application/octet-stream');
    expect(`sha256:${await sha256Hex(blobs[0]!)}`).toBe(f.bundle.files[0]!.digest);
    expect(screen.queryByText('真实文件')).toBeNull();
  });
  it.each(['digest', 'id', 'unavailable', 'metadata_failure'])('当前产物变为%s立即清除旧文件', async change => {
    const f = await fixture();
    const artifact = makeArtifact({id: f.spec.artifactId, kind: 'delivery', status: 'ready', name: f.spec.fileName, bytes: f.spec.expectedBytes, digest: f.spec.expectedDigest});
    const view = (a: typeof artifact | null) => <ArtifactsView task={makeTask()} leader={null} audit={null} transport={f.transport} artifacts={a ? [{status: 'ok', artifact: a}] : [{status: 'failed', id: artifact.id, error: new Error('not_available')}]} />;
    const {rerender} = render(view(artifact));
    await userEvent.click(screen.getByRole('button', {name: '查看包内文件'}));
    await screen.findByLabelText('另存文件名');
    rerender(view(change === 'metadata_failure' ? null : {...artifact, ...(change === 'id' ? {id: 'different'} : change === 'digest' ? {digest: `sha256:${'1'.repeat(64)}`} : {status: 'unavailable' as const})}));
    expect(screen.queryByRole('button', {name: '下载此文件'})).toBeNull();
    expect(screen.queryByLabelText('另存文件名')).toBeNull();
  });
  it('迟到原包响应不能复活已不可用的下载', async () => {
    const f = await fixture(); let finish!: (value: Blob) => void;
    const transport = {getArtifactContent: () => new Promise<Blob>(resolve => {finish = resolve;})} as unknown as Transport;
    const artifact = makeArtifact({id: f.spec.artifactId, kind: 'delivery', status: 'ready', name: f.spec.fileName, bytes: f.spec.expectedBytes, digest: f.spec.expectedDigest});
    const view = (status: 'ready' | 'unavailable') => <ArtifactsView task={makeTask()} leader={null} audit={null} transport={transport} artifacts={[{status: 'ok', artifact: {...artifact, status}}]} />;
    const {rerender} = render(view('ready'));
    await userEvent.click(screen.getByRole('button', {name: '查看包内文件'}));
    rerender(view('unavailable'));
    await act(async () => {finish(f.blob); await new Promise(resolve => setTimeout(resolve, 20));});
    expect(screen.queryByRole('button', {name: '下载此文件'})).toBeNull();
    expect(screen.queryByLabelText('另存文件名')).toBeNull();
  });
});
