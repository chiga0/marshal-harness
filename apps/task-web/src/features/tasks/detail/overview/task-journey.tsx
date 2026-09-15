import {GitBranch, Timer, Users} from 'lucide-react';
import {Link} from 'react-router-dom';
import type {
  LeaderRecord,
  PlanRecord,
  TaskAuditRecord,
  TaskRecord,
  WorkerRecord,
} from '@/lib/transport/types';
import {
  formatDuration,
  formatRelative,
  leaderStageLabel,
  taskStatusLabel,
  workerRoleLabel,
  workerStatusLabel,
} from '../shared/format';
import {useNow} from '../shared/use-now';
import {diagnosticReason} from '../../../workers/worker-diagnostic';
import {observationLabel} from '../../../workers/worker-observation';
const stages = ['需求', '计划', '执行', '评审', '检查', '交付'];
/** 仅定位当前已观察阶段，不将前序阶段画成通过。 */
export function currentStage(
  task: TaskRecord,
  leader: LeaderRecord | null,
): number | null {
  if (task.phase === 'terminal') return null;
  const currentLeader = leader?.taskId === task.id && leader.taskRevision === task.revision ? leader : null;
  if (task.status === 'awaiting-confirmation') {
    if (currentLeader?.pendingRequest?.status === 'pending' && currentLeader.pendingRequest.kind === 'publication') return 5;
    return ({intake:0,planning:1,execution:2,verification:4,delivery:5} as Record<string,number>)[task.phase] ?? null;
  }
  if (['planning', 'awaiting-approval'].includes(task.status)) return 1;
  if (task.status === 'draft') return 0;
  if (leader?.taskId === task.id && leader.taskRevision === task.revision) {
    if (leader.stage === 'review') return 3;
    if (leader.stage === 'verification') return 4;
    if (leader.stage === 'delivery' || leader.stage === 'finalizing') return 5;
  }
  return (
    (
      {
        intake: 0,
        planning: 1,
        execution: 2,
        verification: 4,
        delivery: 5,
      } as Record<string, number>
    )[task.phase] ?? null
  );
}
export function taskFailureExplanation(task: TaskRecord): string {
  return ({invalid_leader_decision:'Leader 提交的决定未通过合同校验',leader_result_rejected:'Leader 执行结果未被接纳',invalid_review_report:'独立评审报告未通过合同校验',worker_failed:'某次执行未能完成',cleanup_unconfirmed:'尚未确认执行环境已清理',deadline_exceeded:'任务超过执行期限',leader_concluded_failure:'Leader 已提出结束本次未完成的交付',publication_failed:'获准发布未能完成'} as Record<string,string>)[task.code ?? ''] ?? '服务尚未报告可读的具体失败原因';
}
export function taskFocus(task: TaskRecord, leader: LeaderRecord | null) {
  if (task.status === 'failed') return taskFailureExplanation(task);
  if (task.status === 'awaiting-confirmation') {
    const request = leader?.taskId === task.id && leader.taskRevision === task.revision ? leader.pendingRequest : null;
    if (request?.status === 'pending' && request.kind === 'publication') return '等待你决定是否允许本次发布';
    if (request?.status === 'pending' && request.kind === 'business') return '有业务事项等待你确认';
    return task.phase === 'planning' ? '计划已准备，等待你确认' : '等待你的确认，请核对下方请求';
  }
  const labels: Partial<Record<TaskRecord['status'], string>> = {
    draft: '需求已保存，等待整理计划',
    planning: '正在整理需求与执行计划',
    'awaiting-approval': '计划已准备，等待你确认开始',
    'awaiting-answer': '有问题等待你的答复',
    paused: '任务已暂停，不再安排新的执行',
    cancelling: '正在取消任务并确认执行停止',
    intervention: '执行遇到异常，需要排查',
    queued: '计划已批准，等待调度',
    completed: '流程已完成，查看成果与检查依据',
    failed: '任务未完成，请查看失败原因',
    cancelled: '任务已取消',
  };
  return (
    labels[task.status] ??
    (leader && leader.taskId === task.id && leader.taskRevision === task.revision
      ? leaderStageLabel(leader.stage)
      : taskStatusLabel(task.status))
  );
}
export function workerTitle(worker: WorkerRecord, plan?: PlanRecord | null) {
  if(worker.role === 'verifier') return `配置检查 · 执行 ${worker.attempt}`;
  if(worker.role === 'planner') return `${worker.nodeId.startsWith('managed-leader-') ? 'Leader 决策' : '规划决策'} · 执行 ${worker.attempt}`;
  return (
    plan?.nodes.find(
      (node) => node.id === worker.nodeId && node.role === worker.role,
    )?.goal || `${workerRoleLabel(worker.role)}工作`
  );
}
const FOCUS_WORKER_STATUSES: WorkerRecord['status'][] = ['running', 'queued', 'awaiting-answer', 'stopping', 'unknown', 'failed'];

