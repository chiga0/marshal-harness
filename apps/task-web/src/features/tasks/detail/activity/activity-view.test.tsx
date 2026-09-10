import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ApiError} from '@/lib/transport/types';
import {ActivityView} from './activity-view';
import type {EventsLoader, EventsPage, TaskEvent} from './events';
import {makeFakeTransport} from '../testing/fixtures';

function event(sequence: number, summary = `条目 ${sequence}`): TaskEvent {
  return {id: `ev-${sequence}`, taskId: 'task-1', sequence, type: 'worker_progress', at: `2026-09-10T01:00:${String(sequence).padStart(2, '0')}.000Z`, workerId: sequence % 2 === 0 ? 'w-2' : null, summary, source: 'application'};
}

function loaderOf(pages: EventsPage[]): {loader: EventsLoader; calls: Array<{cursor: string | null; limit: number}>} {
  const calls: Array<{cursor: string | null; limit: number}> = [];
  const queue = [...pages];
  const loader: EventsLoader = async (_taskId, options) => {
    calls.push({cursor: options.cursor, limit: options.limit});
    const next = queue.shift();
    if (!next) throw new Error('no more pages');
    return next;
  };
  return {loader, calls};
}

describe('活动事件（P11 / E20-E22）', () => {
  it('分页加载：首页自动加载，加载更多携带 nextCursor，跨页去重', async () => {
    const {loader, calls} = loaderOf([
      {items: [event(3), event(2)], nextCursor: 'cursor-older', taskId: 'task-1'},
      {items: [event(2), event(1)], nextCursor: null, taskId: 'task-1'},
    ]);
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={loader} />);

    // 首页
    await screen.findByText('条目 3');
    expect(screen.getAllByTestId('activity-event')).toHaveLength(2);

    // 加载更多：带服务端游标；重复条目 ev-2 不重复出现
    await user.click(screen.getByTestId('activity-load-more'));
    await waitFor(() => expect(screen.getAllByTestId('activity-event')).toHaveLength(3));
    expect(calls[1]).toEqual({cursor: 'cursor-older', limit: 50});
    expect(screen.queryByText('加载更多')).toBeNull(); // 第二页 nextCursor=null 后不再提供
  });

  it('加载失败：显示 code+requestId，保留已成功内容并标注非实时', async () => {
    const failing: EventsLoader = async (_taskId, options) => {
      if (options.cursor === null) {
        // 第一次成功，第二次（刷新）失败
        return {items: [event(2)], nextCursor: null, taskId: 'task-1'};
      }
      throw new Error('unreachable');
    };
    let succeeded = false;
    const flaky: EventsLoader = async (taskId, options) => {
      if (!succeeded) {
        succeeded = true;
        return failing(taskId, options);
      }
      throw new ApiError(504, 'request_timeout', '请求超时', 'req-events-1');
    };
    const {transport} = makeFakeTransport();
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={flaky} />);
    await screen.findByText('条目 2');

    await userEvent.setup().click(screen.getByRole('button', {name: '刷新'}));
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('请求超时');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-events-1');
    // 已加载内容保留并明确标注非实时
    expect(screen.getByText('条目 2')).toBeInTheDocument();
    expect(screen.getByTestId('activity-stale')).toBeInTheDocument();
  });

  it('无事件接口时如实显示不可用，不伪造事件流', () => {
    const {transport} = makeFakeTransport();
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={null} />);
    expect(screen.getByTestId('activity-unavailable')).toHaveTextContent('不编造事件');
  });

  it('长内容可展开/收起', async () => {
    const longSummary = `非常长的事件：${'长'.repeat(260)}`;
    const {loader} = loaderOf([{items: [event(1, longSummary)], nextCursor: null, taskId: 'task-1'}]);
    const {transport} = makeFakeTransport();
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={loader} />);
    const row = (await screen.findByTestId('activity-event')) as HTMLElement;
    expect(within(row).getByRole('button', {name: '展开全文'})).toBeInTheDocument();
    await userEvent.setup().click(within(row).getByRole('button', {name: '展开全文'}));
    expect(within(row).getByRole('button', {name: '收起'})).toBeInTheDocument();
    expect(within(row).getByText(longSummary)).toBeInTheDocument();
  });

  it('冻结 transport（无 getEvents 方法）时自动落入不可用解释', () => {
    const {transport} = makeFakeTransport();
    render(<ActivityView taskId="task-1" transport={transport} />);
    expect(screen.getByTestId('activity-unavailable')).toBeInTheDocument();
  });
});
