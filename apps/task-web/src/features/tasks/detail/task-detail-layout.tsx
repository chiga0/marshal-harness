// 任务详情（W2）：概览/团队/成果/活动 四个子视图 + 统一数据装载与轮询。
// 投影逐一对齐 Task API 合同：task/workers/plan/questions/leader/artifacts 各为独立查询；
// 轮询用 intervalFor('detail') 节奏；乱序旧快照由 preferFreshTask 拦截（E21）；
// plan（首次冻结前 404）、questions、leader 的加载失败如实降级为对应视图的「不可用/尚未提供」，
// 不冒充整页错误；成果清单整体失败才显示 ErrorNotice。

import {useInfiniteQuery, useQuery, useQueryClient} from '@tanstack/react-query';
import {Link, NavLink, Route, Routes, useParams} from 'react-router-dom';
import {useMemo} from 'react';
import {Button} from '@/components/ui/button';
import {useConnection} from '@/features/connection/connection';
import {usePollMode} from '@/lib/queries/polling';
import type {LeaderRecord, PlanRecord, QuestionsResponse, TaskAuditRecord, TaskRecord, Transport, WorkersResponse} from '@/lib/transport/types';
import {cn} from '@/lib/cn';
import {ActivityView} from './activity/activity-view';
import {OverviewView} from './overview/overview-view';
import {taskKeys} from './query-keys';
import {preferFreshTask, questionNeedsAttention} from './shared/derive';
import {useLeaderReplyReceipt} from './shared/leader-reply-receipt';
import {ErrorNotice} from './shared/error-notice';
import {formatRelative, taskStatusLabel} from './shared/format';
import {StatusBadge, toneForTask} from './shared/status-badge';
import {WorkersView} from '../../workers/workers-view';
import {ArtifactsView} from '../../artifacts/artifacts-view';
import {useTaskArtifacts} from '../../artifacts/use-task-artifacts';
import {useObservedInputs} from '../../artifacts/use-observed-inputs';
import {TaskGraph} from './graph/task-graph';

