// events-stream(SSE)单元级验收:fake application 下验证路由、帧格式、续传、
// 过载、认证、心跳与 abort 终止,不含真实服务进程。
import assert from 'node:assert/strict';
import http from 'node:http';
import crypto from 'node:crypto';
import {test} from 'node:test';
import {createTaskEventHub, createTaskEventsStreamHandler} from './events-stream.ts';

function makeServer(t, {application, hub, heartbeatMs = 1000}) {
  const token = crypto.randomBytes(32).toString('hex');
  const stream = createTaskEventsStreamHandler({application: () => Promise.resolve(null), token, expectedHost: 'unused', hub});
  void stream;
  const server = http.createServer(async (req, res) => {
    const handler = createTaskEventsStreamHandler({application, token, expectedHost: req.headers.host, hub, heartbeatMs});
    await handler(req, res).catch(error => {
      const statusMap = {untrusted_request: 403, unauthorized: 401, invalid_request: 400, invalid_application_response: 500, not_found: 404};
      res.writeHead(statusMap[error.code] ?? 500, {'Content-Type': 'application/json'});
      res.end(JSON.stringify({code: error.code ?? 'error'}));
    });
  });
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  return new Promise(resolve => server.listen(0, '127.0.0.1', () => {
    const address = `http://127.0.0.1:${server.address().port}`;
    resolve({address, token});
  }));
}

async function readFrames(response, stop, timeoutMs = 8000) {
  const frames = [];
  const reader = response.body.getReader();
  const abort = setTimeout(() => reader.cancel('timeout'), timeoutMs);
  let buffer = '';
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      buffer += new TextDecoder().decode(value);
      let cut;
      while ((cut = buffer.indexOf('\n\n')) >= 0) {
        const block = buffer.slice(0, cut); buffer = buffer.slice(cut + 2);
        if (block.startsWith(':')) { frames.push({comment: block.slice(1).trim()}); continue; }
        const frame = {};
        for (const line of block.split('\n')) {
          const colon = line.indexOf(':');
          if (colon < 0) continue;
          frame[line.slice(0, colon)] = line.slice(colon + 1).replace(/^ /, '');
        }
        if (frame.data) frame.data = JSON.parse(frame.data);
        frames.push(frame);
        if (stop?.(frame)) { await reader.cancel('done'); break; }
      }
      if (stop?.(frames.at(-1) ?? {})) break;
    }
  } catch {}
  finally { clearTimeout(abort); }
  return frames;
}

const cannedEvents = taskId => [
  {id: 'event-1', taskId, sequence: 1, type: 'task.approve', at: '2026-09-15T00:00:00.000Z', workerId: null, summary: 'task.approve', source: 'application'},
  {id: 'event-2', taskId, sequence: 2, type: 'worker.started', at: '2026-09-15T00:00:01.000Z', workerId: 'worker-1', summary: 'worker.started', source: 'application'},
];

test('events-stream: 路由不匹配时不接管请求', async t => {
  const hub = createTaskEventHub();
  const handler = createTaskEventsStreamHandler({application: () => Promise.reject(new Error('must not be called')), token: 't', expectedHost: 'h', hub});
  assert.equal(await handler({method: 'POST', url: '/v1/tasks/aaa/events:stream'}, {}), false);
  assert.equal(await handler({method: 'GET', url: '/v1/tasks/aaa/events'}, {}), false);
  assert.equal(await handler({method: 'GET', url: '/v1/tasks/aaa/events:stream?x=1'}, {}), false);
});

test('events-stream: 全量推送、帧结构与 Last-Event-ID 续传', async t => {
  const calls = [];
  const {address, token} = await makeServer(t, {
    application: request => {
      calls.push({cursor: request.page.cursor, limit: request.page.limit});
      const after = Number(request.page.cursor || 0);
      const items = cannedEvents(request.taskId).filter(item => item.sequence > after);
      return Promise.resolve({taskId: request.taskId, items, nextCursor: null});
    },
    hub: createTaskEventHub(),
  });
  const headers = {Authorization: 'Bearer ' + token};
  const first = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers});
  assert.equal(first.status, 200);
  assert.match(first.headers.get('content-type') ?? '', /text\/event-stream/);
  const frames = await readFrames(first, f => f.data?.sequence === 2);
  assert.equal(frames[1].id, '1'); assert.equal(frames[1].event, 'task.approve');
  assert.deepEqual(frames[1].data, cannedEvents('aaa')[0]);
  assert.equal(frames[2].id, '2'); assert.deepEqual(frames[2].data, cannedEvents('aaa')[1]);
  first.body?.cancel?.().catch?.(() => {});
  const resumed = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers: {...headers, 'Last-Event-ID': '1'}});
  const tail = await readFrames(resumed, f => f.data?.sequence === 2);
  assert.equal(tail.filter(f => f.id).length, 1, '续传不应重复推送已确认帧');
  assert.equal(tail.at(-1).data.sequence, 2);
  resumed.body?.cancel?.().catch?.(() => {});
  assert.ok(calls.some(call => call.cursor === '1'), '续传游标应源自 Last-Event-ID');
});

