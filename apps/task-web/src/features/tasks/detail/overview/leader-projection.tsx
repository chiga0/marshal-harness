// Leader 投影：只读展示服务端 LeaderView（stage/policyDigest/活跃 Worker/最近决定摘要/概要成果）。
// 缺失如实「暂无数据」；不把 activeWorker/最后观察当作模型正在持续工作的证明，也不显示 Worker 投递/ACK。

import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {LeaderRecord, WorkerRecord} from '@/lib/transport/types';
import {formatRelative, leaderStageLabel} from '../shared/format';

export interface LeaderProjectionProps {
  leader: LeaderRecord | null;
  workers: WorkerRecord[];
}

export function LeaderProjection({leader, workers}: LeaderProjectionProps) {
  return (
    <Card aria-label="Leader 投影" className="space-y-2" data-testid="leader-projection">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold leading-6">Leader 投影</h2>
        {leader ? <Badge variant="secondary">{leaderStageLabel(leader.stage)} <code className="ml-1 text-xs">{leader.stage}</code></Badge> : null}
      </div>
      {leader ? (
        <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">合同档案</dt><dd><code className="text-xs">{leader.profile}</code></dd></div>
          <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">任务 revision</dt><dd><code>{leader.taskRevision}</code></dd></div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">活跃 Worker</dt>
            <dd>{leader.activeWorkerId ? <code className="text-xs">{leader.activeWorkerId}</code> : '无（不代表没有已完成工作）'}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">策略摘要</dt>
            <dd className="break-all"><code className="text-xs">{leader.policyDigest}</code></dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">最近决定</dt>
            <dd className="break-all">
              {leader.lastDecision
                ? <code className="text-xs">{leader.lastDecision.digest}（callId {leader.lastDecision.callId}）</code>
                : '暂无数据'}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">待处理请求</dt>
            <dd>
              {leader.pendingRequest
                ? <><code className="text-xs">{leader.pendingRequest.id}</code>（{leader.pendingRequest.kind} · {leader.pendingRequest.status}）</>
                : '无'}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">概要成果</dt>
            <dd>{leader.summaryArtifactId ? <code className="text-xs">{leader.summaryArtifactId}</code> : '暂无数据'}</dd>
          </div>
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
