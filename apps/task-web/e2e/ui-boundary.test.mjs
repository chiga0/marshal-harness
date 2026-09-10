// @vitest-environment node
// UI-1 浏览器接入边界（ADR0098）：唯一公开 127.0.0.1 端口上的精确 Host/Origin、
// /ui/ 冻结清单静态、同源 API 中继、API-only 兼容反例与启动期静态目录门禁。
// 覆盖验收 E26/E27/E29/E32 的服务侧断言；均以真实 main.mjs 子进程为对象。
import {afterAll, beforeAll, describe, expect, test} from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {spawnSync} from 'node:child_process';
import {TaskClient} from '../../../packages/task-client/index.mjs';
import {withoutSQLiteRuntimeNotices} from '../../../packages/task-store/runtime-notices.fixture.mjs';
import {CLI, DIST, FIXTURE_MINIMAL, ensureDist, rawRequest, spawnService, tempParent} from './helpers.mjs';

const CSP_SELF = "'self'";
function expectApiCsp(headers) {
  const csp = headers.get('content-security-policy') ?? '';
  expect(csp).toContain(`default-src ${CSP_SELF}`);
  expect(csp).toContain(`script-src ${CSP_SELF}`);
  expect(csp).toContain(`connect-src ${CSP_SELF}`);
  expect(csp).not.toContain('https://');
  expect(csp).not.toContain('http://');
  expect(csp).toContain("object-src 'none'");
}
async function fetchUi(base, p, options = {}) {
  const response = await fetch(base + p, options);
  return {response, text: await response.text(), headers: response.headers};
}

