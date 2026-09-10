import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError, type Transport} from '@/lib/transport/types';
import {WorkersView} from './workers-view';
import {callsOf, makeFakeTransport, makeTask, makeWorker, TASK_ID, WORKER_ID} from '../tasks/detail/testing/fixtures';

function renderView(workers = [makeWorker()], overrides: Partial<Transport> = {}) {
  const {transport, calls} = makeFakeTransport(overrides);
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  const task = makeTask({revision: 7});
  const utils = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/tasks/${TASK_ID}/team`]}>
        <Routes>
          <Route path="/tasks/:taskId/team/*" element={<WorkersView task={task} workers={workers} transport={transport} onChanged={() => {}} />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return {transport, calls, ...utils};
}

describe('团队视图（P08 / E23）', () => {
  it('列表展示节点/角色/状态/阶段/尝试/最近观察/进展摘要/用量，无进度百分比假数据', () => {
    renderView();
    const row = screen.getByTestId('worker-row');
    expect(row).toHaveTextContent('east');
    expect(row).toHaveTextContent('执行');
    expect(row).toHaveTextContent('运行中');
    expect(row).toHaveTextContent('agent.running');
    // attempt=1 单独成列
    expect(row).toHaveTextContent('1');
    // usage.source=unavailable 如实「不可用」（E23）
    expect(within(row).getByTestId('worker-usage-cell')).toHaveTextContent('不可用');
    expect(screen.getByText(/不代表模型仍在持续工作/)).toBeInTheDocument();
    expect(row.textContent).not.toContain('%');
  });

  it('点击行打开抽屉明细：用量不可用如实显示，审计字段如实空态', async () => {
    renderView();
    const user = userEvent.setup();
    await user.click(screen.getByText('east'));
    const drawer = await screen.findByTestId('worker-drawer');
    expect(within(drawer).getByTestId('usage-unavailable')).toHaveTextContent('不可用');
    expect(within(drawer).getByText('pi')).toBeInTheDocument();
    expect(within(drawer).getByText(/最近观察时间不代表模型仍在持续工作/)).toBeInTheDocument();
    // 尝试次数：合同字段 attempt（单数）
    expect(within(drawer).getByText('尝试')).toBeInTheDocument();
  });

  it('单 Worker 取消走 cancelWorker（workerId + 任务 revision + 幂等键），与取消任务严格分开', async () => {
    const {calls} = renderView();
    const user = userEvent.setup();
    await user.click(screen.getByText('east'));
    const drawer = await screen.findByTestId('worker-drawer');
    await user.click(within(drawer).getByTestId('cancel-worker-open'));
    const dialog = await screen.findByRole('dialog', {name: /取消 Worker/});
    expect(dialog).toHaveTextContent('只请求取消这一个 Worker');
    expect(dialog).toHaveTextContent('不会取消整个任务');
    await user.click(within(dialog).getByRole('button', {name: '确认取消该 Worker'}));
    await waitFor(() => expect(callsOf(calls, 'cancelWorker')).toHaveLength(1));
    const [workerId, body] = callsOf(calls, 'cancelWorker')[0]!.args as [string, {expectedRevision: number; idempotencyKey: string}];
    expect(workerId).toBe(WORKER_ID);
    // expectedRevision 来自所属 Task 的当前 revision（task.revision=7）
    expect(body.expectedRevision).toBe(7);
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    // 绝不回退为取消整 Task
    expect(callsOf(calls, 'cancelTask')).toHaveLength(0);
    expect(within(drawer).getByTestId('cancel-worker-accepted')).toHaveTextContent('受理不代表该 Worker 已停止');
  });

  it('501 不支持单 Worker 取消：如实提示且不回退取消整 Task（E13）', async () => {
    const {calls} = renderView([makeWorker()], {
      cancelWorker: vi.fn(async () => {
        throw new ApiError(501, 'unsupported_operation', '该能力未启用', 'req-501-1');
      }) as Transport['cancelWorker'],
    });
    const user = userEvent.setup();
    await user.click(screen.getByText('east'));
    const drawer = await screen.findByTestId('worker-drawer');
    await user.click(within(drawer).getByTestId('cancel-worker-open'));
    const dialog = await screen.findByRole('dialog', {name: /取消 Worker/});
    await user.click(within(dialog).getByRole('button', {name: '确认取消该 Worker'}));
    expect(await screen.findByTestId('cancel-worker-unsupported')).toHaveTextContent('不会用「取消整个任务」代替执行');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-501-1');
    expect(callsOf(calls, 'cancelTask')).toHaveLength(0);
  });

  it('终态 Worker 不再提供取消入口', async () => {
    renderView([makeWorker({status: 'completed', finishedAt: '2026-09-10T02:00:00.000Z'})]);
    const user = userEvent.setup();
    await user.click(screen.getByText('east'));
    const drawer = await screen.findByTestId('worker-drawer');
    expect(within(drawer).getByText(/已到达终态/)).toBeInTheDocument();
    expect(within(drawer).queryByTestId('cancel-worker-open')).toBeNull();
  });
});
