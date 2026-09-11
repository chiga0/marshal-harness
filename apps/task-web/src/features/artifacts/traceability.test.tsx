import {afterEach, describe, expect, it, vi} from 'vitest';
import {renderHook, render, screen, within, waitFor, act} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
// @ts-expect-error Vitest 在 Node 执行；浏览器 tsconfig 不安装 Node 类型，原生 Blob 只用于测试真实字节。
import {Blob as NodeBlob} from 'node:buffer';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import contract from '../../../../../packages/task-api/openapi.json';
import {ApiError} from '@/lib/transport/types';
import {observedInputIds, checkArtifact, matchesContract} from './traceability';
import {useObservedInputs} from './use-observed-inputs';
import {useTaskArtifacts} from './use-task-artifacts';
import {ArtifactsView} from './artifacts-view';
import {sha256Hex} from './downloader';
import {makeArtifact, makeAudit, makeFakeTransport, makeLeader, makeTask, makeWorker, TASK_ID} from '../tasks/detail/testing/fixtures';

const input = (id = 'input-one') => makeArtifact({id, taskId: null, kind: 'input', name: 'sales.json'});
function audit(ids = ['input-one']) {
  return {...contract.components.schemas.Audit.examples[0], taskId: TASK_ID, workers: [makeWorker()],
    prompts: [{workerId: makeWorker().id, text: '', contextRefs: ids, source: 'unavailable'}]};
}
function harness() {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
  return {client, wrapper: ({children}: {children: ReactNode}) => <QueryClientProvider client={client}>{children}</QueryClientProvider>};
}
describe('精确审计输入关联与合同', () => {
  it('source unavailable 不等于无输入；跨Worker重复引用只拉一次；历史无observation合法', () => {
    const value = audit();
    value.workers.push(makeWorker({id: 'worker-two'}));
    value.prompts.push({...value.prompts[0]!, workerId: 'worker-two'});
    expect(observedInputIds(value, TASK_ID)).toEqual(['input-one']);
    expect(observedInputIds(null, TASK_ID)).toBeNull();
    expect(observedInputIds({...value, prompts: []}, TASK_ID)).toEqual([]);
  });
  it.each(['auditTask', 'workerTask', 'unlistedWorker', 'badId', 'duplicatePrompt', 'duplicateRef', 'missingRefs', 'extraField'])('拒绝关联反例 %s', variant => {
    const value = audit() as unknown as Record<string, any>;
    if (variant === 'auditTask') value.taskId = 'other';
    if (variant === 'workerTask') value.workers[0].taskId = 'other';
    if (variant === 'unlistedWorker') value.prompts[0].workerId = 'other';
    if (variant === 'badId') value.prompts[0].contextRefs = ['../other'];
    if (variant === 'duplicatePrompt') value.prompts.push(value.prompts[0]);
    if (variant === 'duplicateRef') value.prompts[0].contextRefs.push('input-one');
    if (variant === 'missingRefs') delete value.prompts[0].contextRefs;
    if (variant === 'extraField') value.prompts[0].arbitrary = 'input-one';
    expect(() => observedInputIds(value, TASK_ID)).toThrow();
  });
  it('原metadata-only观察可用，unavailable阶段不能带refs', () => {
    const value = audit();
    const observation = {stage: 'handed-off', coverage: 'metadata-only', promptDigest: 'sha256:' + 'a'.repeat(64), promptBytes: 100,
      inputDigest: 'sha256:' + 'b'.repeat(64), reservationDigest: 'sha256:' + 'c'.repeat(64), preparedAt: '2026-09-11T00:00:00Z',
      handedOffAt: '2026-09-11T00:00:01Z', policy: null, snapshot: null, previewTruncated: false};
    const withObservation = {...value, prompts: [{...value.prompts[0], observation}]};
    expect(observedInputIds(withObservation, TASK_ID)).toEqual(['input-one']);
    observation.stage = 'unavailable';
    expect(() => observedInputIds(withObservation, TASK_ID)).toThrow();
  });
  it.each([{id: 'wrong'}, {taskId: TASK_ID}, {kind: 'evidence'}, {bytes: -1}, {bytes: 0.5}, {bytes: 8388609}, {digest: 'bad'}])('输入元数据拒绝 %j', change => {
    expect(() => checkArtifact({...input(), ...change} as ReturnType<typeof input>, 'input-one', null, true)).toThrow();
  });
  it('Artifact 合同为8MiB，不把上传256KiB限制误用到读取', () => {
    expect(matchesContract(input(), 'Artifact')).toBe(true);
    expect(checkArtifact({...input(), bytes: 8388608}, 'input-one', null, true).bytes).toBe(8388608);
  });
});

