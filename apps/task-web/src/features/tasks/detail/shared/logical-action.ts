// 页面内存中的逻辑动作；SPA 卸载不丢原键/冻结请求，断开会话销毁全部闭包。
// 不持久化、不自动重试；刷新/崩溃后只能人工核对服务事实。
import {createContext, createElement, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useState, useSyncExternalStore, type Dispatch, type ReactNode, type SetStateAction} from 'react';
import {ApiError, newIdempotencyKey} from '@/lib/transport/types';

export type ActionPhase =
  | {kind: 'idle'} | {kind: 'submitting'} | {kind: 'accepted'; result?: unknown}
  | {kind: 'rejected'; error: unknown} | {kind: 'unknown'; error: unknown};

export function isAmbiguousFailure(error: unknown): boolean {
  return error instanceof ApiError ? error.status >= 500 && error.status !== 501 : true;
}

export interface LogicalAction {
  phase: ActionPhase;
  idempotencyKey: string;
  depsStale: boolean;
  submit: (run: (key: string) => Promise<unknown>) => Promise<void>;
  replay: (run: (key: string) => Promise<unknown>) => Promise<void>;
  reset: () => void;
}

type Run = (key: string) => Promise<unknown>;
type Entry = {
  phase: ActionPhase; key: string; generation: number; flight: number | null;
  observedDeps: readonly string[] | null; frozenDeps: readonly string[] | null; frozenRun: Run | null;
};
const unresolved = new Set<Entry>();
let inFlightWrites = 0;
let guardTarget: Window | null = null;
function onBeforeUnload(event: BeforeUnloadEvent): void {
  if (unresolved.size > 0) { event.preventDefault(); event.returnValue = ''; }
}
export function installBeforeUnloadGuard(target: Window = window): () => void {
  if (guardTarget !== null) return () => {};
  guardTarget = target;
  target.addEventListener('beforeunload', onBeforeUnload);
  return () => { target.removeEventListener('beforeunload', onBeforeUnload); guardTarget = null; };
}
export function inFlightWriteCount(): number { return inFlightWrites; }

function depsChanged(a: readonly string[], b: readonly string[]): boolean {
  return a.length !== b.length || a.some((value, index) => value !== b[index]);
}
function freshEntry(): Entry {
  return {phase: {kind: 'idle'}, key: newIdempotencyKey(), generation: 0, flight: null, observedDeps: null, frozenDeps: null, frozenRun: null};
}

class Registry {
  entries = new Map<string, Entry>();
  memory = new Map<string, {value: unknown; initialize: () => unknown}>();
  active = true;
  version = 0;
  listeners = new Set<() => void>();
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  snapshot = () => this.version;
  emit() { this.version += 1; this.listeners.forEach(listener => listener()); }
  entry(identity: string) {
    let entry = this.entries.get(identity);
    if (!entry) { entry = freshEntry(); this.entries.set(identity, entry); }
    return entry;
  }
  reset(entry: Entry) {
    entry.generation += 1;
    entry.key = newIdempotencyKey();
    entry.frozenDeps = null;
    entry.frozenRun = null;
    entry.phase = {kind: 'idle'};
    if (entry.flight === null) unresolved.delete(entry);
    // 旧 flight 仍持有锁，直到 finally；代际阻止它覆盖新状态。
    this.emit();
  }
  destroy() {
    this.active = false;
    for (const record of this.memory.values()) record.value = record.initialize();
    for (const entry of this.entries.values()) {
      entry.generation += 1;
      entry.frozenRun = null;
      entry.frozenDeps = null;
      entry.observedDeps = null;
      entry.phase = {kind: 'idle'};
      entry.key = newIdempotencyKey();
      unresolved.delete(entry);
    }
    // 保留空壳以支持 React StrictMode effect 重演；不保留错误/请求/secret。
  }
  async execute(entry: Entry, run: Run, deps: readonly string[], replay = false) {
    if (!this.active || entry.flight !== null) return;
    // unknown 只能走显式 replay，不允许 submit 替换原请求。
    if (!replay && entry.phase.kind !== 'idle') return;
    if (replay && entry.phase.kind !== 'unknown') return;
    const frozenRun = replay ? entry.frozenRun : run;
    if (!frozenRun) return;
    const generation = ++entry.generation;
    entry.flight = generation;
    if (!replay) { entry.frozenDeps = [...deps]; entry.frozenRun = frozenRun; }
    const key = entry.key;
    entry.phase = {kind: 'submitting'};
    unresolved.add(entry);
    inFlightWrites += 1;
    this.emit();
    try {
      const result = await frozenRun(key);
      if (this.active && entry.generation === generation) entry.phase = {kind: 'accepted', result};
    } catch (error) {
      if (this.active && entry.generation === generation) entry.phase = isAmbiguousFailure(error) ? {kind: 'unknown', error} : {kind: 'rejected', error};
    } finally {
      inFlightWrites = Math.max(0, inFlightWrites - 1);
      if (entry.flight === generation) entry.flight = null;
      if (!this.active || (entry.phase.kind !== 'submitting' && entry.phase.kind !== 'unknown')) unresolved.delete(entry);
      if (this.active) this.emit();
    }
  }
}

const LogicalActionContext = createContext<Registry | null>(null);

