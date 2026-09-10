// 连接回归（E27）：断开必须清理 token 与查询缓存，不串服务数据。
import {afterEach, describe, expect, it, vi} from 'vitest';
import {act, render, screen} from '@testing-library/react';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {clearToken, installToken} from '../../lib/transport/client';
import {signalUnauthorized} from '../../lib/queries/client';
import {ConnectionProvider, useConnection} from './connection';

function DisconnectProbe({onDisconnect}: {onDisconnect: () => void}) {
  const {disconnect} = useConnection();
  return <button type="button" onClick={() => { disconnect(); onDisconnect(); }}>断开</button>;
}

describe('断开连接的缓存清理（E27）', () => {
  afterEach(() => clearToken());

  it('disconnect 清空 QueryClient 查询缓存（queryClient.clear 同时清理 mutation 缓存）', async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(['tasks', 'list'], {items: [{id: 'task-server-a'}], nextCursor: null});
    queryClient.setQueryData(['task', 'task-server-b', 'detail'], {id: 'task-server-b'});

    render(
      <QueryClientProvider client={queryClient}>
        <ConnectionProvider>
          <DisconnectProbe onDisconnect={() => undefined} />
        </ConnectionProvider>
      </QueryClientProvider>,
    );

    expect(queryClient.getQueryCache().getAll().length).toBe(2);
    await act(async () => {
      screen.getByRole('button', {name: '断开'}).click();
    });
    expect(queryClient.getQueryCache().getAll().length).toBe(0);
    expect(queryClient.getQueryData(['tasks', 'list'])).toBeUndefined();
  });
});

describe('服务端 401 后连接失效（UI-07）', () => {
  afterEach(() => {
    clearToken();
    vi.unstubAllGlobals();
  });

  function ReadyProbe() {
    const {state, connected, errorMessage} = useConnection();
    return (
      <div>
        <span data-testid="state">{state}</span>
        <span data-testid="connected">{String(connected)}</span>
        {errorMessage ? <span data-testid="error-message">{errorMessage}</span> : null}
      </div>
    );
  }

  it('ready 中收到 401：状态转 unauthorized、缓存清空、给出明确重连指引', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({items: [], nextCursor: null}), {
      status: 200,
      headers: {'Content-Type': 'application/json'},
    })));
    const queryClient = new QueryClient();

    function ConnectButton() {
      const {connect} = useConnection();
      return <button type="button" onClick={() => void connect('a'.repeat(64))}>接入</button>;
    }

    render(
      <QueryClientProvider client={queryClient}>
        <ConnectionProvider>
          <ConnectButton />
          <ReadyProbe />
        </ConnectionProvider>
      </QueryClientProvider>,
    );

    await act(async () => {
      screen.getByRole('button', {name: '接入'}).click();
    });
    expect(screen.getByTestId('state').textContent).toBe('ready');
    expect(screen.getByTestId('connected').textContent).toBe('true');

    queryClient.setQueryData(['tasks', 'list'], {items: [{id: 'task-old'}], nextCursor: null});
    expect(queryClient.getQueryCache().getAll().length).toBe(1);
    installToken('stale-token');

    await act(async () => {
      signalUnauthorized();
    });

    // 连接失效、缓存清空（重连不闪现上一连接数据）、内存 token 已清
    expect(screen.getByTestId('state').textContent).toBe('unauthorized');
    expect(screen.getByTestId('connected').textContent).toBe('false');
    expect(queryClient.getQueryCache().getAll().length).toBe(0);
    expect(screen.getByTestId('error-message').textContent).toContain('401');
  });
});