describe('隔离读取与回执来源', () => {
  it.each([{id: 'wrong'}, {taskId: 'wrong'}, {taskId: null}, {kind: 'input'}])('发布回执同样拒绝错归属/类型 %j', async change => {
    const leader = makeLeader({publication: {actionId: 'action-p', status: 'succeeded', authorizationDigest: 'sha256:' + 'a'.repeat(64), receiptArtifactId: 'receipt'}});
    const {transport, calls} = makeFakeTransport({getArtifact: async () => makeArtifact({id: 'receipt', kind: 'evidence', ...change} as Parameters<typeof makeArtifact>[0])});
    const h = harness(); const view = renderHook(() => useTaskArtifacts({taskId: TASK_ID, artifactIds: [], leader, transport, refetchInterval: false}), {wrapper: h.wrapper});
    await waitFor(() => expect(view.result.current.isError).toBe(true));
    expect(view.result.current.data).toBeUndefined(); expect(calls.some(c => c.method === 'getArtifactContent')).toBe(false);
    view.unmount(); h.client.clear();
  });
  it('错审计不发制品请求；合法审计读取的输入必须为精确null归属', async () => {
    const {transport, calls} = makeFakeTransport({getArtifact: async () => input()});
    const h = harness();
    const view = renderHook(({value}) => useObservedInputs({taskId: TASK_ID, audit: value, transport, refetchInterval: false}), {wrapper: h.wrapper, initialProps: {value: {...audit(), taskId: 'wrong'}}});
    expect(view.result.current.referenceError).toBeTruthy(); expect(calls).toHaveLength(0);
    view.rerender({value: audit()});
    await waitFor(() => expect(view.result.current.entries?.[0]?.status).toBe('ok'));
    expect(calls.filter(call => call.method === 'getArtifact')).toHaveLength(1);
    view.rerender({value: {...audit(), taskId: 'wrong'}});
    expect(view.result.current.entries).toBeNull();
    view.unmount(); h.client.clear();
  });
  it('单项404/错绑如实占位；不请求无关联ID/内容', async () => {
    const {transport, calls} = makeFakeTransport({getArtifact: async id => {
      if (id === 'missing') throw new ApiError(404, 'not_found', '', null);
      if (id === 'wrong') return input('other');
      return input(id);
    }});
    const h = harness();
    const view = renderHook(() => useObservedInputs({taskId: TASK_ID, audit: audit(['good', 'missing', 'wrong']), transport, refetchInterval: false}), {wrapper: h.wrapper});
    await waitFor(() => expect(view.result.current.entries?.map(entry => entry.status)).toEqual(['ok', 'failed', 'failed']));
    expect(calls.some(call => call.method === 'getArtifactContent')).toBe(false);
    view.unmount(); h.client.clear();
  });
  it('超过100引用明确分批，最多4并发，不误当32输入限制', async () => {
    const value = audit(Array.from({length: 64}, (_, i) => 'input-' + i));
    value.workers.push(makeWorker({id: 'worker-two'}));
    value.prompts.push({...value.prompts[0]!, workerId: 'worker-two', contextRefs: Array.from({length: 64}, (_, i) => 'input-' + (i + 64))});
    let active = 0, peak = 0;
    const {transport} = makeFakeTransport({getArtifact: async id => {active++; peak = Math.max(peak, active); await Promise.resolve(); active--; return input(id);}});
    const h = harness(); const view = renderHook(() => useObservedInputs({taskId: TASK_ID, audit: value, transport, refetchInterval: false}), {wrapper: h.wrapper});
    await waitFor(() => expect(view.result.current.entries).toHaveLength(100));
    expect(view.result.current.total).toBe(128); expect(peak).toBeLessThanOrEqual(4);
    act(() => view.result.current.loadMore());
    await waitFor(() => expect(view.result.current.entries).toHaveLength(128));
    view.unmount(); h.client.clear();
  });
  it('切Task与清连接缓存后迟到输入不进入新Task', async () => {
    let finish!: (value: ReturnType<typeof input>) => void;
    const {transport} = makeFakeTransport({getArtifact: async () => new Promise(resolve => {finish = resolve;})});
    const h = harness(); const view = renderHook(({taskId}) => useObservedInputs({taskId, audit: audit(), transport, refetchInterval: false}), {wrapper: h.wrapper, initialProps: {taskId: TASK_ID}});
    await waitFor(() => expect(finish).toBeDefined());
    act(() => h.client.clear()); view.rerender({taskId: 'other-task'});
    await act(async () => finish(input()));
    expect(view.result.current.entries).toBeNull();
    expect(h.client.getQueryCache().getAll().some(query => query.queryKey.includes(TASK_ID))).toBe(false);
    view.unmount(); h.client.clear();
  });
  it('Leader回执去重、同Task校验与来源并存，串LeaderTask不发请求', async () => {
    const leader = makeLeader({publication: {actionId: 'action-p', status: 'succeeded', authorizationDigest: 'sha256:' + 'a'.repeat(64), receiptArtifactId: 'receipt'},
      postverify: {actionId: 'action-v', status: 'succeeded', evidenceArtifactId: 'receipt'}});
    const {transport, calls} = makeFakeTransport({getArtifact: async id => makeArtifact({id, kind: 'evidence'})});
    const h = harness(); const view = renderHook(({value}) => useTaskArtifacts({taskId: TASK_ID, artifactIds: ['receipt'], leader: value, transport, refetchInterval: false}), {wrapper: h.wrapper, initialProps: {value: leader}});
    await waitFor(() => expect(view.result.current.data?.[0]?.sources).toEqual(['Task 成果', '发布回执', '发布后验证据']));
    expect(calls.filter(c => c.method === 'getArtifact')).toHaveLength(1);
    view.rerender({value: {...leader, taskId: 'wrong'}});
    await waitFor(() => expect(view.result.current.isError).toBe(true));
    expect(calls.filter(c => c.method === 'getArtifact')).toHaveLength(1);
    view.unmount(); h.client.clear();
  });
});

