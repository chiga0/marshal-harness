import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setImmediate as turn} from 'node:timers/promises';
import {createVerificationPort} from '../task-application/application.ts';
import {createFileBusiness} from '../task-business/index.ts';
import {TaskClient} from '../task-client/index.ts';
import {encode} from '../task-store/store.ts';
import {startTaskService} from './composition.ts';
import {PROFILE, STEPS, runImport, verifyRecoveryState, expectedFiles} from './c01-recovery.fixture.ts';

const workerFixture = fileURLToPath(new URL('./c01-recovery-worker.fixture.ts', import.meta.url));
const rows = [
  {id: 'note-a', body: '离线检索与标签整理', tags: ['需求', '离线']},
  {id: 'note-b', body: '只读原笔记，索引损坏可恢复', tags: ['约束', '恢复']},
  {id: 'note-c', body: '不依赖外部网络和发布服务', tags: ['风险']},
];
const sourceBytes = Buffer.from(JSON.stringify(rows) + '\n');
const proposal = {summary: '执行四步本地导入并独立核验恢复状态', nodes: [
  {id: 'import', role: 'author', goal: '按冻结四步模型导入原始笔记', scope: ['notes.json', 'manifest.json', 'index.json', 'complete.marker'], providerId: null},
  {id: 'verify', role: 'verifier', goal: '独立读取原始笔记和导入状态，核验内容与完成标记', scope: ['manifest.json', 'index.json', 'complete.marker'], providerId: null},
], edges: [{from: 'import', to: 'verify'}], deliverables: ['manifest.json', 'index.json', 'complete.marker'],
  acceptance: ['原始输入不变；四步提交和恢复后索引、完成标记完全一致'], assumptions: []};
const policy = {id: 'c01-real-fs-postcondition-fixture', version: '1', description: '独立读取冻结原始笔记和候选状态，核对真实文件内容、摘要与完成标记。'};
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
const until = async predicate => {const deadline = Date.now() + 10000; let value; while (!(value = await predicate())) {assert.ok(Date.now() < deadline, 'bounded C01 HTTP observation'); await turn();} return value;};

function binding({inputArtifacts, proposal: plan}) {
  assert.equal(inputArtifacts.length, 1); assert.deepEqual(plan.nodes.map(node => node.id), ['import', 'verify']);
  const id = inputArtifacts[0].id;
  return {nodeId: 'verify', description: policy.description,
    layouts: [
      {nodeId: 'import', inputs: [{path: 'notes.json', source: {kind: 'input', id}}], allowedPaths: ['manifest.json', 'index.json', 'complete.marker']},
      {nodeId: 'verify', inputs: ['manifest.json', 'index.json', 'complete.marker'].map(path => ({path, source: {kind: 'upstream', nodeId: 'import', path}})), allowedPaths: []},
    ], deliveries: ['manifest.json', 'index.json', 'complete.marker'].map(path => ({nodeId: 'import', path, targetPath: path}))};
}

