import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {parseOptions, cancelPiActiveTeam, runLive} from './driver.fixture.mjs';
import {trackExecution, cancelledExecutionFact} from '../task-qwen-live/driver.fixture.mjs';
import {startTaskService} from '../task-service/composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {data, questions, questionPolicyDigest, policy, bindPlan} from './scenario.fixture.mjs';

const args = ['--execute-real', '--run-dir', '/private/tmp/pi-cancel-new', '--node', '/installed/node',
  '--pi-entry', '/installed/pi/dist/bundle/cli.js', '--pi-sdk', '/installed/pi/dist/index.js'];
test('Task cancellation is explicit and distinct from the unchanged cancelled business answer', async () => {
  assert.equal(parseOptions([...args, '--answer', 'cancelled']).scenario, 'question');
  assert.equal(parseOptions([...args, '--scenario', 'question', '--answer', 'paid']).answer, 'paid');
  const cancel = parseOptions([...args, '--scenario', 'cancel']); assert.equal(cancel.scenario, 'cancel');
  assert.equal(Object.hasOwn(cancel, 'answer'), false);
  for (const input of [args, [...args, '--scenario', 'cancel', '--answer', 'cancelled'], [...args, '--scenario', 'cancel', '--scenario', 'question'],
    [...args, '--scenario'], [...args, '--scenario', 'cancelled'], [...args.slice(1), '--scenario', 'cancel'], [...args, '--scenario', 'cancel', '--retry', '1']])
    assert.throws(() => parseOptions(input));
  await assert.rejects(runLive({executeReal: true, scenario: 'cancel', answer: 'paid'}), /explicit_real_execution_required/);
});

test('Qwen and Pi cancellation reasons are closed, separate and bound to the original execution after request', () => {
  const started = {executionId: 'original-execution', startedAt: '2026-09-09T10:00:00.000Z'}, at = '2026-09-09T10:00:01.000Z';
  for (const reason of ['provider_stopped', 'pi_provider_stopped']) {
    const result = {status: 'cancelled', reason, cleanup: {cleaned: true, started,
      agentExit: {observed: true, at: '2026-09-09T10:00:02.000Z'}}};
    assert.equal(cancelledExecutionFact({}, result, at, started, reason).cleanup, true);
    assert.throws(() => cancelledExecutionFact({}, result, at, started, reason === 'provider_stopped' ? 'pi_provider_stopped' : 'provider_stopped'));
    assert.throws(() => cancelledExecutionFact({}, {...result, reason: 'arbitrary'}, at, started, 'arbitrary'));
    if (reason === 'provider_stopped') assert.equal(cancelledExecutionFact({}, result, at, started).status, 'cancelled');
    else assert.throws(() => cancelledExecutionFact({}, result, at, started));
    for (const change of [r => r.status = 'completed', r => r.cleanup.cleaned = false, r => r.cleanup.agentExit.observed = false,
      r => r.cleanup.started.executionId = 'substitute', r => r.cleanup.agentExit.at = at, r => r.reason = 'pi_agent_settled']) {
      const bad = structuredClone(result); change(bad); assert.throws(() => cancelledExecutionFact({}, bad, at, started, reason));
    }
  }
});

