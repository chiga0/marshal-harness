import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {trackExecution, cancelActiveTeam, cancelledExecutionFact} from './driver.fixture.mjs';
import {startTaskService} from '../task-service/composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';

async function controlled() {
  let cancelled = false, calls = 0, verifierStarts = 0;
  const now = Date.now(), entries = ['planning', 'east', 'west'].map((nodeId, index) => {
    const identity = {taskId: 'task-a', workerId: 'worker-' + index, nodeId, role: index ? 'author' : 'planner'};
    const started = {executionId: 'execution-' + index, startedAt: new Date(now - 3000 + index * 500).toISOString()};
    let resolve, phase = index ? 'running' : 'terminal';
    const completion = new Promise(done => { resolve = done; });
    const handle = {started: Promise.resolve(started), completion, snapshot: () => ({phase})};
    const entry = trackExecution(identity, handle);
    const finish = (status = index ? 'cancelled' : 'completed') => {
      phase = 'terminal'; resolve({status, reason: index ? 'provider_stopped' : 'agent_end_turn', stopReason: index ? null : 'end_turn',
        outputText: 'PRIVATE_NOT_EVIDENCE', cleanup: {cleaned: true, started,
          agentExit: {observed: true, at: new Date(index ? Date.now() + 1 : now - 2000).toISOString()}}});
    };
    if (!index) finish();
    return {entry, finish, setPhase: value => { phase = value; }};
  });
  const observations = entries.map(item => item.entry);
  const client = {
    getTask: async () => ({id: 'task-a', revision: 8, status: cancelled ? 'cancelled' : 'running', artifactIds: []}),
    request: async (operation, request) => {
      if (operation === 'task.cancel') {
        calls++; assert.deepEqual(request, {path: {taskId: 'task-a'}, idempotencyKey: 'qwen-cancel-task', body: {expectedRevision: 8}});
        assert.ok(observations.slice(1).every(entry => entry.started && !entry.settled));
        cancelled = true; entries.slice(1).forEach(item => item.finish());
        return {id: 'operation-a', status: 'accepted', taskId: 'task-a', kind: 'task.cancel'};
      }
      if (operation === 'operation.get') return {id: 'operation-a', status: 'succeeded', taskId: 'task-a', kind: 'task.cancel'};
      if (operation === 'task.audit') return {attempts: 3, acceptance: {status: 'pending'}};
      assert.equal(operation, 'task.workers');
      return {nextCursor: null, items: observations.map(({identity, started}, index) => ({id: identity.workerId, nodeId: identity.nodeId,
        role: identity.role, startedAt: started.startedAt, status: index ? cancelled ? 'cancelled' : 'running' : 'completed'}))};
    },
  };
  await Promise.resolve();
  return {entries, client, observations, getVerifierStarts: () => verifierStarts, end: now + 2000, taskId: 'task-a',
    get calls() { return calls; }, setVerifier: () => { verifierStarts++; }};
}
test('one cancellation uses both original started handles and returns only sanitized original cleanup', async () => {
  const f = await controlled(), result = await cancelActiveTeam(f);
  assert.equal(f.calls, 1); assert.equal(result.done.status, 'cancelled');
  assert.deepEqual(result.executions.map(item => item.status), ['completed', 'cancelled', 'cancelled']);
  assert.doesNotMatch(JSON.stringify(result.executions), /PRIVATE/);
});
test('completed/stopping author, verifier already started, or expired window cannot issue cancel', async () => {
  for (const change of [f => f.entries[1].finish('completed'), f => f.entries[1].setPhase('stopping'), f => f.setVerifier(), f => f.end = Date.now() - 1]) {
    const f = await controlled(); change(f); await Promise.resolve();
    await assert.rejects(cancelActiveTeam(f), /cancel_window_/); assert.equal(f.calls, 0);
  }
});
test('CAS loss is inconclusive failure without obtaining a new revision or retrying mutation', async () => {
  const f = await controlled(), request = f.client.request; let calls = 0;
  f.client.request = async (operation, options) => {
    if (operation === 'task.cancel') { calls++; throw Object.assign(new Error('private peer message'), {status: 409}); }
    return request(operation, options);
  };
  await assert.rejects(cancelActiveTeam(f), /cancel_window_missed/); assert.equal(calls, 1);
});
test('task cancellation alone cannot hide verifier, delivery, wrong Worker or unexpected acceptance', async () => {
  for (const kind of ['verifier', 'delivery', 'worker', 'audit']) {
    const f = await controlled(), request = f.client.request, getTask = f.client.getTask;
    if (kind === 'delivery') f.client.getTask = async () => ({...await getTask(), artifactIds: ['artifact-forbidden']});
    f.client.request = async (operation, options) => {
      const result = await request(operation, options);
      if (f.calls && kind === 'verifier') f.setVerifier();
      if (f.calls && kind === 'worker' && operation === 'task.workers') result.items[1].id = 'foreign-worker';
      if (kind === 'audit' && operation === 'task.audit') result.acceptance.status = 'passed';
      return result;
    };
    await assert.rejects(cancelActiveTeam(f)); assert.equal(f.calls, 1);
  }
});
test('cleanup must prove the same original execution stopped after this request, never normal completion', () => {
  const started = {executionId: 'execution-a', startedAt: '2026-09-08T10:00:00.000Z'}, requestedAt = '2026-09-08T10:00:01.000Z';
  const valid = {status: 'cancelled', reason: 'provider_stopped', cleanup: {started, cleaned: true,
    agentExit: {observed: true, at: '2026-09-08T10:00:02.000Z'}}};
  assert.equal(cancelledExecutionFact({workerId: 'worker-a'}, valid, requestedAt, started).status, 'cancelled');
  for (const change of [r => r.status = 'completed', r => r.reason = 'agent_cancelled', r => r.cleanup.cleaned = false,
    r => r.cleanup.started.executionId = 'foreign', r => r.cleanup.agentExit.observed = false,
    r => r.cleanup.agentExit.at = requestedAt, r => delete r.cleanup.started]) {
    const result = structuredClone(valid); change(result);
    assert.throws(() => cancelledExecutionFact({}, result, requestedAt, started), /cancel_execution_unproven/);
  }
});

