import {supportsNode} from '../task-store/runtime.mjs';
// Explicit full HTTP/v6 fixture. Run after integrating the frozen Core v6 source.
// Not a model test or fallback implementation for worker.cancel's production API.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {trackExecution} from '../task-qwen-live/driver.fixture.mjs';
import {filePermission} from '../task-pi-live/driver.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {data, questions, questionPolicyDigest, policy, bindPlan, equal, taskBody} from './scenario.fixture.mjs';
import {validatePlan} from './driver.fixture.mjs';
import {cancelPiWorker, workerCancellation, readRetainedResult} from './worker-cancel.fixture.mjs';

export async function runWorkerCancelFixture() {
  assert.ok(supportsNode());
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'pi-worker-cancel-http-fixture-')));
  const here = name => fileURLToPath(new URL(name, import.meta.url)), observed = [], tickets = new Map(), services = [];
  let complete = false, verifierStarts = 0, releaseWest;
  const westGate = new Promise(resolve => {releaseWest = resolve;});
  const proposal = {summary: '无模型单 Worker 取消夹具', nodes: ['east', 'west', 'verify'].map(id => ({id,
    role: id === 'verify' ? 'verifier' : 'author', providerId: null, goal: '完成' + id, scope: [id]})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: ['east.json', 'west.json'], acceptance: [policy.description], assumptions: []};
  const native = createPiProvider({id: 'pi-fixture', executable: process.execPath, args: [here('./worker-cancel-peer.fixture.mjs')],
    bridge: {sdkEntry: here('../agent-provider-pi/fixtures/sdk/index.mjs')},
    custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
  const provider = {...native, start(input) {const ticket = tickets.get(input.cwd); assert.ok(ticket);
    const handle = native.start(input); observed.push(trackExecution({taskId: ticket.taskId, workerId: ticket.workerId,
      nodeId: ticket.nodeId, role: ticket.role}, handle)); return handle;}};
  const checkerPath = here('./checker.fixture.mjs'), command = createVerificationCommand({executable: process.execPath,
    checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: digest(encode(policy)),
    assertions: [{name: 'answered-regions', validate: () => false}], delivery() {throw Error('never accepted');}});
  const startChecker = input => {verifierStarts++; return command.start(input);};
  Object.defineProperty(startChecker, 'custodyProfile', {value: command.custodyProfile});
  const config = {root: path.join(parent, 'data'), workerCancellation, custody: {profile: 'node-execution-custody/v1'},
    runtimeQuestions: questions(), providers: new Map([[provider.id, provider]]), supervisorOptions: {intervalMs: 10},
    verification: createVerificationPort({id: 'original-checker', policy, bindPlan, interactionPolicyDigests: [questionPolicyDigest], start: startChecker}),
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
      const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        authorize: async (ticket, request, {signal}) => {
          // Test ONLY: make the ordering deterministic with a real outstanding
          // permission, not sleep/fictional cleanup. Real driver has no gate.
          if (ticket.nodeId === 'west') await new Promise(resolve => {
            if (signal.aborted) {resolve(); return;}
            const abort = () => resolve(); signal.addEventListener('abort', abort, {once: true});
            westGate.then(() => {signal.removeEventListener('abort', abort); resolve();});
          });
          const prepared = [...tickets.entries()].find(([, value]) => value.workerId === ticket.workerId);
          return signal.aborted ? {outcome: {outcome: 'cancelled'}} : filePermission({...ticket, cwd: prepared?.[0]}, request);
        }});
      return {...business, async prepare(ticket, context) {
        const prepared = await business.prepare(ticket, context); tickets.set(prepared.cwd, ticket);
        return {...prepared, prompt: prepared.prompt + '\nWORKER_CANCEL_FIXTURE=' + JSON.stringify(ticket.role === 'planner' ? {plan: proposal} : {node: ticket.nodeId})};
      }};
    }};
  try {
    const start = async mode => {const service = await startTaskService({...config, mode}); services.push(service);
      const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};};
    const first = await start('create'), client = first.client;
    assert.equal(JSON.parse(fs.readFileSync(path.join(config.root, 'profile.json'))).layout, 6);
    const input = await client.request('input.create', {idempotencyKey: 'fixture-input', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const body = taskBody(input.id, 45000), created = await client.createTask(body, 'fixture-create'), end = Date.now() + 30000;
    let task;
    do {task = await client.getTask(created.id); assert.ok(Date.now() < end); assert.ok(!['failed', 'intervention'].includes(task.status));
      if (task.status !== 'awaiting-approval') await pause(20);
    } while (task.status !== 'awaiting-approval');
    const plan = await client.request('task.plan', {path: {taskId: task.id}}), approval = validatePlan(task, plan, body, input.id);
    const receipt = await client.approveTask(task.id, approval, 'fixture-approve');
    // Only observing the successful original HTTP response releases the test
    // permission gate. No Core reducer or original cleanup is replaced.
    const observeClient = {getTask: id => client.getTask(id), getAudit: id => client.getAudit(id),
      request: async (name, value) => {const result = await client.request(name, value); if (name === 'worker.cancel') releaseWest(); return result;}};
    const result = await cancelPiWorker({client: observeClient, taskId: task.id, observations: observed, getVerifierStarts: () => verifierStarts, end});
    assert.equal(result.done.code, 'worker_cancelled'); assert.equal(result.done.status, 'failed'); assert.equal(verifierStarts, 0);
    assert.equal((await first.service.shutdown()).shutdownClean, true);
    const expected = {taskId: task.id, workerId: result.siblingWorkerId, planDigest: plan.digest,
      executionId: result.executions.find(entry => entry.workerId === result.siblingWorkerId).executionId};
    const retained = readRetainedResult(config.root, expected), reopened = await start('open');
    assert.ok(equal(await reopened.client.createTask(body, 'fixture-create'), created));
    assert.ok(equal(await reopened.client.approveTask(task.id, approval, 'fixture-approve'), receipt));
    assert.ok(equal(await reopened.client.request('worker.cancel', result.request), result.operation));
    assert.ok(equal(await reopened.client.getTask(task.id), result.done));
    assert.ok(equal(await reopened.client.request('operation.get', {path: {operationId: result.operation.id}}), result.terminalOperation));
    assert.ok(equal((await reopened.client.request('task.workers', {path: {taskId: task.id}})).items, result.workers));
    assert.ok(equal(await reopened.client.request('task.graph', {path: {taskId: task.id}}), result.graph));
    assert.equal(observed.length, 3); assert.equal(verifierStarts, 0); assert.equal((await reopened.client.request('supervisor.get')).activeWorkers, 0);
    assert.equal((await reopened.service.shutdown()).shutdownClean, true);
    assert.ok(equal(readRetainedResult(config.root, expected), retained)); complete = true;
    return {models: 0, fixture: true, taskStatus: result.done.status, taskCode: result.done.code, attempts: 3, verifierStarts,
      originalTargetCleaned: true, retainedSibling: retained, duplicateStarts: 0};
  } finally {
    releaseWest();
    for (const service of services) assert.equal((await service.shutdown()).shutdownClean, true);
    for (const {handle} of observed) assert.equal((await handle.stop()).cleanup?.cleaned, true);
    if (complete) fs.rmSync(parent, {recursive: true, force: true});
    else process.stderr.write(JSON.stringify({fixtureFailed: true, privateEvidence: parent}) + '\n');
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {process.stdout.write(JSON.stringify(await runWorkerCancelFixture()) + '\n');}
  catch (error) {process.stderr.write(JSON.stringify({code: error.code ?? 'worker_cancel_fixture_failed'}) + '\n'); process.exitCode = 1;}
}