async function childRun({root, source, crashAfter = -1, skipMode = 'complete-and-consistent'}) {
  const sourcePath = path.join(path.dirname(root), 'source.json');
  if (!fs.existsSync(sourcePath)) {const fd = fs.openSync(sourcePath, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL, 0o600); try {fs.writeFileSync(fd, sourceBytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}}
  const child = spawn(process.execPath, [workerFixture, '--root', root, '--source', sourcePath, '--crash-after', String(crashAfter), '--skip-mode', skipMode], {stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = ''; child.stdout.on('data', bytes => {stdout += bytes;}); child.stderr.on('data', bytes => {stderr += bytes;});
  const [code, signal] = await new Promise(resolve => child.once('exit', (exitCode, exitSignal) => resolve([exitCode, exitSignal])));
  assert.equal(signal, null, stderr || stdout); assert.equal(stdout.trim().split('\n').length, 1, stderr || stdout);
  return {code, result: JSON.parse(stdout), stderr};
}
function freshRoot(parent) {return fs.realpathSync(fs.mkdtempSync(path.join(parent, 'recovery-')));}

function serviceFixture(t, mode = 'good') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-c01-http-'))), root = path.join(parent, 'data');
  const byCwd = new Map(), services = [], executions = [], diagnostics = []; let activeDepot = null;
  const fact = ticket => ({executionId: 'c01-fixture-' + ticket.workerId, startedAt: new Date().toISOString()});
  const provider = {id: 'c01-fixture-provider', start(input) {
    const ticket = byCwd.get(input.cwd); assert.ok(ticket, 'provider receives a bound business directory');
    const started = fact(ticket), completion = deferred(); executions.push({ticket, cwd: input.cwd});
    if (ticket.role === 'planner') completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn', outputText: JSON.stringify(proposal), cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    else if (ticket.role === 'author') {
      const original = JSON.parse(fs.readFileSync(path.join(input.cwd, 'notes.json'), 'utf8'));
      assert.deepEqual(original, rows);
      if (mode === 'record-exists-error') {
        const bad = expectedFiles(rows); fs.writeFileSync(path.join(input.cwd, 'manifest.json'), bad.manifestBytes, {mode: 0o600});
        fs.writeFileSync(path.join(input.cwd, 'index.json'), encode({profile: PROFILE, rows: []}), {mode: 0o600});
        fs.writeFileSync(path.join(input.cwd, 'complete.marker'), bad.markerBytes, {mode: 0o600});
      } else runImport({root: input.cwd, source: original});
      completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn', outputText: '已执行受控四步导入；作者报告不提供验收权威。', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    }
    return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
  }};
  const verification = createVerificationPort({id: policy.id, policy, bindPlan: binding,
    start({ticket, prepared}) {
      const started = fact(ticket), completion = deferred(), inputRef = ticket.input.inputArtifacts[0];
      const input = activeDepot.get({digest: inputRef.digest, bytes: inputRef.bytes});
      const original = JSON.parse(input.toString('utf8'));
      const report = verifyRecoveryState({root: prepared.cwd, source: original});
      const result = {profile: 'c01-real-fs-postcondition/v1', authority: false, ...report,
        boundary: '仅证明此受控候选目录和冻结输入的文件后验；不证明生产恢复接管或任意业务方案。'};
      const status = report.status === 'pass' ? 'passed' : 'failed';
      const outcome = {type: 'verification', status, cleanup: {started, cleaned: true, scope: 'controlled-fixture'}, evidence: {name: 'c01-recovery-postcondition.json', mediaType: 'application/json', content: encode(result)}};
      if (status === 'passed') outcome.delivery = {name: 'c01-recovery-state.json', mediaType: 'application/json', content: fs.readFileSync(path.join(prepared.cwd, 'manifest.json'))};
      completion.resolve(outcome);
      return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
    }});
  const config = {root, mode, providers: new Map([[provider.id, provider]]), verification, supervisorOptions: {intervalMs: 5}, onDiagnostic: value => diagnostics.push(value), businessFactory: ports => {
    activeDepot = ports.depot;
    const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
      approvedLayout: ticket => ports.approvedLayout(ticket), observeExecution: ports.observeExecution});
    return {...business, async prepare(ticket, context) {const prepared = await business.prepare(ticket, context); byCwd.set(prepared.cwd, ticket); return prepared;}, close() {business.close(); activeDepot = null;}};
  }};
  const start = async requestedMode => {const service = await startTaskService({...config, mode: requestedMode}); services.push(service); const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};};
  t.after(async () => {for (const service of services) await service.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  return {start, executions, diagnostics};
}

async function approve(client, key = 'c01-create') {
  const input = await client.request('input.create', {idempotencyKey: key + '-input', body: {name: 'notes.json', mediaType: 'application/json', contentBase64: sourceBytes.toString('base64')}});
  const created = await client.createTask({intent: '对冻结本地笔记执行可恢复四步导入并独立验收。', context: {inputRefs: [input.id]}, limits: {timeoutMs: 30000, maxAttempts: 4, maxWorkers: 2}}, key);
  let task; await until(async () => {task = await client.getTask(created.id); return task.status === 'awaiting-approval';});
  const plan = await client.request('task.plan', {path: {taskId: task.id}});
  assert.ok(plan.acceptance.some(value => value.includes(policy.id)));
  await client.approveTask(task.id, {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}, key + '-approve');
  return task.id;
}

test('C01真实FS：每个四步边界发生真实子进程崩溃后，重启可恢复且重复执行稳定', {timeout: 60000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-c01-fs-'))); t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  for (let crashAfter = 0; crashAfter <= STEPS.length; crashAfter++) {
    const root = freshRoot(parent), first = await childRun({root, source: rows, crashAfter});
    assert.equal(first.code, 42, `crash boundary ${crashAfter}`); assert.equal(first.result.status, 'crashed'); assert.equal(first.result.step, crashAfter);
    const restarted = await childRun({root, source: rows});
    assert.equal(restarted.code, 0); assert.ok(['completed', 'skipped'].includes(restarted.result.status));
    assert.deepEqual(verifyRecoveryState({root, source: rows}).status, 'pass');
    const replay = await childRun({root, source: rows}); assert.equal(replay.code, 0); assert.equal(replay.result.status, 'skipped');
    assert.deepEqual(fs.readFileSync(path.join(parent, 'source.json')), sourceBytes);
  }
});

