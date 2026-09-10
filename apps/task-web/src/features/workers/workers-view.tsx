// 团队（P08）：Worker 列表（节点/角色/状态/阶段/尝试/最近观察/进展摘要/用量），点击进入抽屉明细。
// 不展示进度百分比；usage.source='unavailable' 如实「不可用」（E23）；
// 「最近观察」是服务端最近一次看到该 Worker 状态的时间，不代表模型仍在持续工作。

import {Link, Route, Routes, useNavigate, useParams} from 'react-router-dom';
import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {TaskRecord, Transport, Usage, WorkerRecord} from '@/lib/transport/types';
import {formatDateTime, formatRelative, workerPhaseLabel, workerRoleLabel, workerStatusLabel} from '../tasks/detail/shared/format';
import {StatusBadge, toneForWorker} from '../tasks/detail/shared/status-badge';
import {WorkerDrawer} from './worker-drawer';

export interface WorkersViewProps {
  task: TaskRecord;
  workers: WorkerRecord[] | null;
  transport: Transport;
  onChanged: () => void;
}

/** 用量摘要：source=unavailable 一律「不可用」；其余按实返数值展示并注明来源。 */
export function usageSummary(usage: Usage): string {
  if (usage.source === 'unavailable') return '不可用';
  const parts: string[] = [];
  if (usage.tokens !== null) parts.push(`${usage.tokens} token`);
  if (usage.cost !== null) parts.push(usage.currency !== null ? `${usage.cost} ${usage.currency}` : String(usage.cost));
  return parts.length > 0 ? `${parts.join(' / ')}（来源：${usage.source}）` : `暂无数值（来源：${usage.source}）`;
}

export function WorkersView({task, workers, transport, onChanged}: WorkersViewProps) {
  return (
    <Routes>
      <Route index element={<WorkersList task={task} workers={workers} transport={transport} onChanged={onChanged} drawerId={null} />} />
      <Route path=":workerId" element={<WorkersListWithDrawer task={task} workers={workers} transport={transport} onChanged={onChanged} />} />
    </Routes>
  );
}

function WorkersListWithDrawer(props: Omit<WorkersListProps, 'drawerId'>) {
  const {workerId} = useParams<{workerId: string}>();
  return <WorkersList {...props} drawerId={workerId ?? null} />;
}

interface WorkersListProps {
  task: TaskRecord;
  workers: WorkerRecord[] | null;
  transport: Transport;
  onChanged: () => void;
  drawerId: string | null;
}

function WorkersList({task, workers, transport, onChanged, drawerId}: WorkersListProps) {
  const navigate = useNavigate();
  const base = `/tasks/${encodeURIComponent(task.id)}/team`;
  const openWorker = drawerId !== null ? (workers ?? []).find(worker => worker.id === drawerId) ?? null : null;

  if (workers === null) {
    return (
      <Card className="space-y-2">
        <h2 className="text-base font-semibold leading-6">团队</h2>
        <p className="text-sm text-text-secondary">Worker 列表加载失败或未加载；请刷新重试。</p>
      </Card>
    );
  }

  return (
    <div className="space-y-3" data-testid="workers-view">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">团队（{workers.length} 个 Worker）</h2>
      </div>
      {workers.length === 0 ? (
        <Card><p className="text-sm text-text-secondary">暂无 Worker（尚未调度或该服务未提供）。</p></Card>
      ) : (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full min-w-[720px] text-sm leading-[22px]">
            <thead className="bg-surface-muted/60 text-left text-xs text-text-secondary">
              <tr>
                <th className="px-3 py-2 font-medium">节点</th>
                <th className="px-3 py-2 font-medium">角色</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium">阶段</th>
                <th className="px-3 py-2 font-medium">尝试</th>
                <th className="px-3 py-2 font-medium">最近观察</th>
                <th className="px-3 py-2 font-medium">进展摘要</th>
                <th className="px-3 py-2 font-medium">用量</th>
                <th className="px-3 py-2 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {workers.map(worker => (
                <tr
                  key={worker.id}
                  className="cursor-pointer border-t border-border hover:bg-surface-muted/40"
                  onClick={() => navigate(`${base}/${encodeURIComponent(worker.id)}`)}
                  data-testid="worker-row"
                  data-worker-id={worker.id}
                >
                  <td className="px-3 py-2"><code className="text-xs">{worker.nodeId}</code></td>
                  <td className="px-3 py-2"><Badge variant="secondary">{workerRoleLabel(worker.role)}</Badge></td>
                  <td className="px-3 py-2"><StatusBadge machine={worker.status} label={workerStatusLabel(worker.status)} tone={toneForWorker(worker.status)} showMachine={false} /></td>
                  <td className="px-3 py-2 text-text-secondary">{workerPhaseLabel(worker.phase)}</td>
                  <td className="px-3 py-2 text-text-secondary">{worker.attempt}</td>
                  <td className="px-3 py-2 text-text-secondary" title={formatDateTime(worker.lastObservedAt)}>{formatRelative(worker.lastObservedAt)}</td>
                  <td className="max-w-[220px] truncate px-3 py-2 text-text-secondary" title={worker.progress?.summary ?? ''}>
                    {worker.progress ? worker.progress.summary : '暂无数据'}
                  </td>
                  <td className="max-w-[180px] truncate px-3 py-2 text-text-secondary" data-testid="worker-usage-cell">{usageSummary(worker.usage)}</td>
                  <td className="px-3 py-2" onClick={event => event.stopPropagation()}>
                    <Link className="text-xs text-accent underline-offset-4 hover:underline" to={`${base}/${encodeURIComponent(worker.id)}`}>明细</Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="text-xs text-text-secondary">
        「最近观察」是服务端最近一次看到该 Worker 状态的时间，不代表模型仍在持续工作；不展示进度百分比。
      </p>
      {openWorker ? (
        <WorkerDrawer taskRevision={task.revision} worker={openWorker} transport={transport} onClose={() => navigate(base)} onChanged={onChanged} />
      ) : null}
    </div>
  );
}
