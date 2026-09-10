import {beforeEach, describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {Transport} from '@/lib/transport/types';
import {TaskDetailLayout} from './task-detail-layout';
import {makeDetail, makeFakeTransport, makeLeader, makePlan, makeWorker, TASK_ID} from './testing/fixtures';

let fakeTransport: Transport;

vi.mock('@/features/connection/connection', () => ({
  useConnection: () => ({
    connected: true,
    state: 'ready',
    errorMessage: null,
    connect: vi.fn(),
    disconnect: vi.fn(),
    transport: fakeTransport,
  }),
  ConnectionProvider: ({children}: {children: React.ReactNode}) => <>{children}</>,
}));

function renderAt(path: string) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/tasks/:taskId/*" element={<TaskDetailLayout />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('任务详情装配（四个子视图 + 数据装载）', () => {
  beforeEach(() => {
    fakeTransport = makeFakeTransport({
      getTask: async () => makeDetail({status: 'awaiting-confirmation', allowedActions: ['approve', 'cancel'], plan: makePlan()}),
      getWorkers: async () => ({workers: [makeWorker()]}),
      getLeader: async () => makeLeader(),
      getPublications: async () => ({publications: []}),
    }).transport;
  });

  it('概览默认渲染：标题、状态徽标（中文+机器名）、计划确认入口', async () => {
    renderAt(`/tasks/${TASK_ID}`);
    expect(await screen.findByRole('heading', {name: '按窗口汇总两个地区的销售清单'})).toBeInTheDocument();
    expect(screen.getByTestId('machine-state')).toHaveTextContent('awaiting-confirmation');
    expect(await screen.findByTestId('plan-approve-open')).toBeInTheDocument();
    // 四个子视图导航
    for (const label of ['概览', '团队', '成果', '活动']) {
      expect(screen.getByRole('link', {name: label})).toBeInTheDocument();
    }
  });

  it('团队 tab 展示真实 Worker；活动 tab 在缺事件接口时如实不可用', async () => {
    const user = userEvent.setup();
    renderAt(`/tasks/${TASK_ID}`);
    await screen.findByTestId('plan-approve-open');
    await user.click(screen.getByRole('link', {name: '团队'}));
    expect(await screen.findByTestId('worker-row')).toHaveTextContent('east');
    await user.click(screen.getByRole('link', {name: '活动'}));
    expect(await screen.findByTestId('activity-unavailable')).toBeInTheDocument();
  });

  it('详情加载失败显示带 requestId 的错误卡', async () => {
    const {ApiError} = await import('@/lib/transport/types');
    fakeTransport = makeFakeTransport({
      getTask: async () => {
        throw new ApiError(503, 'application_unavailable', '暂不可用', 'req-detail-1');
      },
    }).transport;
    renderAt(`/tasks/${TASK_ID}`);
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('加载任务详情失败');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-detail-1');
  });
});
