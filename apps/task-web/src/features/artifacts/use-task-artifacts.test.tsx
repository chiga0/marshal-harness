import {describe, expect, it} from 'vitest';
import {renderHook, waitFor} from '@testing-library/react';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {useTaskArtifacts} from './use-task-artifacts';
import {makeArtifact, makeFakeTransport, TASK_ID, ARTIFACT_ID} from '../tasks/detail/testing/fixtures';

describe('Task成果清单归属防线', () => {
  it.each([{id: 'other-artifact'}, {taskId: 'other-task'}])('其他Transport返回错绑也不进入可下载清单：%j', async override => {
    const {transport, calls} = makeFakeTransport({getArtifact: async () => makeArtifact(override)});
    const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
    const wrapper = ({children}: {children: ReactNode}) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const view = renderHook(() => useTaskArtifacts({taskId: TASK_ID, artifactIds: [ARTIFACT_ID], transport, refetchInterval: false}), {wrapper});
    await waitFor(() => expect(view.result.current.isError).toBe(true));
    expect(view.result.current.error).toMatchObject({code: 'artifact_binding_mismatch'});
    expect(view.result.current.data).toBeUndefined();
    expect(calls.some(call => call.method === 'getArtifactContent')).toBe(false);
    view.unmount(); client.clear();
  });
  it('一项错绑时保留失败占位，不抹去同Task的合法成果', async () => {
    const {transport} = makeFakeTransport({getArtifact: async id => makeArtifact({id, taskId: id === 'bad' ? 'other-task' : TASK_ID})});
    const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
    const wrapper = ({children}: {children: ReactNode}) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const view = renderHook(() => useTaskArtifacts({taskId: TASK_ID, artifactIds: ['good', 'bad'], transport, refetchInterval: false}), {wrapper});
    await waitFor(() => expect(view.result.current.isSuccess).toBe(true));
    expect(view.result.current.data?.map(item => item.status)).toEqual(['ok', 'failed']);
    view.unmount(); client.clear();
  });
});
