// 逻辑动作控制器：一个逻辑动作一个 Idempotency-Key，内存持有到动作完结；
// 结果未知（丢响应/超时/5xx）时可显式「原键重放」（同一 key），绝不重新生成、不自动重试、不乐观成功；
// 输入（CAS/revision/选择值）变化 => 新逻辑动作 => 生成新 key。

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
  /** 当前逻辑动作的幂等键（诊断用；输入变化会换新键）。 */
  idempotencyKey: string;
  /** 提交。submitting 中重复调用直接忽略（防双击/重复点击）。 */
  submit: (run: (key: string) => Promise<unknown>) => Promise<void>;
  /** 显式原键重放：仅在结果未知后可用，复用同一键。 */
  replay: (run: (key: string) => Promise<unknown>) => Promise<void>;
  /** 放弃本次结论回到初始；会生成新键（重开逻辑动作）。 */
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
 * deps 变化 => 视为新逻辑动作，自动换新键并回到 idle（保留 UI 草稿）。
 */
export function useLogicalAction(deps: readonly unknown[]): LogicalAction {
  const depStrings = useMemo(() => deps.map(value => String(value)), [deps]);
  const [phase, setPhase] = useState<ActionPhase>({kind: 'idle'});
  const keyRef = useRef<string>(newIdempotencyKey());
  const depsRef = useRef<readonly string[]>(depStrings);
  const submittingRef = useRef(false);

  if (depsChanged(depsRef.current, depStrings)) {
    depsRef.current = depStrings;
    keyRef.current = newIdempotencyKey();
    submittingRef.current = false;
    if (phase.kind !== 'idle') setPhase({kind: 'idle'});
  }

  const execute = useCallback(async (run: (key: string) => Promise<unknown>) => {
    if (submittingRef.current) return;
    submittingRef.current = true;
    inFlightWrites += 1;
    setPhase({kind: 'submitting'});
    try {
      await run(keyRef.current);
      setPhase({kind: 'accepted'});
    } catch (error) {
      setPhase(isAmbiguousFailure(error) ? {kind: 'unknown', error} : {kind: 'rejected', error});
    } finally {
      inFlightWrites = Math.max(0, inFlightWrites - 1);
      submittingRef.current = false;
    }
  }, []);

  const replay = useCallback(async (run: (key: string) => Promise<unknown>) => {
    // 原键重放：不换新键，不自动触发，仅由用户显式点击
    await execute(run);
  }, [execute]);

  const reset = useCallback(() => {
    keyRef.current = newIdempotencyKey();
    setPhase({kind: 'idle'});
  }, []);

  return {phase, idempotencyKey: keyRef.current, submit: execute, replay, reset};
}
