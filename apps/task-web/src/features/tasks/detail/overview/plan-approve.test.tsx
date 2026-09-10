import {describe, expect, it, vi} from 'vitest';
import {fireEvent, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {ApiError, type PlanApproveBody, type Transport} from '@/lib/transport/types';
import {PlanCard} from './plan-card';
import {PLAN_DIGEST, callsOf, makeFakeTransport, makePlan, makeTask, TASK_ID} from '../shared/test-fakes';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const AWAITING = makeTask({status: 'awaiting-approval', allowedActions: ['approve', 'cancel']});

describe('计划批准（P04 / E06 / E08）', () => {
  it('批准走 approvePlan：expectedRevision+planRevision+planDigest+幂等键；提交中双击不重复提交', async () => {
    vi.useFakeTimers({shouldAdvanceTime: true});
    try {
      const gate = new Promise<never>(() => {});
      const {transport, calls} = makeFakeTransport({approvePlan: vi.fn(() => gate) as Transport['approvePlan']});
      const user = userEvent.setup({advanceTimers: vi.advanceTimersByTime});
      wrap(<PlanCard task={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);

      await user.click(screen.getByTestId('plan-approve-open'));
      const confirm = await screen.findByRole('button', {name: '批准执行'});
      // 同步双击：第一次已进入 submitting，第二次必须被忽略
      fireEvent.click(confirm);
      fireEvent.click(confirm);
      await waitFor(() => expect(callsOf(calls, 'approvePlan')).toHaveLength(1));
      const body = callsOf(calls, 'approvePlan')[0]!.args[1] as PlanApproveBody;
      expect(body.expectedRevision).toBe(7); // 任务 revision（数字）
      expect(body.planRevision).toBe(3); // 计划 revision（数字）
      expect(body.planDigest).toBe(PLAN_DIGEST);
      expect(typeof body.idempotencyKey).toBe('string');
      expect(body.idempotencyKey.length).toBeGreaterThan(0);
      // 合同闭集：不再出现 decisionDigest/字符串 revision
      expect(body).not.toHaveProperty('decisionDigest');
    } finally {
      vi.useRealTimers();
    }
  });

  it('受理成功显示受理叙事，不乐观宣称已执行', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<PlanCard task={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '批准执行'}));
    expect(await screen.findByText(/批准已受理/)).toBeInTheDocument();
    expect(screen.getByText(/受理不等于已开始执行/)).toBeInTheDocument();
  });

  it('409 冲突：保留已查看快照并指引查看新内容，不自动替换摘要', async () => {
    const {transport} = makeFakeTransport({
      approvePlan: vi.fn(async () => {
        throw new ApiError(409, 'plan_conflict', '计划已更新', 'req-409-1');
      }) as Transport['approvePlan'],
    });
    const onViewLatest = vi.fn();
    const user = userEvent.setup();
    wrap(<PlanCard task={AWAITING} plan={makePlan()} transport={transport} onViewLatest={onViewLatest} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '批准执行'}));

    const stale = await screen.findByTestId('plan-approve-stale');
    expect(stale).toHaveTextContent('任务 revision 7');
    expect(stale).toHaveTextContent('计划 revision 3');
    expect(stale).toHaveTextContent(PLAN_DIGEST);
    expect(stale).toHaveTextContent('不会自动替换为新摘要再提交');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-409-1');
    await user.click(screen.getByTestId('plan-approve-view-latest'));
    expect(onViewLatest).toHaveBeenCalledTimes(1);
  });

  it('结果未知（504）可显式原键重放：两次调用使用同一 Idempotency-Key', async () => {
    const approvePlan = vi.fn()
      .mockRejectedValueOnce(new ApiError(504, 'request_timeout', '请求超时', 'req-504-1'))
      .mockResolvedValueOnce({});
    const {transport, calls} = makeFakeTransport({approvePlan: approvePlan as Transport['approvePlan']});
    const user = userEvent.setup();
    wrap(<PlanCard task={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '批准执行'}));

    expect(await screen.findByText(/批准结果未知/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', {name: /原键重放/}));
    await waitFor(() => expect(callsOf(calls, 'approvePlan')).toHaveLength(2));
    const first = callsOf(calls, 'approvePlan')[0]!.args[1] as PlanApproveBody;
    const second = callsOf(calls, 'approvePlan')[1]!.args[1] as PlanApproveBody;
    expect(second.idempotencyKey).toBe(first.idempotencyKey);
    expect(second.expectedRevision).toBe(first.expectedRevision);
    expect(second.planRevision).toBe(first.planRevision);
    expect(second.planDigest).toBe(first.planDigest);
  });

  it('allowedActions 不含 approve 时不提供批准入口', () => {
    const {transport} = makeFakeTransport();
    wrap(<PlanCard task={makeTask({status: 'running', allowedActions: ['cancel']})} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    expect(screen.queryByTestId('plan-approve-open')).toBeNull();
    expect(screen.getByTestId('plan-approve-unavailable')).toHaveTextContent('allowedActions 不含 approve');
  });

  it(`任务 ID 用于路由绑定（${'approvePlan'} 第一参数）`, async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<PlanCard task={AWAITING} plan={makePlan()} transport={transport} onViewLatest={() => {}} />);
    await user.click(screen.getByTestId('plan-approve-open'));
    await user.click(await screen.findByRole('button', {name: '批准执行'}));
    await waitFor(() => expect(callsOf(calls, 'approvePlan')).toHaveLength(1));
    expect(callsOf(calls, 'approvePlan')[0]!.args[0]).toBe(TASK_ID);
  });
});
