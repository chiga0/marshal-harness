import {describe, expect, it, vi} from 'vitest';
import {fireEvent, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {ApiError, type Transport} from '@/lib/transport/types';
import {PlanCard} from './plan-card';
import {callsOf, makeDetail, makeFakeTransport, makePlan, TASK_ID} from '../testing/fixtures';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const AWAITING = makeDetail({status: 'awaiting-confirmation', allowedActions: ['approve', 'cancel']});

describe('计划确认（P04 / E06 / E08）', () => {
  it('确认提交绑定 revision+decisionDigest+幂等键；提交中双击不重复提交', async () => {
    vi.useFakeTimers({shouldAdvanceTime: true});
    try {
      const gate = new Promise<never>(() => {});
      const {transport, calls} = makeFakeTransport({approveTask: vi.fn(() => gate) as Transport['approveTask']});
      const user = userEvent.setup({advanceTimers: vi.advanceTimersByTime});
      wrap(<PlanCard taskId={TASK_ID} detail={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);

      await user.click(screen.getByTestId('plan-approve-open'));
      const confirm = await screen.findByRole('button', {name: '确认执行'});
      // 同步双击：第一次已进入 submitting，第二次必须被忽略
      fireEvent.click(confirm);
      fireEvent.click(confirm);
      await waitFor(() => expect(callsOf(calls, 'approveTask')).toHaveLength(1));
      const body = callsOf(calls, 'approveTask')[0]!.args[1] as {revision: string; decisionDigest: string; idempotencyKey: string};
      expect(body.revision).toBe('3');
      expect(body.decisionDigest).toBe('sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed');
      expect(typeof body.idempotencyKey).toBe('string');
      expect(body.idempotencyKey.length).toBeGreaterThan(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it('受理成功显示受理叙事，不乐观宣称已执行', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<PlanCard taskId={TASK_ID} detail={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '确认执行'}));
    expect(await screen.findByText(/确认已受理/)).toBeInTheDocument();
    expect(screen.getByText(/受理不等于已开始执行/)).toBeInTheDocument();
  });

  it('409 冲突：保留已查看 revision/digest 草稿并指引查看新内容，不自动替换摘要', async () => {
    const {transport} = makeFakeTransport({
      approveTask: vi.fn(async () => {
        throw new ApiError(409, 'plan_conflict', '计划已更新', 'req-409-1');
      }) as Transport['approveTask'],
    });
    const onViewLatest = vi.fn();
    const user = userEvent.setup();
    wrap(<PlanCard taskId={TASK_ID} detail={AWAITING} plan={makePlan()} transport={transport} onViewLatest={onViewLatest} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '确认执行'}));

    const stale = await screen.findByTestId('plan-approve-stale');
    expect(stale).toHaveTextContent('revision 3');
    expect(stale).toHaveTextContent('sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed');
    expect(stale).toHaveTextContent('不会自动替换为新摘要再提交');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-409-1');
    await user.click(screen.getByTestId('plan-approve-view-latest'));
    expect(onViewLatest).toHaveBeenCalledTimes(1);
  });

  it('结果未知（504）可显式原键重放：两次调用使用同一 Idempotency-Key', async () => {
    const approveTask = vi.fn()
      .mockRejectedValueOnce(new ApiError(504, 'request_timeout', '请求超时', 'req-504-1'))
      .mockResolvedValueOnce({});
    const {transport, calls} = makeFakeTransport({approveTask: approveTask as Transport['approveTask']});
    const user = userEvent.setup();
    wrap(<PlanCard taskId={TASK_ID} detail={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '确认执行'}));

    expect(await screen.findByText(/确认结果未知/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', {name: /原键重放/}));
    await waitFor(() => expect(callsOf(calls, 'approveTask')).toHaveLength(2));
    const first = callsOf(calls, 'approveTask')[0]!.args[1] as {revision: string; idempotencyKey: string};
    const second = callsOf(calls, 'approveTask')[1]!.args[1] as {revision: string; idempotencyKey: string};
    expect(second.idempotencyKey).toBe(first.idempotencyKey);
    expect(second.revision).toBe(first.revision);
  });

  it('非 allowedActions 含 approve 时不提供确认入口', () => {
    const {transport} = makeFakeTransport();
    wrap(<PlanCard taskId={TASK_ID} detail={makeDetail({status: 'running', allowedActions: ['cancel']})} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    expect(screen.queryByTestId('plan-approve-open')).toBeNull();
    expect(screen.getByTestId('plan-approve-unavailable')).toHaveTextContent('allowedActions 不含 approve');
  });
});