async function controlled() {
  let cancelled = false, calls = 0, verifierStarts = 0;
  const now = Date.now(), entries = ['planning', 'east', 'west'].map((nodeId, index) => {
    const identity = {taskId: 'task-a', workerId: 'worker-' + index, nodeId, role: index ? 'author' : 'planner'};
    const started = {executionId: 'execution-' + index, startedAt: new Date(now - 3000 + index * 500).toISOString()};
    let resolve, phase = index ? 'running' : 'terminal';
    const completion = new Promise(done => {resolve = done;});
    const handle = {started: Promise.resolve(started), completion, snapshot: () => ({phase})};
    const entry = trackExecution(identity, handle);
    const finish = (status = index ? 'cancelled' : 'completed') => {
      phase = 'terminal'; resolve({status, reason: index ? 'pi_provider_stopped' : 'pi_agent_settled', stopReason: index ? null : 'end_turn',
        outputText: 'PRIVATE_NOT_EVIDENCE', cleanup: {cleaned: true, started,
          agentExit: {observed: true, at: new Date(index ? Date.now() + 1 : now - 2000).toISOString()}}});
    };
    if (!index) finish(); return {entry, finish, setPhase: value => {phase = value;}};
  });
  const observations = entries.map(item => item.entry);
  const client = {getTask: async () => ({id: 'task-a', revision: 8, status: cancelled ? 'cancelled' : 'running', artifactIds: []}),
    request: async (operation, request) => {
      if (operation === 'task.cancel') {
        calls++; assert.deepEqual(request, {path: {taskId: 'task-a'}, idempotencyKey: 'pi-runtime-cancel-task', body: {expectedRevision: 8}});
        cancelled = true; entries.slice(1).forEach(item => item.finish());
        return {id: 'operation-a', status: 'accepted', taskId: 'task-a', kind: 'task.cancel'};
      }
      if (operation === 'operation.get') return {id: 'operation-a', status: 'succeeded', taskId: 'task-a', kind: 'task.cancel'};
      if (operation === 'task.audit') return {attempts: 3, acceptance: {status: 'pending'}};
      assert.equal(operation, 'task.workers');
      return {nextCursor: null, items: observations.map(({identity, started}, index) => ({id: identity.workerId, nodeId: identity.nodeId,
        role: identity.role, startedAt: started.startedAt, status: index ? cancelled ? 'cancelled' : 'running' : 'completed'}))};
    }};
  await Promise.resolve();
  return {entries, client, observations, getVerifierStarts: () => verifierStarts, end: now + 2000, taskId: 'task-a',
    get calls() {return calls;}, setVerifier: () => {verifierStarts++;}};
}
test('Pi wrapper uses one original revision/key and sanitized native cleanup, not a Qwen reason', async () => {
  const f = await controlled(), result = await cancelPiActiveTeam(f); assert.equal(f.calls, 1);
  assert.deepEqual(result.executions.map(value => value.status), ['completed', 'cancelled', 'cancelled']);
  assert.doesNotMatch(JSON.stringify(result.executions), /PRIVATE/);
});
test('already waiting, completed, stopping or expired window cannot issue cancellation; CAS does not retry', async () => {
  for (const change of [f => f.client.getTask = async () => ({id: 'task-a', revision: 8, status: 'awaiting-answer'}),
    f => f.entries[1].finish('completed'), f => f.entries[1].setPhase('stopping'), f => f.setVerifier(), f => f.end = Date.now() - 1]) {
    const f = await controlled(); change(f); await Promise.resolve(); await assert.rejects(cancelPiActiveTeam(f), /cancel_window_/); assert.equal(f.calls, 0);
  }
  const f = await controlled(), request = f.client.request; let writes = 0;
  f.client.request = async (operation, options) => {if (operation === 'task.cancel') {writes++; throw Object.assign(Error('not logged'), {status: 409});}
    return request(operation, options);};
  await assert.rejects(cancelPiActiveTeam(f), /cancel_window_missed/); assert.equal(writes, 1);
});

