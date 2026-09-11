// 任务列表（P02）：nextCursor「加载更多」分页 + 已加载范围内的文本/状态筛选（明确局部范围，不冒称全局检索）；
// 轮询节奏走 lib/queries 的 usePollMode('list')；401 由查询层统一清理 token，本页给出重新连接出口。
import {useMemo, useState} from 'react';
import {Link} from 'react-router-dom';
import {useInfiniteQuery} from '@tanstack/react-query';
import {LayoutGrid, List, Plus, RefreshCw, Search} from 'lucide-react';
import {ApiError} from '../../lib/transport/types';
import type {TaskRecord, TaskStatus, Transport} from '../../lib/transport/types';
import {usePollMode} from '../../lib/queries/polling';
import {useConnection} from '../connection/connection';
import {Badge} from '../../components/ui/badge';
import {Button} from '../../components/ui/button';
import {Alert} from '../../components/ui/alert';
import {Input} from '../../components/ui/input';
import {Label} from '../../components/ui/label';
import {Select} from '../../components/ui/select';
import {Skeleton} from '../../components/ui/skeleton';
import {formatDateTime, isAwaitingStatus, statusMeta} from './format';

export const TASK_LIST_PAGE_SIZE = 24;

const STATUS_OPTIONS: (TaskStatus | 'any')[] = [
  'any', 'draft', 'planning', 'awaiting-answer', 'awaiting-confirmation', 'awaiting-approval',
  'queued', 'running', 'paused', 'cancelling', 'completed', 'failed', 'cancelled', 'intervention',
];

function isUnauthorizedError(error: unknown): boolean {
  return error instanceof ApiError && error.isUnauthorized;
}

export function describeTaskApiError(error: unknown): string {
  if (error instanceof ApiError) {
    const parts = ['错误码 ' + error.code + '（HTTP ' + error.status + '）'];
    if (error.message && error.message !== error.code) parts.push(error.message);
    if (error.requestId) parts.push('requestId：' + error.requestId);
    return parts.join('；');
  }
  if (error instanceof Error) return '网络层失败：' + error.message;
  return '发生未知错误。';
}

function TaskListSkeleton() {
  return (
    <ul aria-busy="true" aria-label="任务列表加载中" className="space-y-2">
      {Array.from({length: 6}, (_, index) => (
        <li key={index} className="rounded-md border border-border bg-surface p-4">
          <div className="flex items-center justify-between gap-3">
            <Skeleton className="h-5 w-2/5" />
            <Skeleton className="h-4 w-36" />
          </div>
          <div className="mt-2 flex gap-2">
            <Skeleton className="h-5 w-16 rounded-full" />
            <Skeleton className="h-5 w-24 rounded-full" />
          </div>
        </li>
      ))}
    </ul>
  );
}

type ViewMode = 'list' | 'cards';

function TaskRow({task, mode}: {task: TaskRecord; mode: ViewMode}) {
  const meta = statusMeta(task.status);
  return (
    <li data-task-id={task.id} className={mode === 'cards'
      ? 'flex min-w-0 flex-col gap-4 rounded-lg border border-border bg-surface p-4'
      : 'grid min-w-0 gap-2 border-b border-border px-4 py-3 last:border-b-0 md:grid-cols-[minmax(0,1fr)_minmax(0,20rem)] md:items-center md:gap-6'}>
      <div className="flex min-w-0 flex-col gap-1">
        <Link
          to={'/tasks/' + encodeURIComponent(task.id)}
          className="min-w-0 [overflow-wrap:anywhere] text-sm font-medium leading-[22px] text-text-primary underline-offset-4 hover:text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          {task.intent}
        </Link>
        <span className="text-xs leading-[18px] text-text-secondary" title={task.updatedAt}>
          更新于 {formatDateTime(task.updatedAt)}
        </span>
      </div>
      <div className={mode === 'cards' ? 'mt-auto flex min-w-0 flex-wrap items-center gap-2 border-t border-border pt-3' : 'flex min-w-0 flex-wrap items-center gap-2'}>
        <Badge variant={meta.variant} title={'status: ' + task.status}>{meta.label}</Badge>
        {meta.awaiting ? <Badge variant="warning">待处理</Badge> : null}
        {task.status === 'failed' && task.code ? (
          <span className="min-w-0 [overflow-wrap:anywhere] text-xs leading-[18px] text-danger">失败码：{task.code}</span>
        ) : null}
        {isAwaitingStatus(task.status) && task.deadlineAt ? (
          <span className="text-xs leading-[18px] text-text-secondary" title={task.deadlineAt}>
            期限 {formatDateTime(task.deadlineAt)}
          </span>
        ) : null}
        <span className="w-full min-w-0 [overflow-wrap:anywhere] text-xs leading-[18px] text-text-secondary">ID：{task.id}</span>
      </div>
    </li>
  );
}

export interface TaskListViewProps {
  transport: Transport;
  /** 401 后的出口；页面由 useConnection().disconnect 提供，测试可注入替身。 */
  onReconnect?: () => void;
}

