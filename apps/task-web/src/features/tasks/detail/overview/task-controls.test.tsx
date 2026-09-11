import {describe, expect, it} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import type {ControlBody} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {TaskControls} from './task-controls';
import {callsOf, makeFakeTransport, makeTask} from '../shared/test-fakes';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

// 任务 revision=7（合同必返数字），CAS 永远可用。
const WITH_REVISION = makeTask({allowedActions: ['pause', 'cancel']});

describe('任务控制（P07 / E12 / E14）', () => {
  it('未知取消不允许新操作或换键，轮询推进后显式重放仍用原 CAS', async () => {
    const {transport, calls} = makeFakeTransport({cancelTask: async () => { throw new TypeError('lost response'); }});
    const client = new QueryClient();
    const node = (revision: number) => <QueryClientProvider client={client}><TaskControls task={{...WITH_REVISION, revision}} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
    const view = render(node(7));
    const user = userEvent.setup();
    await user.click(screen.getByTestId('control-cancel'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '取消任务'}));
    await screen.findByTestId('error-notice');
    view.rerender(node(8));
    expect(screen.getByTestId('control-pause')).toBeDisabled();
    expect(screen.queryByText('已核对结果，关闭并重新选择操作')).toBeNull();
    await user.click(screen.getByRole('button', {name: /原键重放/}));
    await waitFor(() => expect(callsOf(calls, 'cancelTask')).toHaveLength(2));
    expect(callsOf(calls, 'cancelTask')[0]!.args).toEqual(callsOf(calls, 'cancelTask')[1]!.args);
  });
  it('409 后显式核对重开采用新键/新 CAS，确认期间不会偷换版本', async () => {
    let first = true;
    const {transport, calls} = makeFakeTransport({cancelTask: async () => {
      if (first) { first = false; throw new ApiError(409, 'revision_conflict', '版本冲突', null); }
      return {};
    }});
    const client = new QueryClient();
    const node = (revision: number) => <QueryClientProvider client={client}><TaskControls task={{...WITH_REVISION, revision}} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
    const view = render(node(7));
    const user = userEvent.setup();
    await user.click(screen.getByTestId('control-cancel'));
    view.rerender(node(8));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '取消任务'}));
    await screen.findByText('已核对结果，关闭并重新选择操作');
    expect(screen.getByTestId('control-cancel')).toBeDisabled();
    await user.click(screen.getByText('已核对结果，关闭并重新选择操作'));
    await user.click(screen.getByTestId('control-cancel'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '取消任务'}));
    await screen.findByTestId('control-cancel-accepted');
    const bodies = callsOf(calls, 'cancelTask').map(call => call.args[1] as ControlBody);
    expect(bodies.map(body => body.expectedRevision)).toEqual([7, 8]);
    expect(bodies[0]!.idempotencyKey).not.toBe(bodies[1]!.idempotencyKey);
    view.rerender(node(9));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByTestId('control-cancel-accepted')).toBeInTheDocument();
  });
  it('按 allowedActions 控制按钮可见性', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls task={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('control-pause')).toBeInTheDocument();
    expect(screen.getByTestId('control-cancel')).toBeInTheDocument();
    expect(screen.queryByTestId('control-resume')).toBeNull();
  });

  it('恢复操作在 allowedActions 含 resume 时出现', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls task={makeTask({status: 'paused', allowedActions: ['resume', 'cancel']})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('control-resume')).toBeInTheDocument();
    expect(screen.queryByTestId('control-pause')).toBeNull();
  });

  it('暂停语义如实说明：只停止新的调度；body=ControlTask {expectedRevision,幂等键}', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<TaskControls task={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('control-pause'));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('暂停只停止新的调度');
    expect(dialog).toHaveTextContent('不是进程暂停');
    await user.click(within(dialog).getByRole('button', {name: '暂停任务'}));
    await waitFor(() => expect(callsOf(calls, 'pauseTask')).toHaveLength(1));
    const body = callsOf(calls, 'pauseTask')[0]!.args[1] as ControlBody;
    expect(body.expectedRevision).toBe(7); // 数字 revision
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    expect(body).not.toHaveProperty('revision'); // 合同闭集字段名为 expectedRevision
    const accepted = await screen.findByTestId('control-pause-accepted');
    expect(accepted).toHaveTextContent('受理不代表已暂停');
  });

  it('取消任务走 cancelTask 且是独立二次确认；不触碰 cancelWorker', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<TaskControls task={WITH_REVISION} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('control-cancel'));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('取消是终态操作');
    expect(dialog).toHaveTextContent('取消请求被受理不代表任务已经停止');
    await user.click(within(dialog).getByRole('button', {name: '取消任务'}));
    await waitFor(() => expect(callsOf(calls, 'cancelTask')).toHaveLength(1));
    expect((callsOf(calls, 'cancelTask')[0]!.args[1] as ControlBody).expectedRevision).toBe(7);
    expect(callsOf(calls, 'cancelWorker')).toHaveLength(0);
    const accepted = await screen.findByTestId('control-cancel-accepted');
    expect(accepted).toHaveTextContent('受理不代表任务已停止');
  });

  it('终态任务只展示事实回执，不提供控制操作', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls task={{...WITH_REVISION, status: 'cancelled'}} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('controls-terminal')).toHaveTextContent('任务已到达终态');
    expect(screen.queryByTestId('control-cancel')).toBeNull();
    expect(screen.queryByTestId('control-pause')).toBeNull();
  });

  it('allowedActions 为空如实说明', () => {
    const {transport} = makeFakeTransport();
    wrap(<TaskControls task={{...WITH_REVISION, allowedActions: []}} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('controls-unavailable')).toHaveTextContent('当前状态不提供控制操作');
  });
});
