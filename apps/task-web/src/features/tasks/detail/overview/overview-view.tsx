// 概览（P04/P05）：需要你处理的卡片 → 当前进展与 Leader 投影 → 独立验收读数 → 原需求 → 计划与批准 → 成果摘要。
// 数据来自 task/plan/questions/workers/leader 五个合同投影（null=未加载/未提供），不互相借用、不据执行结束推断成功。

import {Link} from 'react-router-dom';
import type {ReactNode} from 'react';
import {LeaderCorrection} from './leader-correction';
import {ReviewExplanation} from './review-explanation';
import {LeaderDecision} from './leader-decision';
import {TaskJourney, TeamSummary} from './task-journey';
import {Card} from '@/components/ui/card';
import type {
  LeaderRecord,
  PlanRecord,
  QuestionsResponse,
  TaskAuditRecord,
  TaskRecord,
  Transport,
  WorkerRecord,
} from '@/lib/transport/types';
import {casRevisionOf, leaderPendingRequest, questionNeedsAttention} from '../shared/derive';
import {formatDateTime} from '../shared/format';
import {AcceptancePanel} from './acceptance-panel';
import {LeaderProjection} from './leader-projection';
import {LeaderRequestCard} from './leader-request-card';
import {PlanCard} from './plan-card';
import {QuestionCard} from './question-card';
import {QuestionHistory} from './question-history';
import {TaskControls} from './task-controls';
import {useLeaderReplyReceipt} from '../shared/leader-reply-receipt';

// 详情布局按此接口接线：task/plan/questions/workers/leader/audit/transport/onChanged，字段名固定。
export interface OverviewViewProps {
  task: TaskRecord;
  /** null = 计划未生成、未加载或该服务未提供计划内容。 */
  plan: PlanRecord | null;
  /** null = 问题投影未加载；提供时 items 含预批准与运行中两类问题。 */
  questions: QuestionsResponse | null;
  /** null = 团队投影加载失败；[] = 当前无 Worker。 */
  workers: WorkerRecord[] | null;
  /** null = Leader 未启用/端点不可用。 */
  leader: LeaderRecord | null;
  /** null = audit 投影未加载/不可用；独立验收不能以评审推导（UI-04）。 */
  audit: TaskAuditRecord | null;
  transport: Transport;
  onChanged: () => void;
  graph?: ReactNode;
}

