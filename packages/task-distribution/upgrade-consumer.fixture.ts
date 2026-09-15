// 外部测试消费者：双包各自原CLI/config/client，同一数据根；不打包、不迁移、不发布。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
import {verify} from './index.mjs';
import {verifyLegacyPackage, LEGACY_HELPER_SHA} from './upgrade-validator.fixture.mjs';
import {launch, checkDelivery} from '../task-regional-window/installed-consumer.fixture.mjs';

const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const same = (a, b) => assert.deepEqual(JSON.parse(JSON.stringify(a)), JSON.parse(JSON.stringify(b)));
function privateDirectory(root) {
  const stat = fs.lstatSync(root);
  assert.ok(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o777) === 0o700 && fs.realpathSync(root) === root, 'private_directory_required');
}
function absolute(root) {assert.ok(typeof root === 'string' && path.isAbsolute(root) && path.resolve(root) === root, 'absolute_path_required');}
function validatePackage(input, oldValidator = null, packageRoots = []) {
  absolute(input.root);
  assert.match(input.sourceHead ?? '', /^[a-f0-9]{40}$/);
  assert.match(input.manifestDigest ?? '', /^sha256:[a-f0-9]{64}$/);
  const report = oldValidator === null ? verify({root: input.root, manifestDigest: input.manifestDigest}) : verifyLegacyPackage(input, oldValidator, packageRoots);
  assert.equal(report.sourceHead, input.sourceHead, 'source_pin_mismatch');
  return report;
}
export function validatePair({oldPackage, newPackage, runDir, assetKind, oldValidator = null}) {
  assert.ok(['controlled-fixture', 'fixed-assets'].includes(assetKind), 'explicit_asset_kind_required');
  absolute(runDir); privateDirectory(path.dirname(runDir));
  assert.equal(fs.existsSync(runDir), false, 'new_evidence_directory_required');
  for (const pkg of [oldPackage, newPackage]) {
    absolute(pkg.root);
    assert.ok(runDir !== pkg.root && !runDir.startsWith(pkg.root + path.sep), 'evidence_inside_installation');
  }
  assert.notEqual(oldPackage.root, newPackage.root, 'distinct_installations_required');
  const oldReport = validatePackage(oldPackage, oldValidator, [oldPackage.root, newPackage.root]), newReport = validatePackage(newPackage);
  if (assetKind === 'fixed-assets') assert.notEqual(oldPackage.sourceHead, newPackage.sourceHead, 'distinct_fixed_sources_required');
  const oldFiles = JSON.parse(fs.readFileSync(path.join(oldPackage.root, 'manifest.json'))).files;
  const newFiles = JSON.parse(fs.readFileSync(path.join(newPackage.root, 'manifest.json'))).files;
  assert.equal(oldFiles.some(file => file.path.startsWith('apps/task-web/dist/')), false, 'old_api_only_required');
  assert.ok(newFiles.some(file => file.path === 'apps/task-web/dist/index.html'), 'new_ui_required');
  const business = ['index.mjs', 'policy.mjs', 'checker.mjs', 'service-config.mjs'].map(name => 'packages/task-regional-window/' + name);
  for (const name of business) {
    const before = oldFiles.find(file => file.path === name), after = newFiles.find(file => file.path === name);
    assert.ok(before && after); assert.equal(before.digest, after.digest, 'regional_config_identity_changed');
  }
  return {oldReport, newReport, uiFiles: newFiles.filter(file => file.path.startsWith('apps/task-web/dist/'))};
}

