// 独立显式执行：真实浏览器/HTTP/SQLite/ACP 受控 Provider；不加载真实模型。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {pathToFileURL} from 'node:url';
import {createHash, randomUUID} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {WORKTREE, ensureDist, spawnService, FIXTURE_LEADER} from './helpers.mjs';
import {bounded, readJSON, until, assertReplay, finish} from './browser-fault-guards.mjs';

const modulePath = process.env.PLAYWRIGHT_MODULE;
assert.ok(modulePath && path.isAbsolute(modulePath), '提供已安装 Playwright 的绝对 PLAYWRIGHT_MODULE');
const {chromium, webkit} = await import(pathToFileURL(modulePath).href);
const engine = process.env.BROWSER_ENGINE ?? 'chromium';
assert.ok(['chromium', 'webkit'].includes(engine));
const parent = path.join(WORKTREE, '.marshal', 'evidence');
fs.mkdirSync(parent, {recursive: true});
const output = fs.mkdtempSync(path.join(parent, `browser-fault-${engine}-`));
// ACP custody socket 使用短路径；深层 worktree 会超过 Darwin Unix socket 路径上限。
const runtime = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'ui-fault-')));
const hash = value => createHash('sha256').update(value).digest('hex');
const evidence = {head: execFileSync('git', ['rev-parse', 'HEAD'], {cwd: WORKTREE, encoding: 'utf8'}).trim(), engine,
  nodeVersion: process.version, scriptSha256: hash(fs.readFileSync(new URL(import.meta.url))),
  guardsSha256: hash(fs.readFileSync(new URL('./browser-fault-guards.mjs', import.meta.url))),
  boundary: '真实浏览器 + HTTP/SQLite + 既有受控 ACP，无真实模型、无业务发布、非目标用户可用性验收', checks: []};
