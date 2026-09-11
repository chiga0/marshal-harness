// 显式受控测试：原 HTTP/SQLite/ACP 与本地 fixture 发布；不是实际模型或正式业务交付。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {pathToFileURL} from 'node:url';
import {createHash, randomUUID} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {TaskClient} from '../../../packages/task-client/index.mjs';
import {WORKTREE, FIXTURE_LEADER, spawnService} from './helpers.mjs';
import {until, finish} from './browser-fault-guards.mjs';

assert.ok(path.isAbsolute(process.env.PLAYWRIGHT_MODULE ?? ''), '需要显式 Playwright 模块路径');
const {chromium, webkit} = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
const engine = process.env.BROWSER_ENGINE ?? 'chromium';
assert.ok(['chromium', 'webkit'].includes(engine));
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const runtime = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'ui-trace-')));
const reportRoot = path.join(runtime, 'reports'); fs.mkdirSync(reportRoot, {mode: 0o700});
const evidenceParent = path.join(WORKTREE, '.marshal/evidence'); fs.mkdirSync(evidenceParent, {recursive: true});
const output = fs.mkdtempSync(path.join(evidenceParent, 'traceability-' + engine + '-'));
const evidence = {head: execFileSync('git', ['rev-parse', 'HEAD'], {cwd: WORKTREE, encoding: 'utf8'}).trim(), engine, runtime,
  scriptDigest: hash(fs.readFileSync(new URL(import.meta.url))), uiIndexDigest: hash(fs.readFileSync(path.join(WORKTREE, 'apps/task-web/dist/index.html'))),
  scope: '真实浏览器/HTTP/SQLite + 既有确定性ACP和隔离本地fixture发布；不是真实模型、实际sales业务或正式发行', downloads: []};
