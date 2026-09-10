// 逻辑动作控制器回归：幂等语义 + ADR0098 §8 离开前提示（有未决写操作时阻止静默离开）。
import {afterEach, describe, expect, it} from 'vitest';
import {act, renderHook} from '@testing-library/react';
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
