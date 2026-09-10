// 逻辑动作控制器回归：幂等语义 + ADR0098 §8 离开前提示（有未决写操作时阻止静默离开）。
import {afterEach, describe, expect, it, vi} from 'vitest';
import {act, renderHook} from '@testing-library/react';
import {ApiError} from '@/lib/transport/types';
import {installBeforeUnloadGuard, inFlightWriteCount, useLogicalAction} from './logical-action';

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
