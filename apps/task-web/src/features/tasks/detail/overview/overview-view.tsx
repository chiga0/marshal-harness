// 概览（P04/P05）：需要你处理的卡片 → 当前进展与 Leader 投影 → 独立验收读数 → 原需求 → 计划与确认 → 成果摘要。

import {Link} from 'react-router-dom';
import {Card} from '@/components/ui/card';
import type {LeaderRecord, TaskDetail, Transport, WorkerRecord} from '@/lib/transport/types';
import {casRevisionOf, leaderPendingRequest} from '../shared/derive';
import {formatBytes, formatDateTime} from '../shared/format';
import {AcceptancePanel} from './acceptance-panel';
import {LeaderProjection} from './leader-projection';
import {LeaderRequestCard} from './leader-request-card';
import {PlanCard} from './plan-card';
import {QuestionCard} from './question-card';
import {TaskControls} from './task-controls';

export interface OverviewViewProps {
  detail: TaskDetail;
  workers: WorkerRecord[] | null;
  leader: LeaderRecord | null | undefined;
  transport: Transport;
  onChanged: () => void;
}

export function OverviewView({detail, workers, leader, transport, onChanged}: OverviewViewProps) {
  const revision = casRevisionOf(detail);
  const pending = leaderPendingRequest(leader);
  const questions = detail.pendingQuestions ?? [];
  const planNeedsApproval = detail.plan !== null && detail.plan !== undefined && detail.plan.acceptedAt === null && detail.plan.rejectedAt === null && detail.allowedActions.includes('approve');
  const waitingCount = questions.length + (pending ? 1 : 0) + (planNeedsApproval ? 1 : 0);

  return (
    <div className="space-y-4" data-testid="overview-view">
      <section aria-label="需要处理" className="space-y-2">
        <h2 className="text-sm font-medium text-text-secondary">
          需要你的处理（{waitingCount} 项）
        </h2>
        {waitingCount === 0 ? (
          <p className="rounded-md border border-border bg-surface p-3 text-sm text-text-secondary" data-testid="waiting-empty">
            当前没有等待你处理的事项。
            {pending === undefined ? '（该服务版本未提供 Leader 待处理请求投影，无法确认是否有 Leader 请求。）' : ''}
          </p>
        ) : null}
        {questions.map(question => (
          <QuestionCard key={question.questionId} taskId={detail.id} revision={revision} question={question} transport={transport} onChanged={onChanged} />
        ))}
        {pending ? (
          <LeaderRequestCard taskId={detail.id} revision={revision} request={pending} transport={transport} onChanged={onChanged} />
        ) : null}
      </section>

      <section aria-label="当前进展" className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <LeaderProjection leader={leader} workers={workers ?? []} />
        <AcceptancePanel detail={detail} />
      </section>

      <section aria-label="原需求">
        <Card className="space-y-2">
          <h2 className="text-base font-semibold leading-6">原需求</h2>
          <p className="whitespace-pre-wrap text-sm leading-[22px]" data-testid="task-intent">{detail.intent}</p>
          <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">创建时间</dt><dd>{formatDateTime(detail.createdAt)}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">期限</dt><dd>{detail.deadlineAt ? formatDateTime(detail.deadlineAt) : '无'}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">上下文引用</dt><dd>{detail.contextRefs.length > 0 ? `${detail.contextRefs.length} 个` : '无'}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">尝试/重试/返工</dt><dd>{detail.attempts} / {detail.retryCount} / {detail.reworkCount}</dd></div>
          </dl>
          {detail.failureCode ? (
            <p className="text-sm text-danger">失败代码：<code>{detail.failureCode}</code></p>
          ) : null}
        </Card>
      </section>

      <section aria-label="计划" id="plan-card-anchor">
        {detail.plan ? (
          <PlanCard taskId={detail.id} detail={detail} plan={detail.plan} transport={transport} onViewLatest={onChanged} />
        ) : (
          <Card className="space-y-1">
            <h2 className="text-base font-semibold leading-6">当前计划</h2>
            <p className="text-sm text-text-secondary" data-testid="plan-unavailable">暂无计划（计划未冻结或该服务未提供）。</p>
          </Card>
        )}
      </section>

      <section aria-label="成果摘要">
        <Card className="space-y-2" data-testid="delivery-summary">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-semibold leading-6">成果摘要</h2>
            <Link to={`/tasks/${encodeURIComponent(detail.id)}/artifacts`} className="text-sm text-accent underline-offset-4 hover:underline">查看成果页</Link>
          </div>
          {detail.latestDelivery ? (
            <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">交付摘要</dt><dd className="break-all"><code className="text-xs">{detail.latestDelivery.digest}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">大小</dt><dd>{formatBytes(detail.latestDelivery.bytes)}</dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">本机落盘</dt><dd>{detail.latestDelivery.isLocalRecipient ? '已标记交付到本机接收目录' : '未标记本机落盘'}</dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">文件清单</dt><dd>{detail.latestDelivery.files && detail.latestDelivery.files.length > 0 ? `${detail.latestDelivery.files.length} 个文件` : '未提供'}</dd></div>
            </dl>
          ) : (
            <p className="text-sm text-text-secondary">尚无交付成果。</p>
          )}
        </Card>
      </section>

      <TaskControls detail={detail} transport={transport} onChanged={onChanged} />
    </div>
  );
}
