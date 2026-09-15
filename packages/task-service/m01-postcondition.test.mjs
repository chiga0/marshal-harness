import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {setImmediate as turn} from 'node:timers/promises';
import {createVerificationPort} from '../task-application/application.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {verifyOrders} from '../../scripts/business-postconditions.mjs';
import {startTaskService} from './composition.mjs';

const orders = [
  {region: 'east', status: 'paid', cents: 1200},
  {region: 'east', status: 'paid', cents: 0},
  {region: 'west', status: 'paid', cents: 800},
  {region: 'west', status: 'refunded', cents: -200},
  {region: 'east', status: 'cancelled', cents: 999},
];
const source = Buffer.from(JSON.stringify(orders));
const policy = {id: 'm01-order-postcondition-fixture', version: '1',
  description: '独立按冻结 orders.json 重算订单汇总；不执行外部操作。'};
const proposal = {summary: '生成订单汇总并独立验收', nodes: [
  {id: 'author', role: 'author', goal: '读取 orders.json，生成 orders-summary.json', scope: ['orders.json'], providerId: null},
  {id: 'verify', role: 'verifier', goal: '按原 orders.json 独立重算并核对汇总', scope: ['orders.json', 'orders-summary.json'], providerId: null},
], edges: [{from: 'author', to: 'verify'}], deliverables: ['orders-summary.json'],
  acceptance: ['paid 与 refunded 计入，cancelled 排除；零金额订单仍计数；地区与总计完全一致'], assumptions: []};
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
const expected = {regions: {east: {count: 2, cents: 1200}, west: {count: 2, cents: 600}}, total: {count: 4, cents: 1800}};
const until = async predicate => {
  const deadline = Date.now() + 10000;
  let last;
  while (!(last = await predicate())) {assert.ok(Date.now() < deadline, 'bounded M01 HTTP observation: ' + JSON.stringify(last)); await turn();}
};

function bindPlan({inputArtifacts, proposal: plan}) {
  assert.equal(inputArtifacts.length, 1); assert.deepEqual(plan.nodes.map(node => node.id), ['author', 'verify']);
  const id = inputArtifacts[0].id;
  return {nodeId: 'verify', description: policy.description,
    layouts: [
      {nodeId: 'author', inputs: [{path: 'orders.json', source: {kind: 'input', id}}], allowedPaths: ['orders-summary.json']},
      {nodeId: 'verify', inputs: [{path: 'orders-summary.json', source: {kind: 'upstream', nodeId: 'author', path: 'orders-summary.json'}}], allowedPaths: []},
    ], deliveries: [{nodeId: 'author', path: 'orders-summary.json', targetPath: 'orders-summary.json'}]};
}