let service, browser, page;
try {
  ensureDist();
  evidence.uiIndexSha256 = hash(fs.readFileSync(path.join(WORKTREE, 'apps/task-web/dist/index.html')));
  evidence.runtime = runtime;
  service = spawnService({root: path.join(runtime, 'data'), config: FIXTURE_LEADER, env: {MARSHAL_LEADER_RECOVERY_FIXTURE: '1'}});
  const {address, token} = await service.ready;
  const api = async (route, body) => {
    return readJSON(address + route, {method: body ? 'POST' : 'GET', headers: {Authorization: 'Bearer ' + token,
      Origin: address, ...(body ? {'Content-Type': 'application/json', 'Idempotency-Key': randomUUID()} : {})}, ...(body ? {body: JSON.stringify(body)} : {})});
  };
  const create = async intent => {
    const task = await api('/v1/tasks', {intent, context: {text: JSON.stringify({east: 10, west: 20})}, requirements: {
      deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']}});
    await until(() => api('/v1/tasks/' + task.id), value => value.status === 'awaiting-answer'); return task;
  };
  const task = await create('浏览器受控问答与批准');
  const cancelTask = await create('浏览器受控取消');
  browser = await (engine === 'webkit' ? webkit : chromium).launch({headless: true,
    ...(process.env.BROWSER_EXECUTABLE ? {executablePath: process.env.BROWSER_EXECUTABLE} : {})});
  evidence.browserVersion = browser.version();
  page = await browser.newPage({viewport: {width: 1440, height: 1000}});
  page.setDefaultTimeout(20000);
  await page.goto(address + '/ui/');
  await page.getByLabel('Bearer token', {exact: true}).fill(token);
  await page.getByRole('button', {name: '连接', exact: true}).press('Enter');
  await page.getByRole('link', {name: task.input?.intent ?? '浏览器受控问答与批准', exact: true}).click();
  // 请求真实到达服务并取得真实响应后，只丢弃首个浏览器响应。绝不伪造受理或业务状态。
  async function lostReceipt(name, suffix, trigger) {
    const observed = [];
    const pattern = '**/v1/tasks/**' + suffix;
    const handler = async route => {
      if (route.request().method() !== 'POST') return route.continue();
      const request = route.request();
      const response = await route.fetch({timeout: 10000});
      const value = await bounded(() => response.json(), 10000);
      observed.push({body: request.postData(), key: request.headers()['idempotency-key'], status: response.status(), id: value.id});
      if (observed.length === 1) await route.abort('failed'); else await route.fulfill({response});
    };
    await page.route(pattern, handler);
    await trigger();
    const pending = page.getByRole('status', {name: '本次连接未决操作'});
    await pending.getByRole('button', {name: '显式原键重放', exact: true}).waitFor();
    await page.getByRole('link', {name: '设置', exact: true}).click();
    await pending.getByRole('button', {name: '显式原键重放', exact: true}).waitFor();
    await page.screenshot({path: path.join(output, name + '-unknown.png')});
    assert.equal(observed.length, 1, '跨路由不得自动重发');
    let stableCancel;
    if (name === 'cancel') {
      await until(() => api('/v1/tasks/' + cancelTask.id), value => value.status === 'cancelled');
      stableCancel = {task: await api('/v1/tasks/' + cancelTask.id), audit: await api('/v1/tasks/' + cancelTask.id + '/audit')};
    }
    await pending.getByRole('button', {name: '显式原键重放', exact: true}).press('Enter');
    await until(async () => observed.length, value => value === 2);
    await pending.waitFor({state: 'hidden'});
    assertReplay(name, observed);
    if (stableCancel) {
      const after = {task: await api('/v1/tasks/' + cancelTask.id), audit: await api('/v1/tasks/' + cancelTask.id + '/audit')};
      assert.deepEqual(after, stableCancel, '取消原键重放不得再改变 Task 或审计');
      evidence.cancelReplaySnapshotSha256 = hash(JSON.stringify(after));
    }
    evidence.checks.push({name, requests: 2, status: observed[0].status, receiptId: observed[0].id,
      bodySha256: hash(observed[0].body), keySha256: hash(observed[0].key), sameBodyKey: true,
      ...(observed[0].id ? {sameOperationId: true} : {}), crossRouteNoAutoReplay: true});
    await page.unroute(pattern, handler);
    await page.getByRole('link', {name: '返回工作台', exact: true}).click();
    await page.getByRole('link', {name: '任务', exact: true}).click();
  }
  await page.getByLabel('答复内容', {exact: true}).fill('华东与华西');
  await lostReceipt('leader-reply', '/reply', async () => {
    await page.getByTestId('leader-answer-open').click();
    await page.getByRole('dialog').getByRole('button', {name: '确认答复', exact: true}).press('Enter');
  });
  await until(() => api('/v1/tasks/' + task.id), value => value.status === 'awaiting-approval');
  await page.getByRole('link', {name: '浏览器受控问答与批准', exact: true}).click();
  await page.getByTestId('plan-approve-open').waitFor();
  await page.screenshot({path: path.join(output, 'plan-preview.png')});
  await lostReceipt('approve', '/plan/approve', async () => {
    await page.getByTestId('plan-approve-open').click();
    await page.getByRole('dialog').getByRole('button', {name: '批准执行', exact: true}).press('Enter');
  });
  await page.getByRole('link', {name: '浏览器受控取消', exact: true}).click();
  // Escape 必须关闭确认且不写入，再重新明确确认。
  await page.getByTestId('control-cancel').click();
  await page.keyboard.press('Escape');
  await page.getByRole('dialog').waitFor({state: 'hidden'});
  assert.equal((await api('/v1/tasks/' + cancelTask.id)).status, 'awaiting-answer');
  await lostReceipt('cancel', '/cancel', async () => {
    await page.getByTestId('control-cancel').click();
    await page.getByRole('dialog').getByRole('button', {name: '取消任务', exact: true}).press('Enter');
  });
  const cancelled = await until(() => api('/v1/tasks/' + cancelTask.id), value => value.status === 'cancelled');
  assert.equal(cancelled.id, cancelTask.id);
  evidence.taskIds = [task.id, cancelTask.id];
  evidence.cancelStatus = cancelled.status;
  evidence.result = 'PASS（限定上述场景）';
} catch (error) {
  evidence.result = 'FAIL';
  // 不输出 Playwright 完整调用日志/请求头/连接字段。
  evidence.failure = {name: error.name, message: String(error.message).split('\n')[0]};
  process.exitCode = 1;
} finally {
  const failed = await finish({evidence, closeBrowser: browser ? () => browser.close() : undefined,
    stopService: service ? () => service.stop() : undefined,
    persist: value => fs.writeFileSync(path.join(output, 'evidence.json'), JSON.stringify(value, null, 2) + '\n', {mode: 0o600})});
  if (failed) process.exitCode = 1;
  console.log(JSON.stringify({output, ...evidence}));
}
