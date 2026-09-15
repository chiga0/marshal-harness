// 评审、独立验收、交付、后验四者分开呈现（UI-04）：
// - 集中评审事实只来自 leader.review（verdict=accept 是评审通过）；
// - 独立验收事实只来自 GET /v1/tasks/{taskId}/audit 的 acceptance（passed 才是验收通过）；
// - 发布/后验来自 Leader 动作投影；无发布动作不等于无文件交付；执行结束或评审通过都不能推导验收通过。

import {configuredCheckLabel, VerificationScope} from '../shared/verification-scope';
import {Card} from '@/components/ui/card';
import type {LeaderRecord, TaskAuditRecord} from '@/lib/transport/types';
import {acceptanceLabel, leaderActionStatusLabel} from '../shared/format';
import {StatusBadge, toneForAcceptance, toneForAcceptanceStatus, toneForLeaderAction} from '../shared/status-badge';

export interface AcceptancePanelProps {
  leader: LeaderRecord | null;
  /** null = audit 投影未加载或端点不可用；不得用 review 推导验收。 */
  audit: TaskAuditRecord | null;
}

export function AcceptancePanel({leader, audit}: AcceptancePanelProps) {
  const review = leader?.review ?? null;
  const publication = leader?.publication ?? null;
  const postverify = leader?.postverify ?? null;
  const acceptance = audit?.acceptance ?? null;
  return (
    <Card aria-label="评审与配置检查" className="space-y-2" data-testid="acceptance-panel">
      <h2 className="text-base font-semibold leading-6">业务评审 / 配置检查 / 交付</h2>

      <div className="space-y-1" data-testid="review-readout">
        <span className="text-xs font-medium text-text-secondary">独立 Agent 业务评审</span>
        {leader === null ? (
          <p className="text-sm text-text-secondary" data-testid="acceptance-unavailable">Leader 投影不可用，无法确认评审状态。</p>
        ) : review === null ? (
          <p className="text-sm text-text-secondary">集中评审尚未完成。</p>
        ) : (
          <>
            <StatusBadge machine={review.verdict} label={acceptanceLabel(review.verdict)} tone={toneForAcceptance(review.verdict)} />
            <details className="text-xs text-text-secondary"><summary className="cursor-pointer py-2">查看证据标识</summary><dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审摘要</dt><dd className="break-all"><code className="text-xs">{review.digest}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审 Worker</dt><dd><code className="text-xs">{review.workerId}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">证据</dt><dd>{review.evidenceIds.length > 0 ? `${review.evidenceIds.length} 条（${review.evidenceIds.join('，')}）` : '暂无数据'}</dd></div>
            </dl></details>
          </>
        )}
      </div>

      <div className="space-y-1 border-t border-border pt-2" data-testid="acceptance-readout">
        <span className="text-xs font-medium text-text-secondary">配置检查结果</span>
        {acceptance === null ? (
          <p className="text-sm text-text-secondary" data-testid="acceptance-unloaded">验收读数未加载或 audit 投影不可用；不能以评审结果代替验收。</p>
        ) : (
          <>
            <StatusBadge machine={acceptance.status} label={configuredCheckLabel(acceptance.status)} tone={toneForAcceptanceStatus(acceptance.status)} />
            <VerificationScope />
            <details className="text-xs text-text-secondary"><summary className="cursor-pointer py-2">查看证据标识</summary><dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
              <div className="flex gap-2">
                <dt className="shrink-0 text-text-secondary">验收摘要</dt>
                <dd className="break-all">{acceptance.digest !== null ? <code className="text-xs">{acceptance.digest}</code> : '暂无摘要'}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="shrink-0 text-text-secondary">验收证据</dt>
                <dd>{acceptance.evidenceIds.length > 0 ? `${acceptance.evidenceIds.length} 条（${acceptance.evidenceIds.join('，')}）` : '暂无数据'}</dd>
              </div>
            </dl></details>
          </>
        )}
      </div>

      <div className="space-y-1 border-t border-border pt-2">
        <span className="text-xs font-medium text-text-secondary">发布与后验</span>
        {leader === null ? (
          <p className="text-sm text-text-secondary">Leader 投影不可用。</p>
        ) : publication === null && postverify === null ? (
          <p className="text-sm text-text-secondary">无发布/后验动作；文件交付请查看成果页</p>
        ) : (
          <div className="space-y-1">
            {publication ? (
              <div className="flex flex-wrap items-center gap-2 text-sm leading-[22px]" data-testid="publication-readout">
                <span className="text-text-secondary">发布</span>
                <StatusBadge machine={publication.status} label={leaderActionStatusLabel(publication.status)} tone={toneForLeaderAction(publication.status)} />
              </div>
            ) : null}
            {postverify ? (
              <div className="flex flex-wrap items-center gap-2 text-sm leading-[22px]" data-testid="postverify-readout">
                <span className="text-text-secondary">交付后验</span>
                <StatusBadge machine={postverify.status} label={leaderActionStatusLabel(postverify.status)} tone={toneForLeaderAction(postverify.status)} />
              </div>
            ) : null}
          </div>
        )}
      </div>

      <p className="text-xs text-text-secondary" data-testid="acceptance-note">
        Agent 评审是独立业务判断；配置检查按已配置策略执行。计划要求、评审接受或任务结束均不证明逐项业务已经实际验证。
      </p>
    </Card>
  );
}