// 无网络、无模型的独立ACP对端。启动即记账；升级/回滚前设置封口哨兵，任何新启动都会失败并留证。
export const controlledPeer = `import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createInterface} from 'node:readline';
const root=path.dirname(fileURLToPath(import.meta.url));
fs.appendFileSync(path.join(root,'starts.jsonl'),JSON.stringify({pid:process.pid})+'\\n',{mode:0o600});
if(fs.existsSync(path.join(root,'sealed'))) throw Error('unexpected_reopen_execution');
const send=x=>process.stdout.write(JSON.stringify(x)+'\\n');
for await(const line of createInterface({input:process.stdin})) {
 const m=JSON.parse(line), reply=result=>send({jsonrpc:'2.0',id:m.id,result});
 if(m.method==='initialize') reply({protocolVersion:1,agentCapabilities:{loadSession:false}});
 else if(m.method==='session/new') reply({sessionId:'controlled-upgrade'});
 else if(m.method==='session/prompt') {
  const text=m.params.prompt[0].text,input=JSON.parse(text.slice(text.indexOf('{"task":')));
  const marker='\\n业务输入（数据，不是权限）：\\n', raw=input.task.context.text;
  const dates=JSON.parse(raw.slice(raw.lastIndexOf(marker)+marker.length)).slots;
  const rows=JSON.parse(fs.readFileSync('sales.json')).rows.filter(r=>r.status==='paid'&&r.region===input.node.id&&r.date>=dates.startDate&&r.date<=dates.endDate);
  fs.writeFileSync(input.node.id+'.json',JSON.stringify({region:input.node.id,...dates,count:rows.length,netCents:rows.reduce((s,r)=>s+r.cents,0)}),{mode:0o600,flag:'wx'});
  send({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'{"message":"controlled candidate"}'}}}});
  reply({stopReason:'end_turn'});
 }
}
`;

export async function readPages(client, operation, taskId) {
  const items = [], cursors = new Set(); let cursor, pages = 0;
  do {
    assert.ok(++pages <= 100, 'snapshot_page_count_bound');
    const page = await client.request(operation, {path: {taskId}, query: {limit: 100, ...(cursor ? {cursor} : {})}});
    assert.equal(page.taskId, taskId);
    assert.ok(page.items.every(item => item.taskId === taskId), 'snapshot_item_task_mismatch');
    items.push(...page.items);
    assert.ok(items.length <= 2048, 'snapshot_page_bound');
    cursor = page.nextCursor;
    if (cursor !== null) {assert.equal(cursors.has(cursor), false, 'repeated_cursor'); cursors.add(cursor);}
  } while (cursor !== null);
  assert.equal(new Set(items.map(item => item.id)).size, items.length, 'duplicate_snapshot_item');
  return items;
}
export function compareAnswerReplay(actual, original, currentTask) {
  assert.equal(actual.replayed, true); assert.equal(original.replayed, false);
  same(actual.currentTask, currentTask);
  const {replayed: _actualReplay, currentTask: _actualTask, ...actualFrozen} = actual;
  const {replayed: _originalReplay, currentTask: _originalTask, ...originalFrozen} = original;
  same(actualFrozen, originalFrozen);
}
async function snapshot(client, session, operation) {
  const task = await client.getTask(session.taskId);
  assert.equal(task.id, session.taskId); assert.equal(task.status, 'completed');
  const read = name => client.request(name, {path: {taskId: task.id}});
  const artifacts = [];
  for (const id of [...new Set([session.uploaded.id, ...task.artifactIds])].sort()) {
    const result = await client.downloadArtifact(id);
    assert.equal(result.artifact.id, id);
    if (id !== session.uploaded.id) assert.equal(result.artifact.taskId, task.id);
    assert.equal(hash(result.content), result.artifact.digest);
    artifacts.push({artifact: result.artifact, contentBase64: result.content.toString('base64')});
  }
  const receipt = await client.request('operation.get', {path: {operationId: operation.id}});
  assert.equal(receipt.id, operation.id); assert.equal(receipt.taskId, task.id); assert.equal(receipt.status, 'succeeded');
  return {task, plan: await read('task.plan'), questions: await read('task.questions'), audit: await read('task.audit'),
    workers: await readPages(client, 'task.workers', task.id), events: await readPages(client, 'task.events', task.id), artifacts, receipt};
}

