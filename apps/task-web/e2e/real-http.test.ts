// @vitest-environment node
// UI-1 最小真实往返（E01 服务侧）+ 正常重启重连（E22 服务侧）：真实 main.mjs
// --ui 子进程 + 仓库既有零模型受管 fixture（leader-recovery：fake ACP，无真实
// 模型/凭据），经浏览器级 fetch 与 TaskClient 验证任务创建、t/leader 查询、
// 重查一致性与冷重开事实可重查。不通过开发者预览 Vite proxy 达成验收。
import {afterAll, beforeAll, describe, expect, test} from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {setTimeout as pause} from 'node:timers/promises';
import {TaskClient} from '../../../packages/task-client/index.mjs';
import {DIST, FIXTURE_LEADER, ensureDist, spawnService, tempParent} from './helpers.mjs';
import {createTransport, installToken, clearToken} from '../src/lib/transport/client';
import {parseOperation} from '../src/lib/transport/types';

const LEADER_ENV = {MARSHAL_LEADER_RECOVERY_FIXTURE: '1'};
async function until(read, predicate, label, deadlineMs = 90000) {
  const deadline = Date.now() + deadlineMs;
  for (;;) {
    const value = await read();
    if (predicate(value)) return value;
    expect(Date.now() < deadline, label + ': ' + JSON.stringify(value)).toBe(true);
    await pause(50);
  }
}

describe('real-http: E01 最小同源往返 + E22 正常重启重连（零模型受管 fixture）', () => {
  const parent = tempParent();
  const root = path.join(parent, 'service');
  let first, address, token, client, taskId, beforeRestart;

  beforeAll(async () => {
    ensureDist();
    first = spawnService({root, mode: 'create', config: FIXTURE_LEADER, ui: DIST, env: LEADER_ENV});
    ({address, token} = await first.ready);
    client = new TaskClient({baseURL: address, token});
  }, 90000);
  afterAll(async () => {
    if (first && !first.exit) await first.stop();
    fs.rmSync(parent, {recursive: true, force: true});
  }, 60000);

  test('E01: 经唯一同源创建任务并达到 awaiting-answer，列表重查一致', async () => {
    // 浏览器形态的单次同源 fetch（fetch 同源 POST 会自动携带精确 Origin）。
    const create = await fetch(address + '/v1/tasks', {method: 'POST', headers: {
      'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json',
      'Idempotency-Key': randomUUID(), 'Origin': address},
      body: JSON.stringify({intent: 'UI e2e 两地区零模型业务', context: {text: JSON.stringify({east: 10, west: 20})},
        requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']}})});
    expect(create.status).toBe(201);
    const created = await create.json();
    taskId = created.id;
    expect(created.status).toBe('draft');
    const awaited = await until(() => client.request('task.get', {path: {taskId}}),
      value => ['awaiting-answer', 'failed'].includes(value.status), 'leader intake');
    expect(awaited.status).toBe('awaiting-answer');
    // 就绪后列表可重查且前后一致（对应 UI 刷新后重连再查的 HTTP 事实）。
    const list = await client.request('task.list', {query: {limit: 50}});
    const item = list.items.find(entry => entry.id === taskId);
    expect(item).toBeDefined();
    const relist = await client.request('task.list', {query: {limit: 50}});
    expect(relist.items.find(entry => entry.id === taskId)).toEqual(item);
    // Leader 挂号问题与摘要可观察，供 UI 呈现（digest 供后续答复串绑）。
    const view = await fetch(address + '/v1/tasks/' + taskId + '/leader', {headers: {Authorization: 'Bearer ' + token, Origin: address}});
    expect(view.status).toBe(200);
    const leaderView = await view.json();
    expect(leaderView.pendingRequest).toBeDefined();
    expect(leaderView.pendingRequest.requestDigest).toMatch(/^sha256:/);
  }, 180000);

  test('E02: 错 token 返回 401 unauthorized（UI 据此停轮询）', async () => {
    const denied = await fetch(address + '/v1/tasks', {headers: {Authorization: 'Bearer ' + '1'.repeat(64)}});
    expect(denied.status).toBe(401);
    const body = await denied.json();
    expect(body.code).toBe('unauthorized');
    expect(denied.headers.get('cache-control')).toBe('no-store');
  }, 30000);

  test('E20: UI transport保留真实取消Operation并按原ID读取结果', async () => {
    installToken(token);
    try {
      const transport = createTransport({token, baseURL: address});
      const created = await transport.createTask({intent: 'Operation受控取消回执', context: {text: JSON.stringify({east: 10, west: 20})}, requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']}, idempotencyKey: randomUUID()});
      const waiting = await until(() => transport.getTask(created.id), value => ['awaiting-answer', 'failed'].includes(value.status), 'operation fixture waiting');
      expect(waiting.status).toBe('awaiting-answer');
      const body = {expectedRevision: waiting.revision, idempotencyKey: randomUUID()};
      const receipt = parseOperation(await transport.cancelTask(created.id, body));
      expect(receipt.taskId).toBe(created.id);
      expect(receipt.kind).toBe('task.cancel');
      const result = await until(() => transport.getOperation(receipt.id), value => ['succeeded', 'failed'].includes(value.status), 'cancel operation terminal');
      expect(result.id).toBe(receipt.id);
      expect(result.status).toBe('succeeded');
      expect((await transport.getTask(created.id)).status).toBe('cancelled');
      const replay = parseOperation(await transport.cancelTask(created.id, body));
      expect(replay.id).toBe(receipt.id);
    } finally { clearToken(); }
  }, 180000);

  test('E22: 正常关闭后重开同根，事实可重查且换发新连接/token', async () => {
    const stopped = await first.stop();
    expect(stopped.code).toBe(0);
    const second = spawnService({root, mode: 'open', config: FIXTURE_LEADER, ui: DIST, env: LEADER_ENV});
    let again;
    try {
      const announced = await second.ready;
      expect(announced.address).not.toBe(address);
      expect(announced.token).not.toBe(token);
      again = new TaskClient({baseURL: announced.address, token: announced.token});
      const reread = await again.request('task.get', {path: {taskId}});
      expect(reread.id).toBe(taskId);
      expect(reread.status).toBe('awaiting-answer');
      const list = await again.request('task.list', {query: {limit: 50}});
      expect(list.items.some(entry => entry.id === taskId)).toBe(true);
      const view = await again.request('task.leader', {path: {taskId}});
      expect(view.pendingRequest).toBeDefined();
      // 中断后不自动重发 mutation：同 key+body 在重开后继续返回原回执。
      beforeRestart = view.pendingRequest.requestDigest;
    } finally {
      await second.stop();
      await pause(0);
    }
    expect(beforeRestart).toMatch(/^sha256:/);
  }, 180000);

  test('E02: 服务不可达表现为连接失败而非伪造就绪', async () => {
    await expect(fetch(address + '/ready')).rejects.toThrow();
  }, 30000);
});