/** 与动作共用连接内存；initialize 必须返回无凭据的初始草稿，不捕获旧连接/API。
 * 未挂载 scope 的组件测试退化为组件局部内存；销毁后旧 setter/回调禁止写回。
 */
export function useLogicalActionMemory<T>(identity: readonly unknown[], initialize: () => T): readonly [T, Dispatch<SetStateAction<T>>, () => boolean] {
  const scope = useContext(LogicalActionContext);
  const [local] = useState(() => new Registry());
  const owner = scope ?? local;
  const key = JSON.stringify(identity);
  let record = owner.memory.get(key);
  if (!record) { record = {value: initialize(), initialize}; owner.memory.set(key, record); }
  const stableRecord = record;
  useSyncExternalStore(owner.subscribe, owner.snapshot, owner.snapshot);
  useLayoutEffect(() => { local.active = true; return () => local.destroy(); }, [local]);
  const setState = useCallback<Dispatch<SetStateAction<T>>>(next => {
    if (!owner.active) return;
    const value = typeof next === 'function' ? (next as (previous: T) => T)(stableRecord.value as T) : next;
    if (Object.is(value, stableRecord.value)) return;
    stableRecord.value = value;
    owner.emit();
  }, [owner, stableRecord]);
  const isActive = useCallback(() => owner.active, [owner]);
  return [stableRecord.value as T, setState, isActive];
}

/** 挂在连接 gate 内、路由外；session 变化即创建全新注册表。 */
export function LogicalActionScope({children, session}: {children: ReactNode; session?: unknown}) {
  const registry = useMemo(() => new Registry(), [session]);
  useLayoutEffect(() => { registry.active = true; return () => registry.destroy(); }, [registry]);
  return createElement(LogicalActionContext.Provider, {value: registry},
    createElement('div', {className: 'flex h-screen min-w-0 flex-col overflow-hidden'},
      createElement(PendingActionsNotice, {registry}),
      createElement('div', {className: 'min-h-0 min-w-0 flex-1 overflow-auto [&>*]:h-full'}, children),
    ),
  );
}

function PendingActionsNotice({registry}: {registry: Registry}) {
  useSyncExternalStore(registry.subscribe, registry.snapshot, registry.snapshot);
  const pending = [...registry.entries.entries()].filter(([, entry]) => entry.phase.kind === 'unknown' || entry.phase.kind === 'submitting');
  if (!pending.length) return null;
  return createElement('section', {role: 'status', 'aria-label': '本次连接未决操作', className: 'max-h-40 shrink-0 overflow-auto border-b border-warning bg-surface p-3 text-sm [overflow-wrap:anywhere]'},
    createElement('p', null, '有操作正在提交或结果未知。切换页面仍保留原请求；刷新、关闭页面或断开连接会丢失内存中的原键，请先核对任务和回执。不会自动重试。'),
    ...pending.map(([identity, entry]) => createElement('div', {key: identity, className: 'mt-2 flex flex-wrap items-center gap-2'},
      createElement('span', null, `${identity}：${entry.phase.kind === 'unknown' ? '结果未知，请核对服务端事实' : '正在提交'}`),
      entry.phase.kind === 'unknown' ? createElement('button', {type: 'button', className: 'min-h-11 rounded border border-border px-3 text-accent focus-visible:outline focus-visible:outline-2', onClick: () => { void registry.execute(entry, entry.frozenRun!, entry.frozenDeps ?? [], true); }}, '显式原键重放') : null,
    )),
  );
}

/** identity 只含 Task/动作/请求或 Worker 标识，不含 CAS/摘要/回答；deps 才表示当前输入。 */
export function useLogicalAction(deps: readonly unknown[], identity?: readonly unknown[]): LogicalAction {
  const scope = useContext(LogicalActionContext);
  const [local] = useState(() => new Registry());
  // 未提供稳定 identity 时仍隔离在组件自身，不猜测输入中的业务字段。
  const owner = scope && identity ? scope : local;
  const entry = owner.entry(identity ? JSON.stringify(identity) : 'component-local');
  const serializedDeps = JSON.stringify(deps.map(value => String(value)));
  const depStrings: readonly string[] = useMemo(() => JSON.parse(serializedDeps) as string[], [serializedDeps]);
  useSyncExternalStore(owner.subscribe, owner.snapshot, owner.snapshot);
  useLayoutEffect(() => { local.active = true; return () => local.destroy(); }, [local]);
  useEffect(() => {
    const changed = entry.observedDeps !== null && depsChanged(entry.observedDeps, depStrings);
    entry.observedDeps = depStrings;
    if (changed && entry.phase.kind === 'accepted') owner.reset(entry);
  }, [owner, entry, depStrings]);
  const submit = useCallback((run: Run) => owner.execute(entry, run, depStrings), [owner, entry, depStrings]);
  const replay = useCallback((run: Run) => owner.execute(entry, run, depStrings, true), [owner, entry, depStrings]);
  const reset = useCallback(() => { if (owner.active) owner.reset(entry); }, [owner, entry]);
  return {phase: entry.phase, idempotencyKey: entry.key, depsStale: entry.frozenDeps !== null && depsChanged(entry.frozenDeps, depStrings), submit, replay, reset};
}
