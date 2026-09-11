// 真实loopback HTTP + 原生产HTTP handler + 浏览器transport/downloader模块。
// DOM保存动作由测试捕获；不是浏览器引擎点击验收，全部内容均为显式受控fixture。
import {afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi} from 'vitest';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {createHash, webcrypto} from 'node:crypto';
import {Blob as NodeBlob} from 'node:buffer';
import {createRequire} from 'node:module';
import {transferableAbortController} from 'node:util';
import {fireEvent, render, screen} from '@testing-library/react';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {createTransport, installToken, clearToken} from '../src/lib/transport/client';
import {downloadArtifact, MAX_ARTIFACT_DOWNLOAD_BYTES} from '../src/features/artifacts/downloader';
import {useTaskArtifacts} from '../src/features/artifacts/use-task-artifacts';
import {ArtifactsView} from '../src/features/artifacts/artifacts-view';
import {makeTask} from '../src/features/tasks/detail/testing/fixtures';

// 原Node handler必须由Node加载，不能让jsdom的URL转换改写其磁盘OpenAPI路径。
const nativeRequire = createRequire(`${process.cwd()}/e2e/artifact-download.test.tsx`);
const {createTaskApiHandler} = nativeRequire('../../../packages/task-api/http-handler.mjs');
const {TaskApiError} = nativeRequire('../../../packages/task-api/contract.mjs');

