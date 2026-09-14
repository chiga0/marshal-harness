import type {ObservationFrame, WorkerRecord} from '@/lib/transport/types';
import {
  formatDateTime,
  formatDuration,
  workerStatusLabel,
} from '../tasks/detail/shared/format';
import {useNow} from '../tasks/detail/shared/use-now';

export const ACTIVITY_LABELS: Record<ObservationFrame['activity'], string> = {
  starting: '正在启动',
  waiting: '等待 Agent 响应',
  thinking: 'Agent 报告正在思考',
  output: '正在输出文本',
  tool: '正在调用工具',
  retrying: 'Agent 正在重试',
  compacting: '正在整理上下文',
  stopping: '正在停止',
  terminal: '执行已结束',
  unknown: '活动未知',
};
const TOOL_LABELS = {
  read: '读取',
  edit: '编辑',
  delete: '删除',
  move: '移动',
  search: '搜索',
  execute: '执行命令',
  think: '思考工具',
  fetch: '请求数据',
  other: '其他工具',
};
const TOOL_STATUS = {
  pending: '待执行',
  in_progress: '执行中',
  completed: '已结束',
  failed: '失败',
};
export function observationLabel(worker: WorkerRecord) {
  if (
    [
      'completed',
      'failed',
      'cancelled',
      'unknown',
      'stopping',
      'awaiting-answer',
      'queued',
    ].includes(worker.status)
  )
    return workerStatusLabel(worker.status);
  const observation = worker.observation;
  if (!observation) return workerStatusLabel(worker.status);
  const label =
    observation.activity === 'tool' && observation.tool?.status === 'completed'
      ? '工具调用已结束'
      : observation.activity === 'tool' && observation.tool?.status === 'failed'
        ? '工具调用失败'
        : (ACTIVITY_LABELS[observation.activity] ?? '活动未知');
  return Date.now() - Date.parse(observation.observedAt) > 15000
    ? `上次活动：${label}`
    : label;
}
export function WorkerObservationView({worker}: {worker: WorkerRecord}) {
  const now = useNow(1000),
    observation = worker.observation;
  const terminal = ['completed', 'failed', 'cancelled'].includes(worker.status);
  const elapsed =
    worker.audit?.elapsedMs ??
    (worker.startedAt
      ? (worker.finishedAt
          ? Date.parse(worker.finishedAt)
          : terminal
            ? NaN
            : now) - Date.parse(worker.startedAt)
      : null);
  if (!observation)
    return (
      <section
        className="space-y-2 border-t border-border pt-5"
        aria-label="执行活动"
      >
        <h3 className="text-sm font-semibold">执行活动</h3>
        <p className="font-medium">{workerStatusLabel(worker.status)}</p>
        <p className="text-sm text-text-secondary">
          执行历时 {formatDuration(elapsed)} ·
          详细活动与模型未报告；原始进展见技术详情。
        </p>
      </section>
    );
  const stale = !terminal && now - Date.parse(observation.observedAt) > 15000;
  const usage = observation.usage;
  return (
    <section
      className="space-y-4 border-t border-border pt-5"
      aria-label="执行活动"
      data-testid="worker-observation"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">执行活动</h3>
        <span className="text-xs text-text-secondary">
          {observation.model?.id ?? '模型未报告'}
        </span>
      </div>
      <div className="rounded-lg bg-surface-muted p-4">
        <p
          className="mb-2 text-xs text-text-secondary"
          data-testid="worker-elapsed"
        >
          执行历时 {formatDuration(elapsed)}
        </p>
        <p className="font-medium">{observationLabel(worker)}</p>
        <p className="mt-1 text-xs text-text-secondary">
          最后观察 {formatDateTime(observation.observedAt)}
          {stale ? ' · 观察已陈旧' : ''}
        </p>
        {stale ? (
          <p className="mt-2 text-xs text-warning">
            暂未收到新活动，无法确认当前是否仍在执行。
          </p>
        ) : null}
      </div>
      {usage ? (
        <div
          className="grid grid-cols-3 gap-3 text-xs text-text-secondary"
          data-testid="observed-usage"
        >
          <div>
            输入 Token
            <strong className="mt-1 block text-lg font-medium text-text-primary">
              {usage.inputTokens?.toLocaleString() ?? '未报告'}
            </strong>
          </div>
          <div>
            输出 Token
            <strong className="mt-1 block text-lg font-medium text-text-primary">
              {usage.outputTokens?.toLocaleString() ?? '未报告'}
            </strong>
          </div>
          <div>
            累计 Token
            <strong className="mt-1 block text-lg font-medium text-text-primary">
              {usage.totalTokens?.toLocaleString() ?? '未报告'}
            </strong>
          </div>
          <p className="col-span-3">
            Provider 报告 · {usage.complete ? '已收到终结读数' : '当前部分读数'}
            ；无价格信息时不推算费用。
          </p>
        </div>
      ) : (
        <p
          className="text-xs text-text-secondary"
          data-testid="observation-usage-unavailable"
        >
          累计 Token 用量未报告，不按零计。
        </p>
      )}
      {observation.lastResponseUsage ? (
        <section className="space-y-2 text-xs text-text-secondary" aria-label="最近一次响应用量" data-testid="last-response-usage">
          <h4 className="font-medium text-text-primary">最近一次响应 Token（非累计）</h4>
          <p>输入 {observation.lastResponseUsage.inputTokens.toLocaleString()} · 输出 {observation.lastResponseUsage.outputTokens.toLocaleString()} · 合计 {observation.lastResponseUsage.totalTokens.toLocaleString()}</p>
          <p>Qwen 提供方最近一次报告；零值可能由提供方默认，完整性未确认。不计入成员或任务累计用量。</p>
        </section>
      ) : null}
      <details className="workspace-disclosure">
        <summary>公开活动历史（{observation.history.length} 条）</summary>
        <ol
          className="space-y-0 border-l border-border pl-4"
          aria-label="最近活动时间线"
        >
          {observation.history.map((frame) => (
            <li key={frame.sequence} className="relative space-y-1 py-3">
              <span className="absolute -left-[21px] top-5 h-2 w-2 rounded-full bg-border" />
              <div className="flex flex-wrap justify-between gap-2 text-xs">
                <span className="font-medium">
                  {ACTIVITY_LABELS[frame.activity]}
                </span>
                <time className="text-text-secondary">
                  {formatDateTime(frame.observedAt)}
                </time>
              </div>
              {frame.tool ? (
                <p className="text-sm">
                  {TOOL_LABELS[frame.tool.kind]} ·{' '}
                  {TOOL_STATUS[frame.tool.status]}
                </p>
              ) : null}
              {frame.publicText ? (
                <p className="whitespace-pre-wrap break-words text-sm text-text-secondary">
                  {frame.publicText}
                </p>
              ) : null}
            </li>
          ))}
        </ol>
      </details>
      <p className="text-xs text-text-secondary">
        {observation.historyTruncated
          ? '显示最近的有界活动记录，较早记录已截断。'
          : '记录为已观察的公开活动。'}
        活动记录不包含隐藏推理或原始工具参数。
      </p>
    </section>
  );
}

export function workerTokenSummary(worker: WorkerRecord) {
  const usage = worker.observation?.usage;
  return usage
    ? `${usage.totalTokens?.toLocaleString() ?? '未报告总量'} Token · ${usage.complete ? '终结读数' : '部分读数'}`
    : null;
}
