// 详情域派生读数：全部基于冻结 Transport 类型（逐一对齐 packages/task-api/openapi.json）。
// Revision 一律为数字；合同已声明的字段直接读取，不做防御性加宽，未投影的事实如实缺失。

import type {
  LeaderRecord,
  LeaderRequestDTO,
  NodeId,
  QuestionItem,
  QuestionKind,
  Revision,
  TaskRecord,
  TaskStatus,
} from '@/lib/transport/types';

/** 写操作 CAS 使用的所属 Task revision。TaskRecord.revision 为合同必返字段，永远可用。 */
export function casRevisionOf(task: Pick<TaskRecord, 'revision'>): Revision {
  return task.revision;
}

export function isTerminalStatus(status: TaskStatus | string | null | undefined): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

/**
 * 读取 Leader 当前待处理请求（kind=business/publication）。
 * 合同保证投影必返：leader 为 null 表示端点不可用/未启用；pendingRequest 为 null 表示当前无待处理请求。
 */
export function leaderPendingRequest(leader: LeaderRecord | null | undefined): LeaderRequestDTO | null {
  return leader?.pendingRequest ?? null;
}

export interface QuestionStageView {
  kind: QuestionKind | 'business';
  nodeId: NodeId | null;
}

/** 问题的阶段归属：RunningQuestion（kind=business）必有 workerId/nodeId；预批准问题的 nodeId 可空。 */
export function questionStage(question: QuestionItem): QuestionStageView {
  if (question.kind === 'business') {
    return {kind: 'business', nodeId: question.nodeId};
  }
  return {kind: question.kind, nodeId: question.nodeId ?? null};
}

/** 预批准问题三类标签；运行问题带节点徽章。措辞固定，不猜测未知 kind。 */
export function questionStageBadge(question: QuestionItem): string {
  const stage = questionStage(question);
  if (stage.kind === 'business') {
    return `Worker 运行问题（节点 ${stage.nodeId ?? '未知'}）`;
  }
  switch (stage.kind) {
    case 'clarification':
      return '预批准澄清问题';
    case 'permission':
      return '预批准许可问题';
    case 'acceptance':
      return '验收口径问题';
    default:
      return '待回答问题';
  }
}

/** E21：轮询乱序时旧 revision 快照不得覆盖新快照。作为 useQuery 的 structuralSharing 使用（签名按 unknown 契约）。 */
export function preferFreshTask(oldData: unknown, newData: unknown): unknown {
  const fresh = newData as TaskRecord;
  const previous = oldData as TaskRecord | undefined;
  if (!previous) return fresh;
  if (typeof previous.revision === 'number' && typeof fresh.revision === 'number') {
    if (fresh.revision > previous.revision) return fresh;
    if (fresh.revision < previous.revision) return previous;
  }
  const oldAt = Date.parse(previous.updatedAt);
  const newAt = Date.parse(fresh.updatedAt);
  if (!Number.isNaN(oldAt) && !Number.isNaN(newAt) && newAt < oldAt) return previous;
  return fresh;
}

export function isPastAt(iso: string | null | undefined, now: number): boolean {
  if (!iso) return false;
  const time = Date.parse(iso);
  return !Number.isNaN(time) && time < now;
}

export function isPast(iso: string | null | undefined): boolean {
  return isPastAt(iso, Date.now());
}

/**
 * 概览需要你处理/可核对的问题（UI-08）：
 * - open：等待作答；
 * - 运行中 Worker 问题已答但 ACK 未落定（pending/dispatched/unknown/无读数）：保留在概览供核对，不因离开 open 而消失。
 */
export function questionNeedsAttention(question: QuestionItem): boolean {
  if (question.status === 'open') return true;
  if (question.kind === 'business' && question.status === 'answered') {
    return question.deliveryStatus !== 'acknowledged' && question.deliveryStatus !== 'cancelled' && question.deliveryStatus !== 'expired';
  }
  return false;
}
