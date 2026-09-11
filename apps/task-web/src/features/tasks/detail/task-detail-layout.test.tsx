import {beforeEach, describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError, type Transport} from '@/lib/transport/types';
import {TaskDetailLayout} from './task-detail-layout';
import {
  callsOf, makeAudit, makeFakeTransport, makeLeader, makePlan, makeQuestions, makeTask, makeWorker, TASK_ID,
  type TransportCalls,
} from './testing/fixtures';

let fakeTransport: Transport;
let fakeCalls: TransportCalls;

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
  it('任务图路由消费 graph 投影，不以当前计划推导执行状态', async () => {
    const fake = makeFakeTransport({getGraph: async taskId => ({taskId, planRevision: 1,
      nodes: [{id: 'actual-node', role: 'verifier', status: 'unknown', workerIds: []}], edges: []})});
    fakeTransport = fake.transport;
    renderAt(`/tasks/${TASK_ID}/graph`);
    expect(await screen.findByRole('button', {name: '查看节点 actual-node，校验，状态未知'})).toBeInTheDocument();
    expect(callsOf(fake.calls, 'getGraph')[0]!.args[0]).toBe(TASK_ID);
    expect(screen.getByRole('link', {name: '任务图'})).toHaveAttribute('aria-current', 'page');
  });
  beforeEach(() => {
    const fake = makeFakeTransport({
      getTask: async () => makeTask({
        status: 'awaiting-confirmation',
        allowedActions: ['approve', 'cancel'],
        plan: {revision: 1, digest: 'sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890'},
      }),
      getWorkers: async taskId => ({items: [makeWorker()], nextCursor: null, taskId}),
      getPlan: async () => makePlan(),
      getQuestions: async () => makeQuestions(),
      getLeader: async () => makeLeader(),
    });
    fakeTransport = fake.transport;
    fakeCalls = fake.calls;
  });

  it('概览默认渲染：标题、状态徽标（中文+机器名）、计划确认入口', async () => {
    renderAt(`/tasks/${TASK_ID}`);
    expect(await screen.findByRole('heading', {name: '按窗口汇总东、西两个地区的销售清单'})).toBeInTheDocument();
    // 任务状态徽标在页头（验收面板等也会渲染各自的 machine-state，故限定首个）
    expect(screen.getAllByTestId('machine-state')[0]).toHaveTextContent('awaiting-confirmation');
    expect(await screen.findByTestId('plan-approve-open')).toBeInTheDocument();
    // 四个子视图导航
    for (const label of ['概览', '团队', '成果', '活动']) {
      expect(screen.getByRole('link', {name: label})).toBeInTheDocument();
    }
  });

  it('团队 tab 展示真实 Worker（workers.items）；活动 tab 空事件流如实显示', async () => {
    const user = userEvent.setup();
    renderAt(`/tasks/${TASK_ID}`);
    await screen.findByTestId('plan-approve-open');
    await user.click(screen.getByRole('link', {name: '团队'}));
    expect(await screen.findByTestId('worker-row')).toHaveTextContent('east');
    await user.click(screen.getByRole('link', {name: '活动'}));
    const activity = await screen.findByTestId('activity-view');
    expect(activity).toHaveTextContent('暂无事件');
  });

  it('plan/questions 404 不冒充页面错误：其余投影照常可用', async () => {
    const fake = makeFakeTransport({
      getPlan: async () => {
        throw new ApiError(404, 'plan_not_frozen', '计划尚未冻结', 'req-plan-1');
      },
      getQuestions: async () => {
        throw new ApiError(404, 'questions_unavailable', '问题流不可用', 'req-q-1');
      },
    });
    fakeTransport = fake.transport;
    fakeCalls = fake.calls;
    renderAt(`/tasks/${TASK_ID}`);
    expect(await screen.findByRole('heading', {name: '按窗口汇总东、西两个地区的销售清单'})).toBeInTheDocument();
    // plan/questions 的失败只在对应视图如实降级，不出现页面级错误卡
    expect(screen.queryByTestId('error-notice')).toBeNull();
    // workers/leader 正常拉取
    await waitFor(() => expect(callsOf(fakeCalls, 'getWorkers')).not.toHaveLength(0));
    await waitFor(() => expect(callsOf(fakeCalls, 'getLeader')).not.toHaveLength(0));
  });

  it('刷新按钮按组键全部失效：六个投影都会重拉（含 audit 验收）', async () => {
    const user = userEvent.setup();
    renderAt(`/tasks/${TASK_ID}`);
    await screen.findByTestId('plan-approve-open');
    await user.click(screen.getByTestId('detail-refresh'));
    await waitFor(() => {
      for (const method of ['getTask', 'getWorkers', 'getPlan', 'getQuestions', 'getLeader', 'getAudit']) {
        expect(callsOf(fakeCalls, method).length).toBeGreaterThanOrEqual(2);
      }
    });
  });

  it('详情加载失败显示带 requestId 的错误卡', async () => {
    const fake = makeFakeTransport({
      getTask: async () => {
        throw new ApiError(503, 'application_unavailable', '暂不可用', 'req-detail-1');
      },
    });
    fakeTransport = fake.transport;
    fakeCalls = fake.calls;
    renderAt(`/tasks/${TASK_ID}`);
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('加载任务详情失败');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-detail-1');
  });

  it('成果路由使用 audit 独立验收，Leader 请求失败不遮蔽通过证据', async () => {
    const fake = makeFakeTransport({
      getLeader: async () => { throw new ApiError(503, 'leader_unavailable', '不可用', 'req-leader'); },
      getAudit: async () => makeAudit({acceptance: {status: 'passed', digest: 'sha256:audit-proof', evidenceIds: ['ev-acceptance']}}),
    });
    fakeTransport = fake.transport;
    renderAt(`/tasks/${TASK_ID}/artifacts`);
    await waitFor(() => expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('sha256:audit-proof'));
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('passed');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('ev-acceptance');
    expect(screen.getByTestId('verification-unavailable')).toBeInTheDocument();
  });

  it('成果路由 audit 请求失败如实显示不可用，不推断验收状态', async () => {
    const fake = makeFakeTransport({getAudit: async () => { throw new ApiError(503, 'audit_unavailable', '不可用', 'req-audit'); }});
    fakeTransport = fake.transport;
    renderAt(`/tasks/${TASK_ID}/artifacts`);
    expect(await screen.findByTestId('acceptance-unloaded')).toHaveTextContent('audit 投影不可用');
    await waitFor(() => expect(callsOf(fake.calls, 'getAudit')).toHaveLength(1));
  });
});
