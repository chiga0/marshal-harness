// 格式化与状态标签：中文是标签，机器状态名原样保留；解析失败回退原文，不编造。
// 标签表逐一对齐 packages/task-api/openapi.json 的闭集枚举；未命中值统一「未知」，不猜测。

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

// TaskStatus（contract 闭集 13 值）
export const TASK_STATUS_LABELS: Record<string, string> = {
  'draft': '草稿',
  'planning': '规划中',
  'awaiting-answer': '等待你的答复',
  'awaiting-confirmation': '等待计划确认',
  'awaiting-approval': '等待计划批准',
  'queued': '排队中',
  'running': '执行中',
  'paused': '已暂停',
  'cancelling': '取消中',
  'completed': '已完成',
  'failed': '失败',
  'cancelled': '已取消',
  'intervention': '需要人工干预',
};
export const taskStatusLabel = pairs(TASK_STATUS_LABELS);

// WorkerStatus（contract 闭集 8 值）
export const WORKER_STATUS_LABELS: Record<string, string> = {
  'queued': '排队中',
  'running': '运行中',
  'awaiting-answer': '等待答复',
  'stopping': '停止中',
  'completed': '已完成',
  'failed': '失败',
  'cancelled': '已取消',
  'unknown': '状态未知',
};
export const workerStatusLabel = pairs(WORKER_STATUS_LABELS);

// WorkerRole / PlanNodeRole（contract 闭集 5 值）
export const WORKER_ROLE_LABELS: Record<string, string> = {
  'planner': '规划',
  'author': '执行',
  'reviewer': '评审',
  'integrator': '集成',
  'verifier': '校验',
};
export const workerRoleLabel = pairs(WORKER_ROLE_LABELS);

// WorkerPhase（contract 闭集 6 值）
export const WORKER_PHASE_LABELS: Record<string, string> = {
  'planning': '规划',
  'development': '开发',
  'review': '评审',
  'verification': '校验',
  'integration': '集成',
  'terminal': '终态',
};
export const workerPhaseLabel = pairs(WORKER_PHASE_LABELS);

// 验收（受理于 Leader 集中评审 verdict；contract 闭集 3 值）
export const ACCEPTANCE_LABELS: Record<string, string> = {
  'accept': '评审通过',
  'rework': '要求返工',
  'reject': '评审拒绝',
};
export const acceptanceLabel = pairs(ACCEPTANCE_LABELS);

// 独立验收 Acceptance.status（GET /v1/tasks/{taskId}/audit；contract 闭集 4 值）
export const ACCEPTANCE_STATUS_LABELS: Record<string, string> = {
  'pending': '验收待定',
  'passed': '验收通过',
  'failed': '验收未通过',
  'unknown': '验收状态未知',
};
export const acceptanceStatusLabel = pairs(ACCEPTANCE_STATUS_LABELS);

// 运行中 Worker 问题的消费状态 deliveryStatus（contract 闭集 6 值；null 视为未知）
export const DELIVERY_STATUS_LABELS: Record<string, string> = {
  'pending': '已受理（待投递 Worker）',
  'dispatched': '已投递（Worker 未确认消费）',
  'acknowledged': '已消费（Worker 已确认）',
  'cancelled': '已取消',
  'expired': '已过期',
  'unknown': '消费状态未知',
};
export const deliveryStatusLabel = pairs(DELIVERY_STATUS_LABELS);

// Leader 动作状态（publication/postverify；contract 闭集 6 值）
export const LEADER_ACTION_STATUS_LABELS: Record<string, string> = {
  'pending': '待执行',
  'running': '执行中',
  'succeeded': '已成功',
  'failed': '已失败',
  'unknown': '状态未知',
  'cancelled': '已取消',
};
export const leaderActionStatusLabel = pairs(LEADER_ACTION_STATUS_LABELS);
// 兼容旧名：发布状态即 Leader 动作状态。
export const PUBLICATION_LABELS = LEADER_ACTION_STATUS_LABELS;
export const publicationLabel = leaderActionStatusLabel;

// LeaderStage（contract 闭集 7 值）
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
