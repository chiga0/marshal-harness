import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import type {ControlBody, OperationRecord} from '@/lib/transport/types';
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
  const original: OperationRecord = {id: 'pause-receipt', taskId: WITH_REVISION.id, kind: 'task.pause', status: 'accepted', taskRevision: 8,
    createdAt: '2026-09-11T00:00:00Z', updatedAt: '2026-09-11T00:00:00Z'};
  const paused = {...WITH_REVISION, status: 'paused' as const, revision: 8, allowedActions: ['resume', 'cancel'] as const};
  async function controlFixture(read: () => Promise<OperationRecord>) {
    const {transport, calls} = makeFakeTransport({pauseTask: async () => original, getOperation: read});
    const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
    const node = (task: typeof WITH_REVISION) => <QueryClientProvider client={client}><TaskControls task={task} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
    const view = render(node(WITH_REVISION)); const user = userEvent.setup();
    await user.click(screen.getByTestId('control-pause'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '暂停任务'}));
    await screen.findByTestId('control-pause-accepted');
    view.rerender(node({...paused, allowedActions: [...paused.allowedActions]}));
    return {view, node, user, calls, transport, client};
  }
  it.each(['accepted', 'running', 'unknown'] as const)('Operation %s 即便Task已paused也不解锁，不能用关闭提示绕过', async status => {
    const read = vi.fn(async () => ({...original, status}));
    const {calls, client} = await controlFixture(read);
    await waitFor(() => expect(read).toHaveBeenCalled());
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(screen.getByTestId('control-resume')).toBeDisabled();
    expect(screen.getByTestId('control-cancel')).toBeDisabled();
    expect(within(screen.getByTestId('control-pause-accepted')).queryByRole('button', {name: '关闭'})).toBeNull();
    expect(screen.getByText(/无需关闭提示/)).toBeInTheDocument();
    expect(callsOf(calls, 'pauseTask')).toHaveLength(1);
  });
  it.each([
    {id: 'foreign'}, {taskId: 'other-task'}, {kind: 'task.resume' as const},
    {taskRevision: 7}, {updatedAt: '2026-09-10T00:00:00Z'},
  ])('错误归属或陈旧Operation不解锁 %j', async changed => {
    const read = vi.fn(async () => ({...original, status: 'succeeded' as const, ...changed}));
    const {client} = await controlFixture(read); await waitFor(() => expect(read).toHaveBeenCalled());
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(screen.getByTestId('control-resume')).toBeDisabled();
  });
  it('暂停Operation成功但当前Task尚非paused不解锁；失败Operation经Task版本核对后可继续', async () => {
    const {view, node, client} = await controlFixture(async () => ({...original, status: 'succeeded', taskRevision: 9}));
    await waitFor(() => expect(client.isFetching()).toBe(0));
    view.rerender(node({...WITH_REVISION, revision: 9}));
    expect(screen.getByTestId('control-pause')).toBeDisabled();
    view.unmount(); client.clear();
    const failed = await controlFixture(async () => ({...original, status: 'failed', code: 'state_conflict'}));
    await waitFor(() => expect(screen.getByTestId('control-resume')).toBeEnabled());
    expect(screen.getByRole('region', {name: '已核对的任务控制回执'})).toHaveTextContent('操作失败');
    failed.view.unmount(); failed.client.clear();
  });
  it('submitting即便轮询得到paused也保持锁，不发送第二个控制请求', async () => {
    let resolve!: (value: unknown) => void;
    const {transport, calls} = makeFakeTransport({pauseTask: () => new Promise(done => {resolve = done;})});
    const client = new QueryClient(); const user = userEvent.setup();
    const node = (task: typeof WITH_REVISION) => <QueryClientProvider client={client}><TaskControls task={task} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
    const view = render(node(WITH_REVISION));
    await user.click(screen.getByTestId('control-pause')); await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '暂停任务'}));
    view.rerender(node({...paused, allowedActions: [...paused.allowedActions]}));
    expect(screen.getByTestId('control-resume')).toBeDisabled();expect(callsOf(calls, 'pauseTask')).toHaveLength(1);
    resolve(original);await screen.findByTestId('control-pause-accepted');
  });
  it('取消成功回执等待Task终态，保留原回执且不开放终态控制', async () => {
    const receipt = {...original, id: 'cancel-receipt', kind: 'task.cancel' as const};
    const {transport} = makeFakeTransport({cancelTask: async () => receipt, getOperation: async () => ({...receipt, status: 'succeeded'})});
    const client = new QueryClient(); const user = userEvent.setup();
    const node = (task: typeof WITH_REVISION) => <QueryClientProvider client={client}><TaskControls task={task} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
    const view = render(node(WITH_REVISION));await user.click(screen.getByTestId('control-cancel'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '取消任务'}));
    await screen.findByTestId('control-cancel-accepted');await waitFor(() => expect(client.isFetching()).toBe(0));
    view.rerender(node({...WITH_REVISION, revision: 8, status: 'cancelling'}));
    expect(screen.getByTestId('control-pause')).toBeDisabled();
    view.rerender(node({...WITH_REVISION, revision: 8, status: 'cancelled', allowedActions: []}));
    await screen.findByRole('region', {name: '已核对的任务控制回执'});
    expect(screen.getByTestId('controls-terminal')).toHaveTextContent('cancelled');
    expect(screen.queryByTestId('control-cancel')).toBeNull();
  });
  it('精确成功回执仍等待Task事实，暂停成功后直接恢复且保留两次回执', async () => {
    const resume: OperationRecord = {...original, id: 'resume-receipt', kind: 'task.resume', taskRevision: 9};
    const {view, node, user, transport, calls} = await controlFixture(async () => ({...original, status: 'succeeded', taskRevision: 9}));
    await waitFor(() => expect(callsOf(calls, 'getOperation').length).toBeGreaterThan(0));
    expect(screen.getByTestId('control-resume')).toBeDisabled(); // Task rev8 < terminal operation rev9
    view.rerender(node({...paused, revision: 9, allowedActions: [...paused.allowedActions]}));
    await waitFor(() => expect(screen.getByTestId('control-resume')).toBeEnabled());
    expect(screen.getByRole('region', {name: '已核对的任务控制回执'})).toHaveTextContent('pause-receipt');
    transport.resumeTask = async () => ({...resume, taskRevision: 10});
    transport.getOperation = async id => ({...(id === resume.id ? {...resume, taskRevision: 10} : original), status: 'succeeded'});
    await user.click(screen.getByTestId('control-resume'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '恢复任务'}));
    await screen.findByTestId('control-resume-accepted');
    view.rerender(node({...WITH_REVISION, revision: 10}));
    await waitFor(() => expect(screen.getByTestId('control-pause')).toBeEnabled());
    const history = screen.getByRole('region', {name: '已核对的任务控制回执'});
    expect(history).toHaveTextContent('pause-receipt'); expect(history).toHaveTextContent('resume-receipt');
  });
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
