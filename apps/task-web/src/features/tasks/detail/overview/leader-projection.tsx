// Leader 投影：只读展示服务端 LeaderView（stage/policyDigest/活跃 Worker/最近决定摘要/概要成果）。
// 缺失如实「暂无数据」；不把 activeWorker/最后观察当作模型正在持续工作的证明，也不显示 Worker 投递/ACK。

import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {LeaderRecord, WorkerRecord} from '@/lib/transport/types';
import {formatRelative, leaderStageLabel, workerRoleLabel} from '../shared/format';

export interface LeaderProjectionProps {
  leader: LeaderRecord | null;
  workers: WorkerRecord[];
}

export function LeaderProjection({leader, workers}: LeaderProjectionProps) {
  const activeWorker = leader?.activeWorkerId ? workers.find(worker => worker.id === leader.activeWorkerId) : null;
  const requestStatus = {pending: '待处理', replied: '已答复', closed: '已关闭'};
  return (
    <Card aria-label="Leader 投影" className="space-y-2" data-testid="leader-projection">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold leading-6">团队协调进展</h2>
        {leader ? <Badge variant="secondary">{leaderStageLabel(leader.stage)}</Badge> : null}
      </div>
      {leader ? (
        <>
        <dl className="grid grid-cols-1 gap-y-2 text-sm leading-[22px]">
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">关联成员</dt>
            <dd className="min-w-0 break-words">{activeWorker ? `${workerRoleLabel(activeWorker.role)} · ${activeWorker.nodeId}` : leader.activeWorkerId ? '已有成员引用，成员详情尚未加载' : '当前无活跃成员引用（不代表没有已完成工作）'}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">最近决定</dt>
            <dd className="min-w-0 break-words">
              {leader.lastDecision
                ? '已记录决定摘要；该投影未提供决定正文，不能据此推断执行结果。'
                : '暂无数据'}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">待处理请求</dt>
            <dd className="min-w-0 break-words">
              {leader.pendingRequest
                ? <>{leader.pendingRequest.kind === 'publication' ? '发布授权' : '业务问答'} · {requestStatus[leader.pendingRequest.status]}{leader.pendingRequest.status === 'pending' ? '。请在需要处理区域核对原文并决定。' : ''}</>
                : '无'}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">概要成果</dt>
            <dd className="min-w-0 break-words">{leader.summaryArtifactId ? '已有概要成果引用；内容和交付结果请在成果页核对。' : '暂无数据'}</dd>
          </div>
        </dl>
        <details className="rounded border border-border p-2 text-xs" data-testid="leader-technical-details">
          <summary className="min-h-11 cursor-pointer py-3 font-medium text-text-secondary focus-visible:outline focus-visible:outline-2">Leader 技术与审计详情</summary>
          <dl className="space-y-2 break-all">
            <div><dt className="text-text-secondary">合同档案</dt><dd><code>{leader.profile}</code></dd></div>
            <div><dt className="text-text-secondary">阶段 / 任务 revision</dt><dd><code>{leader.stage} / {leader.taskRevision}</code></dd></div>
            <div><dt className="text-text-secondary">活跃 Worker 引用</dt><dd><code>{leader.activeWorkerId ?? '无'}</code></dd></div>
            <div><dt className="text-text-secondary">策略摘要</dt><dd><code>{leader.policyDigest}</code></dd></div>
            <div><dt className="text-text-secondary">最近决定摘要 / callId</dt><dd>{leader.lastDecision ? <code>{leader.lastDecision.digest} / {leader.lastDecision.callId}</code> : '暂无数据'}</dd></div>
            <div><dt className="text-text-secondary">待处理请求引用 / kind / status</dt><dd>{leader.pendingRequest ? <code>{leader.pendingRequest.id} / {leader.pendingRequest.kind} / {leader.pendingRequest.status}</code> : '无'}</dd></div>
            <div><dt className="text-text-secondary">概要成果引用</dt><dd><code>{leader.summaryArtifactId ?? '暂无数据'}</code></dd></div>
            {workers.length > 0 ? <div><dt className="text-text-secondary">成员最近观察</dt><dd><ul className="space-y-1">{workers.map(worker => <li key={worker.id}>{worker.nodeId}（{formatRelative(worker.lastObservedAt)}）</li>)}</ul></dd></div> : null}
          </dl>
        </details>
        </>
      ) : (
        <p className="text-sm text-text-secondary" data-testid="leader-unavailable">暂无 Leader 投影（Leader 未启用或该服务未提供）。</p>
      )}
      {workers.length > 0 ? (
        <p className="text-xs text-text-secondary">
          已加载 {workers.length} 个执行成员，最近观察读数见技术与审计详情。
          最后观察时间不代表模型仍在持续工作。
        </p>
      ) : null}
    </Card>
  );
}