const token = 'fixture-only-artifact-http-not-real-secret';
const taskId = 'task-expected', artifactId = 'artifact-fixture';
let mode = 'ok', content = Buffer.from('受控fixture，不是业务报告'), transport: ReturnType<typeof createTransport>;
let handler: ReturnType<typeof createTaskApiHandler>;
const calls: string[] = [];
const saved: string[] = [];
const hash = (bytes: Uint8Array) => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
const manifest = () => ({id: artifactId, taskId, status: 'ready', kind: 'delivery', name: 'fixture.bin', mediaType: 'application/octet-stream', bytes: content.length, digest: hash(content), createdAt: '2026-09-11T00:00:00Z'});
const server = createServer((request, response) => {
  // 专门注入超限wire以验证浏览器侧拒存；普通场景均经原handler。
  if (mode === 'oversize-wire') { calls.push('oversize-wire'); response.writeHead(200, {'Content-Type': 'application/octet-stream'}); response.end(content); return; }
  void handler(request, response);
});
beforeAll(async () => {
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('fixture unavailable');
  const expectedHost = `127.0.0.1:${address.port}`;
  handler = createTaskApiHandler({token, expectedHost, application: async (request: {operation: string}) => {
    calls.push(request.operation);
    if (mode === 'missing') throw new TaskApiError('not_found');
    const artifact = manifest();
    if (request.operation === 'artifact.content') return {artifact, content: new Uint8Array(content)};
    if (request.operation === 'artifact.get') return {...artifact,
      ...(mode === 'wrong-task' ? {taskId: 'task-foreign'} : {}),
      ...(mode === 'wrong-id' ? {id: 'artifact-foreign'} : {}),
      ...(mode === 'bad-digest' ? {digest: `sha256:${'0'.repeat(64)}`} : {})};
    throw new TaskApiError('not_found');
  }});
  transport = createTransport({token, baseURL: `http://${expectedHost}`, fetchLike: (input, init) => {
    // jsdom与Node fetch的AbortSignal分属不同realm；保留取消语义跨realm桥接。
    const controller = transferableAbortController();
    const signal = init?.signal;
    if (signal?.aborted) controller.abort(signal.reason);
    else signal?.addEventListener('abort', () => controller.abort(signal.reason), {once: true});
    return fetch(input, {...init, signal: controller.signal});
  }});
});
afterAll(async () => { await new Promise<void>(resolve => server.close(() => resolve())); });
beforeEach(() => {
  mode = 'ok'; content = Buffer.from('受控fixture，不是业务报告'); calls.length = 0; saved.length = 0; installToken(token);
  vi.stubGlobal('crypto', webcrypto);
  // Node fetch也使用全局Blob；jsdom Blob没有arrayBuffer，使用真实Web Blob实现。
  vi.stubGlobal('Blob', NodeBlob);
  Object.defineProperty(URL, 'createObjectURL', {configurable: true, value: vi.fn(() => 'blob:fixture')});
  Object.defineProperty(URL, 'revokeObjectURL', {configurable: true, value: vi.fn()});
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { saved.push(this.download); });
});
afterEach(() => { clearToken(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function ResultProbe() {
  const query = useTaskArtifacts({taskId, artifactIds: [artifactId], transport, refetchInterval: false});
  return <>{query.isError ? <p data-testid="binding-error">{(query.error as {code?: string}).code}</p> : null}<ArtifactsView task={makeTask({id: taskId, artifactIds: [artifactId]})} leader={null} audit={null} artifacts={query.data ?? null} transport={transport} /></>;
}

describe('E17实际HTTP成果传输（隔离fixture）', () => {
  it('合法归属清单真实HTTP加载后，点击下载经复验才保存', async () => {
    render(<QueryClientProvider client={new QueryClient({defaultOptions: {queries: {retry: false}}})}><ResultProbe /></QueryClientProvider>);
    fireEvent.click(await screen.findByTestId('download-button'));
    expect(await screen.findByTestId('download-success')).toBeVisible();
    expect(calls).toEqual(['artifact.get', 'artifact.content']);
    expect(saved).toEqual(['fixture.bin']);
  });
  it.each([['wrong-task', 'artifact_binding_mismatch'], ['wrong-id', 'invalid_application_response'], ['missing', 'not_found']])('%s元数据不展示也不下载', async (scenario, code) => {
    mode = scenario!;
    render(<QueryClientProvider client={new QueryClient({defaultOptions: {queries: {retry: false}}})}><ResultProbe /></QueryClientProvider>);
    expect(await screen.findByTestId('binding-error')).toHaveTextContent(code!);
    expect(screen.queryByTestId('download-button')).toBeNull();
    expect(screen.queryByTestId('artifact-row')).toBeNull();
    expect(screen.queryByText('fixture.bin')).toBeNull();
    expect(calls).toEqual(['artifact.get']);
    expect(saved).toEqual([]);
  });
  it('实际HTTP内容404不触发保存', async () => {
    mode = 'missing';
    await expect(downloadArtifact(transport, {artifactId, fileName: 'fixture.bin', expectedBytes: content.length, expectedDigest: hash(content)})).rejects.toMatchObject({status: 404, code: 'not_found'});
    expect(saved).toEqual([]);
  });
  it('精确8MiB经真实HTTP读取、浏览器SHA256复算通过才保存', async () => {
    content = Buffer.alloc(MAX_ARTIFACT_DOWNLOAD_BYTES, 37);
    const metadata = await transport.getArtifact(artifactId, {expectedTaskId: taskId});
    const result = await downloadArtifact(transport, {artifactId, fileName: metadata.name, expectedBytes: metadata.bytes, expectedDigest: metadata.digest});
    expect(result.bytes).toBe(MAX_ARTIFACT_DOWNLOAD_BYTES);
    expect(`sha256:${result.digestHex}`).toBe(hash(content));
    expect(saved).toEqual(['fixture.bin']);
  });
  it('真实HTTP数据摘要与已取清单不同，浏览器拒绝保存', async () => {
    mode = 'bad-digest';
    const metadata = await transport.getArtifact(artifactId, {expectedTaskId: taskId});
    await expect(downloadArtifact(transport, {artifactId, fileName: metadata.name, expectedBytes: metadata.bytes, expectedDigest: metadata.digest})).rejects.toMatchObject({reason: 'digest_mismatch'});
    expect(saved).toEqual([]);
  });
  it('8MiB+1清单在网络请求前拒绝；超限wire也不能保存', async () => {
    content = Buffer.alloc(MAX_ARTIFACT_DOWNLOAD_BYTES + 1, 37);
    const spec = {artifactId, fileName: 'fixture.bin', expectedBytes: content.length, expectedDigest: hash(content)};
    await expect(downloadArtifact(transport, spec)).rejects.toMatchObject({reason: 'size_limit'});
    expect(calls).toEqual([]);
    mode = 'oversize-wire';
    await expect(downloadArtifact(transport, {...spec, expectedBytes: null})).rejects.toMatchObject({reason: 'size_limit'});
    expect(calls).toEqual(['oversize-wire']);
    expect(saved).toEqual([]);
  });
  it('原HTTP handler拒绝8MiB+1内容，不输出可信文件', async () => {
    content = Buffer.alloc(MAX_ARTIFACT_DOWNLOAD_BYTES + 1, 37);
    await expect(transport.getArtifactContent(artifactId)).rejects.toMatchObject({status: 503});
    expect(saved).toEqual([]);
  });
});
