// GET /v1/tasks/{taskId}/events:stream — 任务事件的 SSE 推送传输。
// 不变量(与 docs/extension-contracts.md / 架构文档一致):
// - 与 task.events 轮询接口同一权威:每一帧都来自对 application({operation:'task.events'})
//   的真实调用结果;本模块不保存、不推断任何任务状态,推送没有平行真值。
// - Last-Event-ID 续传(id = 事件 sequence 的十进制串);断档由客户端以轮询接口重对齐,
//   本端点不承诺跨断线零丢失,也不以 GET 产生任何副作用。
// - 认证与 protect() 一致(Authorization: Bearer,一次性 token);不接受 URL/Cookie 凭据。
// - 心跳注释帧保活;单任务与全局并发连接有界;超界即 429 并附 Retry-After。
import {randomUUID} from 'node:crypto';
import {protect} from './http-boundary.ts';
import {TaskApiError, validate} from './contract.ts';

const EVENTS_STREAM_PATH = /^\/v1\/tasks\/([A-Za-z0-9][A-Za-z0-9_-]{0,127})\/events:stream$/;

// 事件对象已经过 application/HTTP 闭集校验;序列化失败不得写出半帧。
function render(item) {
  const raw = JSON.stringify(item);
  if (typeof raw !== 'string' || raw.length > 65536) throw new TaskApiError('invalid_application_response');
  return `id: ${item.sequence}\nevent: ${item.type}\ndata: ${raw}\n\n`;
}

export function createTaskEventHub({maxPerTask = 8, maxTotal = 64} = {}) {
  if (!Number.isSafeInteger(maxPerTask) || maxPerTask < 1 || maxPerTask > 64 ||
      !Number.isSafeInteger(maxTotal) || maxTotal < 1 || maxTotal > 1024) throw new TypeError('invalid-task-event-hub');
  let total = 0;
  const listeners = new Map(), aborts = new Set();
  return {
    /** 事件提交后的提示唤醒。提示仅触发重查,从不携带数据。 */
    notify(taskId) { for (const wake of listeners.get(taskId) ?? []) wake(); },
    acquire(taskId) {
      if ((listeners.get(taskId)?.size ?? 0) >= maxPerTask || total >= maxTotal) return false;
      total++;
      return true;
    },
    release() { total = Math.max(0, total - 1); },
    /** 服务关闭时中止所有在长等待中的连接,不依赖连接侧超时自生自灭。 */
    abortAll() { for (const abort of [...aborts]) abort(); },
    /** 等待任务提示或外部 abort;返回 'notify' | 'abort'。 */
    wait(taskId, signal) {
      let set = listeners.get(taskId);
      if (!set) listeners.set(taskId, set = new Set());
      return new Promise(resolve => {
        const cleanup = () => { set.delete(wake); signal.removeEventListener('abort', abort); aborts.delete(abort); };
        const wake = () => { cleanup(); resolve('notify'); };
        const abort = () => { cleanup(); resolve('abort'); };
        set.add(wake);
        aborts.add(abort);
        signal.addEventListener('abort', abort, {once: true});
      });
    },
  };
}


export function createTaskEventsStreamHandler({application, token, expectedHost, hub, heartbeatMs = 15000, pageLimit = 100}) {
  if (typeof application !== 'function' || !hub || !Number.isSafeInteger(heartbeatMs) || heartbeatMs < 1000 || heartbeatMs > 120000 ||
      !Number.isSafeInteger(pageLimit) || pageLimit < 1 || pageLimit > 100) throw new TypeError('invalid-task-events-stream-composition');
  return async (req, res) => {
    const taskId = req.method === 'GET' && typeof req.url === 'string' && !req.url.includes('?') ? EVENTS_STREAM_PATH.exec(req.url)?.[1] : undefined;
    if (taskId === undefined || taskId === null) return false;
    protect(req, token, expectedHost, true);
    if (!validate(taskId, 'Id')) throw new TaskApiError('invalid_request');
    const lastEventId = req.headers['last-event-id'];
    if (lastEventId !== undefined && (typeof lastEventId !== 'string' || !/^[1-9][0-9]{0,15}$/.test(lastEventId))) throw new TaskApiError('invalid_request');
    if (!hub.acquire(taskId)) {
      res.writeHead(429, {'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'Retry-After': '5'});
      res.end(JSON.stringify({code: 'too_many_streams', requestId: randomUUID()}));
      return true;
    }
    const controller = new AbortController();
    const onClose = () => { controller.abort(); hub.release(); };
    req.once('close', onClose);
    try {
      let after = lastEventId ?? '';
      let sentAny = false;
      const fetchPage = async cursor =>
        application({operation: 'task.events', taskId, page: {cursor, limit: pageLimit}},
          {principal: 'local-operator', requestId: randomUUID(), signal: controller.signal});
      // 首拉在写头之前完成:任务不存在/未授权等错误以原有 JSON 错误面返回,不冒充流。
      let result = await fetchPage(after);
      res.writeHead(200, {'Content-Type': 'text/event-stream; charset=utf-8', 'Cache-Control': 'no-store',
        'Connection': 'keep-alive', 'X-Accel-Buffering': 'no', 'X-Request-Id': randomUUID()});
      res.write(': marshal-task-events-stream\n\n');
      for (;;) {
        if (controller.signal.aborted) break;
        for (const item of result.items) { res.write(render(item)); sentAny = true; after = String(item.sequence); }
        if (result.nextCursor !== null) { result = await fetchPage(result.nextCursor); continue; }
        // 心跳/通知竞争:通知只负责早醒,醒来后重查权威序列。
        const woken = await Promise.race([hub.wait(taskId, controller.signal),
          new Promise(resolve => setTimeout(() => resolve('tick'), heartbeatMs))]);
        if (controller.signal.aborted || woken === 'abort') break;
        if (woken === 'tick') res.write(`: heartbeat ${sentAny ? after : ''}\n\n`);
        result = await fetchPage(after);
      }
    } finally {
      hub.release();
      req.off('close', onClose);
    }
    if (!res.destroyed) res.end();
    return true;
  };
}
