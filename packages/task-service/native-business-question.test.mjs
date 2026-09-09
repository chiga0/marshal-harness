import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from './composition.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort, createRuntimeQuestionPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {digest, encode} from '../task-store/store.mjs';

const here = name => fileURLToPath(new URL(name, import.meta.url));
const peer = here('../agent-provider-pi/bridge-agent.fixture.mjs');
const sdkEntry = process.env.MARSHAL_PI_TEST_SDK ?? here('../agent-provider-pi/fixtures/sdk/index.mjs');
const checkerPath = here('./native-business-question-checker.fixture.mjs');
const policy = {id: 'business-answer', version: '1', description: '固定独立检查器同时核验原业务答案和两个完整文件；确定性测试。'};
async function until(fn, accept, limit = 15000) {const deadline = Date.now() + limit;
  for (;;) {const value = await fn(); if (accept(value)) return value;
    assert.ok(Date.now() < deadline, 'bounded observation: ' + JSON.stringify(value)); await pause(20);}}

test('real HTTP/SQLite/Pi native tools/owned custody/independent command deliver and cold replay the exact answer', {timeout: 45000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-native-question-http-'))), root = path.join(parent, 'data');
  const services = [], observed = [];
  t.after(async () => {for (const service of services) await service.shutdown(); for (const handle of observed) await handle.stop();
    fs.rmSync(parent, {recursive: true, force: true});});
  const providers = new Map([['planner', 'business-plan'], ['pi-code', 'business'], ['pi-docs', 'normal']].map(([id, mode]) => {
    const native = createPiProvider({id, executable: process.execPath, args: [peer, mode], bridge: {sdkEntry},
      custodyProfile: {id: 'native-file-fixture-v1', scope: 'inherited-process-group', eligible: true}});
    return [id, {...native, start(input) {const handle = native.start(input); observed.push(handle); return handle;}}];
  }));
  const runtimeQuestions = createRuntimeQuestionPort({policy: {id: 'region', version: '1', description: '显式选择业务区域'}, nodeIds: ['code'],
    maxQuestions: 1, maxWaitMs: 10000, applies: () => true,
    validateQuestion: value => value.prompt === '请确定本次业务区域' && value.kind === 'select' && JSON.stringify(value.options) === '["north","south"]',
    validateAnswer: value => ['north', 'south'].includes(value)});
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), assertions: [{name: 'business-answer', validate: actual => {
      try {assert.deepEqual(actual, {answer: 'north', code: 'north', docs: 'independent native candidate', count: 1}); return true;} catch {return false;}
    }}], delivery: ({prepared}) => ({name: 'business.json', mediaType: 'application/json',
      content: encode({code: fs.readFileSync(path.join(prepared.cwd, 'code.txt'), 'utf8'), docs: fs.readFileSync(path.join(prepared.cwd, 'docs.txt'), 'utf8')})})});
  const verification = createVerificationPort({id: 'checker', policy, interactionPolicyDigests: [runtimeQuestions.policyDigest], start: command.start,
    bindPlan: () => ({nodeId: 'verify', description: '精确交付两个独立文件并验证答案',
      layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: ['output.txt']})).concat({nodeId: 'verify', allowedPaths: [],
        inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: 'output.txt'}}))}),
      deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: 'output.txt', targetPath: nodeId + '.txt'}))})});
  const config = {root, providers, runtimeQuestions, verification, custody: {profile: 'node-execution-custody/v1'}, supervisorOptions: {intervalMs: 10},
    applicationOptions: {execution: {defaultProvider: 'planner'}},
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
      authorize: (ticket, request) => request.toolCall?._meta?.toolName === 'write' && request.toolCall.rawInput?.path === 'output.txt' &&
        ticket.input.fileLayout.allowedPaths.includes('output.txt') ? {outcome: {outcome: 'selected', optionId: 'allow-once'}} : {outcome: {outcome: 'cancelled'}}})};
  async function start(mode) {const service = await startTaskService({...config, mode}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};}
  const {service, client} = await start('create');
  const created = await client.createTask({intent: '在执行中明确区域后提交两个独立文件，完整独立验收', limits: {timeoutMs: 30000, maxAttempts: 4, maxWorkers: 2}}, 'create');
  const task = await until(() => client.getTask(created.id), value => value.status === 'awaiting-approval');
  const plan = await client.request('task.plan', {path: {taskId: task.id}});
  assert.equal(plan.interaction.policyDigest, runtimeQuestions.policyDigest);
  const approval = {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
  await client.approveTask(task.id, approval, 'approve');
  const questions = await until(() => client.request('task.questions', {path: {taskId: task.id}}), value => value.items.length === 1);
  const q = questions.items[0]; assert.equal(q.deliveryStatus, null); assert.deepEqual(q.options.map(option => option.value), ['north', 'south']);
  await until(() => client.request('task.workers', {path: {taskId: task.id}}), value => value.items.some(worker => worker.nodeId === 'docs' && worker.status === 'completed'));
  const waiting = await client.request('worker.get', {path: {workerId: q.workerId}}); assert.equal(waiting.status, 'awaiting-answer');
  const current = await client.getTask(task.id), answerRequest = {path: {taskId: task.id, questionId: q.id}, idempotencyKey: 'answer',
    body: {expectedRevision: current.revision, questionRevision: 1, questionDigest: q.questionDigest, answer: 'north'}};
  const receipt = await client.request('task.answer', answerRequest); assert.equal(receipt.deliveryStatus, 'pending');
  let done;
  try {done = await until(() => client.getTask(task.id), value => ['completed', 'failed', 'intervention'].includes(value.status));}
  catch (error) {throw new Error(JSON.stringify({message: error.message,
    workers: await client.request('task.workers', {path: {taskId: task.id}}),
    supervisor: await client.request('supervisor.get'), questions: (await client.request('task.questions', {path: {taskId: task.id}})).items.map(({id, status, deliveryStatus}) => ({id, status, deliveryStatus}))}));}
  assert.equal(done.status, 'completed');
  assert.equal((await client.request('operation.get', {path: {operationId: receipt.operation.id}})).status, 'succeeded');
  const files = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id))), delivery = files.find(file => file.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content), {code: 'north', docs: 'independent native candidate'});
  assert.equal(observed.length, 3); for (const handle of observed) assert.equal((await handle.completion).cleanup.cleaned, true);
  const finalWorker = await client.request('worker.get', {path: {workerId: q.workerId}});
  assert.equal(finalWorker.startedAt, waiting.startedAt); assert.equal(finalWorker.attempt, waiting.attempt);
  await service.shutdown(); const resumed = await start('open');
  const replay = await resumed.client.request('task.answer', answerRequest); assert.equal(replay.replayed, true); assert.equal(replay.deliveryStatus, 'pending');
  assert.equal((await resumed.client.request('task.questions', {path: {taskId: task.id}})).items[0].deliveryStatus, 'acknowledged');
  assert.deepEqual((await resumed.client.downloadArtifact(delivery.artifact.id)).content, delivery.content); assert.equal(observed.length, 3);
});