export function OverviewView({task, plan, questions, workers, leader, audit, transport, onChanged, graph}: OverviewViewProps) {
  const expectedRevision = casRevisionOf(task);
  const pendingRequest = leaderPendingRequest(leader);
  const awaitingRequest = pendingRequest !== null && pendingRequest.status === 'pending' ? pendingRequest : null;
  const [replyAccepted] = useLeaderReplyReceipt(task.id, awaitingRequest);
  // UI-08：除 open 外，已答但 Worker ACK 未落定的运行问题保留供核对
  const attentionQuestions = (questions?.items ?? []).filter(questionNeedsAttention)
    .sort((a, b) => (a.deadlineAt ? Date.parse(a.deadlineAt) : Infinity) - (b.deadlineAt ? Date.parse(b.deadlineAt) : Infinity));
  const planNeedsApproval = plan !== null && task.allowedActions.includes('approve');
  const waitingCount = attentionQuestions.length + (awaitingRequest && !replyAccepted ? 1 : 0) + (planNeedsApproval ? 1 : 0);
  const intervention = task.status === 'intervention' || task.code === 'cleanup_unconfirmed';
  const unresolvedWorkers = (workers ?? []).filter(worker => ['unknown', 'stopping'].includes(worker.status));

  return (
    <div className="space-y-6" data-testid="overview-view">
      <TaskJourney task={task} leader={leader} workers={workers} audit={audit} />
      <div className="task-workspace"><div className="min-w-0 space-y-6">
      {intervention ? (
        <Card role="alert" aria-label="系统执行异常" className="space-y-3 border-danger" data-testid="intervention-notice">
          <h2 className="text-base font-semibold text-danger">系统执行异常，需要排查</h2>
          <p className="text-sm">此执行异常不等同于待答问题；请分别核对下方请求，不能将没有待答事项理解为运行正常。</p>
          <p className="text-sm [overflow-wrap:anywhere]">原因：{task.code === 'cleanup_unconfirmed'
            ? '尚未确认所属执行已安全清理，任务不能继续。'
            : '服务报告任务需要干预，具体执行原因请结合团队与活动记录核对。'}
            {task.code ? <>（<code>{task.code}</code>）</> : '（服务未提供原因码）'}</p>
          <p className="text-sm text-text-secondary">当前界面没有安全恢复此异常的操作。请保留现场并联系运行环境维护者排查；不要重复创建同一任务、强制清除状态或把重新启动当作恢复成功。</p>
          {workers === null ? <p className="text-sm text-text-secondary">团队数据未加载，暂时无法确认受影响的 Worker。</p>
            : unresolvedWorkers.length ? (
              <ul className="space-y-1 text-sm" aria-label="清理状态待确认的 Worker">
                {unresolvedWorkers.map(worker => <li key={worker.id} className="[overflow-wrap:anywhere]">
                  <Link className="text-accent underline focus-visible:outline focus-visible:outline-2" to={`/tasks/${encodeURIComponent(task.id)}/team/${encodeURIComponent(worker.id)}`}>
                    {worker.nodeId} · {worker.id}
                  </Link>（{worker.status}）
                </li>)}
              </ul>
            ) : <p className="text-sm text-text-secondary">当前已加载团队中没有 unknown/stopping Worker；不能据此认定清理已完成。</p>}
          <div className="flex flex-wrap gap-4 text-sm">
            <Link className="text-accent underline" to={`/tasks/${encodeURIComponent(task.id)}/team`}>查看团队与 Worker</Link>
            <Link className="text-accent underline" to={`/tasks/${encodeURIComponent(task.id)}/activity`}>查看活动与异常证据</Link>
          </div>
        </Card>
      ) : null}
      <section aria-label="需要处理" className="space-y-2">
        <h2 className="text-sm font-medium text-text-secondary">
          需要你的处理（{waitingCount} 项）
        </h2>
        {waitingCount === 0 ? (
          <p className="rounded-md border border-border bg-surface p-3 text-sm text-text-secondary" data-testid="waiting-empty">
            {intervention ? '当前没有待答或待批准事项，但系统执行异常尚未解决；请查看上方异常说明。' : replyAccepted ? '你的本次答复已受理，正在等待 Leader 更新；无需重复答复。' : '当前没有等待你处理的事项。'}
            {questions === null ? '（问题投影未加载，无法确认是否有待答问题。）' : ''}
            {leader === null ? '（Leader 投影不可用，无法确认是否有 Leader 待处理请求。）' : ''}
          </p>
        ) : null}
        {attentionQuestions.map(question => (
          <QuestionCard
            key={question.id}
            taskId={task.id}
            expectedRevision={expectedRevision}
            question={question}
            previewDigest={questions?.previewDigest ?? null}
            transport={transport}
            onChanged={onChanged}
          />
        ))}
        {awaitingRequest ? (
          <LeaderRequestCard taskId={task.id} expectedRevision={expectedRevision} request={awaitingRequest} transport={transport} onChanged={onChanged} />
        ) : null}
        <section aria-label="计划" id="plan-card-anchor">
          {plan ? (
            <details open={planNeedsApproval}>
              <summary className="cursor-pointer text-sm text-text-secondary">{planNeedsApproval ? '待批准的执行计划' : '查看执行计划与批准回执'}</summary>
              <PlanCard task={task} plan={plan} transport={transport} onViewLatest={onChanged} />
            </details>
          ) : (
            <Card className="space-y-1">
              <h2 className="text-base font-semibold leading-6">当前计划</h2>
              <p className="text-sm text-text-secondary" data-testid="plan-unavailable">
                {task.plan
                  ? `服务端已给出计划引用（revision ${task.plan.revision}），但计划内容未加载。`
                  : '暂无计划（计划未生成或该服务未提供）。'}
              </p>
            </Card>
          )}
        </section>
      </section>



      <section aria-label="当前进展" className="space-y-6">
        <LeaderCorrection task={task} leader={leader} workers={workers} />
        <LeaderDecision leader={leader} transport={transport} /><ReviewExplanation leader={leader} transport={transport} />
        <details className="workspace-disclosure"><summary>团队协调与审计信息</summary><LeaderProjection leader={leader} workers={workers ?? []} /></details>

      </section>

      {graph ? <details className="workspace-disclosure"><summary>工作依赖与执行路径</summary><div className="pt-4">{graph}</div></details> : null}

      <details className="workspace-disclosure" aria-label="原需求"><summary>需求与任务信息</summary>
        <Card className="space-y-2">
          <h2 className="text-base font-semibold leading-6">原需求</h2>
          <p className="whitespace-pre-wrap text-sm leading-[22px]" data-testid="task-intent">{task.intent}</p>
          <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">创建时间</dt><dd>{formatDateTime(task.createdAt)}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">最近更新</dt><dd>{formatDateTime(task.updatedAt)}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">revision</dt><dd><code>{task.revision}</code></dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">期限</dt><dd>{task.deadlineAt ? formatDateTime(task.deadlineAt) : '无'}</dd></div>
          </dl>
          {task.code ? (
            <p className="text-sm text-danger">失败代码：<code>{task.code}</code></p>
          ) : null}
        </Card>
      </details>

      <section aria-label="成果摘要">
        <Card className="space-y-2" data-testid="delivery-summary">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-semibold leading-6">成果摘要</h2>
            <Link to={`/tasks/${encodeURIComponent(task.id)}/artifacts`} className="text-sm text-accent underline-offset-4 hover:underline">查看成果页</Link>
          </div>
          {task.artifactIds.length > 0 ? (
            <p className="text-sm text-text-secondary">
              服务端投影 {task.artifactIds.length} 个成果引用；成果明细与下载见成果页。
            </p>
          ) : (
            <p className="text-sm text-text-secondary">尚无交付成果（执行完成不代表已产生成果）。</p>
          )}
        </Card>
      </section>

      <details className="workspace-disclosure"><summary>已处理的问题与答复</summary><QuestionHistory taskId={task.id} questions={questions} /></details>
      <TaskControls task={task} transport={transport} onChanged={onChanged} />
      </div><aside className="min-w-0 space-y-6 border-border xl:border-l xl:pl-6"><TeamSummary task={task} plan={plan} workers={workers} /><AcceptancePanel leader={leader} audit={audit} /></aside></div>
    </div>
  );
}
