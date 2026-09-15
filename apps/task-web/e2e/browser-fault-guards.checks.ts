// 显式 node --test 执行，不引入默认 Vitest/浏览器安装要求。
import {test} from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {assertReplay, finish, readJSON, until} from './browser-fault-guards.mjs';

test('批准/取消拒绝双缺失/空 ID 与非 202，Leader reply 不要求 Operation ID', () => {
  const receipt = {status: 202, body: '{}', key: 'original'};
  for (const name of ['approve', 'cancel']) {
    for (const id of [undefined, '', ' ']) assert.throws(() => assertReplay(name, [{...receipt, id}, {...receipt, id}]));
    assert.throws(() => assertReplay(name, [{...receipt, id: 'operation-1', status: 200}, {...receipt, id: 'operation-1', status: 200}]));
    assertReplay(name, [{...receipt, id: 'operation-1'}, {...receipt, id: 'operation-1'}]);
  }
  assertReplay('leader-reply', [receipt, receipt]);
});

test('浏览器清理失败仍停止服务、写 FAIL 证据；服务/落盘失败不逃逸', async () => {
  const steps = [], evidence = {result: 'PASS'};
  assert.equal(await finish({evidence, closeBrowser: async () => {steps.push('browser'); throw Error();},
    stopService: async () => {steps.push('service'); return {code: 0};},
    persist: async value => {steps.push('persist'); assert.equal(value.result, 'FAIL');}}), true);
  assert.deepEqual(steps, ['browser', 'service', 'persist']);
  const failed = {result: 'PASS'};
  await finish({evidence: failed, stopService: async () => {throw Error();}, persist: async () => {throw Error();}});
  assert.deepEqual(failed.cleanupFailures, ['service.stop', 'evidence.persist']);
});

test('挂起 read 由总墙钟截止终止；挂起 browser close 不阻止 service stop', async () => {
  await assert.rejects(until(() => new Promise(() => {}), () => false, 25), /超时/);
  let stopped = false, persisted = false;
  await finish({evidence: {result: 'PASS'}, timeoutMs: 25, closeBrowser: () => new Promise(() => {}),
    stopService: async () => {stopped = true; return {code: 0};}, persist: async () => {persisted = true;}});
  assert.ok(stopped && persisted);
});

test('真实 HTTP 响应头或响应体挂起均超时', async () => {
  const server = http.createServer((req, res) => {if (req.url === '/body') {res.writeHead(200); res.write('{');}});
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    for (const route of ['/headers', '/body']) await assert.rejects(readJSON(`http://127.0.0.1:${server.address().port}${route}`, {}, 50));
  } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
});