/** 概览只展示服务已投影的可读观察，不从状态推断模型或业务结果。 */
export function workerPublicActivity(worker: WorkerRecord): string {
  const text = worker.observation?.publicText.trim();
  if (text) return text;
  if (worker.observation) return observationLabel(worker);
  if (worker.progress?.summary) {
    return worker.progress.tool ? `${worker.progress.summary} · 最近工具 ${worker.progress.tool}` : worker.progress.summary;
  }
  return '暂无公开活动';
}

export function workerNextStep(worker: WorkerRecord): string {
  if (worker.observation?.diagnostic) return '核对该诊断对应的活动与执行记录，再决定是否需要处理。';
  switch (worker.status) {
    case 'running': return '继续观察；若长时间没有新观察，打开成员详情核对。';
    case 'queued': return '等待调度；可打开成员详情核对执行序号。';
    case 'awaiting-answer': return '核对任务中的待答问题并提交答复。';
    case 'stopping':
    case 'unknown': return '保留现场，核对停止或清理证据。';
    case 'failed': return '查看失败诊断与活动证据，确认后续处理。';
    default: return '打开成员详情核对执行记录。';
  }
}

function focusWorkerPriority(worker: WorkerRecord): number {
  if (worker.observation?.diagnostic) return 0;
  return ({running: 1, 'awaiting-answer': 2, stopping: 3, unknown: 3, queued: 4, failed: 5} as Record<string, number>)[worker.status] ?? 9;
}

function JourneyWorkerFocus({task, workers}: {task: TaskRecord; workers: WorkerRecord[] | null}) {
  const focused = [...(workers ?? [])]
    .filter(worker => FOCUS_WORKER_STATUSES.includes(worker.status) || worker.observation?.diagnostic !== undefined)
    .sort((a, b) => focusWorkerPriority(a) - focusWorkerPriority(b) || b.attempt - a.attempt)
    .slice(0, 4);
  if (!focused.length) return null;
  return (
    <section aria-label="当前执行成员" className="mt-5 border-t border-border pt-4" data-testid="task-journey-focus">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold">当前执行成员</h2>
        <Link className="text-xs text-accent underline-offset-4 hover:underline" to={`/tasks/${encodeURIComponent(task.id)}/team`}>查看全部成员</Link>
      </div>
      <ul className="grid gap-3 md:grid-cols-2">
        {focused.map(worker => {
          const observation = worker.observation;
          const diagnostic = observation?.diagnostic;
          return (
            <li key={worker.id} className="rounded-lg border border-border bg-surface-muted/30 p-3" data-testid="task-journey-worker">
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Link className="font-semibold text-accent underline-offset-4 hover:underline" to={`/tasks/${encodeURIComponent(task.id)}/team/${encodeURIComponent(worker.id)}`}>
                  {workerRoleLabel(worker.role)} · {worker.id}
                </Link>
                <span className="rounded-full border border-border px-2 py-0.5 text-text-secondary">{workerStatusLabel(worker.status)}</span>
              </div>
              <p className="mt-1 text-xs text-text-secondary">节点：<code>{worker.nodeId}</code> · {worker.providerId} · 执行 {worker.attempt}</p>
              <p className="mt-2 text-sm leading-5"><span className="text-text-secondary">最近公开活动：</span>{workerPublicActivity(worker)}</p>
              {observation?.observedAt ? <p className="mt-1 text-xs text-text-secondary">观察于 {formatRelative(observation.observedAt)}</p> : null}
              {diagnostic ? <p className="mt-2 text-sm text-danger"><span className="font-medium">阻塞/问题：</span>{diagnosticReason(diagnostic)}</p> : null}
              <p className="mt-2 text-xs text-text-secondary"><span className="font-medium text-text-primary">下一步：</span>{workerNextStep(worker)}</p>
            </li>
          );
        })}
      </ul>
      {(workers ?? []).filter(worker => FOCUS_WORKER_STATUSES.includes(worker.status) || worker.observation?.diagnostic !== undefined).length > focused.length ? (
        <p className="mt-2 text-xs text-text-secondary">其余当前成员见团队页；概览仅保留最需要关注的 4 个成员。</p>
      ) : null}
    </section>
  );
}