describe('ui-boundary: real --ui edge over a managed fixture service', () => {
  const parent = tempParent();
  const root = path.join(parent, 'service');
  const service = spawnService({root});
  let address, token, host, port;
  beforeAll(async () => {
    ensureDist();
    ({address, token} = await service.ready);
    ({hostname: host, port} = new URL(address));
  }, 90000);
  afterAll(async () => {
    await service.stop();
    fs.rmSync(parent, {recursive: true, force: true});
    expect(service.stdout + service.stderr).not.toContain(token);
  }, 30000);

  test('E29/E32 正向: /ui/ 与 /ui/index.html 返回同源 HTML,响应头符合 ADR0098', async () => {
    for (const p of ['/ui/', '/ui/index.html']) {
      const {response, text, headers} = await fetchUi(address, p);
      expect(response.status).toBe(200);
      expect(headers.get('content-type')).toBe('text/html; charset=utf-8');
      expect(headers.get('cache-control')).toBe('no-store');
      expect(headers.get('x-content-type-options')).toBe('nosniff');
      expect(headers.get('cross-origin-resource-policy')).toBe('same-origin');
      expectApiCsp(headers);
      expect(headers.get('access-control-allow-origin')).toBeNull();
      expect(headers.get('set-cookie')).toBeNull();
      expect(text).toContain('<div id="root"></div>');
    }
  });

  test('E29 静态清单: content-hash 资产与 dist 逐字节一致且 immutable', async () => {
    const names = fs.readdirSync(path.join(DIST, 'assets')).sort();
    expect(names.length).toBeGreaterThanOrEqual(2);
    for (const name of names) {
      const response = await fetch(address + '/ui/assets/' + name);
      expect(response.status).toBe(200);
      expect(Buffer.from(await response.arrayBuffer()).equals(fs.readFileSync(path.join(DIST, 'assets', name)))).toBe(true);
      expect(response.headers.get('cache-control')).toBe('public, max-age=31536000, immutable');
      expectApiCsp(response.headers);
    }
  });

  test('E32 HEAD 只读: 无正文且头信息与 GET 一致', async () => {
    const get = await fetchUi(address, '/ui/index.html');
    const head = await fetchUi(address, '/ui/index.html', {method: 'HEAD'});
    expect(head.response.status).toBe(200);
    expect(head.text).toBe('');
    expect(head.headers.get('content-length')).toBe(get.headers.get('content-length'));
  });

  test('E32 反向: 遍历/未知/查询串/动态方法一律 404/405,不回显宿主路径', async () => {
    const cases = [
      ['/ui/%2e%2e/%2e%2e/packages/task-api/contract.mjs', 404],
      ['/ui/..%2f..%2fpackages/task-api/contract.mjs', 404],
      ['/ui/../secret', 404],
      ['/ui/%2E%2E%2F', 404],
      ['/ui/assets/../index.html', 404],
      ['/ui/unknown.js', 404],
      ['/ui/index.html?v=1', 404],
      ['/ui//index.html', 404],
    ];
    for (const [p, expected] of cases) {
      const result = await rawRequest({host, port: Number(port), path: p});
      expect(result.status).toBe(404);
      expect(JSON.parse(result.bodyText).code).toBe('not_found');
      expect(result.bodyText).not.toContain(parent);
    }
    for (const method of ['POST', 'PUT', 'DELETE']) {
      const result = await rawRequest({host, port: Number(port), method, path: '/ui/index.html'});
      expect(result.status).toBe(405);
      expect(JSON.parse(result.bodyText).code).toBe('method_not_allowed');
    }
  });

  test('E26 Origin/Host 边界: 只有精确单值同源被放行', async () => {
    const exact = `http://${host}:${port}`;
    const negatives = [
      ['外站 Origin', {origin: 'http://evil.example'}],
      ['相似后缀 Origin', {origin: `http://${host}:${port}.evil.example`}],
      ['不同端口 Origin', {origin: `http://${host}:${Number(port) === 65535 ? 1 : Number(port) + 1}`}],
      ['localhost 而非 127.0.0.1', {origin: `http://localhost:${port}`}],
      ['https scheme', {origin: `https://${host}:${port}`}],
      ['null Origin', {origin: 'null'}],
    ];
    for (const [label, options] of negatives) {
      const result = await rawRequest({host, port: Number(port), path: '/ui/', ...options});
      expect(result.status, label).toBe(403);
      expect(JSON.parse(result.bodyText).code, label).toBe('untrusted_request');
    }
    const doubleOrigin = await rawRequest({host, port: Number(port), path: '/ui/', headers: [['Origin', exact], ['Origin', exact]]});
    expect(doubleOrigin.status).toBe(403);
    const wrongHost = await rawRequest({host, port: Number(port), path: '/health', hostHeader: `127.0.0.1:${Number(port) === 65535 ? 1 : Number(port) + 1}`});
    expect(wrongHost.status).toBe(403);
    const doubleHost = await rawRequest({host, port: Number(port), path: '/health', hostHeader: null,
      headers: [['Host', `${host}:${port}`], ['Host', `${host}:${port}`]]});
    expect(doubleHost.status).toBe(403);
    const positive = await rawRequest({host, port: Number(port), path: '/ui/', origin: exact});
    expect(positive.status).toBe(200);
    // 旧客户端形态（无 Origin）仍然可行：Node fetch 默认不发送 Origin。
    const metadata = await fetchUi(address, '/health');
    expect(metadata.response.status).toBe(200);
  });

  test('E02/E01-HTTP: 同源 POST /v1/tasks 201、无/错 token 401、未认证仍不泄露路由', async () => {
    const exact = `http://${host}:${port}`;
    const client = new TaskClient({baseURL: address, token});
    expect((await client.request('ready.get')).ready).toBe(true);
    const created = await client.request('task.create', {idempotencyKey: randomUUID(),
      body: {intent: 'UI 边界 e2e：最小无模型任务', context: {text: 'fixture prepare 拒绝属预期'}}});
    expect(created.id).toMatch(/^task-/);
    expect(created.revision).toBe(1);
    // 同源浏览器形态（带精确 Origin + Bearer）同权。
    const sameOrigin = await fetch(address + '/v1/tasks/' + created.id, {headers: {Authorization: 'Bearer ' + token, Origin: exact}});
    expect(sameOrigin.status).toBe(200);
    // 幂等键由逻辑动作持有：相同 key/body 返回原回执，不同 body 拒绝冲突。
    const body = JSON.stringify({intent: 'UI 边界 e2e：幂等语义', context: {text: 'x'}});
    const key = randomUUID();
    const first = await client.request('task.create', {idempotencyKey: key, body: JSON.parse(body)});
    const replay = await client.request('task.create', {idempotencyKey: key, body: JSON.parse(body)});
    expect(replay).toEqual(first);
    const conflict = await fetch(address + '/v1/tasks', {method: 'POST',
      headers: {Authorization: 'Bearer ' + token, 'Content-Type': 'application/json', 'Idempotency-Key': key},
      body: JSON.stringify({intent: '不同 body 同 key 必须冲突'})});
    expect(conflict.status).toBe(409);
    expect((await conflict.json()).code).toBe('idempotency_conflict');
    // 无 token / 错 token：401 unauthorized，响应 no-store + CSP；已认证前的 4xx 不泄露路由存在性。
    for (const bad of [{}, {Authorization: 'Bearer ' + '0'.repeat(64)}]) {
      const denied = await fetchUi(address, '/v1/tasks', {headers: bad});
      expect(denied.response.status).toBe(401);
      expect(JSON.parse(denied.text).code).toBe('unauthorized');
      expect(denied.headers.get('cache-control')).toBe('no-store');
      expectApiCsp(denied.headers);
    }
    const unknown = await fetchUi(address, '/definitely-not-a-route', {headers: {Authorization: 'Bearer ' + token}});
    expect(unknown.response.status).toBe(404);
    expect(unknown.text).not.toContain(parent);
  });

  test('E27 token 卫生: 静态制品、错误响应与公告输出均不含 token', async () => {
    expect(service.stdout).not.toContain(token);
    for (const entry of ['index.html', ...fs.readdirSync(path.join(DIST, 'assets')).map(n => 'assets/' + n)]) {
      expect(fs.readFileSync(path.join(DIST, entry)).toString('latin1')).not.toContain(token);
    }
    const denied = await fetchUi(address, '/v1/tasks', {headers: {Authorization: 'Bearer ' + token.replace(/^../, '00')}});
    expect(denied.text).not.toContain(token);
  });
});

