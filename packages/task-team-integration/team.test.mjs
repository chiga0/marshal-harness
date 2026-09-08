import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {policy, bindPlan} from './scenario.fixture.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
const checkerPath = here('./checker.fixture.mjs');
const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
// Independent oracle deliberately does not share the fixture Agent's computation.
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
async function until(observe, predicate, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {const value = await observe(); if (predicate(value)) return value;
    assert.ok(Date.now() < end, 'bounded HTTP observation timeout: ' + JSON.stringify(value)); await pause(20);}
}
async function fixture(t, mode = 'good') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-team-http-'))), root = path.join(parent, 'data');
  const observed = [], services = [];
  const native = createAcpProvider({id: 'fixture-acp', executable: process.execPath, args: [here('./agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: mode}});
  const provider = {id: native.id, start(input) {const handle = native.start(input); observed.push(handle); return handle;}};
  let verifierStarts = 0;
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: actual => {
      try {assert.deepEqual(actual, expected); return true;} catch {return false;}
    }}], delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json',
      content: encode({files: ['east', 'west'].map(region => ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
  const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan,
    start(input) {verifierStarts++; return command.start(input);}});
  const config = {root, mode: 'create', providers: new Map([[provider.id, provider]]), verification,
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createFileBusiness({parent: executionParent, depot,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout, approvedLayout, observeExecution}),
    supervisorOptions: {intervalMs: 10}};
  t.after(async () => {for (const service of services) await service.shutdown(); for (const handle of observed) await handle.stop(); fs.rmSync(parent, {recursive: true, force: true});});
  async function start(mode) {const service = await startTaskService({...config, mode}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};}
  const running = await start('create');
  return {...running, start, observed, get verifierStarts() {return verifierStarts;}};
}
async function approved(f) {
  const input = await f.client.request('input.create', {idempotencyKey: 'sales-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const body = {intent: '两个地区并行计算净销售额，排除取消订单，保留退款和零额；整套报告必须独立验收。',
    context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await f.client.createTask(body, 'create-task');
  const task = await until(() => f.client.getTask(created.id), item => item.status === 'awaiting-approval');
  const plan = await f.client.request('task.plan', {path: {taskId: task.id}});
  assert.equal(plan.nodes.length, 3); assert.ok(plan.acceptance.some(item => item.includes(policy.id)));
  const request = {expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest};
  const operation = await f.client.approveTask(task.id, request, 'approve-task');
  assert.deepEqual(await f.client.approveTask(task.id, request, 'approve-task'), operation);
  return {task, created, body, operation};
}

test('HTTP team uses real ACP processes, exact FileBusiness chain, independent command and verified download across restart', {timeout: 30000}, async t => {
  const f = await fixture(t), {task, created, body, operation} = await approved(f);
  const done = await until(() => f.client.getTask(task.id), item => ['completed', 'failed', 'intervention'].includes(item.status));
  assert.equal(done.status, 'completed'); assert.equal(f.observed.length, 3); assert.equal(f.verifierStarts, 1);
  const authors = await Promise.all(f.observed.slice(1).map(handle => handle.completion));
  for (const result of authors) assert.equal(result.cleanup.cleaned, true);
  assert.ok(Math.max(...authors.map(item => Date.parse(item.cleanup.started.startedAt))) < Math.min(...authors.map(item => Date.parse(item.cleanup.agentExit.at))), 'actual author process lifetimes overlap');
  const audit = await f.client.request('task.audit', {path: {taskId: task.id}});
  assert.equal(audit.acceptance.status, 'passed'); assert.equal(audit.attempts, 4); assert.equal(audit.usage.source, 'unavailable');
  const downloaded = await Promise.all(done.artifactIds.map(id => f.client.downloadArtifact(id)));
  const delivery = downloaded.find(item => item.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  assert.equal((await f.client.request('operation.get', {path: {operationId: operation.id}})).status, 'succeeded');
  await f.service.shutdown(); const resumed = await f.start('open');
  assert.deepEqual(await resumed.client.createTask(body, 'create-task'), created);
  assert.deepEqual(await resumed.client.getTask(task.id), done);
  assert.deepEqual((await resumed.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.equal(f.observed.length, 3); assert.equal(f.verifierStarts, 1);
});

test('individually valid author JSON with wrong business total fails independent acceptance and publishes no delivery', {timeout: 30000}, async t => {
  const f = await fixture(t, 'corrupt'), {task} = await approved(f);
  const done = await until(() => f.client.getTask(task.id), item => ['completed', 'failed', 'intervention'].includes(item.status));
  assert.equal(done.status, 'failed'); assert.equal(f.verifierStarts, 1); assert.deepEqual(done.artifactIds, []);
  assert.notEqual((await f.client.request('task.audit', {path: {taskId: task.id}})).acceptance.status, 'passed');
});

test('HTTP cancel stops only its real live author processes, prevents verifier and survives restart without replacements', {timeout: 30000}, async t => {
  const f = await fixture(t, 'hang'), {task} = await approved(f);
  await until(() => f.client.request('task.workers', {path: {taskId: task.id}}), value => value.items.filter(worker => worker.role === 'author' && worker.startedAt).length === 2);
  const current = await f.client.getTask(task.id);
  const request = {path: {taskId: task.id}, idempotencyKey: 'cancel-task', body: {expectedRevision: current.revision}};
  const operation = await f.client.request('task.cancel', request);
  const done = await until(() => f.client.getTask(task.id), item => item.status === 'cancelled');
  for (const handle of f.observed) assert.equal((await handle.completion).cleanup.cleaned, true);
  assert.equal(f.verifierStarts, 0); assert.deepEqual(done.artifactIds, []);
  await f.service.shutdown(); const resumed = await f.start('open');
  assert.deepEqual(await resumed.client.request('task.cancel', request), operation);
  assert.equal((await resumed.client.getTask(task.id)).status, 'cancelled');
  assert.equal(f.observed.length, 3); assert.equal(f.verifierStarts, 0);
});
