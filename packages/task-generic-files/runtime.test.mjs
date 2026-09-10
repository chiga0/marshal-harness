import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {setTimeout as pause} from 'node:timers/promises';
import {createGenericConfig, createGenericFileTeamConfig} from './index.mjs';
import {filePermission} from './permission.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {startTaskService} from '../task-service/composition.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
const here = name => fileURLToPath(new URL(name, import.meta.url));
const temp = t => {const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-generic-test-')));
  t.after(() => fs.rmSync(root, {recursive: true, force: true})); return root;};
async function until(read, predicate, label) {
  const deadline = Date.now() + 25000;
  for (;;) {const value = await read(); if (predicate(value)) return value; assert.ok(Date.now() < deadline, label + ': ' + JSON.stringify(value)); await pause(15);}
}
async function serviceFixture(t, mode = '') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-generic-test-'))), root = path.join(parent, 'state'); let service, client;
  const diagnostics = [], executions = [];
  const config = () => {
    const native = createAcpProvider({id: 'controlled-acp', executable: process.execPath, args: [here('agent.fixture.mjs')],
      env: {GENERIC_TEST_MODE: mode}, custodyProfile: {id: 'generic-fixture-native', scope: 'inherited-process-group', eligible: true}});
    return createGenericConfig({provider: {...native, start(input) {executions.push(input.cwd); return native.start(input);}}});
  };
  async function open(mode) {
    service = await startTaskService({root, mode, port: 0, ...config(), supervisorOptions: {intervalMs: 10}, onDiagnostic: item => diagnostics.push(item)});
    const connection = JSON.parse(fs.readFileSync(service.connectionFile)); client = new TaskClient({baseURL: connection.url, token: connection.token});
  }
  t.after(async () => {if (service) await service.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  await open('create');
  return {get client() {return client;}, root, executions, diagnostics,
    async reopen() {await service.shutdown(); service = null; await open('open');}};
}
const body = (intent, text) => ({intent, context: {text, inputRefs: []}, requirements: {deliverables: ['完整文件'], acceptance: ['正文必须包含原意图、节点ID和原上下文']},
  limits: {timeoutMs: 120000, maxAttempts: 24, maxWorkers: 3}});
async function approve(f, task) {
  const ready = await until(() => f.client.getTask(task.id), value => ['awaiting-approval', 'failed', 'intervention'].includes(value.status), 'plan');
  assert.equal(ready.status, 'awaiting-approval', JSON.stringify(f.diagnostics));
  const plan = await f.client.request('task.plan', {path: {taskId: task.id}});
  await f.client.approveTask(task.id, {expectedRevision: ready.revision, planRevision: plan.revision, planDigest: plan.digest}, 'approve-' + task.id);
  return plan;
}
test('same generic HTTP composition handles distinct one/two author intents, real checker and cold same-bytes delivery', {timeout: 90000}, async t => {
  const f = await serviceFixture(t), done = [];
  for (const [intent, context, count] of [['写一份操作说明', 'one original context', 1], ['编写需求摘要和互补评议', 'two unrelated context', 2]]) {
    const task = await f.client.createTask(body(intent, context), 'create-' + count), plan = await approve(f, task);
    assert.equal(plan.nodes.filter(node => node.role === 'author').length, count);
    const final = await until(() => f.client.getTask(task.id), value => ['completed', 'failed', 'intervention'].includes(value.status), 'completion');
    assert.equal(final.status, 'completed', JSON.stringify({diagnostics: f.diagnostics, leader: await f.client.getLeader(task.id)}));
    const leader = await f.client.getLeader(task.id), audit = await f.client.getAudit(task.id);
    assert.equal(leader.review.verdict, 'accept'); assert.equal(audit.acceptance.status, 'passed');
    assert.equal(leader.publication, null); assert.equal(leader.postverify, null);
    const artifacts = await Promise.all(final.artifactIds.map(artifactId => f.client.request('artifact.get', {path: {artifactId}})));
    const delivery = await f.client.downloadArtifact(artifacts.find(item => item.kind === 'delivery').id), parsed = JSON.parse(delivery.content);
    assert.equal(parsed.profile, 'task-generic-files-delivery/v1'); assert.equal(parsed.planDigest, plan.digest); assert.equal(parsed.files.length, count);
    for (const file of parsed.files) {assert.equal(file.content, intent + '\n' + file.nodeId + '\n' + context);
      assert.equal(file.digest, digest(Buffer.from(file.content))); assert.equal(file.bytes, Buffer.byteLength(file.content));}
    done.push({task, delivery});
  }
  const starts = f.executions.length; await f.reopen();
  for (const {task, delivery} of done) {assert.equal((await f.client.getTask(task.id)).status, 'completed');
    assert.deepEqual((await f.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);}
  await pause(100); assert.equal(f.executions.length, starts);
});
for (const mode of ['bad', 'extra']) test('actual ' + mode + ' candidate never completes successfully', {timeout: 45000}, async t => {
  const f = await serviceFixture(t, mode), task = await f.client.createTask(body('原要求', 'one'), 'create'); await approve(f, task);
  const final = await until(() => f.client.getTask(task.id), value => ['completed', 'failed', 'intervention'].includes(value.status), 'negative terminal');
  assert.equal(final.status, 'failed'); const audit = await f.client.getAudit(task.id); assert.notEqual(audit.acceptance.status, 'passed');
  if (mode === 'bad') assert.equal((await f.client.getLeader(task.id)).review.verdict, 'reject');
});
test('cancellation retains original owned cleanup and does not restart after cold open', {timeout: 45000}, async t => {
  const f = await serviceFixture(t, 'wait'), task = await f.client.createTask(body('取消中的工作', 'one'), 'create'); await approve(f, task);
  await until(() => f.executions.length, count => count >= 2, 'author start');
  const current = await f.client.getTask(task.id);
  await f.client.request('task.cancel', {path: {taskId: task.id}, idempotencyKey: 'cancel', body: {expectedRevision: current.revision}});
  await until(() => f.client.getTask(task.id), value => value.status === 'cancelled', 'cancelled');
  const starts = f.executions.length; await f.reopen(); await pause(100);
  assert.equal((await f.client.getTask(task.id)).status, 'cancelled'); assert.equal(f.executions.length, starts);
});
test('Qwen permissions constrain real argument shapes and one-time options to exact owned file layout', t => {
  const root = temp(t); fs.mkdirSync(path.join(root, 'inputs')); fs.writeFileSync(path.join(root, 'inputs', 'one'), 'original');
  const ticket = {role: 'author', input: {fileLayout: {inputs: [{path: 'inputs/one'}], allowedPaths: ['result.md']}}};
  const request = (kind, rawInput) => ({toolCall: {kind, rawInput}, options: [{kind: 'allow_once', optionId: 'proceed_once'}]});
  const allows = q => filePermission(ticket, root, q).outcome.optionId === 'proceed_once';
  assert.ok(allows(request('read', {file_path: 'inputs/one'})));
  assert.ok(allows(request('edit', {file_path: path.join(root, 'result.md'), content: 'actual'})));
  assert.ok(allows(request('edit', {file_path: 'result.md', old_string: '', new_string: 'actual'})));
  for (const q of [request('edit', {file_path: 'inputs/one', content: 'overwrite'}), request('edit', {file_path: '../result.md', content: 'escape'}),
    request('execute', {file_path: 'result.md', command: 'touch result.md'}), request('edit', {file_path: 'result.md', content: 'x', command: 'touch x'}),
    request('other', {file_path: 'result.md', content: 'x'}), request('edit', {file_path: 'result.md', content: '\0'}),
    {toolCall: {kind: 'edit', rawInput: {file_path: 'result.md', content: 'x'}}, options: [{kind: 'allow_always', optionId: 'proceed_once'}]}]) assert.equal(allows(q), false);
  fs.symlinkSync(path.join(root, 'inputs/one'), path.join(root, 'result.md'));
  assert.equal(allows(request('edit', {file_path: 'result.md', content: 'x'})), false);
  assert.equal(filePermission({...ticket, role: 'reviewer'}, root, request('read', {file_path: 'inputs/one'})).outcome.outcome, 'cancelled');
});
test('fixed checker measures bytes independently and rejects extra files and links', t => {
  const root = temp(t); fs.mkdirSync(path.join(root, 'results')); fs.writeFileSync(path.join(root, 'results/a.md'), 'actual');
  const expected = [{nodeId: 'a', path: 'results/a.md', bytes: 9, digest: digest(Buffer.from('different'))}];
  const run = () => spawnSync(process.execPath, [here('checker.mjs')], {cwd: root, env: {}, encoding: 'utf8', timeout: 5000,
    input: encode({profile: 'task-verification-command/v1', nonce: 'nonce', binding: {}, input: {files: expected}}).toString() + '\n'});
  const measured = run(); assert.equal(measured.status, 0); const files = JSON.parse(measured.stdout).assertions[0].actual;
  assert.notDeepEqual(files, expected); assert.equal(files[0].digest, digest(Buffer.from('actual')));
  fs.writeFileSync(path.join(root, 'extra'), 'x'); assert.equal(run().status, 1); fs.unlinkSync(path.join(root, 'extra'));
  fs.unlinkSync(path.join(root, 'results/a.md')); fs.symlinkSync('/etc/hosts', path.join(root, 'results/a.md')); assert.equal(run().status, 1);
});
test('default wrapper builds without starting a model and rejects missing or relative executable', () => {
  assert.throws(() => createGenericFileTeamConfig({executable: 'qwen'}), /generic_files_executable/);
  assert.equal(createGenericFileTeamConfig({executable: '/not-started/qwen'}).providers.get('qwen').id, 'qwen');
});