// 注意：本路由是 splat（tasks/:taskId/*），其内部的相对链接会按完整当前 URL 解析（React Router 规则）。
// 因此 tab 一律使用绝对路径，避免从子页再导航时出现 team/activity 这类错误叠加。
function tabsFor(taskId: string) {
  const base = `/tasks/${encodeURIComponent(taskId)}`;
  return [
    {to: base, end: true, label: '概览', key: 'overview'},
    {to: `${base}/graph`, end: false, label: '任务图', key: 'graph'},
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
  // signal 贯通：卸载/查询作废即取消在途读；另有 transport 默认 READ deadline 兜底悬挂（UI-06）
  const taskQuery = useQuery<TaskRecord>({
    queryKey: taskKeys.detail(taskId),
    queryFn: ({signal}) => transport.getTask(taskId, {signal}),
    refetchInterval,
    structuralSharing: preferFreshTask,
  });
  // UI-05：Worker 列表分页（服务端默认页 50）；首屏第一页，超出显式「加载更多」并显示已加载范围
  const workersQuery = useInfiniteQuery<WorkersResponse>({
    queryKey: taskKeys.workers(taskId),
    queryFn: ({pageParam, signal}) => transport.getWorkers(taskId, {cursor: pageParam as string | null, signal}),
    initialPageParam: null as string | null,
    // 页与页可能因轮询重叠（首页重取含新项），getNextPageParam 仅负责游标，去重在扁平化时做
    getNextPageParam: lastPage => lastPage.nextCursor ?? undefined,
    refetchInterval,
  });
  const workerItems = useMemo(() => {
    const seen = new Set<string>();
    const items: WorkersResponse['items'] = [];
    for (const page of workersQuery.data?.pages ?? []) {
      for (const worker of page.items) {
        if (seen.has(worker.id)) continue;
        seen.add(worker.id);
        items.push(worker);
      }
    }
    return items;
  }, [workersQuery.data]);
  const workersNextCursor = workersQuery.data ? (workersQuery.data.pages[workersQuery.data.pages.length - 1]?.nextCursor ?? null) : null;
  const workersPagination = useMemo(() => ({
    nextCursor: workersNextCursor,
    loadingMore: workersQuery.isFetchingNextPage,
    onLoadMore: () => { void workersQuery.fetchNextPage(); },
  }), [workersNextCursor, workersQuery.isFetchingNextPage, workersQuery.fetchNextPage]);
  // 计划首次冻结前服务端返回 404：容忍，不下发为页面错误，概览如实显示「尚未冻结计划」
  const planQuery = useQuery<PlanRecord>({
    queryKey: taskKeys.plan(taskId),
    queryFn: ({signal}) => transport.getPlan(taskId, {signal}),
    refetchInterval,
    retry: false,
  });
  // 问题流加载失败同样容忍：概览以 questions:null 如实降级
  const questionsQuery = useQuery<QuestionsResponse>({
    queryKey: taskKeys.questions(taskId),
    queryFn: ({signal}) => transport.getQuestions(taskId, {limit: 100, signal}), // 合同 items 上限 100；单页取齐（合同本身限同期开放问题 ≤3）
    refetchInterval,
    retry: false,
  });
  const leaderQuery = useQuery<LeaderRecord>({
    queryKey: taskKeys.leader(taskId),
    queryFn: ({signal}) => transport.getLeader(taskId, {signal}),
    refetchInterval,
    retry: false, // Leader 未启用/未提供时不反复重试；以展示不可用为准
  });
  // UI-04：独立验收只来自 audit 投影（acceptance）；加载失败如实降级，不以 review 推导验收
  const auditQuery = useQuery<TaskAuditRecord>({
    queryKey: taskKeys.audit(taskId),
    queryFn: ({signal}) => transport.getAudit(taskId, {signal}),
    refetchInterval,
    retry: false,
  });

  const task = taskQuery.data ?? null;
  // 成果元数据由 task.artifactIds 逐 id 并行拉取；单项失败保留占位，全部失败转整体错误
  const artifactsQuery = useTaskArtifacts({
    taskId,
    artifactIds: task?.artifactIds ?? [],
    leader: leaderQuery.isError ? null : leaderQuery.data ?? null,
    transport,
    refetchInterval,
  });

  const onChanged = () => {
    void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)});
  };

  const workers = workersQuery.data ? workerItems : (workersQuery.isError ? null : []);
  const plan = planQuery.data ?? null;
  const questions = questionsQuery.data ?? null;
  const leader = leaderQuery.data ?? null;
  const audit = auditQuery.isError ? null : auditQuery.data ?? null;
  const artifacts = artifactsQuery.isError ? null : artifactsQuery.data ?? null;
  const observedInputs = useObservedInputs({taskId, audit, transport, refetchInterval});
  const [leaderReplyAccepted] = useLeaderReplyReceipt(taskId, leader?.pendingRequest ?? null);
  const waitingForLeader = task?.status === 'awaiting-answer' && leader?.pendingRequest?.status === 'pending'
    && leaderReplyAccepted && questions !== null && !questions.items.some(questionNeedsAttention);

  return (
    <section aria-label="任务详情" className="flex min-h-0 flex-1 flex-col overflow-y-auto p-6" data-testid="task-detail">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Link to="/" className="text-sm text-accent underline-offset-4 hover:underline">← 任务列表</Link>
            <code className="text-xs text-text-secondary">{taskId}</code>
          </div>
          <h1 className="mt-1 line-clamp-2 break-words text-[22px] font-semibold leading-[30px]" data-testid="task-heading">{task ? task.intent : '任务详情'}</h1>
          {task ? (
            <details key={taskId} className="mt-1 text-sm" data-testid="header-original-intent">
              <summary className="w-fit cursor-pointer rounded text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent">查看完整原需求</summary>
              <p className="mt-2 max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded border border-border p-3 text-text-secondary" tabIndex={0}>{task.intent}</p>
            </details>
          ) : null}
          {task ? (
            <div className="mt-1 flex flex-wrap items-center gap-3">
              <StatusBadge machine={task.status} label={waitingForLeader ? '答复已受理，等待 Leader 更新' : taskStatusLabel(task.status)} tone={toneForTask(task.status)} />
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
          <nav className="mb-4 flex flex-wrap gap-1 border-b border-border" aria-label="详情子视图">
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
            <Route index element={<OverviewView task={task} plan={plan} questions={questions} workers={workers} leader={leader} audit={audit} transport={transport} onChanged={onChanged} graph={<TaskGraph taskId={taskId} plan={plan} workers={workers} transport={transport} />} />} />
            <Route path="graph" element={<TaskGraph taskId={taskId} plan={plan} workers={workers} transport={transport} />} />
            <Route path="team/*" element={<WorkersView task={task} workers={workers} pagination={workersPagination} transport={transport} onChanged={onChanged} />} />
            <Route path="artifacts" element={<ArtifactsView task={task} leader={leader} audit={audit} artifacts={artifacts} observedInputs={observedInputs} transport={transport} />} />
            <Route path="activity" element={<ActivityView taskId={taskId} transport={transport} />} />
            <Route path="*" element={<OverviewView task={task} plan={plan} questions={questions} workers={workers} leader={leader} audit={audit} transport={transport} onChanged={onChanged} />} />
          </Routes>
        </>
      ) : taskQuery.isPending ? (
        <p className="text-sm text-text-secondary" role="status">正在加载任务详情…</p>
      ) : null}
    </section>
  );
}
