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
  it('ACK给中文消费解释并保留原类型摘要；未知事件不猜消费', async () => {
    const {transport} = makeFakeTransport();
    const {loader} = loaderOf([{taskId: 'task-1', nextCursor: null, items: [
      {...event(1, '原始ACK摘要'), type: 'worker.answer-acknowledged'},
      {...event(2, '原始未知摘要'), type: 'worker.new-event'},
    ]}]);
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={loader} />);
    await screen.findByText('原始ACK摘要');
    const rows = screen.getAllByTestId('activity-event');
    const ack = rows.find(row => row.getAttribute('data-event-id') === 'ev-1')!;
    const unknown = rows.find(row => row.getAttribute('data-event-id') === 'ev-2')!;
    expect(ack).toHaveTextContent('Worker 已确认消费原答复');
    expect(ack).toHaveTextContent('worker.answer-acknowledged');
    expect(ack).toHaveTextContent('这不代表执行完成或验收通过');
    expect(unknown).toHaveTextContent('原始未知摘要');
    expect(unknown).not.toHaveTextContent('已确认消费');
  });

  it('按真实升序 after 合同追赶分页，末页为空游标后仍发现新增事件', async () => {
    const {loader, calls} = loaderOf([
      {items: Array.from({length: 50}, (_, i) => event(i + 1)), nextCursor: '50', taskId: 'task-1'},
      {items: [event(51)], nextCursor: null, taskId: 'task-1'},
      {items: [event(51), event(52)], nextCursor: null, taskId: 'task-1'},
    ]);
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    render(<ActivityView taskId="task-1" transport={transport} eventsLoader={loader} />);
    await screen.findByText('条目 50');
    await user.click(screen.getByTestId('activity-load-more'));
    await screen.findByText('条目 51');
    await user.click(screen.getByRole('button', {name: '刷新'}));
    await screen.findByText('条目 52');
    expect(screen.getAllByTestId('activity-event')).toHaveLength(52);
    expect(calls.map(call => call.cursor)).toEqual([null, '50', '50']);
  });

  it('切换任务清空事件且忽略旧任务迟到响应', async () => {
    let finish!: (page: EventsPage) => void;
    const loader: EventsLoader = taskId => taskId === 'old'
      ? new Promise(resolve => { finish = resolve; })
      : Promise.resolve({taskId, items: [event(2)], nextCursor: null});
    const {transport} = makeFakeTransport();
    const view = render(<ActivityView taskId="old" transport={transport} eventsLoader={loader} />);
    view.rerender(<ActivityView taskId="new" transport={transport} eventsLoader={loader} />);
    await screen.findByText('条目 2');
    finish({taskId: 'old', items: [event(1)], nextCursor: null});
    await waitFor(() => expect(screen.queryByText('条目 1')).toBeNull());
  });
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

  it('缺省注入时自动使用冻结 transport 的 getEvents 加载事件流', async () => {
    const {transport, calls} = makeFakeTransport({
      getEvents: async taskId => ({
        items: [{id: 'ev-9', taskId, sequence: 9, type: 'worker_progress', at: '2026-09-10T01:00:09.000Z', workerId: null, summary: '条目 9', source: 'application' as const}],
        nextCursor: null,
        taskId,
      }),
    });
    render(<ActivityView taskId="task-1" transport={transport} />);
    await screen.findByText('条目 9');
    expect(calls.find(call => call.method === 'getEvents')?.args).toEqual(['task-1', {cursor: null, limit: 50}]);
  });
});
