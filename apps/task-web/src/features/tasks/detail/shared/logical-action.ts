// 逻辑动作控制器：一个逻辑动作一个 Idempotency-Key，内存持有到动作完结；
// 提交时冻结输入与键；输入（CAS/revision/选择值）此后变化不解锁、不换键、不丢未决状态（UI-02），
// 迟到响应按代际丢弃；结果未知（丢响应/超时/5xx）时可显式「原键重放」（冻结 run + 同一 key），
// 绝不重新生成、不自动重试、不乐观成功；用户显式 reset 后才开新逻辑动作（新 key）。

import {useCallback, useMemo, useRef, useState} from 'react';
import {ApiError, newIdempotencyKey} from '@/lib/transport/types';

export type ActionPhase =
  | {kind: 'idle'}
  | {kind: 'submitting'}
  | {kind: 'accepted'}
  | {kind: 'rejected'; error: unknown}
  | {kind: 'unknown'; error: unknown};

export function isAmbiguousFailure(error: unknown): boolean {
  if (error instanceof ApiError) {
    // 5xx/超时/不可用：请求可能已被受理，结果未知；
    // 但 501（unsupported_operation/不支持）是服务端明确答复，属「拒绝」而非未知
    return error.status >= 500 && error.status !== 501;
  }
  // 网络层失败（fetch TypeError 等）：请求是否到达未知
  return true;
}

export interface LogicalAction {
  phase: ActionPhase;
  /** 当前逻辑动作的幂等键（诊断用）。 */
  idempotencyKey: string;
  /** 冻结输入（提交时快照）与当前渲染 deps 已不一致：轮询推进了 revision/版本，但未决动作的原键与状态保持不变。 */
  depsStale: boolean;
  /** 提交。有在途 flight 时重复调用直接忽略；在途锁不因 deps 变化释放。 */
  submit: (run: (key: string) => Promise<unknown>) => Promise<void>;
  /** 显式原键重放：复用提交时冻结的 run 与同一键；传入的 run 仅在没有冻结记录时兜底使用。 */
  replay: (run: (key: string) => Promise<unknown>) => Promise<void>;
  /** 用户明确核对后放弃本次结论回到初始；生成新键（重开逻辑动作），旧 flight 的迟到响应按代际丢弃。 */
  reset: () => void;
}

function depsChanged(previous: readonly string[], next: readonly string[]): boolean {
  if (previous.length !== next.length) return true;
  for (let i = 0; i < next.length; i += 1) {
    if (previous[i] !== next[i]) return true;
  }
  return false;
}

// ---- ADR0098 §8：有未决写操作时离开/刷新前提示（尽力而为，崩溃不保证触发） ----

let inFlightWrites = 0;
let guardTarget: Window | null = null;

function onBeforeUnload(event: BeforeUnloadEvent): void {
  if (inFlightWrites > 0) {
    event.preventDefault();
    event.returnValue = '';
  }
}

/** 应用根调用一次；返回解除函数（测试用）。尽力而为，不承诺浏览器崩溃前必然触发。 */
export function installBeforeUnloadGuard(target: Window = window): () => void {
  if (guardTarget !== null) return () => {};
  guardTarget = target;
  target.addEventListener('beforeunload', onBeforeUnload);
  return () => {
    target.removeEventListener('beforeunload', onBeforeUnload);
    guardTarget = null;
  };
}

/** 测试/诊断读数。 */
export function inFlightWriteCount(): number {
  return inFlightWrites;
}

/**
 * deps 标识逻辑动作的输入（taskId/questionId/revision/选择值等）。
 * 提交时冻结输入快照与幂等键：此后 deps 变化（轮询推进 revision 等）一律不解锁、不换键、不重置未决动作；
 * 迟到的旧响应按代际丢弃，不覆盖新状态（UI-02）。仅在动作已被受理（accepted）后，deps 变化自动开启新动作窗口。
 */
export function useLogicalAction(deps: readonly unknown[]): LogicalAction {
  const depStrings = useMemo(() => deps.map(value => String(value)), [deps]);
  const [phase, setPhase] = useState<ActionPhase>({kind: 'idle'});
  const keyRef = useRef<string>(newIdempotencyKey());
  /** 逻辑动作代际：每次 submit/replay/reset 递增；flight 完成时按代际决定是否落地状态。 */
  const generationRef = useRef(0);
  /** 在途 flight 的代际；null 表示无在途。充当提交锁且不被 deps 变化清除。 */
  const flightRef = useRef<number | null>(null);
  /** 提交时冻结的 deps 快照；用于 depsStale 提示，不用于替换输入。 */
  const frozenDepsRef = useRef<readonly string[] | null>(null);
  /** 提交时冻结的 run（闭包已捕获当次输入）；replay 复用它保证请求字节一致。 */
  const frozenRunRef = useRef<((key: string) => Promise<unknown>) | null>(null);
  const currentDepsRef = useRef<readonly string[]>(depStrings);

  if (depsChanged(currentDepsRef.current, depStrings)) {
    currentDepsRef.current = depStrings;
    if (phase.kind === 'accepted') {
      // 动作已完结：开新窗口（新键、idle）；旧键已被服务端确认，无重放需求。
      generationRef.current += 1;
      keyRef.current = newIdempotencyKey();
      frozenDepsRef.current = null;
      frozenRunRef.current = null;
      setPhase({kind: 'idle'});
    }
    // submitting/unknown/rejected：完全保留原键、在途锁与错误证据，等待用户显式核对（reset/replay）。
  }

  const execute = useCallback(async (run: (key: string) => Promise<unknown>) => {
    if (flightRef.current !== null) return;
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    flightRef.current = generation;
    frozenDepsRef.current = currentDepsRef.current;
    frozenRunRef.current = run;
    const key = keyRef.current;
    inFlightWrites += 1;
    setPhase({kind: 'submitting'});
    try {
      await run(key);
      if (generationRef.current === generation) setPhase({kind: 'accepted'});
    } catch (error) {
      if (generationRef.current === generation) {
        setPhase(isAmbiguousFailure(error) ? {kind: 'unknown', error} : {kind: 'rejected', error});
      }
    } finally {
      inFlightWrites = Math.max(0, inFlightWrites - 1);
      if (flightRef.current === generation) flightRef.current = null;
    }
  }, []);

  const replay = useCallback(async (run: (key: string) => Promise<unknown>) => {
    // 原键重放：不换新键、不自动触发、且必须使用冻结的原始请求（同键不同 body 会触发 idempotency_conflict）
    await execute(frozenRunRef.current ?? run);
  }, [execute]);

  const reset = useCallback(() => {
    generationRef.current += 1;
    keyRef.current = newIdempotencyKey();
    frozenDepsRef.current = null;
    frozenRunRef.current = null;
    setPhase({kind: 'idle'});
  }, []);

  const depsStale = frozenDepsRef.current !== null && depsChanged(frozenDepsRef.current, currentDepsRef.current);
  return {phase, idempotencyKey: keyRef.current, depsStale, submit: execute, replay, reset};
}
