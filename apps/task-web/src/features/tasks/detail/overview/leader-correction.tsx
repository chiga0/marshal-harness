import {Link} from 'react-router-dom';
import type {LeaderRecord, TaskRecord, WorkerRecord} from '@/lib/transport/types';
import {formatDateTime, workerStatusLabel} from '../shared/format';

export function LeaderCorrection({task, leader, workers}: {
  task: TaskRecord; leader: LeaderRecord | null; workers: WorkerRecord[] | null;
}) {
  const correction = leader?.taskId === task.id && leader.taskRevision === task.revision
    ? leader.protocolCorrection : undefined;
  if (!correction || correction.used === 0) return null;
  const original = correction.original;
  const successor = (workers ?? []).find(worker => worker.taskId === task.id && worker.id === correction.successorWorkerId);
  const executionPath = (id: string) => `/tasks/${encodeURIComponent(task.id)}/team/${encodeURIComponent(id)}`;
  return <section className="space-y-3 border-l-2 border-border pl-4" aria-label="Leader 格式纠错记录" data-testid="leader-correction">
    <div className="flex flex-wrap items-baseline gap-2"><h2 className="text-base font-semibold">Leader 格式纠错记录</h2><span className="text-xs text-text-secondary">一次纠错预算已使用（1 / 1）</span></div>
    <p className="text-sm leading-6">原决定未能解析为有效 JSON，已记录一次有限纠错机会。原失败记录仍保留；这不代表后续决定、业务结果或验收已通过。</p>
    <div className="flex flex-wrap gap-4 text-sm">
      {original ? <Link className="text-accent underline" data-testid="correction-original" to={executionPath(original.workerId)}>查看原失败执行</Link> : null}
      {correction.successorWorkerId ? <Link className="text-accent underline" data-testid="correction-successor" to={executionPath(correction.successorWorkerId)}>查看后续执行</Link> : null}
    </div>
    <p className="text-sm text-text-secondary" data-testid="correction-execution-status">{!correction.successorWorkerId
      ? '尚未记录已保留的后续执行；是否继续由任务当前状态决定。'
      : successor ? `后续执行记录：${workerStatusLabel(successor.status)}。` : '后续执行已登记，但未包含在当前已加载记录中，状态暂不可确认。'}</p>
    <details className="workspace-disclosure"><summary>纠错绑定与原失败证据</summary>
      {original ? <p className="text-xs text-text-secondary">原失败记录时间：{formatDateTime(original.at)}</p> : null}
      <pre className="whitespace-pre-wrap break-all text-xs">{JSON.stringify(correction, null, 2)}</pre>
    </details>
  </section>;
}
