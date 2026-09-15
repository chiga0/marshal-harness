import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {TaskClient} from '../task-client/index.mjs';
import {diagnosticCollector} from '../task-leader-report/live-consumer.fixture.mjs';
const here = file => fileURLToPath(new URL(file, import.meta.url));
const digest = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
test('original CLI exposes only closed managed failure diagnostics to its consumer', {timeout: 40000}, async t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-managed-diagnostic-')));
  const baseline = process.env.MARSHAL_DIAGNOSTIC_BASELINE;
  const child = spawn(process.execPath, [baseline ? path.join(baseline, 'packages/task-service/main.mjs') : here('./main.mjs'), '--root', path.join(root, 'data'), '--mode', 'create', '--port', '0',
    '--config', here('./managed-diagnostic.fixture.mjs')], {cwd: root, env: {PATH: path.dirname(process.execPath), MARSHAL_LEADER_RECOVERY_FIXTURE: '1',
      ...(baseline ? {MARSHAL_DIAGNOSTIC_BASE_CONFIG: path.join(baseline, 'packages/task-service/leader-recovery.fixture.mjs')} : {})}, stdio: ['ignore', 'pipe', 'pipe']});
  let output = '', errors = '', closed = false;
  const evidence = {diagnostics: []}, consume = diagnosticCollector(evidence.diagnostics);
  child.stdout.on('data', value => {output += value;}); child.stderr.on('data', value => {errors += value; consume(value);});
  const exit = new Promise(resolve => child.once('close', (code, signal) => {closed = true; resolve({code, signal});}));
  t.after(async () => {if (!closed) child.kill('SIGTERM'); await exit; t.diagnostic('私有无模型现场保留：' + root);});
  const until = async read => {const end = Date.now() + 15000; for (;;) {const value = await read(); if (value) return value; assert.ok(Date.now() < end, 'bounded'); await pause(30);}};
  await until(() => output.includes('\n'));
  const ready = JSON.parse(output.split('\n')[0]), connection = JSON.parse(fs.readFileSync(ready.connectionFile));
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  const task = await client.createTask({intent: '仅验证原Provider失败诊断', context: {text: '{"east":10,"west":20}'},
    requirements: {deliverables: ['原两报告'], acceptance: ['原流水准确']}}, 'diagnostic-task');
  await until(async () => (await client.getTask(task.id)).status === 'failed');
  child.kill('SIGTERM'); assert.deepEqual(await exit, {code: 0, signal: null});
  const reports = errors.split('\n').filter(line => line.startsWith('{')).map(JSON.parse).filter(item => item.code === 'managed_provider_failure');
  assert.equal(reports.length, 1, 'original provider reason must not disappear');
  assert.equal(reports[0].reason, 'pi_agent_error'); assert.equal(reports[0].stopReason, 'error');
  assert.deepEqual(evidence.diagnostics, reports); assert.equal(evidence.diagnostics[0].authority, false);
  assert.equal(JSON.parse(JSON.stringify(evidence)).diagnostics[0].taskId, task.id);
  assert.doesNotMatch(errors, /PRIVATE_OUTPUT|PRIVATE_EXTRA/);
});
// Baseline replay never emits rejected-output records; only the first test
// carries the original-baseline semantics.
test('parse rejection reaches the original CLI stderr only as a bounded base64 copy', {timeout: 40000, skip: !!process.env.MARSHAL_DIAGNOSTIC_BASELINE}, async t => {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-rejected-output-')));
  const baseline = process.env.MARSHAL_DIAGNOSTIC_BASELINE;
  // Unparseable prose with private markers; >2048 bytes exercises truncation.
  const payload = '独立意见：原选果满足要求 SECRET_PARSE_MARKER\n```json\n{"verdict":"accept"}\n```\n' +
    '补充说明 '.repeat(300) + 'SECRET_TAIL_MARKER';
  const child = spawn(process.execPath, [baseline ? path.join(baseline, 'packages/task-service/main.mjs') : here('./main.mjs'), '--root', path.join(root, 'data'), '--mode', 'create', '--port', '0',
    '--config', here('./managed-diagnostic.fixture.mjs')], {cwd: root, env: {PATH: path.dirname(process.execPath), MARSHAL_LEADER_RECOVERY_FIXTURE: '1',
      MARSHAL_DIAGNOSTIC_PARSE_OUTPUT: payload,
      ...(baseline ? {MARSHAL_DIAGNOSTIC_BASE_CONFIG: path.join(baseline, 'packages/task-service/leader-recovery.fixture.mjs')} : {})}, stdio: ['ignore', 'pipe', 'pipe']});
  let output = '', errors = '', closed = false;
  const evidence = {diagnostics: []}, consume = diagnosticCollector(evidence.diagnostics);
  child.stdout.on('data', value => {output += value;}); child.stderr.on('data', value => {errors += value; consume(value);});
  const exit = new Promise(resolve => child.once('close', (code, signal) => {closed = true; resolve({code, signal});}));
  t.after(async () => {if (!closed) child.kill('SIGTERM'); await exit; t.diagnostic('私有无模型现场保留：' + root);});
  const until = async read => {const end = Date.now() + 15000; for (;;) {const value = await read(); if (value) return value; assert.ok(Date.now() < end, 'bounded'); await pause(30);}};
  await until(() => output.includes('\n'));
  const ready = JSON.parse(output.split('\n')[0]), connection = JSON.parse(fs.readFileSync(ready.connectionFile));
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  const task = await client.createTask({intent: '仅验证原解析拒绝诊断', context: {text: '{"east":10,"west":20}'},
    requirements: {deliverables: ['原两报告'], acceptance: ['原流水准确']}}, 'diagnostic-parse-task');
  await until(async () => (await client.getTask(task.id)).status === 'failed');
  child.kill('SIGTERM'); assert.deepEqual(await exit, {code: 0, signal: null});
  const lines = errors.split('\n').filter(line => line.startsWith('{')).map(JSON.parse);
  const failures = lines.filter(item => item.code === 'managed_provider_failure');
  const rejected = lines.filter(item => item.code === 'managed_provider_rejected_output');
  assert.ok(failures.length >= 1 && failures.length <= 32); assert.ok(rejected.length >= 1 && rejected.length <= 8);
  assert.equal(rejected.length, failures.filter(item => item.stage === 'parse').length);
  for (const item of failures) {
    assert.equal(item.stage, 'parse'); assert.equal(item.parseCode, 'invalid_json'); assert.equal(item.executionType, 'leader');
    assert.equal(item.taskId, task.id); assert.equal(item.authority, false);
  }
  const buffer = Buffer.from(payload, 'utf8'), expected = {code: 'managed_provider_rejected_output', authority: false, taskId: task.id,
    providerId: 'test-agent', executionType: 'leader', encoding: 'utf8-base64', wellformed: true, bytes: buffer.length, digest: digest(buffer),
    truncated: true, head: buffer.subarray(0, 1536).toString('base64'), tail: buffer.subarray(buffer.length - 512).toString('base64')};
  for (const item of rejected) {
    assert.ok(/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(item.workerId));
    const {workerId, ...rest} = item;
    assert.deepEqual(rest, expected);
    assert.ok(Buffer.byteLength(JSON.stringify(item)) <= 4096);
  }
  assert.equal(Buffer.from(rejected[0].head, 'base64').includes(Buffer.from('SECRET_PARSE_MARKER')), true);
  assert.equal(Buffer.from(rejected[0].tail, 'base64').includes(Buffer.from('SECRET_TAIL_MARKER')), true);
  assert.equal(evidence.diagnostics.length, failures.length + rejected.length);
  assert.deepEqual(evidence.diagnostics.filter(item => item.code === 'managed_provider_rejected_output'), rejected);
  assert.doesNotMatch(errors, /SECRET_PARSE_MARKER|SECRET_TAIL_MARKER|PRIVATE_EXTRA/);
});
