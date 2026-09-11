// 活动（P11）：服务端按 sequence 升序、after cursor 向后分页；轮询从当前尾页继续，不能把首页当最新页。
// 断线/请求失败保留已加载内容并显示错误与 requestId；长内容可展开；终态仍可手动刷新。

import {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {Button} from '@/components/ui/button';
import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {Transport} from '@/lib/transport/types';
import {usePollMode} from '@/lib/queries/polling';
import {ErrorNotice} from '../shared/error-notice';
import {formatDateTime, truncateMiddle} from '../shared/format';
import {ACTIVITY_CAP, mergeEventPages, resolveEventsLoader, type EventsLoader, type EventsPage, type TaskEvent} from './events';

const PAGE_SIZE = 50;
const EXPAND_THRESHOLD = 200;

export interface ActivityViewProps {
  taskId: string;
  transport: Transport;
  /** 测试注入；缺省为对 transport 的防御性探测结果。 */
  eventsLoader?: EventsLoader | null;
}

export function ActivityView({taskId, transport, eventsLoader: injectedLoader}: ActivityViewProps) {
  const loader = useMemo(() => injectedLoader !== undefined ? injectedLoader : resolveEventsLoader(transport), [injectedLoader, transport]);
  if (loader === null) {
    return (
      <Card className="space-y-2" data-testid="activity-unavailable">
        <h2 className="text-base font-semibold leading-6">活动</h2>
        <p className="text-sm text-text-secondary">
          事件流需要服务端事件接口（GET /v1/tasks/{'{taskId}'}/events），当前 transport 未提供该查询；不编造事件。
          待 transport 补齐后本视图自动可用。
        </p>
      </Card>
    );
  }
  return <ActivityStream key={taskId} taskId={taskId} loader={loader} />;
}

function ActivityStream({taskId, loader}: {taskId: string; loader: EventsLoader}) {
  const intervalMs = usePollMode('detail');
  const [pages, setPages] = useState<EventsPage[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const inFlight = useRef(false);
  // nextCursor=null 仅表示本次读到末尾；继续重读该尾页可发现稍后新增事件。
  const cursor = useRef<string | null>(null);
  const active = useRef(true);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);

  const loadNewest = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const page = await loader(taskId, {cursor: cursor.current, limit: PAGE_SIZE});
      if (!active.current) return;
      if (page.nextCursor !== null) cursor.current = page.nextCursor;
      // 合并后即裁剪内存，不只裁剪 DOM；重复尾页不会持续累积。
      setPages(previous => [{...page, items: mergeEventPages([...previous, page]).items}]);
      setError(null);
      setLastLoadedAt(new Date().toISOString());
    } catch (cause) {
      if (active.current) setError(cause);
    } finally {
      inFlight.current = false;
    }
  }, [loader, taskId]);

  useEffect(() => {
    void loadNewest();
    if (intervalMs === null) return;
    const timer = setInterval(() => void loadNewest(), intervalMs);
    return () => clearInterval(timer);
  }, [loadNewest, intervalMs]);

  // 「加载更多」和轮询都沿服务端 nextCursor 追赶后续事件。
  const olderCursor = pages.length > 0 ? (pages[pages.length - 1]?.nextCursor ?? null) : null;

  const loadMore = useCallback(async () => {
    if (olderCursor === null || inFlight.current) return;
    setLoadingMore(true);
    try {
      await loadNewest();
    } finally {
      if (active.current) setLoadingMore(false);
    }
  }, [loadNewest, olderCursor]);

  const merged = useMemo(() => mergeEventPages(pages), [pages]);

  return (
    <div className="space-y-3" data-testid="activity-view">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">活动事件</h2>
        <div className="flex items-center gap-2 text-xs text-text-secondary">
          {lastLoadedAt ? <span>最后成功加载：{formatDateTime(lastLoadedAt)}</span> : <span>尚未成功加载</span>}
          <Button size="sm" variant="outline" onClick={() => void loadNewest()}>刷新</Button>
        </div>
      </div>

      {error !== null ? (
        <ErrorNotice
          error={error}
          title="加载事件失败"
          outcomeNote="以下为最后一次成功加载的数据（如有）。断线恢复后可显式刷新；不会自动重放写操作。"
          onRefresh={() => void loadNewest()}
        />
      ) : null}
      {error !== null && pages.length > 0 ? (
        <p className="text-xs text-warning" data-testid="activity-stale">当前展示可能有延迟，非实时流。</p>
      ) : null}

      {merged.items.length === 0 && error === null ? (
        <Card><p className="text-sm text-text-secondary">暂无事件。</p></Card>
      ) : null}

      {merged.items.length > 0 ? (
        <ol className="space-y-1" aria-label="事件列表">
          {merged.items.map(event => (
            <EventRow key={event.id} event={event} />
          ))}
        </ol>
      ) : null}

      <div className="flex flex-wrap items-center gap-3">
        {olderCursor !== null ? (
          <Button size="sm" variant="secondary" onClick={() => void loadMore()} loading={loadingMore} data-testid="activity-load-more">
            加载更多
          </Button>
        ) : null}
        <span className="text-xs text-text-secondary">
          已载入 {merged.items.length} 条{merged.items.length >= ACTIVITY_CAP ? `（最多保留最近 ${ACTIVITY_CAP} 条已取得事件）` : ''}。{olderCursor !== null ? '正在按顺序追赶后续事件，尚未取得全部事件。' : ''}
        </span>
      </div>
    </div>
  );
}

function EventRow({event}: {event: TaskEvent}) {
  const [expanded, setExpanded] = useState(false);
  const long = event.summary.length > EXPAND_THRESHOLD;
  return (
    <li className="rounded-md border border-border bg-surface px-3 py-2" data-testid="activity-event" data-event-id={event.id}>
      <div className="flex flex-wrap items-center gap-2 text-xs text-text-secondary">
        <span className="text-text-primary">{formatDateTime(event.at)}</span>
        <code>#{event.sequence}</code>
        <Badge variant="secondary">{event.source}</Badge>
        <code className="text-text-primary">{event.type}</code>
        {event.workerId ? <span>Worker <code>{truncateMiddle(event.workerId, 6, 4)}</code></span> : null}
        <span className="ml-auto" title={`事件 ID：${event.id}`}>ID <code>{truncateMiddle(event.id, 6, 4)}</code></span>
      </div>
      <p className="mt-1 whitespace-pre-wrap break-words text-sm leading-[22px]">
        {long && !expanded ? `${event.summary.slice(0, EXPAND_THRESHOLD)}…` : event.summary}
      </p>
      {long ? (
        <button
          type="button"
          className="mt-0.5 text-xs text-accent underline-offset-4 hover:underline"
          aria-expanded={expanded}
          onClick={() => setExpanded(value => !value)}
        >
          {expanded ? '收起' : '展开全文'}
        </button>
      ) : null}
    </li>
  );
}