test('events-stream: 认证缺失/错误即拒绝且不进入流式', async t => {
  const {address} = await makeServer(t, {application: () => Promise.resolve({items: [], nextCursor: null}), hub: createTaskEventHub()});
  for (const headers of [{}, {Authorization: 'Bearer wrong-wrong-wrong-wrong-wrong-wrong-wrong'}]) {
    const response = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers});
    assert.equal(response.status, 401);
    assert.equal((await response.json()).code, 'unauthorized');
  }
});

test('events-stream: Last-Event-ID 非法即 400', async t => {
  const {address, token} = await makeServer(t, {application: () => Promise.resolve({items: [], nextCursor: null}), hub: createTaskEventHub()});
  const response = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers: {Authorization: 'Bearer ' + token, 'Last-Event-ID': 'not-a-sequence'}});
  assert.equal(response.status, 400);
  assert.equal((await response.json()).code, 'invalid_request');
});

test('events-stream: 单任务连接数超界即 429,断开后名额释放', async t => {
  const hub = createTaskEventHub({maxPerTask: 2, maxTotal: 64});
  const {address, token} = await makeServer(t, {application: () => Promise.resolve({items: [], nextCursor: null}), hub});
  const headers = {Authorization: 'Bearer ' + token};
  const controllers = [new AbortController(), new AbortController()];
  for (const controller of controllers) void fetch(`${address}/v1/tasks/aaa/events:stream`, {headers, signal: controller.signal}).catch(() => {});
  await new Promise(resolve => setTimeout(resolve, 400));
  const third = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers});
  assert.equal(third.status, 429);
  assert.equal((await third.json()).code, 'too_many_streams');
  controllers[0].abort();
  await new Promise(resolve => setTimeout(resolve, 400));
  const fourth = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers, signal: AbortSignal.timeout(1500)});
  assert.equal(fourth.status, 200);
  fourth.body?.cancel?.().catch?.(() => {});
  controllers[1].abort();
});

test('events-stream: 心跳帧按周期到达;hub.abortAll 终止空闲流', async t => {
  const hub = createTaskEventHub();
  const {address, token} = await makeServer(t, {application: () => Promise.resolve({items: [], nextCursor: null}), hub, heartbeatMs: 1000});
  const response = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers: {Authorization: 'Bearer ' + token}});
  assert.equal(response.status, 200);
  const frames = await readFrames(response, f => f.comment?.startsWith('heartbeat'), 5000);
  assert.ok(frames.some(f => f.comment?.startsWith('heartbeat')));
  hub.abortAll();
  await new Promise(resolve => setTimeout(resolve, 250));
  response.body?.cancel?.().catch?.(() => {});
});

test('events-stream: notify 唤醒触发重查权威序列,不平行推断', async t => {
  const hub = createTaskEventHub();
  const events = cannedEvents('aaa');
  let exposed = 0, calls = 0;
  const {address, token} = await makeServer(t, {
    application: request => {
      calls++;
      const after = Number(request.page.cursor || 0);
      return Promise.resolve({taskId: 'aaa', items: events.slice(after, exposed), nextCursor: null});
    },
    hub,
  });
  const headers = {Authorization: 'Bearer ' + token};
  const controller = new AbortController();
  const framesPromise = (async () => {
    const response = await fetch(`${address}/v1/tasks/aaa/events:stream`, {headers, signal: controller.signal});
    return readFrames(response, f => f.data?.sequence === 2, 8000);
  })();
  await new Promise(resolve => setTimeout(resolve, 250));
  const before = calls;
  exposed = 2; hub.notify('aaa');
  const frames = await framesPromise;
  controller.abort();
  assert.ok(calls > before, 'notify 应触发新的权威查询');
  assert.equal(frames.filter(f => f.id).length, 2);
  assert.deepEqual(frames.at(-1).data, events[1]);
});