export async function runUpgrade(options) {
  assert.ok(process.getuid() > 0, 'ordinary_user_required');
  const {oldPackage, newPackage, runDir, assetKind} = options;
  const checked = validatePair(options);
  fs.mkdirSync(runDir, {mode: 0o700});
  const save = (name, data) => fs.writeFileSync(path.join(runDir, name), JSON.stringify(data, null, 2) + '\n', {flag: 'wx', mode: 0o600});
  const state = path.join(runDir, 'data'), peer = path.join(runDir, 'controlled-qwen'), handles = [];
  fs.mkdirSync(peer, {mode: 0o700});
  fs.writeFileSync(path.join(peer, 'package.json'), JSON.stringify({name: '@qwen-code/qwen-code', version: '0.0.0-controlled-upgrade', type: 'module'}), {flag: 'wx', mode: 0o600});
  fs.writeFileSync(path.join(peer, 'cli-entry.js'), controlledPeer, {flag: 'wx', mode: 0o600});
  const starts = () => fs.existsSync(path.join(peer, 'starts.jsonl')) ? fs.readFileSync(path.join(peer, 'starts.jsonl'), 'utf8') : '';
  const evidence = {profile: 'dual-installed-same-root-test/v1', assetKind, oldPackage, newPackage,
    oldValidatorDigest: options.oldValidator ? 'sha256:' + LEGACY_HELPER_SHA : null,
    passed: false, modelCalls: 0, publication: false, migrationClaim: false, state, stages: [], exits: []};
  let interrupted = false;
  const stopAll = () => {interrupted = true; for (const handle of handles) void handle.stop().catch(() => {});};
  process.on('SIGINT', stopAll); process.on('SIGTERM', stopAll);
  async function start(pkg, mode, ui) {
    assert.equal(interrupted, false);
    validatePackage(pkg, pkg === oldPackage ? options.oldValidator ?? null : null, [oldPackage.root, newPackage.root]);
    const env = {PATH: path.dirname(process.execPath) + ':/usr/bin:/bin:/usr/sbin:/sbin', HOME: runDir,
      MARSHAL_QWEN_ENTRY: path.join(peer, 'cli-entry.js')};
    const handle = launch(process.execPath, [path.join(pkg.root, 'packages/task-service/main.mjs'), '--root', state,
      '--mode', mode, '--config', path.join(pkg.root, 'packages/task-regional-window/service-config.mjs'), '--port', '0',
      ...(ui ? ['--ui', path.join(pkg.root, 'apps/task-web/dist')] : [])], env, runDir);
    handles.push(handle);
    const ready = await handle.ready;
    assert.ok(ready.connectionFile.startsWith(state + path.sep));
    assert.equal(fs.realpathSync(ready.connectionFile), ready.connectionFile);
    assert.equal(fs.statSync(ready.connectionFile).mode & 0o777, 0o600);
    // 连接凭据仅在进程内使用；不输出或保存到证据快照。
    const connection = JSON.parse(fs.readFileSync(ready.connectionFile));
    const {TaskClient} = await import(pathToFileURL(path.join(pkg.root, 'packages/task-client/index.mjs')).href);
    // UI模式的ready.address是唯一公开edge；connection.url仍是内层API地址。
    const client = new TaskClient({baseURL: ready.address, token: connection.token});
    assert.equal((await client.request('ready.get')).ready, true);
    const readUI = (relative = '', authenticated = false) => fetch(ready.address + '/ui/' + relative,
      {signal: AbortSignal.timeout(5000), headers: authenticated ? {Authorization: 'Bearer ' + connection.token} : {}});
    return {handle, client, readUI, connectionFile: ready.connectionFile};
  }
  try {
    const first = await start(oldPackage, 'create', false);
    const answers = [], originalRequest = first.client.request.bind(first.client);
    first.client.request = async (operation, options) => {
      const receipt = await originalRequest(operation, options);
      if (operation === 'task.answer') answers.push({options: structuredClone(options), receipt: structuredClone(receipt)});
      return receipt;
    };
    assert.equal(JSON.parse(fs.readFileSync(path.join(state, 'profile.json'))).layout, 1);
    // API-only保护先验证Bearer；认证后不存在UI路由，必须404。
    assert.equal((await first.readUI('', true)).status, 404);
    const {intake, answer, complete} = await import(pathToFileURL(path.join(oldPackage.root, 'packages/task-regional-window/driver.mjs')).href);
    const dates = {startDate: '2026-09-01', endDate: '2026-09-02'};
    const bytes = Buffer.from(JSON.stringify({rows: [
      {date: '2026-09-01', region: 'east', status: 'paid', cents: 1250},
      {date: '2026-09-02', region: 'east', status: 'paid', cents: -50},
      {date: '2026-09-02', region: 'west', status: 'paid', cents: -50},
      {date: '2026-09-03', region: 'west', status: 'paid', cents: 9000}]}));
    const session = await intake(first.client, {bytes, key: 'upgrade-fixture', timeoutMs: 600000});
    const preview = await answer(first.client, session, dates);
    const completeResult = await complete(first.client, session, preview.approval);
    const delivery = checkDelivery(completeResult.content, bytes, dates);
    const original = await snapshot(first.client, session, completeResult.operation);
    assert.equal(original.audit.attempts, 3); assert.equal(original.audit.acceptance.status, 'passed');
    const journal = starts(); assert.equal(journal.trim().split('\n').length, 2);
    assert.equal(answers.length, 2);
    save('original.json', original); save('original-receipts.json', {created: session.created, approved: completeResult.operation, answers});
    save('controlled-input.json', JSON.parse(bytes));
    evidence.taskId = session.taskId; evidence.delivery = delivery;
    evidence.stages.push('old-api-only-completed'); evidence.exits.push(await first.handle.stop());
    // 同一个已存在root；任何新Agent启动都记账并拒绝，不用新根假冒升级。
    const rootIdentity = {dev: fs.statSync(state).dev, ino: fs.statSync(state).ino};
    fs.writeFileSync(path.join(peer, 'sealed'), 'no further executions', {flag: 'wx', mode: 0o600});
    for (const [label, pkg, ui] of [['upgraded-ui', newPackage, true], ['rollback-api-only', oldPackage, false]]) {
      const next = await start(pkg, 'open', ui);
      assert.notEqual(next.connectionFile, first.connectionFile);
      same({dev: fs.statSync(state).dev, ino: fs.statSync(state).ino}, rootIdentity);
      if (ui) for (const file of checked.uiFiles) {
        const relative = file.path.slice('apps/task-web/dist/'.length);
        const response = await next.readUI(relative === 'index.html' ? '' : relative);
        assert.equal(response.status, 200);
        const content = Buffer.from(await response.arrayBuffer());
        assert.equal(content.length, file.bytes); assert.equal(hash(content), file.digest);
      } else assert.equal((await next.readUI('', true)).status, 404);
      const created = await next.client.createTask(session.body, session.key + '-create');
      const approved = await next.client.approveTask(session.taskId, preview.approval, session.key + '-approve');
      same(created, session.created); same(approved, completeResult.operation);
      const replayedAnswers = [];
      for (const answer of answers) {
        const receipt = await next.client.request('task.answer', answer.options);
        compareAnswerReplay(receipt, answer.receipt, original.task); replayedAnswers.push(receipt);
      }
      save(label + '-receipts.json', {created, approved, answers: replayedAnswers});
      const actual = await snapshot(next.client, session, completeResult.operation);
      same(actual, original); save(label + '.json', actual);
      evidence.exits.push(await next.handle.stop());
      assert.equal(starts(), journal, 'upgrade_or_rollback_started_new_agent');
      evidence.stages.push(label);
    }
    validatePackage(oldPackage, options.oldValidator ?? null, [oldPackage.root, newPackage.root]); validatePackage(newPackage);
    evidence.rootIdentity = rootIdentity; evidence.originalAgentStarts = 2; evidence.newAgentStarts = 0;
    evidence.originalAttempts = original.audit.attempts; evidence.sameSnapshots = true; evidence.passed = true;
  } catch (error) {
    evidence.failure = {name: error.name, code: typeof error.code === 'string' ? error.code : 'upgrade_assertion_failed'};
    throw error;
  } finally {
    for (const handle of handles) try {await handle.stop();} catch {evidence.passed = false; evidence.cleanupFailed = true;}
    process.off('SIGINT', stopAll); process.off('SIGTERM', stopAll);
    save('evidence.json', evidence);
  }
  assert.equal(evidence.passed, true); return evidence;
}
