// 隔离 fixture 的 React/jsdom 容量基准；不启动服务/模型，不作为浏览器布局或性能 SLO 通过证据。
import {Profiler} from 'react';
import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {TaskListView, TASK_LIST_PAGE_SIZE} from './task-list-page';
import {ActivityView} from './detail/activity/activity-view';
import * as events from './detail/activity/events';
import {makeFakeTransport, makeTask} from './detail/testing/fixtures';

// 禁后台定时轮询，所有分页和尾页刷新均由实际组件按钮明确触发，基准次数可复现。
vi.mock('@/lib/queries/polling', () => ({usePollMode: () => null}));

function metrics(label: string, started: number, commits: number[], extra: Record<string, unknown>) {
  console.info(JSON.stringify({fixtureOnly: true, environment: 'React/jsdom; no browser layout or network', label,
    wallMs: Math.round(performance.now() - started), commits: commits.length,
    reactActualMs: Math.round(commits.reduce((sum, value) => sum + value, 0)),
    maxReactRenderMs: Math.round(Math.max(0, ...commits)), ...extra}));
}

describe('已加载数据密度与分页有界性基准', () => {
  it('100项任务：逐页载入、长中文内容、筛选/卡片切换不丢数据且不额外取数', async () => {
    const user = userEvent.setup();
    const tasks = Array.from({length: 100}, (_, index) => makeTask({id: `density-task-${index}`,
      intent: `${index % 2 === 0 ? '分组甲' : '分组乙'}-${index}：${'真实组件的隔离长内容容量夹具。'.repeat(10)}`,
      status: index % 4 === 0 ? 'awaiting-answer' : 'running'}));
    const cursorFor = (offset: number) => offset === 0 ? null : `opaque-page-${offset / TASK_LIST_PAGE_SIZE}`;
    const pages = new Map(Array.from({length: Math.ceil(tasks.length / TASK_LIST_PAGE_SIZE)}, (_, page) => {
      const offset = page * TASK_LIST_PAGE_SIZE;
      return [cursorFor(offset), {items: tasks.slice(offset, offset + TASK_LIST_PAGE_SIZE), nextCursor: offset + TASK_LIST_PAGE_SIZE < tasks.length ? cursorFor(offset + TASK_LIST_PAGE_SIZE) : null}] as const;
    }));
    const listTasks = vi.fn(async ({cursor}: {cursor?: string | null}) => {
      const page = pages.get(cursor ?? null);
      if (!page) throw new Error('未知fixture游标');
      return page;
    });
    const {transport} = makeFakeTransport({listTasks});
    const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
    const commits: number[] = [];
    const started = performance.now();
    render(<QueryClientProvider client={client}><MemoryRouter><Profiler id="task-density" onRender={(_id, _phase, duration) => commits.push(duration)}><TaskListView transport={transport} /></Profiler></MemoryRouter></QueryClientProvider>);
    const ids = () => [...screen.getByRole('list', {name: '任务条目'}).querySelectorAll('[data-task-id]')].map(row => row.getAttribute('data-task-id'));
    await waitFor(() => expect(ids()).toHaveLength(24));
    for (const total of [48, 72, 96, 100]) {
      await user.click(screen.getByRole('button', {name: /加载更多（还有下一页/}));
      await waitFor(() => expect(ids()).toHaveLength(total));
    }
    const loadMs = Math.round(performance.now() - started);
    expect(new Set(ids()).size).toBe(100);
    const toggleStart = performance.now();
    await user.click(screen.getByRole('button', {name: '卡片'}));
    expect(ids()).toHaveLength(100);
    expect(screen.getByRole('list', {name: '任务条目'})).toHaveAttribute('data-view', 'cards');
    const toggleMs = Math.round(performance.now() - toggleStart);
    const filterStart = performance.now();
    await user.type(screen.getByLabelText('筛选已加载任务'), '分组甲');
    expect(ids()).toHaveLength(50);
    await user.selectOptions(screen.getByLabelText('状态'), 'awaiting-answer');
    await user.click(screen.getByRole('button', {name: /只看待处理/}));
    expect(ids()).toHaveLength(25);
    const filterMs = Math.round(performance.now() - filterStart);
    await user.click(screen.getByRole('button', {name: '列表'}));
    expect(ids()).toHaveLength(25);
    expect(screen.getByLabelText('筛选已加载任务')).toHaveValue('分组甲');
    expect(listTasks).toHaveBeenCalledTimes(5);
    expect(listTasks.mock.calls.map(call => call[0].cursor ?? null)).toEqual([null, 'opaque-page-1', 'opaque-page-2', 'opaque-page-3', 'opaque-page-4']);
    metrics('tasks-100', started, commits, {loadMs, toggleMs, filterMs, loaded: 100, filtered: 25, listCalls: listTasks.mock.calls.length});
  });

  it('500事件：追赶至600条、重复尾页和后续新增，DOM及后继合并输入均有界', async () => {
    const user = userEvent.setup();
    const all = Array.from({length: 600}, (_, index): events.TaskEvent => ({id: `density-event-${index + 1}`, taskId: 'density-task', sequence: index + 1,
      at: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(), type: 'worker_progress', workerId: 'worker-density', source: 'application',
      summary: `事件 ${index + 1}：${'仅用于测试的长中文观察内容。'.repeat(20)}`}));
    let available = 550;
    const loader = vi.fn<events.EventsLoader>(async (_taskId, {cursor, limit}) => {
      const after = cursor === null ? 0 : Number(cursor.slice('after-'.length));
      const items = all.slice(after, Math.min(available, after + limit));
      return {taskId: 'density-task', items, nextCursor: after + limit < available ? `after-${after + limit}` : null};
    });
    const merge = vi.spyOn(events, 'mergeEventPages');
    const {transport} = makeFakeTransport();
    const commits: number[] = [];
    const started = performance.now();
    try {
      render(<Profiler id="events-density" onRender={(_id, _phase, duration) => commits.push(duration)}><ActivityView taskId="density-task" transport={transport} eventsLoader={loader} /></Profiler>);
      await waitFor(() => expect(screen.getAllByTestId('activity-event')).toHaveLength(50));
      for (let total = 100; total <= 500; total += 50) {
        await user.click(screen.getByTestId('activity-load-more'));
        await waitFor(() => expect(screen.getAllByTestId('activity-event')).toHaveLength(total));
      }
      const load500Ms = Math.round(performance.now() - started);
      await user.click(screen.getByTestId('activity-load-more'));
      await waitFor(() => expect(loader).toHaveBeenCalledTimes(11));
      expect(screen.getAllByTestId('activity-event')).toHaveLength(500);
      expect(screen.queryByTestId('activity-load-more')).not.toBeInTheDocument();
      for (let count = 0; count < 5; count += 1) await user.click(screen.getByRole('button', {name: '刷新'}));
      expect(screen.getAllByTestId('activity-event')).toHaveLength(500);
      available = 600;
      await user.click(screen.getByRole('button', {name: '刷新'}));
      await screen.findByTestId('activity-load-more');
      await user.click(screen.getByTestId('activity-load-more'));
      await waitFor(() => expect(loader).toHaveBeenCalledTimes(18));
      const rows = screen.getAllByTestId('activity-event');
      expect(rows).toHaveLength(500);
      expect(rows[0]).toHaveTextContent('事件 600：');
      expect(rows[499]).toHaveTextContent('事件 101：');
      expect(loader.mock.calls.every(call => call[1].limit === 50)).toBe(true);
      expect(loader.mock.calls.slice(0, 11).map(call => call[1].cursor)).toEqual([null, ...Array.from({length: 10}, (_, index) => `after-${(index + 1) * 50}`)]);
      const maxInputItems = Math.max(...merge.mock.calls.map(([pages]) => pages.reduce((sum, page) => sum + page.items.length, 0)));
      expect(maxInputItems).toBeLessThanOrEqual(550);
      expect(merge.mock.calls.every(([pages]) => pages.length <= 2)).toBe(true);
      metrics('events-500-cap', started, commits, {load500Ms, requestedEvents: 600, rendered: rows.length, readCalls: loader.mock.calls.length, maxMergeInputItems: maxInputItems});
    } finally { merge.mockRestore(); }
  });
});
