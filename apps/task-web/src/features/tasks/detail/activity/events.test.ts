import {describe, expect, it} from 'vitest';
import type {Transport} from '@/lib/transport/types';
import {ACTIVITY_CAP, mergeEventPages, parseEvent, resolveEventsLoader, type EventsPage, type TaskEvent} from './events';

function event(sequence: number, id = `ev-${sequence}`): TaskEvent {
  return {id, taskId: 'task-1', sequence, type: 'worker_progress', at: '2026-09-10T01:00:00.000Z', workerId: null, summary: `条目 ${sequence}`, source: 'application'};
}

function page(items: TaskEvent[], nextCursor: string | null = null): EventsPage {
  return {items, nextCursor, taskId: 'task-1'};
}

describe('事件合并（P11 有界分页/去重）', () => {
  it('按 id 跨页去重，按 sequence 最新在前', () => {
    const merged = mergeEventPages([
      page([event(3), event(2)]),
      page([event(2), event(1)]), // 与上一页重复 ev-2
    ]);
    expect(merged.items.map(item => item.sequence)).toEqual([3, 2, 1]);
    expect(merged.capped).toBe(false);
  });

  it('达到上限ACTIVITY_CAP只保留最新并标记', () => {
    const items = Array.from({length: ACTIVITY_CAP + 5}, (_, index) => event(index + 1));
    const merged = mergeEventPages([page(items)]);
    expect(merged.items).toHaveLength(ACTIVITY_CAP);
    expect(merged.capped).toBe(true);
    expect(merged.items[0]!.sequence).toBe(ACTIVITY_CAP + 5);
    expect(merged.items[ACTIVITY_CAP - 1]!.sequence).toBe(6);
  });

  it('parseEvent 拒绝畸形条目而非伪造', () => {
    expect(parseEvent(null)).toBeNull();
    expect(parseEvent({id: 'x'})).toBeNull();
    expect(parseEvent({id: 'x', type: 't', at: 'a', summary: 's', sequence: 1.5})).toBeNull();
    const ok = parseEvent({id: 'x', type: 't', at: 'a', summary: 's', sequence: 3, workerId: 'w-1', source: 'agent', taskId: 'task-1'});
    expect(ok?.sequence).toBe(3);
    expect(ok?.workerId).toBe('w-1');
  });

  it('resolveEventsLoader：无 getEvents 接口返回 null，有则接线', () => {
    const without = {} as Transport;
    expect(resolveEventsLoader(without)).toBeNull();
    const loader = async () => page([]);
    const withIt = {getEvents: loader} as unknown as Transport;
    expect(resolveEventsLoader(withIt)).not.toBeNull();
  });
});