export function TaskJourney({
  task,
  leader,
  workers,
  audit,
}: {
  task: TaskRecord;
  leader: LeaderRecord | null;
  workers: WorkerRecord[] | null;
  audit: TaskAuditRecord | null;
}) {
  const stage = currentStage(task, leader),
    now = useNow(1000);
  const count = (statuses: string[]) =>
    (workers ?? []).filter((w) => statuses.includes(w.status)).length;
  const terminal = ['completed', 'failed', 'cancelled'].includes(task.status);
  return (
    <section
      aria-label="任务进度概况"
      className="journey"
      data-testid="task-journey"
    >
      {task.status === 'failed' ? <div className="mb-4 space-y-2" data-testid="task-failure-explanation">
        <p className="text-sm font-medium text-danger">任务未完成：{taskFailureExplanation(task)}</p>
        {!task.plan ? <p className="text-sm text-text-secondary">尚未产生可用的执行计划。</p> : null}
        <p className="text-xs text-text-secondary">任务已结束；下方仅列流程步骤，不推断未报告的失败阶段。</p>
        <Link className="inline-flex min-h-11 items-center text-sm text-accent underline" to={`/tasks/${encodeURIComponent(task.id)}/team`}>查看执行记录</Link>
        <details className="workspace-disclosure"><summary>失败技术代码</summary><code className="break-all text-xs">{task.code ?? '未报告'}</code></details>
      </div> : null}
      <ol className="journey-stages" aria-label="任务阶段">
        {stages.map((label, i) => (
          <li
            key={label}
            aria-current={i === stage ? 'step' : undefined}
            className={i === stage ? 'is-current' : ''}
          >
            <span className="journey-stage-dot">{i + 1}</span>
            <span>{label}</span>
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
        <div className="flex items-center gap-3">
          <span className="flex h-9 w-9 items-center justify-center rounded-full bg-accent/10 text-accent">
            <GitBranch size={17} aria-hidden />
          </span>
          <div>
            <p className="text-xs text-text-secondary">当前进展</p>
            <p className="font-medium">{taskFocus(task, leader)}</p>
            {task.status === 'completed' ? <Link className="mt-2 inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-white" data-testid="completed-view-delivery" to={`/tasks/${encodeURIComponent(task.id)}/artifacts`}>查看成果</Link> : null}
          </div>
        </div>
        <div className="flex flex-wrap gap-5 text-xs text-text-secondary">
          <span className="inline-flex items-center gap-1.5">
            <Users size={14} aria-hidden />
            {workers === null
              ? '团队暂不可用'
              : `已加载执行：运行 ${count(['running'])} · 排队 ${count(['queued'])} · 待答 ${count(['awaiting-answer'])} · 待确认停止 ${count(['stopping', 'unknown'])}`}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <Timer size={14} aria-hidden />
            {terminal ? '任务历时' : '自创建起'}{' '}
            {formatDuration(
              (terminal ? Date.parse(task.updatedAt) : now) -
                Date.parse(task.createdAt),
            )}
          </span>
        </div>
      </div>
      <JourneyWorkerFocus task={task} workers={workers} />
      <p className="mt-3 text-xs text-text-secondary" data-testid="task-usage">
        {audit?.usage &&
        audit.usage.source !== 'unavailable' &&
        audit.usage.tokens !== null ? (
          <>
            {audit.usage.source === 'estimated' ? '估算' : '已报告'} {audit.usage.tokens.toLocaleString()} Token · 完整读数覆盖{' '}
            {Math.round(audit.usage.coverage * 100)}%
            的模型执行；未报告部分不计为零。
          </>
        ) : (
          '任务 Token 用量未报告。'
        )}
      </p>
    </section>
  );
}
export function TeamSummary({
  task,
  plan,
  workers,
}: {
  task: TaskRecord;
  plan: PlanRecord | null;
  workers: WorkerRecord[] | null;
}) {
  const order = (w: WorkerRecord) =>
    ['unknown', 'stopping', 'awaiting-answer'].includes(w.status)
      ? 0
      : w.status === 'running'
        ? 1
        : w.status === 'queued'
          ? 2
          : w.status === 'failed' ? 3 : 4;
  const sorted = [...(workers ?? [])].sort((a, b) => order(a) - order(b) || b.attempt - a.attempt);
  return (
    <section aria-label="团队速览" className="team-summary">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-sm font-semibold">执行记录（已加载 {workers?.length ?? 0} 次执行）</h2>
        <Link
          className="text-xs text-accent"
          to={`/tasks/${encodeURIComponent(task.id)}/team`}
        >
          全部执行 ↗
        </Link>
      </div>
      {workers === null ? (
        <p className="text-sm text-text-secondary">团队暂不可用</p>
      ) : !workers.length ? (
        <p className="text-sm text-text-secondary">
          计划确认后，执行成员会出现在这里。
        </p>
      ) : (
        <ul className="divide-y divide-border">
          {sorted.slice(0, 6).map((w) => (
            <li key={w.id}>
              <Link
                className="group flex gap-3 py-3 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent"
                to={`/tasks/${encodeURIComponent(task.id)}/team/${encodeURIComponent(w.id)}`}
              >
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-surface-muted text-xs font-semibold text-text-secondary">
                  {workerRoleLabel(w.role).slice(0, 1)}
                </span>
                <div className="min-w-0">
                  <p className="line-clamp-2 text-sm font-medium group-hover:text-accent">
                    {workerTitle(w, plan)}
                  </p>
                  <p className="mt-1 text-xs text-text-secondary">
                    {w.providerId} · 执行序号 {w.attempt}
                  </p>
                  <p className="mt-1 text-xs text-text-secondary">
                    {w.status === 'failed' ? '该次执行失败' : workerStatusLabel(w.status)}
                  </p>
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {sorted.length > 6 ? (
        <p className="pt-2 text-xs text-text-secondary">
          另有 {sorted.length - 6} 次执行，见全部成员。
        </p>
      ) : null}
    </section>
  );
}
