// 冻结 DTO 的防御性加宽读取：真实服务响应包含冻结 Transport 类型尚未声明的字段
//（task.revision、leader.pendingRequest、question.kind/nodeId 等，见 packages/task-api/openapi.json）。
// 规则：读不到就返回 undefined/null 并如实显示「不可用」，绝不猜测或编造。

import type {LeaderRecord, PendingQuestion, Revision, TaskDetail} from '@/lib/transport/types';

/** 任务 CAS 所需的 revision。冻结 TaskRecord 未声明 revision，真实服务返回数字 Revision；统一转字符串。 */
export function rawTaskRevision(detail: Pick<TaskDetail, 'plan'> & Record<string, unknown> | TaskDetail): string | null {
  const raw = (detail as unknown as {revision?: unknown}).revision;
  if (typeof raw === 'number' && Number.isInteger(raw) && raw >= 0) return String(raw);
  if (typeof raw === 'string' && raw.trim() !== '') return raw;
  return null;
}

/** 写操作 CAS 使用的所属 Task revision；拿不到时返回 null，调用方必须禁用写操作并说明原因。 */
export function casRevisionOf(detail: TaskDetail): Revision | null {
  const own = rawTaskRevision(detail);
  if (own !== null) return own;
  const planRevision = detail.plan?.revision;
  if (typeof planRevision === 'string' && planRevision.trim() !== '') return planRevision;
  return null;
}

export function isTerminalStatus(status: string | null | undefined): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

export interface LeaderRequestOptionView {
  value: string;
  label: string;
}

export interface LeaderPendingRequestView {
  id: string;
  kind: string;
  prompt: string;
  options: LeaderRequestOptionView[];
  deadlineAt: string | null;
  status: string;
  requestDigest: string | null;
  replyDigest: string | null;
  authorization: Record<string, unknown> | null;
  nodeIds: string[];
}

/**
 * 读取 Leader pendingRequest（kind=business/publication）。
 * 返回值语义：undefined = 该服务版本未提供此投影（冻结构型的 LeaderRecord 未声明）；
 * null = 已提供且当前无待处理请求；对象 = 有待处理请求。
 */
export function leaderPendingRequest(leader: LeaderRecord | null | undefined): LeaderPendingRequestView | null | undefined {
  if (!leader) return undefined;
  const raw = leader as unknown as {pendingRequest?: unknown};
  if (!('pendingRequest' in raw)) return undefined;
  const value = raw.pendingRequest;
  if (value === null || value === undefined) return null;
  if (typeof value !== 'object') return undefined;
  const v = value as Partial<LeaderPendingRequestView>;
  if (typeof v.id !== 'string' || v.id === '' || typeof v.prompt !== 'string') return undefined;
  const options: LeaderRequestOptionView[] = Array.isArray(v.options)
    ? v.options
        .map(option => {
          if (!option || typeof option !== 'object') return null;
          const o = option as Partial<LeaderRequestOptionView>;
          if (typeof o.value !== 'string' || o.value === '' || typeof o.label !== 'string') return null;
          return {value: o.value, label: o.label};
        })
        .filter((option): option is LeaderRequestOptionView => option !== null)
    : [];
  return {
    id: v.id,
    kind: typeof v.kind === 'string' ? v.kind : 'unknown',
    prompt: v.prompt,
    options,
    deadlineAt: typeof v.deadlineAt === 'string' ? v.deadlineAt : null,
    status: typeof v.status === 'string' ? v.status : 'unknown',
    requestDigest: typeof v.requestDigest === 'string' ? v.requestDigest : null,
    replyDigest: typeof v.replyDigest === 'string' ? v.replyDigest : null,
    authorization: v.authorization && typeof v.authorization === 'object' ? (v.authorization as Record<string, unknown>) : null,
    nodeIds: Array.isArray(v.nodeIds) ? v.nodeIds.filter((n): n is string => typeof n === 'string') : [],
  };
}

/** Leader 执行阶段（真实 LeaderView.stage；冻结 LeaderRecord 未声明）。 */
export function leaderStage(leader: LeaderRecord | null | undefined): string | null {
  if (!leader) return null;
  const raw = (leader as unknown as {stage?: unknown}).stage;
  return typeof raw === 'string' && raw !== '' ? raw : null;
}

export interface QuestionStageView {
  kind: string | null;
  nodeId: string | null;
}

/** 问题的阶段归属：nodeId 非空为 Worker 运行问题；kind 原样展示。冻结 PendingQuestion 未声明这两个字段。 */
export function questionStage(question: PendingQuestion): QuestionStageView {
  const raw = question as unknown as {kind?: unknown; nodeId?: unknown};
  return {
    kind: typeof raw.kind === 'string' && raw.kind !== '' ? raw.kind : null,
    nodeId: typeof raw.nodeId === 'string' && raw.nodeId !== '' ? raw.nodeId : null,
  };
}

/** E21：轮询乱序时旧 revision 快照不得覆盖新快照。作为 useQuery 的 structuralSharing 使用（签名按 unknown 契约）。 */
export function preferFreshTask(oldData: unknown, newData: unknown): unknown {
  const fresh = newData as TaskDetail;
  const previous = oldData as TaskDetail | undefined;
  if (!previous) return fresh;
  const oldRevision = rawTaskRevision(previous);
  const newRevision = rawTaskRevision(fresh);
  if (oldRevision !== null && newRevision !== null) {
    const o = Number(oldRevision);
    const n = Number(newRevision);
    if (Number.isInteger(o) && Number.isInteger(n)) {
      return n >= o ? fresh : previous;
    }
  }
  const oldAt = Date.parse((previous as {updatedAt?: string}).updatedAt ?? previous.statusAt);
  const newAt = Date.parse((fresh as {updatedAt?: string}).updatedAt ?? fresh.statusAt);
  if (!Number.isNaN(oldAt) && !Number.isNaN(newAt) && newAt < oldAt) return previous;
  return fresh;
}

export function isPast(iso: string | null | undefined): boolean {
  if (!iso) return false;
  const time = Date.parse(iso);
  return !Number.isNaN(time) && time < Date.now();
}
