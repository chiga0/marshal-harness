// 团队（P08）：Worker 列表（节点/角色/状态/阶段/最近观察/进展摘要），点击进入抽屉明细。
// 不展示百分比进度或费用假数据；usage=null 如实「暂无数据」；最后观察时间不代表模型仍在工作。

import {Link, Route, Routes, useNavigate, useParams} from 'react-router-dom';
import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {TaskDetail, Transport, WorkerRecord} from '@/lib/transport/types';
import {casRevisionOf} from '../tasks/detail/shared/derive';
import {formatRelative, formatDateTime, workerRoleLabel, workerPhaseLabel, workerStatusLabel} from '../tasks/detail/shared/format';
import {StatusBadge, toneForWorker} from '../tasks/detail/shared/status-badge';
import {WorkerDrawer} from './worker-drawer';

export interface WorkersViewProps {
  detail: TaskDetail;
  workers: WorkerRecord[] | null;
  transport: Transport;
  onChanged: () => void;
}

export function WorkersView({detail, workers, transport, onChanged}: WorkersViewProps) {
  const revision = casRevisionOf(detail);
  return (
    <Routes>
      <Route index element={<WorkersList detail={detail} workers={workers} revision={revision} transport={transport} onChanged={onChanged} drawerId={null} />} />
      <Route path=":workerId" element={<WorkersListWithDrawer detail={detail} workers={workers} revision={revision} transport={transport} onChanged={onChanged} />} />
    </Routes>
  );
}

function WorkersListWithDrawer(props: Omit<WorkersListProps, 'drawerId'>) {
  const {workerId} = useParams<{workerId: string}>();
  return <WorkersList {...props} drawerId={workerId ?? null} />;
}

interface WorkersListProps {
  detail: TaskDetail;
  workers: WorkerRecord[] | null;
  revision: string | null;
  transport: Transport;
  onChanged: () => void;
  drawerId: string | null;
}

function WorkersList({detail, workers, revision, transport, onChanged, drawerId}: WorkersListProps) {
  const navigate = useNavigate();
  const base = `/tasks/${encodeURIComponent(detail.id)}/team`;
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
        {revision === null ? (
          <span className="text-xs text-warning">未获得任务 revision，单 Worker 取消不可用。</span>
        ) : null}
      </div>
      {workers.length === 0 ? (
        <Card><p className="text-sm text-text-secondary">暂无 Worker（尚未调度或该服务未提供）。</p></Card>
      ) : (
        <div className="overflow-x-auto rounded-md border border-border">
          <table className="w-full min-w-[640px] text-sm leading-[22px]">
            <thead className="bg-surface-muted/60 text-left text-xs text-text-secondary">
              <tr>
                <th className="px-3 py-2 font-medium">节点</th>
                <th className="px-3 py-2 font-medium">角色</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium">阶段</th>
                <th className="px-3 py-2 font-medium">最近观察</th>
                <th className="px-3 py-2 font-medium">进展摘要</th>
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
                  <td className="px-3 py-2 text-text-secondary" title={formatDateTime(worker.lastObservedAt)}>{formatRelative(worker.lastObservedAt)}</td>
                  <td className="max-w-[220px] truncate px-3 py-2 text-text-secondary" title={worker.progress?.summary ?? ''}>
                    {worker.progress ? worker.progress.summary : '暂无数据'}
                  </td>
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
        「最近观察」是服务端最近一次看到该 Worker 状态的时间，不代表模型仍在持续工作；不展示进度百分比或费用。
      </p>
      {openWorker ? (
        <WorkerDrawer taskId={detail.id} revision={revision} worker={openWorker} transport={transport} onClose={() => navigate(base)} onChanged={onChanged} />
      ) : null}
    </div>
  );
}
