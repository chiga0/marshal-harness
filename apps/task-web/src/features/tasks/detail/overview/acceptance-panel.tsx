// 独立验收与集中评审读数：只展示服务端给出的验收事实，不据执行结束推断成功。

import {Card} from '@/components/ui/card';
import type {TaskDetail} from '@/lib/transport/types';
import {acceptanceLabel} from '../shared/format';
import {StatusBadge, toneForAcceptance} from '../shared/status-badge';

export function AcceptancePanel({detail}: {detail: TaskDetail}) {
  const acceptance = detail.acceptance ?? null;
  const review = detail.latestReview ?? null;
  return (
    <Card aria-label="独立验收" className="space-y-2" data-testid="acceptance-panel">
      <h2 className="text-base font-semibold leading-6">独立验收</h2>
      {acceptance ? (
        <div className="space-y-1">
          <StatusBadge machine={acceptance.status} label={acceptanceLabel(acceptance.status)} tone={toneForAcceptance(acceptance.status)} />
          <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">验收摘要</dt><dd className="break-all">{acceptance.digest ? <code className="text-xs">{acceptance.digest}</code> : '暂无数据'}</dd></div>
            <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">证据</dt><dd>{acceptance.evidenceIds.length > 0 ? `${acceptance.evidenceIds.length} 条（${acceptance.evidenceIds.join('，')}）` : '暂无数据'}</dd></div>
          </dl>
        </div>
      ) : (
        <p className="text-sm text-text-secondary" data-testid="acceptance-unavailable">暂无独立验收读数（验收未完成或该服务未提供）。</p>
      )}
      {review ? (
        <p className="text-sm leading-[22px] text-text-secondary" data-testid="review-summary">
          集中评审：共 {review.total} 项，通过 {review.passed} 项，待定 {review.pending} 项。
        </p>
      ) : null}
      <p className="text-xs text-text-secondary">验收由独立校验产生；执行结束不代表验收通过。</p>
    </Card>
  );
}