describe('观测输入实际下载入口', () => {
  afterEach(() => {vi.restoreAllMocks(); delete (URL as unknown as {createObjectURL?: unknown}).createObjectURL; delete (URL as unknown as {revokeObjectURL?: unknown}).revokeObjectURL;});
  it.each(['ready', 'partial', 'unavailable', 'badDigest', 'badSize', '404'] as const)('输入 %s 的可用性与保存事实', async mode => {
    // 使用有真实 arrayBuffer 的 Blob；jsdom Blob 传给 Node Response 会变成同一个字符串，不能验证同长篡改。
    const blob = (text: string) => new NodeBlob([text]) as Blob;
    const content = 'controlled-input', digest = 'sha256:' + await sha256Hex(blob(content));
    const artifact = {...input(), bytes: content.length, digest, status: mode === 'partial' || mode === 'unavailable' ? mode : 'ready' as const};
    const saved: string[] = [];
    Object.defineProperty(URL, 'createObjectURL', {configurable: true, value: vi.fn(() => 'blob:test')});
    Object.defineProperty(URL, 'revokeObjectURL', {configurable: true, value: vi.fn()});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {saved.push(this.download);});
    const {transport, calls} = makeFakeTransport({getArtifact: async () => {
      if (mode === '404') throw new ApiError(404, 'not_found', '不可用', null); return artifact;
    }, getArtifactContent: async () => blob(mode === 'badDigest' ? 'x'.repeat(content.length) : mode === 'badSize' ? 'x' : content)});
    function Page() {
      const observed = useObservedInputs({taskId: TASK_ID, audit: audit(), transport, refetchInterval: false});
      return <ArtifactsView task={makeTask()} leader={makeLeader()} audit={makeAudit()} artifacts={[]} observedInputs={observed} transport={transport} />;
    }
    const h = harness(), view = render(<Page />, {wrapper: h.wrapper});
    const card = screen.getByTestId('observed-inputs');
    if (mode === '404') {
      await waitFor(() => expect(card).toHaveTextContent('not_found'));
      expect(within(card).queryByTestId('download-button')).toBeNull();
    } else {
      await within(card).findByText('sales.json');
      expect(card).toHaveTextContent('不是完整原始输入清单');
      if (mode === 'partial' || mode === 'unavailable') {
        expect(within(card).queryByTestId('download-button')).toBeNull();
        expect(calls.some(c => c.method === 'getArtifactContent')).toBe(false);
      } else {
        await userEvent.setup().click(within(card).getByTestId('download-button'));
        if (mode === 'ready') {await within(card).findByTestId('download-success'); expect(saved).toEqual(['sales.json']);}
        else {await within(card).findByTestId('download-rejected'); expect(saved).toEqual([]);}
      }
    }
    view.unmount(); h.client.clear();
  });
});
