import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {TaskControls} from './task-controls';
import {callsOf, makeDetail, makeFakeTransport} from '../testing/fixtures';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

// 造 revision：makeDetail 加 plan.revision 作为 CAS 回退（生产上真实服务响应含 task.revision）。
const WITH_REVISION = makeDetail({plan: {
  revision: '7', taskId: 'task-test-0001', nodes: [], edges: [], frozenAt: '2026-09-10T01:00:00.000Z',
  expiresAt: null, budgetMs: null, deadlineAt: null, planDigest: 'sha256:aaaa', decisionDigest: 'sha256:bbbb',
  acceptedAt: null, rejectedAt: null,
}, allowedActions: ['pause', 'cancel']});

describe('任务控制（P07 / E12 / E14）', () => {
  it('按 allowedActions 控制按钮可见性', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls detail={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('control-pause')).toBeInTheDocument();
    expect(screen.getByTestId('control-cancel')).toBeInTheDocument();
    expect(screen.queryByTestId('control-resume')).toBeNull();
  });

  it('暂停语义如实说明：只停止新的调度，不立即停止运行中 Worker', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<TaskControls detail={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('control-pause'));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('暂停只停止新的调度');
    expect(dialog).toHaveTextContent('不是进程暂停');
    await user.click(within(dialog).getByRole('button', {name: '暂停任务'}));
    await waitFor(() => expect(callsOf(calls, 'pauseTask')).toHaveLength(1));
    const body = callsOf(calls, 'pauseTask')[0]!.args[1] as {revision: string; idempotencyKey: string};
    expect(body.revision).toBe('7');
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    const accepted = await screen.findByTestId('control-pause-accepted');
    expect(accepted).toHaveTextContent('受理不代表已暂停');
  });

  it('取消任务走 cancelTask 且是独立二次确认；不触碰 cancelWorker', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<TaskControls detail={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('control-cancel'));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('取消是终态操作');
    expect(dialog).toHaveTextContent('取消请求被受理不代表任务已经停止');
    await user.click(within(dialog).getByRole('button', {name: '取消任务'}));
    await waitFor(() => expect(callsOf(calls, 'cancelTask')).toHaveLength(1));
    expect(callsOf(calls, 'cancelWorker')).toHaveLength(0);
    const accepted = await screen.findByTestId('control-cancel-accepted');
    expect(accepted).toHaveTextContent('受理不代表任务已停止');
  });

  it('终态任务只展示事实回执，不提供控制操作', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls detail={{...WITH_REVISION, status: 'cancelled'}} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('controls-terminal')).toHaveTextContent('任务已到达终态');
    expect(screen.queryByTestId('control-cancel')).toBeNull();
    expect(screen.queryByTestId('control-pause')).toBeNull();
  });

  it('allowedActions 为空如实说明', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls detail={{...WITH_REVISION, allowedActions: []}} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('controls-unavailable')).toHaveTextContent('当前状态不提供控制操作');
  });
});