describe('ui-boundary: API-only 模式保持原边界（E29 反例）', () => {
  const parent = tempParent();
  const root = path.join(parent, 'service');
  const service = spawnService({root, ui: null});
  let address, token, host, port;
  beforeAll(async () => {
    ({address, token} = await service.ready);
    ({hostname: host, port} = new URL(address));
  }, 90000);
  afterAll(async () => {
    await service.stop();
    fs.rmSync(parent, {recursive: true, force: true});
  }, 30000);

  test('E29/E26: 未启用 --ui 时不托管 /ui/，非空 Origin 仍按原规则拒绝', async () => {
    const listed = await fetchUi(address, '/ui/', {headers: {Authorization: 'Bearer ' + token}});
    expect(listed.response.status).toBe(404);
    const origin = await rawRequest({host, port: Number(port), path: '/health', origin: `http://${host}:${port}`});
    expect(origin.status).toBe(403);
    expect(JSON.parse(origin.bodyText).code).toBe('untrusted_request');
    const client = new TaskClient({baseURL: address, token});
    expect((await client.request('ready.get')).ready).toBe(true);
  });
});

describe('ui-boundary: 启动期静态目录门禁（E32）', () => {
  test('符号链接/缺失 index.html/未知扩展/宽度名 一律拒绝启动，不留数据根', () => {
    const parent = tempParent();
    try {
      for (const [label, mutate] of [
        ['符号链接', dir => fs.symlinkSync(path.join(DIST, 'index.html'), path.join(dir, 'linked.html'))],
        ['缺失 index.html', dir => fs.rmSync(path.join(dir, 'index.html'))],
        ['未知扩展名', dir => fs.writeFileSync(path.join(dir, 'evil.exe'), 'x')],
        ['非法文件名', dir => fs.writeFileSync(path.join(dir, 'bad name.txt'), 'x')],
      ]) {
        const dir = path.join(parent, 'dist-' + label);
        fs.cpSync(DIST, dir, {recursive: true});
        mutate(dir);
        const result = spawnSync(process.execPath, [CLI, '--root', path.join(parent, 'data-' + label), '--mode', 'create',
          '--port', '0', '--config', FIXTURE_MINIMAL, '--ui', dir], {encoding: 'utf8', timeout: 30000});
        expect(result.status, label).toBe(1);
        expect(withoutSQLiteRuntimeNotices(result.stderr), label).toBe('{"code":"service_start_unavailable"}\n');
        expect(fs.existsSync(path.join(parent, 'data-' + label)), label).toBe(false);
      }
    } finally { fs.rmSync(parent, {recursive: true, force: true}); }
  }, 120000);
});
