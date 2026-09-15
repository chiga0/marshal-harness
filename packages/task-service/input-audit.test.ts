import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as delay} from 'node:timers/promises';
import {digest, encode} from '../task-store/store.mjs';
import {createAuditDisclosure, createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {startTaskService} from './composition.mjs';

const checkerPath = fileURLToPath(new URL('../task-application/repair-checker.fixture.mjs', import.meta.url));
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
async function until(predicate) {
  const deadline = Date.now() + 10000;
  while (!await predicate()) {assert.ok(Date.now() < deadline, 'bounded audit integration observation'); await delay(5);}
}
const proposal = {summary: '两个独立公开文件与客观验收', nodes: ['code', 'docs', 'verify'].map(id => ({id,
  role: id === 'verify' ? 'verifier' : 'author', goal: '完成 ' + id, scope: [id], providerId: null})),
  edges: [{from: 'code', to: 'verify'}, {from: 'docs', to: 'verify'}],
  deliverables: ['完整文件'], acceptance: ['固定原始业务断言'], assumptions: []};
const bindPlan = () => ({nodeId: 'verify', description: '独立 Node 程序核对实际文件字节。',
  layouts: ['code', 'docs'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.txt']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['code', 'docs'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: nodeId + '.txt'}}))}),
  deliveries: ['code', 'docs'].map(nodeId => ({nodeId, path: nodeId + '.txt', targetPath: nodeId + '.txt'}))});

// Public synthetic Agent fixtures, not model or Agent process-cleanup evidence.
// Independent verification does execute the original owned Node command path.
async function fixture(t, {disclose = true, startFailure = false} = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-service-input-audit-'))), root = path.join(parent, 'data');
  const prepared = new Map(), handed = new Map(), authors = [], stopped = [], diagnostics = [];
  let service, client;
  const provider = {id: 'fixture', start(input) {
    const ticket = prepared.get(input.cwd); assert.ok(ticket);
    if (startFailure) throw Error('synthetic-start-failure');
    handed.set(ticket.workerId, input.prompt);
    const started = {executionId: 'fixture-' + ticket.workerId, startedAt: new Date().toISOString()}, completion = deferred();
    const end = () => completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
      outputText: ticket.role === 'planner' ? JSON.stringify(proposal) : 'not verification evidence',
      cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
    if (ticket.role === 'planner') end();
    else {
      fs.writeFileSync(path.join(input.cwd, ticket.nodeId + '.txt'), ticket.nodeId === 'code' ? 'correct' : 'retained', {mode: 0o600});
      authors.push({ticket, end});
    }
    return {started: Promise.resolve(started), completion: completion.promise, stop() {
      stopped.push(ticket.workerId); completion.resolve({providerId: provider.id, status: 'cancelled', stopReason: 'cancelled',
        cleanup: {started, cleaned: true, scope: 'controlled-fixture'}}); return completion.promise;
    }};
  }};
  const policy = {id: 'audit-files-checker', version: '1', description: '实际固定 Node checker 核验公开数据文件'};
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), assertions: [{name: 'business', validate: actual => actual === true}, {name: 'structure', validate: actual => actual === true}],
    delivery: ({prepared: input}) => ({name: 'all.txt', mediaType: 'text/plain',
      content: Buffer.concat(['code', 'docs'].map(id => fs.readFileSync(path.join(input.cwd, id + '.txt'))))})});
  const verification = createVerificationPort({id: 'checker', policy, bindPlan, start: command.start});
  const auditDisclosure = disclose ? createAuditDisclosure({id: 'public-synthetic', version: '1', redact(prompt) {
    assert.ok(prompt.includes('公开合成')); return prompt;
  }}) : null;
  async function open(mode) {
    service = await startTaskService({root, mode, providers: new Map([[provider.id, provider]]), verification, auditDisclosure,
      supervisorOptions: {intervalMs: 5}, onDiagnostic: item => diagnostics.push(item), businessFactory: ports => {
        const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
          approvedLayout: ticket => ports.approvedLayout(ticket), observeExecution: ticket => ports.observeExecution(ticket)});
        return {async prepare(ticket, context) {
          const input = await business.prepare(ticket, context); prepared.set(input.cwd, ticket);
          // Audits the exact actual handoff, not only the bounded Task.context.
          if (ticket.executionType !== 'verification') input.prompt += '\n公开合成补充材料：' + '数据🙂'.repeat(6000);
          return input;
        }, collect: (ticket, result, context) => business.collect(ticket, result, context),
        release: ticket => business.release(ticket), close: () => business.close()};
      }});
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    client = new TaskClient({baseURL: connection.url, token: connection.token});
  }
  t.after(async () => {await service?.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  await open('create');
  return {get client() {return client;}, get service() {return service;}, handed, authors, stopped, diagnostics,
    async reopen() {assert.equal((await service.shutdown()).shutdownClean, true); await open('open');},
    async create() {
      const input = await client.request('input.create', {idempotencyKey: 'public-input', body: {name: 'public.txt', mediaType: 'text/plain',
        contentBase64: Buffer.from('公开合成业务数据').toString('base64')}});
      const task = await client.createTask({intent: '公开合成任务，交付两个文件', context: {text: '公开合成上下文', inputRefs: [input.id]},
        requirements: {deliverables: ['保留业务准确性'], acceptance: ['实际文件断言']},
        limits: {timeoutMs: 30000, maxAttempts: 4, maxWorkers: 2}}, 'create');
      return {taskId: task.id, input};
    },
    async approve(taskId) {
      let task; await until(async () => {task = await client.getTask(taskId); return task.status === 'awaiting-approval';});
      const plan = await client.request('task.plan', {path: {taskId}});
      const receipt = await client.approveTask(taskId, {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}, 'approve');
      await until(() => authors.length === 2); return receipt;
    }};
}

test('HTTP audit downloads exact FileBusiness/Provider input, retains truthful metrics and cold receipts after actual command verification', {timeout: 20000}, async t => {
  const f = await fixture(t), {taskId, input} = await f.create(), approved = await f.approve(taskId);
  const running = await f.client.getAudit(taskId); assert.equal(running.attempts, 3); assert.equal(f.handed.size, 3);
  for (const prompt of running.prompts) {
    const actual = f.handed.get(prompt.workerId); assert.ok(Buffer.byteLength(actual) > 32768);
    assert.equal(prompt.observation.stage, 'handed-off'); assert.equal(prompt.source, 'handed-off-redacted');
    assert.equal(prompt.observation.promptDigest, digest(Buffer.from(actual))); assert.deepEqual(prompt.contextRefs, [input.id]);
    assert.ok(Buffer.byteLength(prompt.text) <= 2048); assert.equal(prompt.observation.previewTruncated, true);
    const downloaded = await f.client.downloadInputSnapshot(taskId, prompt); assert.equal(downloaded.content.toString(), actual);
    assert.ok(actual.includes('公开合成上下文') && actual.includes('保留业务准确性'));
    await assert.rejects(f.client.downloadInputSnapshot('foreign', prompt), {code: 'client_invalid_request'});
  }
  f.authors.forEach(author => author.end());
  let task; await until(async () => {task = await f.client.getTask(taskId); return task.status === 'completed';});
  const audit = await f.client.getAudit(taskId); assert.equal(audit.attempts, 4); assert.equal(audit.reworkCount, 0);
  assert.equal(audit.acceptance.status, 'passed'); assert.deepEqual({...audit.usage}, {tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0});
  assert.equal(audit.measurement.firstReviewSource, 'unavailable'); assert.equal(audit.workers.length, 4);
  assert.ok(audit.workers.every(worker => worker.audit.elapsedSource === 'started-to-settlement' && worker.audit.waitingMs === null));
  const verifier = audit.workers.find(worker => worker.role === 'verifier'); assert.ok(verifier);
  assert.equal(audit.prompts.find(prompt => prompt.workerId === verifier.id).observation.stage, 'unavailable', 'command checker is not an Agent prompt');
  assert.equal(task.artifactIds.length, 2, 'audit artifacts do not contaminate the independent Decision');
  const delivery = (await Promise.all(task.artifactIds.map(id => f.client.downloadArtifact(id)))).find(item => item.artifact.kind === 'delivery');
  assert.equal(delivery.content.toString(), 'correctretained');
  const starts = f.handed.size; await f.reopen();
  assert.deepEqual(await f.client.getAudit(taskId), audit); assert.equal(f.handed.size, starts);
  assert.equal((await f.client.request('operation.get', {path: {operationId: approved.id}})).status, 'succeeded');
  for (const prompt of audit.prompts.filter(prompt => prompt.observation.snapshot)) {
    assert.equal((await f.client.downloadInputSnapshot(taskId, prompt)).content.toString(), f.handed.get(prompt.workerId));
  }
});

test('default HTTP audit remains metadata-only through cancellation, preserving actual handoff and unavailable usage', {timeout: 15000}, async t => {
  const f = await fixture(t, {disclose: false}), {taskId} = await f.create(); await f.approve(taskId);
  const current = await f.client.getTask(taskId);
  const receipt = await f.client.request('task.cancel', {path: {taskId}, idempotencyKey: 'cancel', body: {expectedRevision: current.revision}});
  await until(async () => (await f.client.getTask(taskId)).status === 'cancelled');
  const audit = await f.client.getAudit(taskId); assert.equal(audit.attempts, 3); assert.equal(audit.acceptance.status, 'pending');
  assert.ok(audit.prompts.every(prompt => prompt.source === 'unavailable' && prompt.text === '' && prompt.observation.coverage === 'metadata-only' &&
    prompt.observation.snapshot === null && prompt.observation.stage === 'handed-off'));
  assert.equal(new Set(f.stopped).size, 2); assert.equal((await f.client.getTask(taskId)).artifactIds.length, 0);
  await f.reopen(); assert.deepEqual(await f.client.getAudit(taskId), audit);
  assert.equal((await f.client.request('operation.get', {path: {operationId: receipt.id}})).status, 'succeeded');
});

test('provider start failure retains prepared observation without claiming handoff or model consumption', {timeout: 15000}, async t => {
  const f = await fixture(t, {startFailure: true}), {taskId} = await f.create();
  await until(async () => (await f.client.getTask(taskId)).status === 'intervention');
  const audit = await f.client.getAudit(taskId); assert.equal(audit.attempts, 1); assert.equal(f.handed.size, 0);
  assert.equal(audit.prompts[0].observation.stage, 'prepared'); assert.equal(audit.prompts[0].source, 'prepared-redacted');
  assert.equal(audit.prompts[0].observation.handedOffAt, null); assert.equal(audit.workers[0].audit.elapsedMs, null);
});
