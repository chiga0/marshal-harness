// Leader 可用投影：只读展示服务端 Leader 记录；缺失字段如实「暂无数据」；
// 不把 activeWorker/最后观察当作模型正在持续工作的证明。

import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {LeaderRecord, WorkerRecord} from '@/lib/transport/types';
import {leaderStage} from '../shared/derive';
import {formatRelative, leaderStageLabel} from '../shared/format';

export interface LeaderProjectionProps {
  leader: LeaderRecord | null | undefined;
  workers: WorkerRecord[];
}

export function LeaderProjection({leader, workers}: LeaderProjectionProps) {
  const stage = leaderStage(leader);
  return (
    <Card aria-label="Leader 投影" className="space-y-2" data-testid="leader-projection">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold leading-6">Leader 投影</h2>
        {stage ? <Badge variant="secondary">{leaderStageLabel(stage)} <code className="ml-1 text-xs">{stage}</code></Badge> : null}
      </div>
      {leader ? (
        <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">状态</dt><dd><code>{leader.status}</code></dd></div>
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">尝试次数</dt><dd>{leader.attempts}</dd></div>
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">Worker 数</dt><dd>{leader.workers.length}</dd></div>
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">申请版本</dt><dd>{leader.requestedVersion ?? '暂无数据'}</dd></div>
          <div className="flex gap-2 sm:col-span-2"><dt className="shrink-0 text-text-secondary">决定摘要</dt><dd className="break-all">{leader.decisionDigest ? <code className="text-xs">{leader.decisionDigest}</code> : '暂无数据'}</dd></div>
        </dl>
      ) : (
        <p className="text-sm text-text-secondary" data-testid="leader-unavailable">暂无 Leader 投影（Leader 未启用或该服务未提供）。</p>
      )}
      {workers.length > 0 ? (
        <p className="text-xs text-text-secondary">
          {workers.length} 个 Worker 最近观察：{workers.map(w => `${w.nodeId}（${formatRelative(w.lastObservedAt)}）`).join('、')}。
          最后观察时间不代表模型仍在持续工作。
        </p>
      ) : null}
    </Card>
  );
}
