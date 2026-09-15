// SSE 服务级集成验收:真实 HTTP/SQLite/Supervisor + ACP 进程下,
// 验证任务事件沿 save→onEvent→hub→SSE 链及时推送,与 task.events 轮询同一权威;
// 推送不依赖客户端轮询,也不推断任何任务状态。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {test} from 'node:test';
import {fixture, until} from './api-stable-behavior.fixture.ts';

test('events-stream: 认证、实时推送与权威序列一致(无客户端轮询也能端到端观察到终态)', {timeout: 40000}, async t => {
  const f = await fixture(t);
  const {url, token} = JSON.parse(fs.readFileSync(f.service.connectionFile));
  const missing = await fetch(`${url}/v1/tasks/aaa/events:stream`);
  assert.equal(missing.status, 401, '缺少凭据的流式访问必须 401');
  const harass = await fetch(`${url}/v1/tasks/aaa/events:stream`, {headers: {Authorization: 'Bearer wrong-wrong-wrong-wrong-wrong-wrong-wrong'}});
  assert.equal(harass.status, 401);

  const original = await f.approve('collect events over stream', 'sse-history');
  const taskId = original.created.id;
  const controller = new AbortController();
  t.after(() => controller.abort());
  const seen = [];
  const ready = (async () => {
    try {
      for await (const frame of f.client.streamTaskEvents(taskId, {signal: controller.signal})) seen.push(frame);
    } catch (error) { if (error?.code !== 'client_aborted') throw error; }
  })();

  // 不主动给流让出任何轮询优势:事件由 Supervisor 周期写入,推送必须自行到达。
  const completed = await until(async () => {const task = await f.client.getTask(taskId); return task.status === 'completed' && task;});
  assert.ok(completed);
  const all = await f.client.request('task.events', {path: {taskId}, query: {limit: 100}});
  const lastSequence = all.items.at(-1).sequence;
  await until(() => seen.length > 0 && seen.some(frame => Number(frame.id) === lastSequence), 3000);
  controller.abort();
  await ready;
  assert.ok(seen.length >= 1, '应至少收到一帧事件');
  assert.deepEqual(seen.map(frame => Number(frame.id)), [...seen.map(frame => Number(frame.id))].sort((a, b) => a - b), '推送序列单调递增');
  assert.ok(seen.every(frame => frame.data.taskId === taskId));
  f.complete();
});
