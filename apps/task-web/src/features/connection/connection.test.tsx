// 连接回归（E27）：断开必须清理 token 与查询缓存，不串服务数据。
import {afterEach, describe, expect, it} from 'vitest';
import {act, render, screen} from '@testing-library/react';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {clearToken} from '../../lib/transport/client';
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