function fixture(t, mode = 'good') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-m01-postcondition-')));
  const root = path.join(parent, 'data'), byCwd = new Map(), services = [], executions = [], diagnostics = [];
  let activeDepot = null, verifierStarts = 0;
  const fact = ticket => ({executionId: 'm01-fixture-' + ticket.workerId, startedAt: new Date().toISOString()});
  const provider = {id: 'm01-fixture-provider', start(input) {
    const ticket = byCwd.get(input.cwd); assert.ok(ticket, 'provider receives a bound business directory');
    const started = fact(ticket), completion = deferred(); executions.push({ticket, input, started});
    if (ticket.role === 'planner') completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
      outputText: JSON.stringify(proposal), cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    else {
      const value = structuredClone(expected);
      if (mode === 'wrong-candidate') value.regions.west.cents++;
      fs.writeFileSync(path.join(input.cwd, 'orders-summary.json'), JSON.stringify(value), {flag: 'wx', mode: 0o600});
      completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
        outputText: '候选已写入；不声明业务验收通过。', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    }
    return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
  }};
  const verification = createVerificationPort({id: policy.id, policy, bindPlan,
    start({ticket, prepared}) {
      verifierStarts++;
      const started = fact(ticket), completion = deferred();
      const inputRef = ticket.input.inputArtifacts[0];
      const inputText = activeDepot.get({digest: inputRef.digest, bytes: inputRef.bytes}).toString('utf8');
      const candidateText = fs.readFileSync(path.join(prepared.cwd, 'orders-summary.json'), 'utf8');
      const report = verifyOrders(inputText, candidateText);
      const status = report.status === 'pass' ? 'passed' : 'failed';
      const finish = {type: 'verification', status, cleanup: {started, cleaned: true, scope: 'controlled-fixture'},
        evidence: {name: 'm01-postcondition.json', mediaType: 'application/json', content: encode(report)}};
      if (status === 'passed') finish.delivery = {name: 'orders-summary.json', mediaType: 'application/json', content: Buffer.from(candidateText)};
      completion.resolve(finish);
      return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
    }});
  const config = {root, mode: 'create', providers: new Map([[provider.id, provider]]), verification,
    businessFactory: ports => {
      activeDepot = ports.depot;
      const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        approvedLayout: ticket => ports.approvedLayout(ticket), observeExecution: ports.observeExecution});
      return {...business, async prepare(ticket, context) {
        const prepared = await business.prepare(ticket, context); byCwd.set(prepared.cwd, ticket); return prepared;
      }, release(ticket) {business.release(ticket);}, close() {business.close(); activeDepot = null;}};
    }, supervisorOptions: {intervalMs: 5}, onDiagnostic: value => diagnostics.push(value)};
  const start = async modeValue => {const service = await startTaskService({...config, mode: modeValue}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};};
  t.after(async () => {for (const service of services) await service.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  return {start, executions, diagnostics, get verifierStarts() {return verifierStarts;}, get service() {return services[0];}};
}

async function approve(f, key = 'm01-create') {
  const input = await f.client.request('input.create', {idempotencyKey: key + '-input', body: {
    name: 'orders.json', mediaType: 'application/json', contentBase64: source.toString('base64')}});
  const created = await f.client.createTask({intent: '根据 orders.json 生成订单汇总并独立核对。', context: {inputRefs: [input.id]},
    limits: {timeoutMs: 30000, maxAttempts: 4, maxWorkers: 2}}, key);
  let task;
  const deadline = Date.now() + 10000;
  while (true) {
    task = await f.client.getTask(created.id);
    if (task.status === 'awaiting-approval') break;
    assert.ok(Date.now() < deadline, 'planner did not produce an approval preview: ' + JSON.stringify({task, diagnostics: f.diagnostics}));
    await turn();
  }
  const plan = await f.client.request('task.plan', {path: {taskId: task.id}});
  assert.ok(plan.acceptance.some(value => value.includes(policy.id)));
  await f.client.approveTask(task.id, {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}, key + '-approve');
  return task.id;
}

test('M01真实HTTP/SQLite链路把冻结输入接到独立订单后验并下载单一交付', {timeout: 30000}, async t => {
  const f = fixture(t), {client} = await f.start('create'), taskId = await approve({...f, client});
  await until(async () => ['completed', 'failed', 'intervention'].includes((await client.getTask(taskId)).status));
  const task = await client.getTask(taskId);
  assert.equal(task.status, 'completed'); assert.equal(task.artifactIds.length, 2);
  const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
  const delivery = artifacts.find(item => item.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content.toString()), expected); assert.equal(f.verifierStarts, 1);
  const audit = await client.request('task.audit', {path: {taskId}});
  assert.equal(audit.acceptance.status, 'passed');
});

test('M01独立后验拒绝结构合法但业务总额错误的作者候选', {timeout: 30000}, async t => {
  const f = fixture(t, 'wrong-candidate'), {client} = await f.start('create'), taskId = await approve({...f, client}, 'm01-wrong');
  await until(async () => ['completed', 'failed', 'intervention'].includes((await client.getTask(taskId)).status));
  const task = await client.getTask(taskId);
  assert.equal(task.status, 'failed'); assert.equal(f.verifierStarts, 1);
  const audit = await client.request('task.audit', {path: {taskId}});
  assert.equal(audit.acceptance.status, 'failed');
  const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
  assert.equal(artifacts.some(item => item.artifact.kind === 'delivery'), false);
});
