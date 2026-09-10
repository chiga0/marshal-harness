// 任务详情（W2）：概览/团队/成果/活动 四个子视图 + 统一数据装载与轮询。
// 投影逐一对齐 Task API 合同：task/workers/plan/questions/leader/artifacts 各为独立查询；
// 轮询用 intervalFor('detail') 节奏；乱序旧快照由 preferFreshTask 拦截（E21）；
// plan（首次冻结前 404）、questions、leader 的加载失败如实降级为对应视图的「不可用/尚未提供」，
// 不冒充整页错误；成果清单整体失败才显示 ErrorNotice。

import {useQuery, useQueryClient} from '@tanstack/react-query';
import {Link, NavLink, Route, Routes, useParams} from 'react-router-dom';
import {Button} from '@/components/ui/button';
import {useConnection} from '@/features/connection/connection';
import {usePollMode} from '@/lib/queries/polling';
import type {LeaderRecord, PlanRecord, QuestionsResponse, TaskRecord, Transport, WorkersResponse} from '@/lib/transport/types';
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
import {useTaskArtifacts} from '../../artifacts/use-task-artifacts';

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

  // GET /v1/tasks/{taskId} 直接返回 Task；乱序由 preferFreshTask（数值 revision）拦截
  const taskQuery = useQuery<TaskRecord>({
    queryKey: taskKeys.detail(taskId),
    queryFn: () => transport.getTask(taskId),
    refetchInterval,
    structuralSharing: preferFreshTask,
  });
  const workersQuery = useQuery<WorkersResponse>({
    queryKey: taskKeys.workers(taskId),
    queryFn: () => transport.getWorkers(taskId),
    refetchInterval,
  });
  // 计划首次冻结前服务端返回 404：容忍，不下发为页面错误，概览如实显示「尚未冻结计划」
  const planQuery = useQuery<PlanRecord>({
    queryKey: taskKeys.plan(taskId),
    queryFn: () => transport.getPlan(taskId),
    refetchInterval,
    retry: false,
  });
  // 问题流加载失败同样容忍：概览以 questions:null 如实降级
  const questionsQuery = useQuery<QuestionsResponse>({
    queryKey: taskKeys.questions(taskId),
    queryFn: () => transport.getQuestions(taskId, {limit: 100}), // 合同 items 上限 100；单页取齐（合同本身限同期开放问题 ≤3）
    refetchInterval,
    retry: false,
  });
  const leaderQuery = useQuery<LeaderRecord>({
    queryKey: taskKeys.leader(taskId),
    queryFn: () => transport.getLeader(taskId),
    refetchInterval,
    retry: false, // Leader 未启用/未提供时不反复重试；以展示不可用为准
  });

  const task = taskQuery.data ?? null;
  // 成果元数据由 task.artifactIds 逐 id 并行拉取；单项失败保留占位，全部失败转整体错误
  const artifactsQuery = useTaskArtifacts({
    taskId,
    artifactIds: task?.artifactIds ?? [],
    transport,
    refetchInterval,
  });

  const onChanged = () => {
    void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)});
  };

  const workers = workersQuery.data?.items ?? (workersQuery.isError ? null : []);
  const plan = planQuery.data ?? null;
  const questions = questionsQuery.data ?? null;
  const leader = leaderQuery.data ?? null;
  const artifacts = artifactsQuery.data ?? null;

  return (
    <section aria-label="任务详情" className="flex min-h-0 flex-1 flex-col overflow-y-auto p-6" data-testid="task-detail">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Link to="/" className="text-sm text-accent underline-offset-4 hover:underline">← 任务列表</Link>
            <code className="text-xs text-text-secondary">{taskId}</code>
          </div>
          <h1 className="mt-1 break-words text-[22px] font-semibold leading-[30px]">{task ? task.intent : '任务详情'}</h1>
          {task ? (
            <div className="mt-1 flex flex-wrap items-center gap-3">
              <StatusBadge machine={task.status} label={taskStatusLabel(task.status)} tone={toneForTask(task.status)} />
              <span className="text-xs text-text-secondary">最近状态变化：{formatRelative(task.updatedAt)}</span>
              <span className="text-xs text-text-secondary">轮询节奏：{intervalMs === null ? '已停止（页面隐藏）' : `${Math.round(intervalMs / 1000)} 秒`}</span>
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={onChanged} data-testid="detail-refresh">刷新</Button>
        </div>
      </div>

      {taskQuery.isError ? (
        <ErrorNotice error={taskQuery.error} title="加载任务详情失败" onRefresh={() => void taskQuery.refetch()} />
      ) : null}
      {workersQuery.isError ? (
        <ErrorNotice error={workersQuery.error} title="加载团队失败" onRefresh={() => void workersQuery.refetch()} />
      ) : null}
      {task !== null && artifactsQuery.isError ? (
        <ErrorNotice error={artifactsQuery.error} title="加载成果清单失败" onRefresh={() => void artifactsQuery.refetch()} />
      ) : null}

      {task ? (
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
            <Route index element={<OverviewView task={task} plan={plan} questions={questions} workers={workers} leader={leader} transport={transport} onChanged={onChanged} />} />
            <Route path="team/*" element={<WorkersView task={task} workers={workers} transport={transport} onChanged={onChanged} />} />
            <Route path="artifacts" element={<ArtifactsView task={task} leader={leader} artifacts={artifacts} transport={transport} />} />
            <Route path="activity" element={<ActivityView taskId={taskId} transport={transport} />} />
            <Route path="*" element={<OverviewView task={task} plan={plan} questions={questions} workers={workers} leader={leader} transport={transport} onChanged={onChanged} />} />
          </Routes>
        </>
      ) : taskQuery.isPending ? (
        <p className="text-sm text-text-secondary" role="status">正在加载任务详情…</p>
      ) : null}
    </section>
  );
}
