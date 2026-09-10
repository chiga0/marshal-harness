// 活动事件：字段对齐 packages/task-api/openapi.json 的 Event/Events schema。
// Transport.getEvents 已冻结；本模块保留防御性探测入口（测试可注入 null），缺接口时如实显示「不可用」，不伪造事件流。

import type {Transport} from '@/lib/transport/types';

export interface TaskEvent {
  id: string;
  taskId: string;
  sequence: number;
  type: string;
  at: string;
  workerId: string | null;
  summary: string;
  source: string;
}

export interface EventsPage {
  items: TaskEvent[];
  nextCursor: string | null;
  taskId: string;
}

export type EventsLoader = (taskId: string, options: {cursor: string | null; limit: number}) => Promise<EventsPage>;

/** 冻结 Transport 已声明 getEvents；仍保留探测以便宿主换乘其他 Transport 形态时如实降级。 */
export function resolveEventsLoader(transport: Transport): EventsLoader | null {
  const candidate = (transport as unknown as {getEvents?: unknown}).getEvents;
  if (typeof candidate !== 'function') return null;
  const fn = candidate as (taskId: string, options: {cursor: string | null; limit: number}) => Promise<EventsPage>;
  return (taskId, options) => fn(taskId, options);
}

export function parseEvent(raw: unknown): TaskEvent | null {
  if (!raw || typeof raw !== 'object') return null;
  const value = raw as Partial<TaskEvent>;
  if (typeof value.id !== 'string' || value.id === '') return null;
  if (typeof value.type !== 'string' || typeof value.at !== 'string' || typeof value.summary !== 'string') return null;
  if (typeof value.sequence !== 'number' || !Number.isInteger(value.sequence)) return null;
  return {
    id: value.id,
    taskId: typeof value.taskId === 'string' ? value.taskId : '',
    sequence: value.sequence,
    type: value.type,
    at: value.at,
    workerId: typeof value.workerId === 'string' ? value.workerId : null,
    summary: value.summary,
    source: typeof value.source === 'string' ? value.source : 'unknown',
  };
}

/** 活动列表上限（性能目标：详情已载入事件 ≤500）。 */
export const ACTIVITY_CAP = 500;

export interface MergedEvents {
  items: TaskEvent[];
  capped: boolean;
}

/** 跨分页合并：按 id 去重（首次出现保留），按 sequence 最新在前排序，超出上限保留最新并标记。 */
export function mergeEventPages(pages: readonly EventsPage[]): MergedEvents {
  const byId = new Map<string, TaskEvent>();
  for (const page of pages) {
    for (const raw of page.items) {
      const event = parseEvent(raw);
      if (event !== null && !byId.has(event.id)) byId.set(event.id, event);
    }
  }
  const items = [...byId.values()].sort((a, b) => b.sequence - a.sequence);
  const capped = items.length > ACTIVITY_CAP;
  return {items: capped ? items.slice(0, ACTIVITY_CAP) : items, capped};
}

/** 单页去重移除已见 id（用于加载更多时跳过重复页内容，E21）。 */
export function filterKnownIds(page: EventsPage, known: ReadonlySet<string>): EventsPage {
  return {...page, items: page.items.filter(item => !known.has(item.id))};
}
