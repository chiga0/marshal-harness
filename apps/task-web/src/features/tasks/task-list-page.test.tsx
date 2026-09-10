import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {TaskListView} from './task-list-page';
import {ApiError} from '../../lib/transport/types';
import type {TaskRecord, Transport, TasksResponse} from '../../lib/transport/types';

function makeTask(partial: {id: string; intent: string} & Partial<TaskRecord>): TaskRecord {
  return {
    revision: 1,
    status: 'running',
    phase: 'execution',
    createdAt: '2026-09-10T01:00:00.000Z',
    updatedAt: '2026-09-10T02:00:00.000Z',
    allowedActions: [],
    plan: null,
    artifactIds: [],
    ...partial,
  };
}

function createTestQueryClient(): QueryClient {
  return new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
}

function renderList(transport: Transport, onReconnect?: () => void) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <MemoryRouter>
        <TaskListView transport={transport} {...(onReconnect !== undefined ? {onReconnect} : {})} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function transportWith(listTasks: Transport['listTasks']): Transport {
  return {
    createTask: async () => { throw new Error('not used'); },
    createInput: async () => { throw new Error('not used'); },
    listTasks,
    getTask: async () => { throw new Error('not used'); },
    getWorkers: async () => { throw new Error('not used'); },
    getPlan: async () => { throw new Error('not used'); },
    approvePlan: async () => { throw new Error('not used'); },
    getQuestions: async () => { throw new Error('not used'); },
    answerTask: async () => { throw new Error('not used'); },
    cancelTask: async () => { throw new Error('not used'); },
    pauseTask: async () => { throw new Error('not used'); },
    resumeTask: async () => { throw new Error('not used'); },
    cancelWorker: async () => { throw new Error('not used'); },
    getLeader: async () => { throw new Error('not used'); },
    leaderReply: async () => { throw new Error('not used'); },
    repair: async () => { throw new Error('not used'); },
    getEvents: async () => { throw new Error('not used'); },
    getArtifact: async () => { throw new Error('not used'); },
    getArtifactContent: async () => { throw new Error('not used'); },
  };
}

function rowOf(text: string): HTMLElement {
  const li = screen.getByText(text).closest('li');
  if (!li) throw new Error('找不到行：' + text);
  return li;
}

