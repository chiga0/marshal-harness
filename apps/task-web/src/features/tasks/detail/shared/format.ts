// 格式化与状态标签：中文是标签，机器状态名原样保留；解析失败回退原文，不编造。

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '暂无数据';
  const time = Date.parse(iso);
  if (Number.isNaN(time)) return iso;
  const d = new Date(time);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function formatRelative(iso: string | null | undefined): string {
  if (!iso) return '暂无数据';
  const time = Date.parse(iso);
  if (Number.isNaN(time)) return iso;
  const delta = Date.now() - time;
  if (delta < 0) return '刚刚';
  if (delta < 45_000) return '刚刚';
  if (delta < 90_000) return '1 分钟前';
  const minutes = Math.floor(delta / 60_000);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  return `${days} 天前`;
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined) return '暂无数据';
  if (!Number.isFinite(bytes) || bytes < 0) return '暂无数据';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return '暂无数据';
  if (ms < 1000) return `${ms} 毫秒`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分 ${seconds % 60} 秒`;
  const hours = Math.floor(minutes / 60);
  return `${hours} 小时 ${minutes % 60} 分`;
}

export function truncateMiddle(text: string, head = 12, tail = 8): string {
  if (text.length <= head + tail + 3) return text;
  return `${text.slice(0, head)}…${text.slice(-tail)}`;
}

function pairs<T extends string>(table: Record<T, string>): (value: string) => string {
  return value => table[value as T] ?? '未知';
}

export const TASK_STATUS_LABELS: Record<string, string> = {
  'running': '执行中',
  'awaiting-answer': '等待你的答复',
  'awaiting-confirmation': '等待计划确认',
  'awaiting-decision': '等待 Leader 决定',
  'confirmed': '已确认',
  'completed': '已完成',
  'failed': '失败',
  'cancelled': '已取消',
  'unknown': '状态未知',
};
export const taskStatusLabel = pairs(TASK_STATUS_LABELS);

export const WORKER_STATUS_LABELS: Record<string, string> = {
  'queued': '排队中',
  'running': '运行中',
  'stopping': '停止中',
  'completed': '已完成',
  'failed': '失败',
  'cancelled': '已取消',
  'unknown': '状态未知',
};
export const workerStatusLabel = pairs(WORKER_STATUS_LABELS);

export const WORKER_ROLE_LABELS: Record<string, string> = {
  'author': '执行',
  'reviewer': '评审',
  'verifier': '校验',
  'leader': 'Leader',
  'unknown': '未知角色',
};
export const workerRoleLabel = pairs(WORKER_ROLE_LABELS);

export const WORKER_PHASE_LABELS: Record<string, string> = {
  'intake': '接收',
  'working': '执行',
  'verify': '校验',
  'review': '评审',
  'conclude': '收尾',
  'terminal': '终态',
  'unknown': '未知阶段',
};
export const workerPhaseLabel = pairs(WORKER_PHASE_LABELS);

export const ACCEPTANCE_LABELS: Record<string, string> = {
  'passed': '独立验收通过',
  'failed': '独立验收未通过',
  'pending': '独立验收进行中',
  'unknown': '验收状态未知',
};
export const acceptanceLabel = pairs(ACCEPTANCE_LABELS);

export const PUBLICATION_LABELS: Record<string, string> = {
  'created': '已创建',
  'publishing': '发布中',
  'published': '已发布',
  'postverify-passed': '后验通过',
  'postverify-failed': '后验失败',
  'unknown': '状态未知',
};
export const publicationLabel = pairs(PUBLICATION_LABELS);

export const LEADER_STAGE_LABELS: Record<string, string> = {
  'intake': '需求接收',
  'work': '执行组织',
  'review': '集中评审',
  'verification': '校验',
  'delivery': '交付',
  'finalizing': '收尾',
  'terminal': '终态',
};
export const leaderStageLabel = pairs(LEADER_STAGE_LABELS);