test('actual HTTP/SQLite/ACP fixture cancels original authors and reopens without a verifier or replacement', {timeout: 30000}, async t => {
  // Fixed checked-in fake Agent only. Its waiting behavior is fixture machinery,
  // not added to the real Qwen task or used as Provider acceptance evidence.
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'qwen-cancel-fixture-'))), observations = [], byCwd = new Map(), services = [];
  let verifierStarts = 0;
  const native = createAcpProvider({id: 'fixture-acp', executable: process.execPath,
    args: [fileURLToPath(new URL('../task-team-integration/agent.fixture.mjs', import.meta.url))], env: {TEAM_FIXTURE_MODE: 'hang'}});
  const provider = {id: native.id, start(input) { const handle = native.start(input); observations.push(trackExecution(byCwd.get(input.cwd), handle)); return handle; }};
  const config = {root: path.join(parent, 'data'), providers: new Map([[provider.id, provider]]), supervisorOptions: {intervalMs: 10},
    verification: createVerificationPort({id: 'fixture-verifier-never-starts', policy, bindPlan,
      start() { verifierStarts++; throw Error('Cancellation must not start a verifier'); }}),
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
      const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
      return {...business, prepare: async (ticket, context) => { const prepared = await business.prepare(ticket, context);
        byCwd.set(prepared.cwd, {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role}); return prepared; }};
    }};
  t.after(async () => { for (const service of services) await service.shutdown();
    for (const {handle} of observations) await handle.stop(); fs.rmSync(parent, {recursive: true, force: true}); });
  const start = async mode => { const service = await startTaskService({...config, mode}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})}; };
  const first = await start('create'), client = first.client;
  const input = await client.request('input.create', {idempotencyKey: 'fixture-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: Buffer.from('{}').toString('base64')}});
  const body = {intent: 'Explicit fixture cancellation', context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, 'fixture-create'), end = Date.now() + 15000;
  let task;
  do { task = await client.getTask(created.id); assert.ok(Date.now() < end); if (task.status !== 'awaiting-approval') await pause(20); }
  while (task.status !== 'awaiting-approval');
  const request = {expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest};
  const approval = await client.approveTask(task.id, request, 'fixture-approve');
  const result = await cancelActiveTeam({client, taskId: task.id, observations, getVerifierStarts: () => verifierStarts, end});
  assert.equal(result.done.status, 'cancelled'); assert.equal(verifierStarts, 0);
  assert.equal((await client.request('supervisor.get')).activeWorkers, 0);
  assert.equal((await first.service.shutdown()).shutdownClean, true);
  const resumed = await start('open');
  assert.deepEqual(await resumed.client.createTask(body, 'fixture-create'), created);
  assert.deepEqual(await resumed.client.approveTask(task.id, request, 'fixture-approve'), approval);
  assert.deepEqual(await resumed.client.request('task.cancel', result.request), result.operation);
  assert.deepEqual(await resumed.client.getTask(task.id), result.done);
  assert.equal(observations.length, 3); assert.equal(verifierStarts, 0);
  assert.equal((await resumed.service.shutdown()).shutdownClean, true);
});