describe('TaskListView', () => {
  it('首次加载展示骨架行与状态播报', () => {
    const transport = transportWith(async () => new Promise<TasksResponse>(() => {}));
    renderList(transport);
    expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
    expect(screen.getByRole('status')).toHaveTextContent('正在加载任务列表');
  });

  it('渲染第一页：状态徽标、更新时间、待处理徽标与局部范围声明', async () => {
    const tasks = [
      makeTask({id: 'task-001', intent: '汇总东地区销售清单', status: 'running'}),
      makeTask({id: 'task-002', intent: '生成两地区对账报告', status: 'awaiting-answer', deadlineAt: '2026-09-11T00:00:00.000Z'}),
      makeTask({id: 'task-003', intent: '归档上月交付', status: 'completed'}),
    ];
    renderList(transportWith(async () => ({items: tasks, nextCursor: null})));

    await screen.findByText('汇总东地区销售清单');
    expect(screen.getByText('生成两地区对账报告')).toBeInTheDocument();

    const runningRow = rowOf('汇总东地区销售清单');
    expect(within(runningRow).getByText('运行中')).toBeInTheDocument();
    expect(within(runningRow).queryByText('待处理')).toBeNull();
    expect(within(runningRow).getByText(/更新于/)).toBeInTheDocument();

    const awaitingRow = rowOf('生成两地区对账报告');
    expect(within(awaitingRow).getByText('等待回答')).toBeInTheDocument();
    expect(within(awaitingRow).getByText('待处理')).toBeInTheDocument();

    const doneRow = rowOf('归档上月交付');
    expect(within(doneRow).queryByText('待处理')).toBeNull();

    expect(screen.getByText(/只作用于已加载的 3 项任务，不是全局检索/)).toBeInTheDocument();

    // 待处理开关：只筛已加载的 awaiting-*
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', {name: /只看待处理（已加载 1 项）/}));
    expect(screen.queryByText('汇总东地区销售清单')).toBeNull();
    expect(screen.getByText('生成两地区对账报告')).toBeInTheDocument();
    expect(screen.getByRole('button', {name: /只看待处理/})).toHaveAttribute('aria-pressed', 'true');
  });

  it('空集合展示空状态与新建入口', async () => {
    renderList(transportWith(async () => ({items: [], nextCursor: null})));
    await screen.findByText('还没有任务。');
    expect(screen.getByRole('link', {name: '新建第一个任务'})).toHaveAttribute('href', '/tasks/new');
  });

  it('按 nextCursor 加载更多并按 id 去重，翻页到底给出说明', async () => {
    const user = userEvent.setup();
    const t1 = makeTask({id: 'task-001', intent: '任务甲'});
    const t2 = makeTask({id: 'task-002', intent: '任务乙'});
    const t2Refresh = makeTask({id: 'task-002', intent: '任务乙', status: 'completed'});
    const t3 = makeTask({id: 'task-003', intent: '任务丙'});
    const listTasks = vi.fn(async (options: {cursor?: string | null}) => {
      if (!options.cursor) return {items: [t1, t2], nextCursor: 'cursor-2'};
      return {items: [t2Refresh, t3], nextCursor: null};
    });
    renderList(transportWith(listTasks));

    await screen.findByText('任务甲');
    await user.click(screen.getByRole('button', {name: /加载更多（还有下一页/}));

    await screen.findByText('任务丙');
    expect(listTasks).toHaveBeenCalledTimes(2);
    expect(listTasks.mock.calls[1]?.[0]).toMatchObject({cursor: 'cursor-2', limit: 24});
    // task-002 在两页之间移动仍只出现一次
    expect(screen.getAllByText('任务乙')).toHaveLength(1);
    expect(screen.getByText('服务端没有更多分页。')).toBeInTheDocument();
  });

  it('局部筛选只作用于已加载项；无匹配时明确说明范围', async () => {
    const user = userEvent.setup();
    const tasks = [
      makeTask({id: 'task-001', intent: '汇总东地区销售清单', status: 'running'}),
      makeTask({id: 'task-002', intent: '生成两地区对账报告', status: 'awaiting-answer'}),
    ];
    renderList(transportWith(async () => ({items: tasks, nextCursor: 'cursor-2'})));
    await screen.findByText('汇总东地区销售清单');

    await user.type(screen.getByLabelText('筛选已加载任务'), '不存在的词');
    expect(await screen.findByText('已加载范围内没有匹配项')).toBeInTheDocument();
    expect(screen.getByText(/筛选只作用于已加载的 2 项；服务端可能还有更多任务/)).toBeInTheDocument();

    await user.clear(screen.getByLabelText('筛选已加载任务'));
    await user.type(screen.getByLabelText('筛选已加载任务'), '东地区');
    expect(screen.getByText('汇总东地区销售清单')).toBeInTheDocument();
    expect(screen.queryByText('生成两地区对账报告')).toBeNull();

    await user.clear(screen.getByLabelText('筛选已加载任务'));
    await user.selectOptions(screen.getByLabelText('状态'), 'cancelled');
    expect(await screen.findByText('已加载范围内没有匹配项')).toBeInTheDocument();
    await user.click(screen.getByRole('button', {name: '清除筛选'}));
    await screen.findByText('汇总东地区销售清单');
  });

  it('整页加载失败展示错误码与 requestId，可重试恢复', async () => {
    const user = userEvent.setup();
    const listTasks = vi.fn(async (): Promise<TasksResponse> => {
      throw new ApiError(500, 'server_error', '服务器内部错误', 'req-42');
    });
    renderList(transportWith(listTasks));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('任务列表加载失败');
    expect(alert).toHaveTextContent('server_error');
    expect(alert).toHaveTextContent('HTTP 500');
    expect(alert).toHaveTextContent('req-42');

    listTasks.mockResolvedValueOnce({items: [makeTask({id: 'task-001', intent: '恢复后的任务'})], nextCursor: null});
    await user.click(screen.getByRole('button', {name: '重试'}));
    await screen.findByText('恢复后的任务');
  });

  it('401 时给出凭据失效与重新连接出口', async () => {
    const user = userEvent.setup();
    const onReconnect = vi.fn();
    renderList(
      transportWith(async () => { throw new ApiError(401, 'unauthorized', '未授权', 'req-401'); }),
      onReconnect,
    );
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('凭据已失效（401）');
    await user.click(screen.getByRole('button', {name: '断开并重新连接'}));
    expect(onReconnect).toHaveBeenCalledTimes(1);
  });

  it('后台刷新失败保留已加载内容并显示局部警告', async () => {
    const user = userEvent.setup();
    const listTasks = vi.fn()
      .mockResolvedValueOnce({items: [makeTask({id: 'task-001', intent: '保留中的任务'})], nextCursor: null})
      .mockRejectedValueOnce(new ApiError(503, 'temporarily_unavailable', '暂时不可用', 'req-77'));
    renderList(transportWith(listTasks as Transport['listTasks']));

    await screen.findByText('保留中的任务');
    await user.click(screen.getByRole('button', {name: '立即刷新列表'}));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('自动刷新失败，已保留已加载内容');
    expect(alert).toHaveTextContent('req-77');
    expect(screen.getByText('保留中的任务')).toBeInTheDocument();
  });

  it('状态播报汇总已加载/待处理/命中数量', async () => {
    const tasks = [
      makeTask({id: 'task-001', intent: '甲', status: 'awaiting-confirmation'}),
      makeTask({id: 'task-002', intent: '乙', status: 'failed', code: 'verify_failed'}),
    ];
    renderList(transportWith(async () => ({items: tasks, nextCursor: null})));
    await screen.findByText('甲');
    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent('已加载 2 项，其中待处理 1 项；当前筛选命中 2 项');
    });
    const failedRow = rowOf('乙');
    expect(within(failedRow).getByText(/失败码：verify_failed/)).toBeInTheDocument();
  });
});
