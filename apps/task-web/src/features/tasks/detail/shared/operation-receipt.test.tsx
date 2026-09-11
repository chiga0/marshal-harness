import {describe, expect, it, vi} from 'vitest';
import {act, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError, type OperationRecord} from '@/lib/transport/types';
import {OperationReceipt, TaskOperationReceipts} from './operation-receipt';
import {rememberOperation, SESSION_OPERATIONS_KEY, SESSION_OPERATIONS_LIMIT} from '@/lib/queries/operations';
import {makeFakeTransport} from './test-fakes';

const receipt: OperationRecord = {id: 'op-1', taskId: 'task-1', kind: 'task.cancel', status: 'accepted', taskRevision: 7, createdAt: '2026-09-11T00:00:00Z', updatedAt: '2026-09-11T00:00:00Z'};
function show(result: unknown, getOperation: (id: string) => Promise<OperationRecord>, kind: OperationRecord['kind'] = 'task.cancel') {
  const {transport} = makeFakeTransport({getOperation});
  return render(<QueryClientProvider client={new QueryClient({defaultOptions: {queries: {retry: false}}})}><OperationReceipt result={result} taskId="task-1" kind={kind} transport={transport} /></QueryClientProvider>);
}

describe('原 Operation 回执查询（E20）', () => {
  it('原动作移除后活动回执独立展示、跨Task隔离且缓存清理后消失', async () => {
    const client = new QueryClient();
    const {transport} = makeFakeTransport({getOperation: async () => ({...receipt, status: 'succeeded'})});
    rememberOperation(client, receipt);
    const node = (taskId: string) => <QueryClientProvider client={client}><TaskOperationReceipts taskId={taskId} transport={transport} /></QueryClientProvider>;
    const view = render(node('other'));
    expect(screen.queryByTestId('operation-receipt')).toBeNull();
    view.rerender(node('task-1'));
    await waitFor(() => expect(screen.getByTestId('operation-receipt')).toHaveTextContent('操作成功'));
    expect(screen.getByText(/不是服务端全局历史/)).toBeInTheDocument();
    view.unmount();
    client.clear();
    render(node('task-1'));
    expect(screen.queryByTestId('operation-receipt')).toBeNull();
  });
  it('连接内存最多保留20条原回执，重复ID去重', () => {
    const client = new QueryClient();
    for (let i = 0; i < 25; i++) rememberOperation(client, {...receipt, id: `op-${i}`});
    rememberOperation(client, {...receipt, id: 'op-24'});
    const records = client.getQueryData<OperationRecord[]>(SESSION_OPERATIONS_KEY)!;
    expect(records).toHaveLength(SESSION_OPERATIONS_LIMIT);
    expect(records[0]!.id).toBe('op-5');
    expect(new Set(records.map(op => op.id)).size).toBe(20);
    act(() => client.clear());
  });
  it('受理后查询原ID展示失败码，不把受理当成功且无重派按钮', async () => {
    const read = vi.fn(async (_id: string) => ({...receipt, status: 'failed' as const, code: 'cancel_failed', updatedAt: '2026-09-11T00:00:01Z'}));
    show(receipt, read);
    await screen.findByText('结果码：cancel_failed');
    expect(screen.getByTestId('operation-receipt')).toHaveTextContent('操作失败');
    expect(read.mock.calls[0]?.[0]).toBe('op-1');
    expect(screen.queryByRole('button', {name: /重派|重新执行/})).toBeNull();
  });
  it('读取失败保留原回执和requestId，显式刷新仍查同ID', async () => {
    const read = vi.fn().mockRejectedValueOnce(new ApiError(504, 'request_timeout', '超时', 'req-op')).mockResolvedValueOnce({...receipt, status: 'succeeded'});
    show(receipt, read);
    await screen.findByTestId('error-request-id');
    expect(screen.getByTestId('operation-receipt')).toHaveTextContent('op-1');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-op');
    await userEvent.setup().click(screen.getByRole('button', {name: '刷新原操作回执'}));
    await waitFor(() => expect(screen.getByTestId('operation-receipt')).toHaveTextContent('操作成功'));
    expect(read.mock.calls.map(call => call[0])).toEqual(['op-1', 'op-1']);
  });
  it('问题AnswerReceipt提取内嵌operation，成功明确不是Worker ACK', async () => {
    const op = {...receipt, kind: 'task.answer' as const, status: 'succeeded' as const};
    show({operation: op}, async () => op, 'task.answer');
    await waitFor(() => expect(screen.getByTestId('operation-receipt')).toHaveTextContent('操作成功'));
    expect(screen.getByTestId('operation-receipt')).toHaveTextContent('不代表整个任务交付成功、发布后验通过或 Worker 已消费答复');
  });
  it('未知状态诚实呈现且只提供只读刷新', async () => {
    show(receipt, async () => ({...receipt, status: 'unknown'}));
    await waitFor(() => expect(screen.getByTestId('operation-receipt')).toHaveTextContent('操作结果未知'));
    expect(screen.queryByRole('button', {name: /重派/})).toBeNull();
  });
  it('串任务回执拒绝展示也不查询', () => {
    const read = vi.fn(async () => receipt);
    show({...receipt, taskId: 'other'}, read);
    expect(screen.getByRole('alert')).toHaveTextContent('未取得可核对的原 Operation');
    expect(read).not.toHaveBeenCalled();
  });
  it('后续查询串绑保留原回执', async () => {
    show(receipt, async () => ({...receipt, taskId: 'other', status: 'succeeded'}));
    await screen.findByTestId('error-notice');
    expect(screen.getByTestId('operation-receipt')).toHaveTextContent('已受理');
    expect(screen.getByTestId('operation-receipt')).not.toHaveTextContent('操作成功（');
  });
});
