// 独立验收与交付读数：验收事实只来自 Leader 集中评审（leader.review）与发布/后验投影，不据执行结束推断成功。
// 全部缺失时如实「暂无读数」；verdict=accept 才是评审通过，pending/running 不是通过。

import {Card} from '@/components/ui/card';
import type {LeaderRecord} from '@/lib/transport/types';
import {acceptanceLabel, leaderActionStatusLabel} from '../shared/format';
import {StatusBadge, toneForAcceptance, toneForLeaderAction} from '../shared/status-badge';

export interface AcceptancePanelProps {
  leader: LeaderRecord | null;
}

export function AcceptancePanel({leader}: AcceptancePanelProps) {
  const review = leader?.review ?? null;
  const publication = leader?.publication ?? null;
  const postverify = leader?.postverify ?? null;
  const anyData = review !== null || publication !== null || postverify !== null;
  return (
    <Card aria-label="独立验收" className="space-y-2" data-testid="acceptance-panel">
      <h2 className="text-base font-semibold leading-6">独立验收与交付</h2>
      {leader === null ? (
        <p className="text-sm text-text-secondary" data-testid="acceptance-unavailable">暂无验收读数（Leader 投影不可用，无法确认评审与交付状态）。</p>
      ) : !anyData ? (
        <p className="text-sm text-text-secondary" data-testid="acceptance-unavailable">暂无验收与交付读数（集中评审尚未完成）。</p>
      ) : (
        <div className="space-y-2">
          {review ? (
            <div className="space-y-1" data-testid="review-readout">
              <StatusBadge machine={review.verdict} label={acceptanceLabel(review.verdict)} tone={toneForAcceptance(review.verdict)} />
              <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
                <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审摘要</dt><dd className="break-all"><code className="text-xs">{review.digest}</code></dd></div>
                <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审 Worker</dt><dd><code className="text-xs">{review.workerId}</code></dd></div>
                <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">证据</dt><dd>{review.evidenceIds.length > 0 ? `${review.evidenceIds.length} 条（${review.evidenceIds.join('，')}）` : '暂无数据'}</dd></div>
              </dl>
            </div>
          ) : null}
          {publication ? (
            <div className="flex flex-wrap items-center gap-2 text-sm leading-[22px]" data-testid="publication-readout">
              <span className="text-text-secondary">交付发布</span>
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
      <p className="text-xs text-text-secondary">验收由独立评审产生（verdict=accept 才是通过）；执行结束不代表验收通过。</p>
    </Card>
  );
}
