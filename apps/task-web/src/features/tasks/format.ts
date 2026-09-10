// 列表/新建共享展示辅助：中文标签只作显示，机器状态名原样保留在原始字段（title/技术详情）。
// TaskStatus/TaskPhase 枚举与 packages/task-api/openapi.json 对齐；合同外取值回退中性「未知」标签。
import type {TaskPhase, TaskStatus} from '../../lib/transport/types';

export type StatusBadgeVariant = 'default' | 'secondary' | 'success' | 'warning' | 'danger' | 'outline';

export interface StatusMeta {
  label: string;
  variant: StatusBadgeVariant;
  /** 是否计入「待处理」（仅对已加载项且 awaiting-*）。 */
  awaiting: boolean;
}

export interface PhaseMeta {
  label: string;
  variant: StatusBadgeVariant;
}

export const TASK_STATUS_META: Record<TaskStatus, StatusMeta> = {
  'draft': {label: '草稿', variant: 'secondary', awaiting: false},
  'planning': {label: '规划中', variant: 'default', awaiting: false},
  'awaiting-answer': {label: '等待回答', variant: 'warning', awaiting: true},
  'awaiting-confirmation': {label: '等待确认', variant: 'warning', awaiting: true},
  'awaiting-approval': {label: '等待批准', variant: 'warning', awaiting: true},
  'queued': {label: '排队中', variant: 'secondary', awaiting: false},
  'running': {label: '运行中', variant: 'default', awaiting: false},
  'paused': {label: '已暂停', variant: 'secondary', awaiting: false},
  'cancelling': {label: '取消中', variant: 'secondary', awaiting: false},
  'completed': {label: '已完成', variant: 'success', awaiting: false},
  'failed': {label: '已失败', variant: 'danger', awaiting: false},
  'cancelled': {label: '已取消', variant: 'secondary', awaiting: false},
  'intervention': {label: '需要干预', variant: 'danger', awaiting: false},
};

export function statusMeta(status: TaskStatus | string): StatusMeta {
  return (TASK_STATUS_META as Record<string, StatusMeta | undefined>)[status] ?? {label: '未知', variant: 'outline', awaiting: false};
}

export const TASK_PHASE_META: Record<TaskPhase, PhaseMeta> = {
  'intake': {label: '受理', variant: 'secondary'},
  'planning': {label: '规划', variant: 'default'},
  'execution': {label: '执行', variant: 'default'},
  'verification': {label: '验证', variant: 'warning'},
  'delivery': {label: '交付', variant: 'warning'},
  'terminal': {label: '终态', variant: 'outline'},
};

export function phaseMeta(phase: TaskPhase | string): PhaseMeta {
  return (TASK_PHASE_META as Record<string, PhaseMeta | undefined>)[phase] ?? {label: '未知', variant: 'outline'};
}

export function isAwaitingStatus(status: TaskStatus | string): boolean {
  return status.startsWith('awaiting-');
}

const dateTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit',
  hour12: false,
});

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '暂无数据';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '时间无法解析';
  return dateTimeFormatter.format(date);
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '暂无数据';
  if (bytes < 1024) return bytes + ' B';
  const kib = bytes / 1024;
  if (kib < 1024) return (Number.isInteger(kib) ? String(kib) : kib.toFixed(1)) + ' KiB';
  const mib = kib / 1024;
  return (Number.isInteger(mib) ? String(mib) : mib.toFixed(1)) + ' MiB';
}