test('actual Pi native bridge and custody/layout3 HTTP cancel original authors, then reopen without replacements', {timeout: 30000}, async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'pi-custody-cancel-fixture-'))), observations = [], tickets = new Map(), services = [];
  const here = name => fileURLToPath(new URL(name, import.meta.url));
  let verifierStarts = 0, complete = false;
  const custodyProfile = {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true};
  const nativePlanner = createAcpProvider({id: 'planner-fixture', executable: process.execPath,
    args: [here('../task-service/runtime-question-recovery.worker.fixture.mjs'), 'planner'], custodyProfile});
  const nativePi = createPiProvider({id: 'pi-fixture', executable: process.execPath,
    args: [here('../agent-provider-pi/bridge-agent.fixture.mjs'), 'write'], bridge: {sdkEntry: here('../agent-provider-pi/fixtures/sdk/index.mjs')}, custodyProfile});
  const observe = native => ({...native, start(input) {const ticket = tickets.get(input.cwd); assert.ok(ticket);
    const handle = native.start(input); observations.push(trackExecution({taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role}, handle)); return handle;}});
  const planner = observe(nativePlanner), pi = observe(nativePi);
  const proposal = {summary: 'Test-only cancellation fixture', nodes: ['east', 'west', 'verify'].map(id => ({id,
    role: id === 'verify' ? 'verifier' : 'author', providerId: id === 'verify' ? null : pi.id, goal: 'Test ' + id, scope: [id]})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: ['east.json', 'west.json'], acceptance: [policy.description], assumptions: []};
  const checkerPath = here('./checker.fixture.mjs'), command = createVerificationCommand({executable: process.execPath,
    checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: digest(encode(policy)),
    assertions: [{name: 'answered-regions', validate: () => false}], delivery() {throw Error('never accepted');}});
  const startChecker = input => {verifierStarts++; return command.start(input);};
  Object.defineProperty(startChecker, 'custodyProfile', {value: command.custodyProfile});
  const config = {root: path.join(parent, 'data'), custody: {profile: 'node-execution-custody/v1'}, runtimeQuestions: questions(),
    providers: new Map([[planner.id, planner], [pi.id, pi]]), supervisorOptions: {intervalMs: 10},
    verification: createVerificationPort({id: 'original-checker', policy, bindPlan, interactionPolicyDigests: [questionPolicyDigest], start: startChecker}),
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
      const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        // Deterministic fixture only: hold a real native permission request
        // without granting it. The real-model driver never injects this wait.
        authorize: (_ticket, _request, {signal}) => new Promise(resolve => {
          const deny = () => resolve({outcome: {outcome: 'cancelled'}});
          if (signal.aborted) deny(); else signal.addEventListener('abort', deny, {once: true});
        })});
      return {...business, async prepare(ticket, context) {const prepared = await business.prepare(ticket, context); tickets.set(prepared.cwd, ticket);
        return ticket.role === 'planner' ? {...prepared, prompt: prepared.prompt + '\nFIXTURE_PLAN=' + JSON.stringify(proposal)} : prepared;}};
    }};
  t.after(async () => {
    if (!complete) t.diagnostic('Preserved private failed fixture: ' + parent);
    for (const service of services) await service.shutdown();
    for (const {handle} of observations) assert.equal((await handle.stop()).cleanup?.cleaned, true);
    if (complete) fs.rmSync(parent, {recursive: true, force: true});
  });
  const start = async mode => {const service = await startTaskService({...config, mode}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};};
  const first = await start('create'), client = first.client;
  const input = await client.request('input.create', {idempotencyKey: 'fixture-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  const body = {intent: 'Explicit no-model Pi/custody cancellation fixture', context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, 'fixture-create'), end = Date.now() + 15000;
  let task;
  do {task = await client.getTask(created.id); assert.ok(Date.now() < end); if (task.status !== 'awaiting-approval') await pause(20);}
  while (task.status !== 'awaiting-approval');
  const approval = {expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest};
  const receipt = await client.approveTask(task.id, approval, 'fixture-approve');
  const result = await cancelPiActiveTeam({client, taskId: task.id, observations, getVerifierStarts: () => verifierStarts, end});
  assert.equal(result.done.status, 'cancelled'); assert.equal(verifierStarts, 0);
  assert.equal(JSON.parse(fs.readFileSync(path.join(config.root, 'profile.json'))).layout, 3);
  assert.equal((await client.request('supervisor.get')).activeWorkers, 0);
  assert.equal((await first.service.shutdown()).shutdownClean, true);
  const reopened = await start('open');
  assert.deepEqual(await reopened.client.createTask(body, 'fixture-create'), created);
  assert.deepEqual(await reopened.client.approveTask(task.id, approval, 'fixture-approve'), receipt);
  assert.deepEqual(await reopened.client.request('task.cancel', result.request), result.operation);
  assert.deepEqual(await reopened.client.getTask(task.id), result.done);
  assert.equal((await reopened.client.request('operation.get', {path: {operationId: result.operation.id}})).status, 'succeeded');
  assert.equal(observations.length, 3); assert.equal(verifierStarts, 0);
  assert.equal((await reopened.service.shutdown()).shutdownClean, true); complete = true;
});
