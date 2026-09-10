// 任务详情（W2）：概览/团队/成果/活动 四个子视图 + 统一数据装载与轮询。
// 轮询用 intervalFor('detail') 节奏；乱序旧快照由 preferFreshTask 拦截（E21）；所有写操作见各视图。

import {useQuery, useQueryClient} from '@tanstack/react-query';
import {Link, NavLink, Route, Routes, useParams} from 'react-router-dom';
import {Button} from '@/components/ui/button';
import {useConnection} from '@/features/connection/connection';
import {usePollMode} from '@/lib/queries/polling';
import type {LeaderRecord, PublicationRecord, TaskDetail, Transport, WorkerRecord} from '@/lib/transport/types';
import {cn} from '@/lib/cn';
import {ActivityView} from './activity/activity-view';
import {OverviewView} from './overview/overview-view';
import {taskKeys} from './query-keys';
import {preferFreshTask} from './shared/derive';
import {ErrorNotice} from './shared/error-notice';
import {formatRelative, taskStatusLabel} from './shared/format';
import {StatusBadge, toneForTask} from './shared/status-badge';
import {WorkersView} from '../../workers/workers-view';
import {ArtifactsView} from '../../artifacts/artifacts-view';

// 注意：本路由是 splat（tasks/:taskId/*），其内部的相对链接会按完整当前 URL 解析（React Router 规则）。
// 因此 tab 一律使用绝对路径，避免从子页再导航时出现 team/activity 这类错误叠加。
function tabsFor(taskId: string) {
  const base = `/tasks/${encodeURIComponent(taskId)}`;
  return [
    {to: base, end: true, label: '概览', key: 'overview'},
    {to: `${base}/team`, end: false, label: '团队', key: 'team'},
    {to: `${base}/artifacts`, end: false, label: '成果', key: 'artifacts'},
    {to: `${base}/activity`, end: false, label: '活动', key: 'activity'},
  ] as const;
}

export function TaskDetailLayout() {
  const {taskId} = useParams<{taskId: string}>();
  const {transport} = useConnection();
  if (!taskId) {
    return <section className="p-6"><p className="text-sm text-danger">缺少任务 ID。</p></section>;
  }
  if (!transport) {
    return <section className="p-6"><p className="text-sm text-text-secondary">未连接服务。</p></section>;
  }
  return <TaskDetailLoaded taskId={taskId} transport={transport} />;
}

function TaskDetailLoaded({taskId, transport}: {taskId: string; transport: Transport}) {
  const queryClient = useQueryClient();
  const intervalMs = usePollMode('detail');
  const refetchInterval = intervalMs ?? false;

  const detailQuery = useQuery<TaskDetail>({
    queryKey: taskKeys.detail(taskId),
    queryFn: () => transport.getTask(taskId),
    refetchInterval,
    structuralSharing: preferFreshTask,
  });
  const workersQuery = useQuery<{workers: WorkerRecord[]}>({
    queryKey: taskKeys.workers(taskId),
    queryFn: () => transport.getWorkers(taskId),
    refetchInterval,
  });
  const leaderQuery = useQuery<LeaderRecord>({
    queryKey: taskKeys.leader(taskId),
    queryFn: () => transport.getLeader(taskId),
    refetchInterval,
    retry: false, // Leader 未启用/未提供时不反复重试；以展示不可用为准
  });
  const publicationsQuery = useQuery<{publications: PublicationRecord[]}>({
    queryKey: taskKeys.publications(taskId),
    queryFn: () => transport.getPublications(taskId),
    refetchInterval,
    retry: false,
  });

  const onChanged = () => {
    void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)});
  };

  const detail = detailQuery.data ?? null;
  const workers = workersQuery.data?.workers ?? (workersQuery.isError ? null : []);
  const leader = leaderQuery.data ?? null;
  const publications = publicationsQuery.data?.publications ?? (publicationsQuery.isError ? null : []);

  return (
    <section aria-label="任务详情" className="flex min-h-0 flex-1 flex-col overflow-y-auto p-6" data-testid="task-detail">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Link to="/" className="text-sm text-accent underline-offset-4 hover:underline">← 任务列表</Link>
            <code className="text-xs text-text-secondary">{taskId}</code>
          </div>
          <h1 className="mt-1 break-words text-[22px] font-semibold leading-[30px]">{detail ? detail.intent : '任务详情'}</h1>
          {detail ? (
            <div className="mt-1 flex flex-wrap items-center gap-3">
              <StatusBadge machine={detail.status} label={taskStatusLabel(detail.status)} tone={toneForTask(detail.status)} />
              <span className="text-xs text-text-secondary">最近状态变化：{formatRelative(detail.statusAt)}</span>
              <span className="text-xs text-text-secondary">轮询节奏：{intervalMs === null ? '已停止（页面隐藏）' : `${Math.round(intervalMs / 1000)} 秒`}</span>
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={onChanged} data-testid="detail-refresh">刷新</Button>
        </div>
      </div>

      {detailQuery.isError ? (
        <ErrorNotice error={detailQuery.error} title="加载任务详情失败" onRefresh={() => void detailQuery.refetch()} />
      ) : null}
      {workersQuery.isError ? (
        <ErrorNotice error={workersQuery.error} title="加载团队失败" onRefresh={() => void workersQuery.refetch()} />
      ) : null}

      {detail ? (
        <>
          <nav className="mb-4 flex gap-1 border-b border-border" aria-label="详情子视图">
            {tabsFor(taskId).map(tab => (
              <NavLink
                key={tab.key}
                to={tab.to}
                end={tab.end}
                className={({isActive}) => cn(
                  '-mb-px border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
                  isActive ? 'border-accent text-text-primary' : 'border-transparent text-text-secondary hover:text-text-primary',
                )}
              >
                {tab.label}
              </NavLink>
            ))}
          </nav>
          <Routes>
            <Route index element={<OverviewView detail={detail} workers={workers} leader={leader} transport={transport} onChanged={onChanged} />} />
            <Route path="team/*" element={<WorkersView detail={detail} workers={workers} transport={transport} onChanged={onChanged} />} />
            <Route path="artifacts" element={<ArtifactsView detail={detail} publications={publications} transport={transport} />} />
            <Route path="activity" element={<ActivityView taskId={taskId} transport={transport} />} />
            <Route path="*" element={<OverviewView detail={detail} workers={workers} leader={leader} transport={transport} onChanged={onChanged} />} />
          </Routes>
        </>
      ) : detailQuery.isPending ? (
        <p className="text-sm text-text-secondary" role="status">正在加载任务详情…</p>
      ) : null}
    </section>
  );
}
