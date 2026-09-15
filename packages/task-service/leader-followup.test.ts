import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from './composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
const hash = value => digest(encode(value)), here = value => fileURLToPath(new URL(value, import.meta.url));
async function until(read, predicate, label) {const deadline = Date.now() + 45000; for (;;) {const value = await read();
  if (predicate(value)) return value; assert.ok(Date.now() < deadline, label + ': ' + JSON.stringify(value)); await pause(20);}}
const terminal = value => ['completed', 'failed', 'cancelled', 'intervention'].includes(value.status);
const expected = ticket => ['east', 'west'].map(nodeId => ({nodeId, region: ticket.input.leaderReplies[0].answer, value: JSON.parse(ticket.input.task.context.text)[nodeId]}));
function binding() {return {nodeId: 'verify', description: '固定独立核对两分支原值与用户已答地区',
  layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};}
async function fixture(t, mode) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-followup-'))), root = path.join(parent, 'service');
  let service; const observations = [], diagnostics = [], verifications = [];
  // Preserve owned temporary facts on failure; never inspect private model state.
  t.diagnostic('synthetic fixture root: ' + parent);
  t.after(async () => {if (service) {const result = await service.shutdown(); t.diagnostic('shutdownClean=' + result.shutdownClean);}});
  const reviewPolicy = {id: 'review', version: '1', description: '按原需求读取实际两分支材料，不接受作者pass'},
    policy = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 4, maxRequests: 3,
      repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: 'test-agent', policyDigest: hash(reviewPolicy)}, publication: null};
  const leader = createLeaderPort({id: 'leader', providerId: 'test-agent', policy, parseDecision: parseManagedOutput,
    prepare: ({input}) => ({prompt: renderLeaderPrompt(input)})});
  const review = createReviewPort({id: 'review', providerId: 'test-agent', policy: reviewPolicy, parseReport: parseManagedOutput,
    prepare: ({input}) => ({prompt: renderReviewPrompt(input)})});
  const native = createAcpProvider({id: 'test-agent', executable: process.execPath, args: [here('./leader-followup.fixture.mjs'), mode], env: {},
    custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
  const provider = {...native, custodyProfile: native.custodyProfile, start(options) {
    const managed = options.prompt.includes('\n完整冻结输入：'), input = managed ? JSON.parse(options.prompt.split('\n完整冻结输入：').at(-1)) :
      JSON.parse(options.prompt.slice(options.prompt.indexOf('{"task":')));
    const item = {type: managed ? input.profile : 'author', input, started: null, result: null}; observations.push(item);
    const handle = native.start(options);
    return {stop: (...args) => handle.stop(...args), started: handle.started.then(value => {item.started = value; return value;}),
      completion: handle.completion.then(value => {item.result = value; return value;})};
  }};
  const verifyPolicy = {id: 'two-requirements', version: '1', description: '独立比较本Task原业务值及用户回复'}, checkerPath = here('./leader-checker.fixture.mjs');
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: hash(verifyPolicy),
    assertions: [{name: 'both-original-requirements', validate: (actual, {ticket}) => hash(actual) === hash(expected(ticket))}],
    delivery: ({prepared}) => ({name: 'report.json', mediaType: 'application/json', content: encode(['east', 'west'].map(id => JSON.parse(fs.readFileSync(path.join(prepared.cwd, id + '.json')))))})});
  const start = options => {verifications.push(structuredClone(options.ticket)); return command.start(options);};
  start.custodyProfile = command.start.custodyProfile;
  const verification = createVerificationPort({id: 'verify', policy: verifyPolicy, bindPlan: binding, start});
  const config = {root, providers: new Map([[provider.id, provider]]), custody: {profile: 'node-execution-custody/v1'}, leader, review, verification,
    applicationOptions: {defaultLimits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}},
    supervisorOptions: {intervalMs: 10}, onDiagnostic: value => diagnostics.push(value),
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout})};
  let client;
  const connect = async mode => {service = await startTaskService({...config, mode}); const c = JSON.parse(fs.readFileSync(service.connectionFile));
    client = new TaskClient({baseURL: c.url, token: c.token});};
  await connect('create');
  return {get client() {return client;}, observations, diagnostics, verifications,
    get: taskId => client.getTask(taskId), view: taskId => client.getLeader(taskId),
    workers: taskId => client.request('task.workers', {path: {taskId}, query: {limit: 100}}),
    async prepareTask() {
      const task = await client.createTask({intent: '完整保留两分支业务值，用户地区必须明确', context: {text: JSON.stringify({east: 10, west: 20})},
        requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['east值严格等于原输入', 'west值严格等于原输入']}}, 'create-' + mode);
      let current = await until(() => client.getTask(task.id), value => value.status === 'awaiting-answer' || terminal(value), 'intake');
      assert.equal(current.status, 'awaiting-answer', JSON.stringify(diagnostics)); const view = await client.getLeader(task.id);
      const reply = {path: {taskId: task.id, requestId: view.pendingRequest.id}, idempotencyKey: 'answer-' + mode,
        body: {expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest, answer: 'north'}};
      const receipt = await client.request('task.leader.reply', reply);
      current = await until(() => client.getTask(task.id), value => value.status === 'awaiting-approval' || terminal(value), 'plan');
      assert.equal(current.status, 'awaiting-approval', JSON.stringify(diagnostics)); const plan = await client.request('task.plan', {path: {taskId: task.id}});
      const approve = {path: {taskId: task.id}, idempotencyKey: 'approve-' + mode,
        body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}};
      await client.request('task.approve', approve);
      return {task, plan, reply, receipt, approve};
    },
    async reopen() {assert.equal((await service.shutdown()).shutdownClean, true); service = null; await connect('open');},
    async close() {assert.equal((await service.shutdown()).shutdownClean, true); service = null;},
  };
}
test('v7 real HTTP cancel after reply/approve: original two author handles clean, exact receipts and no cold redispatch', {timeout: 80000}, async t => {
  const f = await fixture(t, 'cancel'), {task, plan, reply, receipt} = await f.prepareTask();
  const running = await until(() => f.workers(task.id), page => page.items.filter(value => value.role === 'author' && value.status === 'running').length === 2, 'both authors running');
  assert.equal(f.observations.filter(value => value.type === 'author' && value.started).length, 2);
  const current = await f.get(task.id), request = {path: {taskId: task.id}, idempotencyKey: 'cancel', body: {expectedRevision: current.revision}};
  const operation = await f.client.request('task.cancel', request); assert.equal(operation.status, 'accepted');
  const closed = await until(() => f.get(task.id), terminal, 'cancel settlement'); assert.equal(closed.status, 'cancelled', JSON.stringify(f.diagnostics));
  const settled = await until(() => f.client.request('operation.get', {path: {operationId: operation.id}}), value => value.status !== 'accepted', 'cancel operation');
  assert.equal(settled.status, 'succeeded'); assert.deepEqual(encode(await f.client.request('task.cancel', request)), encode(operation));
  assert.equal((await f.client.request('task.audit', {path: {taskId: task.id}})).attempts, 4);
  assert.equal((await f.client.request('supervisor.get')).activeWorkers, 0);
  assert.equal(closed.deadlineAt, task.deadlineAt); assert.equal(closed.plan.digest, plan.digest); assert.equal(closed.artifactIds.length, 0);
  const finalWorkers = (await f.workers(task.id)).items;
  for (const old of running.items.filter(value => value.role === 'author')) assert.equal(finalWorkers.find(value => value.id === old.id).status, 'cancelled');
  assert.equal(f.verifications.length, 0);
  assert.ok(f.observations.every(value => value.result?.cleanup?.cleaned === true));
  const before = f.observations.length; await f.reopen();
  assert.equal((await f.get(task.id)).status, 'cancelled'); assert.deepEqual(encode((await f.workers(task.id)).items), encode(finalWorkers));
  assert.deepEqual(encode(await f.client.request('task.cancel', request)), encode(operation));
  assert.deepEqual(encode(await f.client.request('task.leader.reply', reply)), encode({...receipt, replayed: true}));
  assert.equal((await f.client.request('task.audit', {path: {taskId: task.id}})).attempts, 4); assert.equal(f.observations.length, before);
  await f.close();
});
test('v7 real Review rework: only actual bad east reruns; west selected bytes, reply and budget survive through independent verification/cold open', {timeout: 90000}, async t => {
  const f = await fixture(t, 'repair'), {task, plan, reply, receipt} = await f.prepareTask();
  const result = await until(() => f.get(task.id), terminal, 'repair delivery');
  assert.equal(result.status, 'completed', JSON.stringify({diagnostics: f.diagnostics, calls: f.observations.map(value => ({type: value.type, node: value.input.node?.id,
    status: value.result?.status, output: value.result?.outputText}))}));
  const authors = f.observations.filter(value => value.type === 'author'), reviews = f.observations.filter(value => value.type === 'task-independent-review/v1');
  assert.equal(authors.filter(value => value.input.node.id === 'east').length, 2); assert.equal(authors.filter(value => value.input.node.id === 'west').length, 1);
  assert.equal(reviews.length, 2); const reports = reviews.map(value => parseManagedOutput({completion: value.result}));
  assert.equal(reports[0].verdict, 'rework'); assert.deepEqual(reports[0].findings.flatMap(value => value.nodeIds), ['east']); assert.equal(reports[1].verdict, 'accept');
  const firstWest = reviews[0].input.selection.find(value => value.nodeId === 'west'), retainedWest = reviews[1].input.selection.find(value => value.nodeId === 'west');
  assert.deepEqual(encode(retainedWest), encode(firstWest));
  assert.deepEqual(encode(reviews[0].input.materials.find(value => value.nodeId === 'west')), encode(reviews[1].input.materials.find(value => value.nodeId === 'west')));
  const east = authors.filter(value => value.input.node.id === 'east'); assert.equal(east[0].input.repair, undefined);
  assert.equal(east[1].input.repair.basis.kind, 'review'); assert.equal(east[1].input.repair.originalNegativeReport.report.verdict, 'rework');
  assert.deepEqual(encode(east[0].input.leaderReplyRefs), encode(east[1].input.leaderReplyRefs));
  for (const author of authors) {assert.equal(author.input.leaderReplies[0].answer, 'north'); assert.equal(author.input.plan.digest, plan.digest);}
  assert.equal(result.deadlineAt, task.deadlineAt); assert.equal((await f.client.request('task.plan', {path: {taskId: task.id}})).budget.maxAttempts, 17);
  const audit = await f.client.request('task.audit', {path: {taskId: task.id}}); assert.equal(audit.attempts, 14); assert.equal(audit.acceptance.status, 'passed');
  assert.equal(f.verifications.length, 1); assert.equal(f.verifications[0].input.upstream.find(value => value.nodeId === 'west').workerId, firstWest.workerId);
  assert.equal((await f.client.request('supervisor.get')).activeWorkers, 0); assert.ok(f.observations.every(value => value.result?.cleanup?.cleaned === true));
  const refs = await Promise.all(result.artifactIds.map(artifactId => f.client.request('artifact.get', {path: {artifactId}}))), delivery = refs.find(value => value.kind === 'delivery');
  const bytes = (await f.client.downloadArtifact(delivery.id)).content;
  assert.deepEqual(JSON.parse(bytes), [{nodeId: 'east', region: 'north', value: 10}, {nodeId: 'west', region: 'north', value: 20}]);
  const before = f.observations.length; await f.reopen();
  assert.equal((await f.get(task.id)).status, 'completed'); assert.deepEqual(Buffer.from((await f.client.downloadArtifact(delivery.id)).content), Buffer.from(bytes));
  assert.equal((await f.client.request('task.audit', {path: {taskId: task.id}})).attempts, 14); assert.equal(f.observations.length, before);
  assert.deepEqual(encode(await f.client.request('task.leader.reply', reply)), encode({...receipt, replayed: true})); await f.close();
});
