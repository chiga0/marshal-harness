// 团队（P08）：Worker 列表（节点/角色/状态/阶段/尝试/最近观察/进展摘要/用量），点击进入抽屉明细。
// 不展示进度百分比；usage.source='unavailable' 如实「不可用」（E23）；
// 「最近观察」是服务端最近一次看到该 Worker 状态的时间，不代表模型仍在持续工作。

import {Link, Route, Routes, useNavigate, useParams} from 'react-router-dom';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import type {
  TaskRecord,
  TaskAuditRecord,
  PlanRecord,
  Transport,
  Usage,
  WorkerRecord,
} from '@/lib/transport/types';
import {
  formatDateTime,
  formatRelative,
  workerPhaseLabel,
  workerRoleLabel,
} from '../tasks/detail/shared/format';
import {StatusBadge, toneForWorker} from '../tasks/detail/shared/status-badge';
import {observationLabel, workerTokenSummary} from './worker-observation';
import {workerTitle} from '../tasks/detail/overview/task-journey';
import {WorkerDrawer} from './worker-drawer';

/** UI-05：Worker 列表分页状态（服务端默认页 50）；nextCursor 非空表示还有更多。 */
export interface WorkersPagination {
  nextCursor: string | null;
  loadingMore: boolean;
  onLoadMore: () => void;
}

export interface WorkersViewProps {
  task: TaskRecord;
  workers: WorkerRecord[] | null;
  plan?: PlanRecord | null | undefined;
  audit?: TaskAuditRecord | null | undefined;
  /** 未提供时按单页数据展示（测试/旧调用面）。 */
  pagination?: WorkersPagination;
  transport: Transport;
  onChanged: () => void;
}

/** 用量摘要：source=unavailable 一律「不可用」；其余按实返数值展示并注明来源。 */
export function usageSummary(usage: Usage): string {
  if (usage.source === 'unavailable') return '不可用';
  const parts: string[] = [];
  if (usage.tokens !== null) parts.push(`${usage.tokens} token`);
  if (usage.cost !== null)
    parts.push(
      usage.currency !== null
        ? `${usage.cost} ${usage.currency}`
        : String(usage.cost),
    );
  return parts.length > 0
    ? `${parts.join(' / ')}（来源：${usage.source}）`
    : `暂无数值（来源：${usage.source}）`;
}

