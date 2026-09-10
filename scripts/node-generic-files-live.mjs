// Opt-in maintainer consumer. Calls the real discovered Qwen, not a fixture.
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {spawn} from 'node:child_process';
import {setTimeout as pause} from 'node:timers/promises';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';

const args = process.argv.slice(2);
assert.equal(args.length, 5, 'usage: --execute-real --installation ABS --evidence ABS');
assert.equal(args[0], '--execute-real'); assert.equal(args[1], '--installation'); assert.equal(args[3], '--evidence');
const installation = fs.realpathSync(args[2]), root = args[4];
assert.ok(path.isAbsolute(root)); fs.mkdirSync(root, {mode: 0o700});
const settingsDir = path.join(root, '.marshal-client');
const load = file => import(pathToFileURL(path.join(installation, 'packages', file)).href);
const {run, connectLocal} = await load('task-local/main.mjs');
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const save = (name, value) => fs.writeFileSync(path.join(root, name), JSON.stringify(value, null, 2), {mode: 0o600, flag: 'wx'});
const evidence = {passed: false, model: true, externalEffects: false, installation, tasks: []};
let handle;
async function start() {
  const child = spawn(process.execPath, [path.join(installation, 'packages/task-local/main.mjs'), 'serve', '--settings-dir', settingsDir],
    {stdio: ['ignore', 'pipe', 'pipe']});
  let output = '', errorBytes = 0, ended = false, readyResolve, readyReject;
  const ready = new Promise((resolve, reject) => {readyResolve = resolve; readyReject = reject;});
  const exit = new Promise(resolve => {
    child.once('error', () => readyReject(new Error('launcher_spawn_failed')));
    child.once('close', (code, signal) => {ended = true; resolve({code, signal, errorBytes}); readyReject(new Error('launcher_closed'));});
  });
  child.stdout.on('data', bytes => {
    output += bytes.toString();
    if (Buffer.byteLength(output) > 65536) {child.kill('SIGTERM'); readyReject(new Error('launcher_output_limit')); return;}
    if (output.includes('\n')) try {
      const record = JSON.parse(output.split('\n')[0]);
      if (record.state === 'connected') readyResolve();
    } catch {readyReject(new Error('launcher_invalid_output'));}
  });
  child.stderr.on('data', bytes => {errorBytes += bytes.length; if (errorBytes > 65536) child.kill('SIGTERM');});
  const timer = setTimeout(() => {child.kill('SIGTERM'); readyReject(new Error('launcher_timeout'));}, 35000);
  let stopping;
  handle = {stop: () => stopping ??= (async () => {
    if (!ended) child.kill('SIGTERM');
    const force = setTimeout(() => {if (!ended) child.kill('SIGKILL');}, 15000);
    try {const result = await exit; assert.equal(result.code, 0); assert.equal(result.signal, null); return result;}
    finally {clearTimeout(force);}
  })()};
  try {await ready;} finally {clearTimeout(timer);}
  return connectLocal({settingsDir});
}
async function until(client, taskId, wanted, deadline) {
  let last;
  while (Date.now() < deadline) {
    const task = await client.getTask(taskId);
    if (task.status !== last) {console.log(JSON.stringify({taskId, status: task.status})); last = task.status;}
    if (task.status === wanted) return task;
    if (['failed', 'cancelled', 'intervention', 'awaiting-answer', 'awaiting-confirmation', 'completed'].includes(task.status)) {
      save(taskId + '-unexpected.json', {task, leader: await client.getLeader(taskId), audit: await client.getAudit(taskId)});
      throw new Error('unexpected_task_status_' + task.status);
    }
    await pause(500);
  }
  throw new Error('task_deadline');
}
try {
  await run(['init', '--install-root', installation, '--settings-dir', settingsDir], {home: root, output: value => save('init.json', value)});
  let client = await start();
  assert.ok(await client.request('ready.get'));
  const cases = [
    {intent: '为读书会准备两份互补的中文活动材料', topics: ['主持流程', '讨论问题'],
      detail: '主题是时间管理。主持流程列开场、讨论和总结三个阶段；讨论问题至少包含三个开放式问题。'},
    {intent: '为一个虚构的小型咖啡店编写两份互补的中文运营文件', topics: ['开店清单', '顾客反馈问卷'],
      detail: '开店清单至少列出卫生、设备和备料三类检查；问卷至少包含口味、服务和改善建议三个问题。'},
  ];
  for (const [index, item] of cases.entries()) {
    const body = {intent: item.intent,
      context: {inputRefs: [], text: item.detail + ' 这是只交付文件的合成测试，不执行外部动作、不访问网络、不需要用户追加信息。' +
        '请组织两个互补作者并行，分别负责所述两份材料。每人只输出自己的 result.md。独立审查与文件核验后完成下载交付，不发布。'},
      requirements: {deliverables: item.topics, acceptance: [item.detail, '两份文件分别包含对应中文主题；不声称实际举行活动或执行经营操作。']},
      limits: {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3}};
    const task = await client.createTask(body, 'generic-live-task-' + index);
    const entry = {taskId: task.id, intent: item.intent, startedAt: Date.now()}; evidence.tasks.push(entry);
    const deadline = Date.parse(task.deadlineAt);
    const preview = await until(client, task.id, 'awaiting-approval', deadline);
    const plan = await client.request('task.plan', {path: {taskId: task.id}});
    assert.equal(plan.nodes.filter(node => node.role === 'author').length, 2);
    assert.equal(plan.nodes.filter(node => node.role === 'verifier').length, 1);
    await client.approveTask(task.id, {expectedRevision: preview.revision, planRevision: plan.revision, planDigest: plan.digest}, 'generic-live-approve-' + index);
    const done = await until(client, task.id, 'completed', deadline);
    const leader = await client.getLeader(task.id), audit = await client.getAudit(task.id);
    assert.equal(leader.review.verdict, 'accept'); assert.equal(audit.acceptance.status, 'passed');
    save('task-' + index + '.json', {done, plan, leader, audit});
    const results = [];
    for (const artifactId of done.artifactIds) {
      const downloaded = await client.downloadArtifact(artifactId);
      if (downloaded.artifact.kind !== 'delivery') continue;
      const payload = JSON.parse(downloaded.content);
      if (payload.profile !== 'task-generic-files-delivery/v1') continue;
      assert.equal(payload.taskId, task.id); assert.equal(payload.planDigest, plan.digest); assert.equal(payload.files.length, 2);
      for (const file of payload.files) {
        const bytes = Buffer.from(file.content); assert.equal(bytes.length, file.bytes); assert.equal(hash(bytes), file.digest);
        assert.ok(bytes.length > 0); results.push(file.content);
      }
      save('task-' + index + '-delivery.json', payload);
    }
    assert.equal(results.length, 2);
    for (const topic of item.topics) assert.ok(results.some(text => text.includes(topic)), 'missing topic: ' + topic);
    Object.assign(entry, {completed: true, elapsedMs: Date.now() - entry.startedAt, attempts: audit.attempts, reworkCount: audit.reworkCount});
    const before = {task: done, workers: await client.request('task.workers', {path: {taskId: task.id}, query: {limit: 100}})};
    await handle.stop(); client = await start();
    assert.equal(JSON.stringify(await client.getTask(task.id)), JSON.stringify(before.task));
    assert.equal(JSON.stringify(await client.request('task.workers', {path: {taskId: task.id}, query: {limit: 100}})), JSON.stringify(before.workers));
    entry.coldReplayUnchanged = true;
  }
  evidence.passed = true;
} catch (error) {evidence.error = error.code ?? error.message; process.exitCode = 1;}
finally {
  try {if (handle) evidence.stop = await handle.stop();} catch (error) {evidence.passed = false; evidence.stopError = error.message; process.exitCode = 1;}
  save('evidence.json', evidence); console.log(JSON.stringify(evidence));
}
