import {act, render, screen} from '@testing-library/react';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {App} from './app';
import {useConnection, type ConnectionValue} from '../features/connection/connection';
import {useLogicalAction, type LogicalAction} from '../features/tasks/detail/shared/logical-action';
import type {Transport} from '../lib/transport/types';

vi.mock('../features/connection/connection', () => ({
  ConnectionProvider: ({children}: {children: React.ReactNode}) => children,
  useConnection: vi.fn(),
}));
vi.mock('../features/tasks/task-list-page', () => ({TaskListPage: () => <Probe />}));
vi.mock('../features/connection/connect-page', () => ({ConnectPage: () => <p>重新连接</p>}));

let action: LogicalAction;
function Probe() {
  action = useLogicalAction(['task', 'answer', 1], ['task', 'answer', 'question']);
  return <p>动作状态：{action.phase.kind}</p>;
}
function connection(state: ConnectionValue['state'], transport: Transport | null): ConnectionValue {
  return {connected: state === 'ready', state, transport, errorMessage: null, connect: vi.fn(), disconnect: vi.fn()};
}

describe('应用连接 gate 清理逻辑动作会话', () => {
  beforeEach(() => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn()})));
  });
  afterEach(() => vi.unstubAllGlobals());
  it.each(['idle', 'unauthorized'] as const)('%s 离开 ready 时销毁旧原请求，新连接不继承 unknown', async state => {
    const oldTransport = {} as Transport;
    vi.mocked(useConnection).mockReturnValue(connection('ready', oldTransport));
    const view = render(<App />);
    const request = vi.fn(async () => { throw new TypeError('lost'); });
    await act(async () => { await action.submit(request); });
    const old = action;
    expect(screen.getByText('动作状态：unknown')).toBeInTheDocument();
    vi.mocked(useConnection).mockReturnValue(connection(state, null));
    view.rerender(<App />);
    expect(screen.getByText('重新连接')).toBeInTheDocument();
    await act(async () => { await old.replay(request); });
    expect(request).toHaveBeenCalledTimes(1);
    vi.mocked(useConnection).mockReturnValue(connection('ready', {} as Transport));
    view.rerender(<App />);
    expect(screen.getByText('动作状态：idle')).toBeInTheDocument();
    expect(action.idempotencyKey).not.toBe(old.idempotencyKey);
    expect(screen.queryByLabelText('本次连接未决操作')).not.toBeInTheDocument();
  });
});