test('C01真实FS：record-exists 不跳过未完成记录；完成后可稳定重放，冲突索引 fail closed', {timeout: 60000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-c01-errors-'))); t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  const root = freshRoot(parent), crashed = await childRun({root, source: rows, crashAfter: 1}); assert.equal(crashed.code, 42);
  const rejected = await childRun({root, source: rows, skipMode: 'record-exists'}); assert.equal(rejected.code, 2); assert.equal(rejected.result.code, 'record_exists_incomplete');
  assert.equal((await childRun({root, source: rows})).code, 0); assert.equal((await childRun({root, source: rows, skipMode: 'record-exists'})).result.status, 'skipped');

  const corrupt = freshRoot(parent); assert.equal((await childRun({root: corrupt, source: rows, crashAfter: 2})).code, 42);
  fs.writeFileSync(path.join(corrupt, 'index.json'), Buffer.from(JSON.stringify({profile: PROFILE, rows: [{id: 'forged', body: '错误', tags: []}]} ) + '\n'), {mode: 0o600});
  const before = fs.readFileSync(path.join(corrupt, 'index.json'));
  const conflict = await childRun({root: corrupt, source: rows}); assert.equal(conflict.code, 2); assert.equal(conflict.result.code, 'record_exists_inconsistent'); assert.deepEqual(fs.readFileSync(path.join(corrupt, 'index.json')), before);
});

test('C01现有HTTP/TaskApplication/SQLite/VerificationPort：独立读取Depot原始输入，成功和业务错误均经Decision后验', {timeout: 60000}, async t => {
  const f = serviceFixture(t), first = await f.start('create'), taskId = await approve(first.client);
  await until(async () => ['completed', 'failed', 'intervention'].includes((await first.client.getTask(taskId)).status));
  let task = await first.client.getTask(taskId); assert.equal(task.status, 'completed'); assert.equal(task.artifactIds.length, 2);
  const artifacts = await Promise.all(task.artifactIds.map(id => first.client.downloadArtifact(id))), evidence = artifacts.find(item => item.artifact.name === 'c01-recovery-postcondition.json');
  assert.ok(evidence); const report = JSON.parse(evidence.content.toString()); assert.equal(report.status, 'pass'); assert.equal(report.code, 'recovery_state_exact'); assert.equal(report.authority, false);
  assert.ok(artifacts.some(item => item.artifact.kind === 'delivery')); assert.equal((await first.client.request('task.audit', {path: {taskId}})).acceptance.status, 'passed');
  await first.service.shutdown(); const second = await f.start('open'); task = await second.client.getTask(taskId); assert.equal(task.status, 'completed');
  assert.equal((await second.client.request('task.audit', {path: {taskId}})).acceptance.status, 'passed');

  const bad = serviceFixture(t, 'record-exists-error'), badStart = await bad.start('create'), badTaskId = await approve(badStart.client, 'c01-bad');
  await until(async () => ['completed', 'failed', 'intervention'].includes((await badStart.client.getTask(badTaskId)).status));
  const failed = await badStart.client.getTask(badTaskId); assert.equal(failed.status, 'failed');
  const badArtifacts = await Promise.all(failed.artifactIds.map(id => badStart.client.downloadArtifact(id))); assert.ok(badArtifacts.some(item => item.artifact.kind === 'evidence')); assert.equal(badArtifacts.some(item => item.artifact.kind === 'delivery'), false);
});
