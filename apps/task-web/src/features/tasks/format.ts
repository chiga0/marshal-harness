// 列表/新建共享展示辅助：中文标签只作显示，机器状态名原样保留在原始字段（title/技术详情）。
import type {TaskStatus} from '../../lib/transport/types';

export type StatusBadgeVariant = 'default' | 'secondary' | 'success' | 'warning' | 'danger' | 'outline';

export interface StatusMeta {
  label: string;
  variant: StatusBadgeVariant;
  /** 是否计入「待处理」（仅对已加载项且 awaiting-*）。 */
  awaiting: boolean;
}

export const TASK_STATUS_META: Record<TaskStatus, StatusMeta> = {
  'running': {label: '运行中', variant: 'default', awaiting: false},
  'awaiting-answer': {label: '等待回答', variant: 'warning', awaiting: true},
  'awaiting-confirmation': {label: '等待确认', variant: 'warning', awaiting: true},
  'awaiting-decision': {label: '等待决定', variant: 'warning', awaiting: true},
  'completed': {label: '已完成', variant: 'success', awaiting: false},
  'confirmed': {label: '已确认', variant: 'success', awaiting: false},
  'failed': {label: '已失败', variant: 'danger', awaiting: false},
  'cancelled': {label: '已取消', variant: 'secondary', awaiting: false},
  'unknown': {label: '状态未知', variant: 'outline', awaiting: false},
};

export function statusMeta(status: TaskStatus | string): StatusMeta {
  return (TASK_STATUS_META as Record<string, StatusMeta | undefined>)[status] ?? {label: status, variant: 'outline', awaiting: false};
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
