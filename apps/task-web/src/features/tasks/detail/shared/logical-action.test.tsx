// 逻辑动作控制器回归：幂等语义 + ADR0098 §8 离开前提示（有未决写操作时阻止静默离开）。
import {afterEach, describe, expect, it, vi} from 'vitest';
import {act, render, renderHook, screen} from '@testing-library/react';
import {StrictMode} from 'react';
import userEvent from '@testing-library/user-event';
import {ApiError} from '@/lib/transport/types';
import {installBeforeUnloadGuard, inFlightWriteCount, LogicalActionScope, useLogicalAction, useLogicalActionMemory, type LogicalAction} from './logical-action';

describe('会话级原请求保留（SPA 导航）', () => {
  let action: LogicalAction;
  function Consumer({revision = 3}: {revision?: number}) {
    action = useLogicalAction(['task-1', 'answer', revision], ['task-1', 'answer', 'question-1']);
    return <p>{action.phase.kind}</p>;
  }

  it('unknown 卸载重挂保留键和原闭包；新 revision 不替换原 body；始终需要显式重放', async () => {
    const stop = installBeforeUnloadGuard();
    const requests: string[] = [];
    const run = vi.fn(async (key: string) => { requests.push(`${key}:original-body`); throw new TypeError('lost'); });
    const view = render(<LogicalActionScope><Consumer /></LogicalActionScope>);
    try {
      await act(async () => { await action.submit(run); });
      const key = action.idempotencyKey;
      expect(inFlightWriteCount()).toBe(0);
      const event = new Event('beforeunload', {cancelable: true});
      window.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(true);
      view.rerender(<LogicalActionScope><p>其他路由</p></LogicalActionScope>);
      expect(screen.getByRole('status')).toHaveTextContent('刷新、关闭页面或断开连接会丢失');
      expect(run).toHaveBeenCalledTimes(1);
      view.rerender(<LogicalActionScope><Consumer revision={4} /></LogicalActionScope>);
      expect(action.phase.kind).toBe('unknown');
      expect(action.idempotencyKey).toBe(key);
      expect(action.depsStale).toBe(true);
      const replacement = vi.fn(async () => 'new-body');
      await act(async () => { await action.submit(replacement); });
      expect(replacement).not.toHaveBeenCalled();
      await act(async () => { await action.replay(replacement); });
      expect(requests).toEqual([`${key}:original-body`, `${key}:original-body`]);
      expect(replacement).not.toHaveBeenCalled();
    } finally { view.unmount(); stop(); }
  });

  it('submitting 卸载重挂仍锁定；卸载期间的晚失败在会话提示可见，并可显式原键重放', async () => {
    const user = userEvent.setup();
    let reject!: (reason: unknown) => void;
    const pending = new Promise<void>((_, fail) => { reject = fail; });
    let first = true;
    const run = vi.fn(async () => { if (first) { first = false; await pending; } });
    const view = render(<LogicalActionScope><Consumer /></LogicalActionScope>);
    let submission!: Promise<void>;
    await act(async () => { submission = action.submit(run); });
    const key = action.idempotencyKey;
    view.rerender(<LogicalActionScope><p>其他路由</p></LogicalActionScope>);
    view.rerender(<LogicalActionScope><Consumer revision={7} /></LogicalActionScope>);
    expect(action.idempotencyKey).toBe(key);
    expect(action.phase.kind).toBe('submitting');
    await act(async () => { await action.submit(run); });
    expect(run).toHaveBeenCalledTimes(1);
    view.rerender(<LogicalActionScope><p>其他路由</p></LogicalActionScope>);
    await act(async () => { reject(new TypeError('response lost')); await submission; });
    await user.click(screen.getByRole('button', {name: '显式原键重放'}));
    expect(run).toHaveBeenCalledTimes(2);
    expect(run.mock.calls[0]).toEqual(run.mock.calls[1]);
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('会话销毁阻止旧操作重放；晚响应不写入新会话，unknown 离开提示随会话清理', async () => {
    const stop = installBeforeUnloadGuard();
    const oldRun = vi.fn(async () => { throw new TypeError('old connection'); });
    const view = render(<LogicalActionScope session="first"><Consumer /></LogicalActionScope>);
    try {
      await act(async () => { await action.submit(oldRun); });
      const old = action;
      view.rerender(<LogicalActionScope session="second"><Consumer /></LogicalActionScope>);
      expect(action.phase.kind).toBe('idle');
      expect(action.idempotencyKey).not.toBe(old.idempotencyKey);
      await act(async () => { await old.replay(oldRun); });
      expect(oldRun).toHaveBeenCalledTimes(1);
      const event = new Event('beforeunload', {cancelable: true});
      window.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(false);
      let resolve!: () => void;
      const pending = new Promise<void>(done => { resolve = done; });
      let submission!: Promise<void>;
      await act(async () => { submission = action.submit(() => pending); });
      view.rerender(<p>已断开或401</p>);
      view.rerender(<LogicalActionScope session="third"><Consumer /></LogicalActionScope>);
      await act(async () => { resolve(); await submission; });
      expect(action.phase.kind).toBe('idle');
      expect(screen.queryByRole('status')).not.toBeInTheDocument();
    } finally { view.unmount(); stop(); }
  });

  it('StrictMode effect 重演后仍可提交并保留 unknown', async () => {
    render(<StrictMode><LogicalActionScope><Consumer /></LogicalActionScope></StrictMode>);
    await act(async () => { await action.submit(async () => { throw new TypeError('lost'); }); });
    expect(action.phase.kind).toBe('unknown');
  });

  it('受理结果跨卸载重挂保留，供原 Operation 回执核对', async () => {
    const view = render(<LogicalActionScope><Consumer /></LogicalActionScope>);
    const result = {operationId: 'operation-1'};
    await act(async () => { await action.submit(async () => result); });
    view.rerender(<LogicalActionScope><p>其他路由</p></LogicalActionScope>);
    view.rerender(<LogicalActionScope><Consumer /></LogicalActionScope>);
    expect(action.phase).toEqual({kind: 'accepted', result});
  });
});

describe('同一注册表的会话草稿内存', () => {
  it('SPA 保留草稿；新连接重置，旧 setter 与晚回调不能污染新连接', () => {
    let state!: ReturnType<typeof useLogicalActionMemory<{draft: string}>>;
    function Draft() {
      state = useLogicalActionMemory(['create-draft'], () => ({draft: ''}));
      return <p>{state[0].draft}</p>;
    }
    const view = render(<StrictMode><LogicalActionScope session="first"><Draft /></LogicalActionScope></StrictMode>);
    act(() => state[1]({draft: '原草稿'}));
    view.rerender(<StrictMode><LogicalActionScope session="first"><p>其他路由</p></LogicalActionScope></StrictMode>);
    view.rerender(<StrictMode><LogicalActionScope session="first"><Draft /></LogicalActionScope></StrictMode>);
    expect(state[0].draft).toBe('原草稿');
    const old = state;
    view.rerender(<StrictMode><LogicalActionScope session="second"><Draft /></LogicalActionScope></StrictMode>);
    expect(state[0].draft).toBe('');
    expect(old[2]()).toBe(false);
    act(() => old[1]({draft: '迟到旧连接结果'}));
    expect(state[0].draft).toBe('');
    expect(state[2]()).toBe(true);
  });
});

describe('beforeunload 守卫（ADR0098 §8）', () => {
  let stop: (() => void) | null = null;

  afterEach(() => {
    stop?.();
    stop = null;
  });

  it('无未决写操作时不干预；提交中阻止静默离开；完结后放开', async () => {
    stop = installBeforeUnloadGuard();
    expect(inFlightWriteCount()).toBe(0);

    const idleEvent = new Event('beforeunload', {cancelable: true});
    window.dispatchEvent(idleEvent);
    expect(idleEvent.defaultPrevented).toBe(false);

    let release: (() => void) | null = null;
    const pending = new Promise<void>(resolve => { release = resolve; });
    const {result} = renderHook(() => useLogicalAction(['task-1', 'answer']));

    let submission: Promise<void> | null = null;
    await act(async () => {
      submission = result.current.submit(() => pending);
    });
    expect(inFlightWriteCount()).toBe(1);

    const duringEvent = new Event('beforeunload', {cancelable: true});
    window.dispatchEvent(duringEvent);
    expect(duringEvent.defaultPrevented).toBe(true);

    await act(async () => {
      release!();
      await submission;
    });
    expect(inFlightWriteCount()).toBe(0);

    const afterEvent = new Event('beforeunload', {cancelable: true});
    window.dispatchEvent(afterEvent);
    expect(afterEvent.defaultPrevented).toBe(false);
  });
});

describe('UI-02：冻结输入与代际隔离（轮询推进 revision 不得破坏未决动作）', () => {
  function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
    return {promise, resolve, reject};
  }

  it('submitting 中 deps 变化：不解锁、不换键，重复 submit 被忽略', async () => {
    const first = deferred<unknown>();
    const second = vi.fn(async () => 'should-not-run');
    const {result, rerender} = renderHook(
      ({deps}: {deps: readonly unknown[]}) => useLogicalAction(deps),
      {initialProps: {deps: ['task-1', 'answer', 3]}},
    );
    const key = result.current.idempotencyKey;

    let submission: Promise<void> | null = null;
    await act(async () => {
      submission = result.current.submit(() => first.promise);
    });
    expect(result.current.phase.kind).toBe('submitting');

    // 轮询推进 revision：deps 变化不得释放提交锁、不得换键
    rerender({deps: ['task-1', 'answer', 4]});
    expect(result.current.phase.kind).toBe('submitting');
    expect(result.current.idempotencyKey).toBe(key);
    expect(result.current.depsStale).toBe(true);

    await act(async () => {
      await result.current.submit(() => second());
    });
    expect(second).not.toHaveBeenCalled();

    await act(async () => {
      first.resolve('ok');
      await submission;
    });
    expect(result.current.phase.kind).toBe('accepted');
  });

  it('unknown 中 deps 变化：原键与错误证据保留，replay 复用冻结 run 与同一键', async () => {
    const bodies: string[] = [];
    const keys: string[] = [];
    // 冻结模型：闭包即请求——首发丢回执，同一闭包重放则成功（等价于服务端幂等去重后的受理）
    let attempts = 0;
    const request = async (key: string): Promise<unknown> => {
      keys.push(key);
      bodies.push('v1');
      attempts += 1;
      if (attempts === 1) throw new TypeError('network down');
      return 'accepted';
    };
    const {result, rerender} = renderHook(
      ({revision, answer}: {revision: number; answer: string}) => useLogicalAction(['task-1', 'answer', revision, answer]),
      {initialProps: {revision: 3, answer: 'v1'}},
    );

    await act(async () => {
      await result.current.submit(request);
    });
    expect(result.current.phase.kind).toBe('unknown');
    const key = result.current.idempotencyKey;

    // 轮询推进 revision 且用户界面别处数据变化：原键、未决状态保留
    rerender({revision: 4, answer: 'v1'});
    expect(result.current.phase.kind).toBe('unknown');
    expect(result.current.idempotencyKey).toBe(key);
    expect(result.current.depsStale).toBe(true);

    // replay 必须执行提交时冻结的请求（v1），而非新渲染闭包捕获的值
    await act(async () => {
      await result.current.replay(async replayedKey => {
        keys.push(replayedKey);
        bodies.push('v2-closure');
        return 'accepted';
      });
    });
    expect(keys).toEqual([key, key]);
    expect(bodies).toEqual(['v1', 'v1']);
    expect(result.current.phase.kind).toBe('accepted');
  });

  it('409 拒绝后 deps 变化：不自动重置；显式 reset 才开新动作（新键）', async () => {
    const conflict = new ApiError(409, 'revision_conflict', 'revision 已推进', 'req-409');
    const {result, rerender} = renderHook(
      ({revision}: {revision: number}) => useLogicalAction(['task-1', 'answer', revision]),
      {initialProps: {revision: 3}},
    );
    const key = result.current.idempotencyKey;

    await act(async () => {
      await result.current.submit(async () => { throw conflict; });
    });
    expect(result.current.phase.kind).toBe('rejected');

    rerender({revision: 4});
    expect(result.current.phase.kind).toBe('rejected');
    expect(result.current.idempotencyKey).toBe(key);

    await act(async () => {
      result.current.reset();
    });
    expect(result.current.phase.kind).toBe('idle');
    expect(result.current.idempotencyKey).not.toBe(key);
    expect(result.current.depsStale).toBe(false);
  });

  it('迟到旧响应按代际丢弃：reset 后旧 flight resolve 不覆盖 idle', async () => {
    const slow = deferred<unknown>();
    const {result} = renderHook(() => useLogicalAction(['task-1', 'answer', 3]));

    await act(async () => {
      void result.current.submit(() => slow.promise);
    });
    expect(result.current.phase.kind).toBe('submitting');

    await act(async () => {
      result.current.reset();
    });
    expect(result.current.phase.kind).toBe('idle');

    // 旧 flight 迟到落地：不得把状态推进 accepted
    await act(async () => {
      slow.resolve('late');
      await slow.promise;
    });
    expect(result.current.phase.kind).toBe('idle');

    // 新动作不受旧 flight 影响（旧 flight 已完结，锁已释放）
    await act(async () => {
      await result.current.submit(async () => 'fresh');
    });
    expect(result.current.phase.kind).toBe('accepted');
  });

  it('accepted 后 deps 变化自动开新窗口（新键、idle）', async () => {
    const {result, rerender} = renderHook(
      ({revision}: {revision: number}) => useLogicalAction(['task-1', 'answer', revision]),
      {initialProps: {revision: 3}},
    );
    const key = result.current.idempotencyKey;

    await act(async () => {
      await result.current.submit(async () => 'ok');
    });
    expect(result.current.phase.kind).toBe('accepted');

    rerender({revision: 4});
    expect(result.current.phase.kind).toBe('idle');
    expect(result.current.idempotencyKey).not.toBe(key);
    expect(result.current.depsStale).toBe(false);
  });

  it('501 属明确拒绝（rejected），不是结果未知', async () => {
    const unsupported = new ApiError(501, 'unsupported_operation', '不支持', null);
    const {result} = renderHook(() => useLogicalAction(['task-1', 'answer']));
    await act(async () => {
      await result.current.submit(async () => { throw unsupported; });
    });
    expect(result.current.phase.kind).toBe('rejected');
  });
});