export function TaskListView({transport, onReconnect}: TaskListViewProps) {
  const pollIntervalMs = usePollMode('list');
  const [textFilter, setTextFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<TaskStatus | 'any'>('any');
  const [pendingOnly, setPendingOnly] = useState(false);
  // 两种展示共用同一个查询和筛选结果，偏好仅在当前页面内存中。
  const [viewMode, setViewMode] = useState<ViewMode>('list');

  const query = useInfiniteQuery({
    queryKey: ['tasks', 'list'],
    initialPageParam: null as string | null,
    queryFn: ({pageParam, signal}) => transport.listTasks({limit: TASK_LIST_PAGE_SIZE, cursor: pageParam, signal}),
    getNextPageParam: lastPage => lastPage.nextCursor ?? undefined,
    refetchInterval: pollIntervalMs ?? false,
    refetchIntervalInBackground: true,
  });
  // 宽化为普通 boolean/unknown 局部值再做分支判定，避免 infinite query 判别联合把不可能组合收窄成 never。
  const data = query.data;
  const error: unknown = query.error;
  const isPending: boolean = query.isPending;
  const isError: boolean = query.isError;
  const isRefetching: boolean = query.isRefetching;
  const hasNextPage: boolean = query.hasNextPage;
  const isFetchingNextPage: boolean = query.isFetchingNextPage;
  const isFetchNextPageError: boolean = query.isFetchNextPageError;
  const fetchNextPage = query.fetchNextPage;
  const refetch = query.refetch;

  const items = useMemo(() => {
    const byId = new Map<string, TaskRecord>();
    for (const page of data?.pages ?? []) {
      for (const task of page.items) byId.set(task.id, task);
    }
    return [...byId.values()];
  }, [data]);

  const pendingCount = useMemo(() => items.filter(task => isAwaitingStatus(task.status)).length, [items]);

  const filtered = useMemo(() => {
    const needle = textFilter.trim().toLowerCase();
    return items.filter(task => {
      if (pendingOnly && !isAwaitingStatus(task.status)) return false;
      if (statusFilter !== 'any' && task.status !== statusFilter) return false;
      if (needle !== '' && !task.intent.toLowerCase().includes(needle) && !task.id.toLowerCase().includes(needle)) return false;
      return true;
    });
  }, [items, textFilter, statusFilter, pendingOnly]);

  if (isError && !data && isUnauthorizedError(error)) {
    return (
      <section aria-label="任务列表" className="flex flex-col gap-4 p-6">
        <h1 className="text-[22px] font-semibold leading-[30px]">任务</h1>
        <Alert variant="danger" title="凭据已失效（401）">
          查询层已清理内存 token。请重新连接后继续；未决请求不会被自动重放。
          <div className="mt-3">
            <Button variant="outline" size="sm" onClick={onReconnect}>断开并重新连接</Button>
          </div>
        </Alert>
      </section>
    );
  }

  return (
    <section aria-label="任务列表" className="flex min-w-0 flex-col gap-4 p-4 sm:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-[22px] font-semibold leading-[30px]">任务</h1>
          <p className="mt-1 text-sm text-text-secondary">跟进执行，处理等待，查看交付。</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={() => void refetch()} disabled={isRefetching} aria-label="立即刷新列表">
            <RefreshCw aria-hidden className={isRefetching ? 'animate-spin' : undefined} />
            刷新
          </Button>
          <Link
            to="/tasks/new"
            className="inline-flex h-9 select-none items-center justify-center gap-2 rounded-md bg-accent px-3 text-[13px] font-medium leading-5 text-accent-foreground transition-colors hover:bg-accent/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2"
          >
            <Plus aria-hidden className="h-4 w-4" />
            新建任务
          </Link>
        </div>
      </div>

      <div className="flex flex-wrap items-end gap-3 rounded-md border border-border bg-surface p-3">
        <div className="min-w-0 basis-56 flex-1">
          <Label htmlFor="task-filter-text">筛选已加载任务</Label>
          <div className="relative">
            <Search aria-hidden className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-secondary" />
            <Input
              id="task-filter-text"
              size="sm"
              className="pl-9"
              placeholder="按标题或任务 ID 匹配"
              value={textFilter}
              onChange={event => setTextFilter(event.target.value)}
              aria-describedby="task-filter-scope"
            />
          </div>
        </div>
        <div className="w-44">
          <Label htmlFor="task-filter-status">状态</Label>
          <Select
            id="task-filter-status"
            size="sm"
            value={statusFilter}
            onChange={event => setStatusFilter(event.target.value as TaskStatus | 'any')}
            aria-describedby="task-filter-scope"
          >
            {STATUS_OPTIONS.map(option => (
              <option key={option} value={option}>
                {option === 'any' ? '全部状态' : statusMeta(option).label + '（' + option + '）'}
              </option>
            ))}
          </Select>
        </div>
        <Button
          variant={pendingOnly ? 'default' : 'outline'}
          size="sm"
          aria-pressed={pendingOnly}
          onClick={() => setPendingOnly(value => !value)}
        >
          只看待处理（已加载 {pendingCount} 项）
        </Button>
      </div>
      <p id="task-filter-scope" className="text-xs leading-[18px] text-text-secondary">
        搜索与筛选只作用于已加载的 {items.length} 项任务，不是全局检索；更多任务请使用下方「加载更多」。
      </p>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <p role="status" aria-live="polite" className="text-xs leading-[18px] text-text-secondary">
          {isPending
            ? '正在加载任务列表…'
            : '已加载 ' + items.length + ' 项，其中待处理 ' + pendingCount + ' 项；当前筛选命中 ' + filtered.length + ' 项。'}
        </p>
        <div role="group" aria-label="任务展示方式" className="flex shrink-0 gap-1 rounded-md border border-border bg-surface p-1">
          <Button variant={viewMode === 'list' ? 'secondary' : 'ghost'} className="min-h-11" aria-pressed={viewMode === 'list'} onClick={() => setViewMode('list')}><List aria-hidden />列表</Button>
          <Button variant={viewMode === 'cards' ? 'secondary' : 'ghost'} className="min-h-11" aria-pressed={viewMode === 'cards'} onClick={() => setViewMode('cards')}><LayoutGrid aria-hidden />卡片</Button>
        </div>
      </div>

      {isError && data && !isFetchNextPageError ? (
        <Alert variant="warning" title="自动刷新失败，已保留已加载内容">
          {describeTaskApiError(error)}
          <p>内容可能已陈旧，请刷新后核对最新状态。</p>
          <div className="mt-2">
            <Button variant="outline" size="sm" onClick={() => void refetch()}>重试刷新</Button>
          </div>
        </Alert>
      ) : null}

      {isPending ? <TaskListSkeleton /> : null}

      {isError && !data && !isUnauthorizedError(error) ? (
        <Alert variant="danger" title="任务列表加载失败">
          {describeTaskApiError(error)}
          <div className="mt-2">
            <Button variant="outline" size="sm" onClick={() => void refetch()}>重试</Button>
          </div>
        </Alert>
      ) : null}

      {data && items.length === 0 ? (
        <div className="rounded-md border border-dashed border-border bg-surface p-8 text-center">
          <p className="text-sm leading-[22px] text-text-primary">还没有任务。</p>
          <p className="mt-1 text-sm leading-[22px] text-text-secondary">提交业务需求后，会在这里看到执行状态与待处理项。</p>
          <div className="mt-4">
            <Link
              to="/tasks/new"
              className="inline-flex h-9 select-none items-center justify-center gap-2 rounded-md bg-accent px-3 text-[13px] font-medium leading-5 text-accent-foreground transition-colors hover:bg-accent/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2"
            >
              新建第一个任务
            </Link>
          </div>
        </div>
      ) : null}

      {data && items.length > 0 && filtered.length === 0 ? (
        <Alert variant="info" title="已加载范围内没有匹配项" role="status">
          筛选只作用于已加载的 {items.length} 项；服务端可能还有更多任务。
          <div className="mt-2 flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => { setTextFilter(''); setStatusFilter('any'); setPendingOnly(false); }}>
              清除筛选
            </Button>
            {hasNextPage ? (
              <Button variant="outline" size="sm" onClick={() => void fetchNextPage()} disabled={isFetchingNextPage}>
                加载更多后再看
              </Button>
            ) : null}
          </div>
        </Alert>
      ) : null}

      {filtered.length > 0 ? (
        <ul aria-label="任务条目" data-view={viewMode} className={viewMode === 'cards'
          ? 'grid min-w-0 grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3'
          : 'min-w-0 overflow-hidden rounded-md border border-border bg-surface'}>
          {filtered.map(task => <TaskRow key={task.id} task={task} mode={viewMode} />)}
        </ul>
      ) : null}

      {data && items.length > 0 ? (
        <div className="flex flex-col items-start gap-2">
          {isFetchNextPageError ? (
            <Alert variant="danger" title="加载下一页失败">
              {describeTaskApiError(error)}
              <div className="mt-2">
                <Button variant="outline" size="sm" onClick={() => void fetchNextPage()}>重试加载更多</Button>
              </div>
            </Alert>
          ) : null}
          {hasNextPage ? (
            <Button variant="outline" onClick={() => void fetchNextPage()} loading={isFetchingNextPage} disabled={isFetchingNextPage}>
              加载更多（还有下一页，每页 {TASK_LIST_PAGE_SIZE} 项）
            </Button>
          ) : (
            <p className="text-xs leading-[18px] text-text-secondary">服务端没有更多分页。</p>
          )}
        </div>
      ) : null}
    </section>
  );
}

export function TaskListPage() {
  const {transport, disconnect} = useConnection();
  if (!transport) {
    return (
      <section aria-label="任务列表" className="flex flex-col gap-4 p-6">
        <Alert variant="warning" title="尚未连接服务">请先在连接页完成连接后再查看任务。</Alert>
      </section>
    );
  }
  return <TaskListView transport={transport} onReconnect={disconnect} />;
}