let service, browser, reader;
try {
  // 既有 publication fixture 的精确、只读本地目标入口；不提供任意路径。
  reader = http.createServer((req, res) => {
    if (req.method !== 'GET' || !/^\/[A-Za-z0-9_-]+\.json$/.test(req.url ?? '')) {res.writeHead(404).end(); return;}
    try {res.writeHead(200, {'Content-Type': 'application/json'}).end(fs.readFileSync(path.join(reportRoot, req.url.slice(1))));}
    catch {res.writeHead(404).end();}
  });
  await new Promise(resolve => reader.listen(0, '127.0.0.1', resolve));
  service = spawnService({root: path.join(runtime, 'data'), config: FIXTURE_LEADER, env: {MARSHAL_LEADER_RECOVERY_FIXTURE: '1',
    MARSHAL_LEADER_RECOVERY_PUBLICATION: '1', MARSHAL_LEADER_RECOVERY_READ_URL: `http://127.0.0.1:${reader.address().port}/`}});
  const {address, token} = await service.ready;
  const client = new TaskClient({baseURL: address, token});
  const request = (operation, options = {}) => client.request(operation, {...options, signal: AbortSignal.timeout(15000)});
  const content = Buffer.from('{"fixture":"仅验证输入引用和下载，不冒充真实sales数据"}');
  const uploaded = await request('input.create', {idempotencyKey: randomUUID(), body: {name: 'fixture-input.json', mediaType: 'application/json', contentBase64: content.toString('base64')}});
  const created = await request('task.create', {idempotencyKey: randomUUID(), body: {intent: '成果追溯受控浏览器验证', context: {text: JSON.stringify({east: 10, west: 20}), inputRefs: [uploaded.id]},
    requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']}}});
  const taskId = created.id, get = () => request('task.get', {path: {taskId}}), leader = () => request('task.leader', {path: {taskId}});
  const waiting = async status => {const task = await until(get, value => [status, 'failed', 'intervention'].includes(value.status)); assert.equal(task.status, status); return task;};
  let task = await waiting('awaiting-answer'), view = await leader();
  await request('task.leader.reply', {path: {taskId, requestId: view.pendingRequest.id}, idempotencyKey: randomUUID(),
    body: {expectedRevision: task.revision, requestDigest: view.pendingRequest.requestDigest, answer: 'north'}});
  task = await waiting('awaiting-approval');
  const plan = await request('task.plan', {path: {taskId}});
  await request('task.approve', {path: {taskId}, idempotencyKey: randomUUID(), body: {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}});
  task = await waiting('awaiting-confirmation'); view = await leader();
  assert.equal(view.pendingRequest.kind, 'publication');
  await request('task.leader.reply', {path: {taskId, requestId: view.pendingRequest.id}, idempotencyKey: randomUUID(),
    body: {expectedRevision: task.revision, requestDigest: view.pendingRequest.requestDigest, decision: 'allow'}});
  task = await waiting('completed'); view = await leader();
  const audit = await request('task.audit', {path: {taskId}});
  assert.ok(audit.prompts.some(prompt => prompt.contextRefs.includes(uploaded.id)));
  assert.equal(view.publication.status, 'succeeded'); assert.equal(view.postverify.status, 'succeeded');
  const ids = [uploaded.id, view.publication.receiptArtifactId, view.postverify.evidenceArtifactId];
  assert.ok(ids.every(id => !task.artifactIds.includes(id)), '原缺口必须真实存在于fixture：引用不在Task.artifactIds');
  browser = await (engine === 'webkit' ? webkit : chromium).launch({headless: true, ...(process.env.BROWSER_EXECUTABLE ? {executablePath: process.env.BROWSER_EXECUTABLE} : {})});
  evidence.browserVersion = browser.version();
  const page = await browser.newPage({viewport: {width: 1440, height: 1000}}); page.setDefaultTimeout(20000);
  await page.goto(address + '/ui/');
  await page.getByLabel('Bearer token', {exact: true}).fill(token); await page.getByRole('button', {name: '连接', exact: true}).press('Enter');
  await page.getByRole('link', {name: '成果追溯受控浏览器验证', exact: true}).click();
  await page.getByRole('link', {name: '成果', exact: true}).click();
  for (const id of ids) {
    const metadata = await request('artifact.get', {path: {artifactId: id}});
    const row = page.locator(`[data-artifact-id="${id}"]`);
    await row.waitFor();
    assert.ok((await row.innerText()).includes(id === uploaded.id ? '执行审计观测输入' : id === view.publication.receiptArtifactId ? '发布回执' : '发布后验证据'));
    const downloadEvent = page.waitForEvent('download');
    await row.getByTestId('download-button').press('Enter');
    const download = await downloadEvent, destination = path.join(output, metadata.name);
    await download.saveAs(destination);
    const downloaded = fs.readFileSync(destination);
    assert.equal(downloaded.length, metadata.bytes); assert.equal(hash(downloaded), metadata.digest);
    if (id === uploaded.id) assert.deepEqual(downloaded, content);
    evidence.downloads.push({id, kind: metadata.kind, taskId: metadata.taskId, name: metadata.name, bytes: downloaded.length, digest: hash(downloaded)});
  }
  await page.getByTestId('observed-inputs').scrollIntoViewIfNeeded();
  await page.screenshot({path: path.join(output, 'inputs.png')});
  await page.getByTestId('artifact-inventory').scrollIntoViewIfNeeded();
  await page.screenshot({path: path.join(output, 'receipts.png')});
  assert.deepEqual(await get(), task, '只读浏览器下载不能改变原Task');
  evidence.taskId = taskId; evidence.result = 'PASS（限定受控追溯场景）';
} catch (error) {evidence.result = 'FAIL'; evidence.failure = {name: error.name, message: String(error.message).split('\n')[0]}; process.exitCode = 1;}
finally {
  const failed = await finish({evidence, closeBrowser: browser ? () => browser.close() : undefined, stopService: service ? () => service.stop() : undefined,
    persist: async value => {
      if (reader) {reader.closeAllConnections(); await new Promise(resolve => reader.close(resolve));}
      fs.writeFileSync(path.join(output, 'evidence.json'), JSON.stringify(value, null, 2) + '\n', {mode: 0o600});
    }});
  if (failed) process.exitCode = 1;
  console.log(JSON.stringify({output, ...evidence}));
}