export function WorkersView({
  task,
  workers,
  plan,
  audit,
  pagination,
  transport,
  onChanged,
}: WorkersViewProps) {
  return (
    <Routes>
      <Route
        path=":workerId?"
        element={
          <WorkersListWithDrawer
            task={task}
            workers={workers}
            plan={plan}
            audit={audit}
            pagination={pagination}
            transport={transport}
            onChanged={onChanged}
          />
        }
      />
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
  plan?: PlanRecord | null | undefined;
  audit?: TaskAuditRecord | null | undefined;
  pagination?: WorkersPagination | undefined;
  transport: Transport;
  onChanged: () => void;
  drawerId: string | null;
}

function WorkersList({
  task,
  workers,
  plan,
  audit,
  pagination,
  transport,
  onChanged,
  drawerId,
}: WorkersListProps) {
  const navigate = useNavigate();
  const base = `/tasks/${encodeURIComponent(task.id)}/team`;
  const openWorker =
    drawerId !== null
      ? ((workers ?? []).find((worker) => worker.id === drawerId) ?? null)
      : null;

  if (workers === null) {
    return (
      <Card className="space-y-2">
        <h2 className="text-base font-semibold leading-6">团队</h2>
        <p className="text-sm text-text-secondary">
          Worker 列表加载失败或未加载；请刷新重试。
        </p>
      </Card>
    );
  }

  return (
    <div className="space-y-3" data-testid="workers-view">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">
          团队（已加载 {workers.length} 个 Worker
          {pagination?.nextCursor ? '，服务端还有更多' : ''}）
        </h2>
      </div>
      {workers.length === 0 ? (
        <Card>
          <p className="text-sm text-text-secondary">
            暂无 Worker（尚未调度或该服务未提供）。
          </p>
        </Card>
      ) : (
        <ul className="worker-list" aria-label="执行成员工作包">
          {workers.map((worker) => (
            <li
              key={worker.id}
              data-testid="worker-row"
              data-worker-id={worker.id}
            >
              <div
                className="worker-list-row"
                onClick={() =>
                  navigate(`${base}/${encodeURIComponent(worker.id)}`)
                }
              >
                <span className="flex h-9 w-9 items-center justify-center rounded-full bg-surface-muted text-sm font-semibold text-text-secondary">
                  {workerRoleLabel(worker.role).slice(0, 1)}
                </span>
                <div className="min-w-0 space-y-2">
                  <Link
                    className="line-clamp-2 text-sm font-semibold hover:text-accent"
                    to={`${base}/${encodeURIComponent(worker.id)}`}
                  >
                    {workerTitle(worker, plan)}
                  </Link>
                  <p className="flex flex-wrap items-center gap-2 text-xs text-text-secondary">
                    <span>{worker.providerId}</span>
                    <span>·</span>
                    <span>
                      {workerRoleLabel(worker.role)} ·{' '}
                      {workerPhaseLabel(worker.phase)} · 执行序号{' '}
                      {worker.attempt}
                    </span>
                  </p>
                  <div className="text-sm text-text-secondary">
                    {['completed', 'failed', 'cancelled'].includes(
                      worker.status,
                    ) ? (
                      <p className="text-xs">执行已结束；以下为历史观察</p>
                    ) : null}
                    <p
                      className="line-clamp-2"
                      title={worker.progress?.summary ?? ''}
                    >
                      {worker.progress?.summary ?? '尚未收到具体活动'}
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-text-secondary">
                    <span title={formatDateTime(worker.lastObservedAt)}>
                      最后观察 {formatRelative(worker.lastObservedAt)}
                    </span>
                    <span data-testid="worker-usage-cell">
                      用量{' '}
                      {workerTokenSummary(worker) ?? usageSummary(worker.usage)}
                    </span>
                  </div>
                  <details
                    className="text-xs text-text-secondary"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <summary className="cursor-pointer">节点标识</summary>
                    <code>{worker.nodeId}</code>
                  </details>
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <StatusBadge
                    machine={worker.status}
                    label={observationLabel(worker)}
                    tone={toneForWorker(worker.status)}
                    showMachine={false}
                  />
                  <Link
                    onClick={(event) => event.stopPropagation()}
                    className="inline-flex min-h-11 items-center text-xs text-accent"
                    to={`${base}/${encodeURIComponent(worker.id)}`}
                  >
                    明细
                  </Link>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-text-secondary">
        「最近观察」是服务端最近一次看到该 Worker
        状态的时间，不代表模型仍在持续工作；不展示进度百分比。
      </p>
      {pagination?.nextCursor ? (
        <div
          className="flex items-center gap-2"
          data-testid="workers-load-more"
        >
          <Button
            size="sm"
            variant="outline"
            onClick={pagination.onLoadMore}
            loading={pagination.loadingMore}
            disabled={pagination.loadingMore}
          >
            加载更多 Worker
          </Button>
          <span className="text-xs text-text-secondary">
            当前显示前 {workers.length} 条；服务端按页返回（默认每页 50 条）。
          </span>
        </div>
      ) : null}
      {openWorker ? (
        <WorkerDrawer
          taskRevision={task.revision}
          worker={openWorker}
          plan={plan}
          audit={audit}
          transport={transport}
          onClose={() => navigate(base)}
          onChanged={onChanged}
        />
      ) : drawerId !== null ? (
        <Card className="space-y-2" role="status">
          <h3 className="break-all font-medium">
            成员 {drawerId} 的详情尚不可用
          </h3>
          <p className="text-sm text-text-secondary">
            {pagination?.nextCursor
              ? '该成员不在已加载的分页中，请加载更多成员后查看。'
              : '当前列表没有该成员；请刷新核对，不能据此判断它已完成或已删除。'}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={onChanged}>
              刷新团队
            </Button>
            <Link
              to={base}
              className="inline-flex min-h-11 items-center text-accent underline"
            >
              返回团队列表
            </Link>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
