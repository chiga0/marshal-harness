import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {TaskClient} from '../task-client/index.mjs';
import {gracefulApprovalRestart, launchService, waitPhase} from './live-consumer.fixture.mjs';
const here = file => fileURLToPath(new URL(file, import.meta.url));
test('original controlled CLI crosses graceful approval restart then approves the original Task exactly once', {timeout: 60000}, async t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-approval-restart-'))), state = path.join(root, 'data');
  const handles = [], modes = [], diagnostics = [], snapshots = {};
  async function start(mode) {
    modes.push(mode);
    const handle = launchService(process.execPath, [here('../task-service/main.mjs'), '--root', state, '--mode', mode, '--port', '0',
      '--config', here('../task-service/leader-recovery.fixture.mjs')],
    {PATH: path.dirname(process.execPath), MARSHAL_LEADER_RECOVERY_FIXTURE: '1'}, root, diagnostics);
    handles.push(handle); const ready = await handle.ready, connection = JSON.parse(fs.readFileSync(ready.connectionFile));
    return {handle, client: new TaskClient({baseURL: connection.url, token: connection.token})};
  }
  t.after(async () => {for (const handle of handles) await handle.stop(); t.diagnostic('私有无模型重启现场保留：' + root);});
  let {client, handle} = await start('create');
  const body = {intent: '原CLI静止批准前重启，不是活跃故障', context: {text: '{"east":10,"west":20}'},
    requirements: {deliverables: ['原两报告'], acceptance: ['原流水准确']}};
  const created = await client.createTask(body, 'restart-create'), taskId = created.id, deadline = Date.parse(created.deadlineAt);
  let task = await waitPhase(() => client.getTask(taskId), 'awaiting-answer', deadline), view = await client.getLeader(taskId);
  const answer = {path: {taskId, requestId: view.pendingRequest.id}, idempotencyKey: 'restart-answer',
    body: {expectedRevision: task.revision, requestDigest: view.pendingRequest.requestDigest, answer: 'north'}};
  const answerReceipt = await client.request('task.leader.reply', answer);
  task = await waitPhase(() => client.getTask(taskId), 'awaiting-approval', deadline);
  const plan = await client.request('task.plan', {path: {taskId}});
  const restarted = await gracefulApprovalRestart({client, handle, start, taskId, task, plan, answer, answerReceipt,
    save(name, value) {assert.equal(snapshots[name], undefined); snapshots[name] = structuredClone(value);
      fs.writeFileSync(path.join(root, name), JSON.stringify(value), {mode: 0o600, flag: 'wx'});}});
  ({client, handle} = restarted);
  assert.deepEqual(modes, ['create', 'open']); assert.equal(restarted.evidence.passed, true); assert.equal(restarted.evidence.activeFault, false);
  assert.equal(restarted.evidence.attemptsBefore, 2); assert.equal(restarted.evidence.attemptsAfter, 2);
  assert.equal(restarted.evidence.deadlineAt, created.deadlineAt); assert.equal(restarted.evidence.newWorkers, 0);
  assert.equal(snapshots['graceful-approval-restart-after.json'].answerReceipt.replayed, true);
  const approve = {path: {taskId}, idempotencyKey: 'restart-approve',
    body: {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}};
  await client.request('task.approve', approve);
  const done = await waitPhase(() => client.getTask(taskId), 'completed', deadline);
  assert.equal(done.deadlineAt, created.deadlineAt); assert.equal((await client.getAudit(taskId)).attempts, 10);
  assert.equal((await client.getLeader(taskId)).review.verdict, 'accept');
  await handle.stop(); assert.deepEqual(modes, ['create', 'open']);
});
test('nonquiescent checkpoint rejects before stop/open or receipt replay', async () => {
  let stopped = 0, opened = 0;
  const client = {getTask: async () => ({status: 'awaiting-approval'}), getLeader: async () => ({}), getAudit: async () => ({}),
    request: async operation => operation === 'supervisor.get' ? {activeWorkers: 1} : operation === 'task.workers' ? {nextCursor: null} : {}};
  await assert.rejects(gracefulApprovalRestart({client, taskId: 'task-one', handle: {stop: async () => {stopped++;}},
    start: async () => {opened++;}, save() {assert.fail('must not write');}}), /approval_restart_not_quiescent/);
  assert.equal(stopped, 0); assert.equal(opened, 0);
});
