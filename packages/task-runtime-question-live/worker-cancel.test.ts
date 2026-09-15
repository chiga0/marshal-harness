import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {DatabaseSync} from 'node:sqlite';
import {fileURLToPath} from 'node:url';
import {parseOptions, runLive} from './driver.fixture.mjs';
import {cancelPiWorker, retainedResult, readRetainedResult, workerCancellation} from './worker-cancel.fixture.mjs';
import {trackExecution} from '../task-qwen-live/driver.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {launchProtocol} from '../agent-runtime/index.mjs';
import {filePermission} from '../task-pi-live/driver.fixture.mjs';
import {data, questionPolicyDigest} from './scenario.fixture.mjs';

const args = ['--execute-real', '--run-dir', '/private/tmp/pi-worker-cancel-new', '--node', '/installed/node',
  '--pi-entry', '/installed/pi/dist/bundle/cli.js', '--pi-sdk', '/installed/pi/dist/index.js'];
test('worker-cancel is an explicit bounded scenario, not Task cancel or a business answer', async () => {
  assert.equal(parseOptions([...args, '--scenario', 'worker-cancel']).scenario, 'worker-cancel');
  assert.equal(parseOptions([...args, '--scenario', 'cancel']).scenario, 'cancel');
  assert.equal(parseOptions([...args, '--answer', 'cancelled']).scenario, 'question');
  assert.deepEqual(workerCancellation, {profile: 'task-worker-cancellation/v1'});
  for (const tail of [['--answer', 'paid'], ['--timeout-ms', '600001'], ['--retry', '1'], ['--scenario', 'cancel']])
    assert.throws(() => parseOptions([...args, '--scenario', 'worker-cancel', ...tail]));
  await assert.rejects(runLive({executeReal: true, scenario: 'worker-cancel', answer: 'paid'}), /explicit_real_execution_required/);
  await assert.rejects(runLive({executeReal: true, scenario: 'worker-cancel', timeoutMs: 600001}), /worker_cancel_budget_limit/);
});

// Scripted client/handle seam tests validate ONLY observer assertions. They are
// not SQLite/Core or actual Pi evidence; the separate HTTP fixture uses those.
async function scripted() {
  let sent = false, writes = 0, verifierStarts = 0;
  const now = Date.now(), entries = ['planning', 'east', 'west'].map((nodeId, index) => {
    const identity = {taskId: 'task-a', workerId: 'worker-' + index, nodeId, role: index ? 'author' : 'planner'};
    const started = {executionId: 'execution-' + index, startedAt: new Date(now - 3000 + index * 500).toISOString()};
    let resolve, phase = index ? 'running' : 'terminal';
    const completion = new Promise(done => {resolve = done;});
    const entry = trackExecution(identity, {started: Promise.resolve(started), completion, snapshot: () => ({phase})});
    const finish = (status = index === 1 ? 'cancelled' : 'completed', mutate = () => {}) => {
      phase = 'terminal'; const result = {status, reason: status === 'cancelled' ? 'pi_provider_stopped' : 'pi_agent_settled',
        stopReason: status === 'completed' ? 'end_turn' : null, outputText: 'PRIVATE_NOT_EVIDENCE', cleanup: {cleaned: true, started,
          agentExit: {observed: true, at: new Date(index ? Date.now() + 1 : now - 2500).toISOString()}}};
      mutate(result); resolve(result);
    };
    if (!index) finish(); return {entry, finish, setPhase(value) {phase = value;}};
  });
  const observations = entries.map(item => item.entry);
  const operation = {id: 'operation-a', taskId: 'task-a', workerId: 'worker-1', kind: 'worker.cancel', status: 'accepted'};
  const task = () => ({id: 'task-a', revision: sent ? 12 : 8, status: sent ? 'failed' : 'running', code: sent ? 'worker_cancelled' : null, artifactIds: []});
  const client = {getTask: async () => task(), getAudit: async () => ({attempts: 3, retryCount: 0, reworkCount: 0,
    acceptance: {status: 'pending', digest: null, evidenceIds: []}}), request: async (name, request) => {
    if (name === 'worker.cancel') {
      writes++; assert.deepEqual(request, {path: {workerId: 'worker-1'}, body: {expectedRevision: 8}, idempotencyKey: 'pi-runtime-cancel-east'});
      sent = true; entries[1].finish(); entries[2].finish(); return {...operation};
    }
    if (name === 'operation.get') {assert.equal(request.path.operationId, operation.id); return {...operation, status: 'succeeded'};}
    if (name === 'supervisor.get') return {activeWorkers: 0};
    if (name === 'task.graph') return {taskId: 'task-a', nodes: ['east', 'west', 'verify'].map((id, index) =>
      ({id, status: index === 1 ? 'completed' : 'cancelled', workerIds: index === 2 ? [] : ['worker-' + (index + 1)]}))};
    assert.equal(name, 'task.workers');
    return {nextCursor: null, items: observations.map(({identity, started}, index) => ({id: identity.workerId, taskId: identity.taskId,
      nodeId: identity.nodeId, role: identity.role, startedAt: started.startedAt, attempt: 1,
      status: index ? sent ? index === 1 ? 'cancelled' : 'completed' : 'running' : 'completed'}))};
  }};
  await Promise.resolve();
  return {client, observations, taskId: 'task-a', end: now + 1000, getVerifierStarts: () => verifierStarts,
    entries, get writes() {return writes;}, setVerifier() {verifierStarts++;}};
}
test('one Worker command uses Task revision and preserves the original completed sibling without fake acceptance', async () => {
  const f = await scripted(), result = await cancelPiWorker(f);
  assert.equal(f.writes, 1); assert.equal(result.siblingWorkerId, 'worker-2'); assert.ok(result.overlapMs > 0);
  assert.deepEqual(result.executions.map(item => item.status), ['completed', 'cancelled', 'completed']);
  assert.doesNotMatch(JSON.stringify(result), /PRIVATE_NOT_EVIDENCE/);
});
test('normal awaiting-answer is cancellable, but completed/stopping/deadline/verifier windows never issue a command', async () => {
  const waiting = await scripted(), get = waiting.client.getTask; let reads = 0;
  waiting.client.getTask = async () => {const task = await get(); if (++reads === 1) task.status = 'awaiting-answer'; return task;};
  assert.equal((await cancelPiWorker(waiting)).done.code, 'worker_cancelled');
  for (const change of [f => f.entries[1].finish('completed'), f => f.entries[2].finish(), f => f.entries[1].setPhase('stopping'),
    f => f.setVerifier(), f => f.end = Date.now() - 1]) {
    const f = await scripted(); change(f); await Promise.resolve();
    await assert.rejects(cancelPiWorker(f), /worker_cancel_window_/); assert.equal(f.writes, 0);
  }
});
test('CAS and lost response do not retry or fall back to task.cancel', async () => {
  for (const status of [409, 503]) {
    const f = await scripted(), request = f.client.request; let writes = 0;
    f.client.request = async (name, value) => {if (name === 'worker.cancel') {writes++; throw Object.assign(Error('lost'), {status});}
      return request(name, value);};
    await assert.rejects(cancelPiWorker(f)); assert.equal(writes, 1);
  }
});
test('foreign operation, wrong terminal state, extra verifier/capacity and false independent acceptance fail closed', async () => {
  for (const [name, mutate] of [
    ['worker.cancel', value => value.workerId = 'other'], ['worker.cancel', value => value.kind = 'task.cancel'],
    ['operation.get', value => value.taskId = 'other'], ['operation.get', value => value.status = 'unknown'],
    ['task.graph', value => value.nodes[2].workerIds = ['invented']], ['supervisor.get', value => value.activeWorkers = 1]]) {
    const f = await scripted(), request = f.client.request;
    f.client.request = async (operation, options) => {const value = await request(operation, options); if (operation === name) mutate(value); return value;};
    await assert.rejects(cancelPiWorker(f)); assert.equal(f.writes, 1);
  }
  const f = await scripted(); f.client.getAudit = async () => ({attempts: 4, retryCount: 0, reworkCount: 0, acceptance: {status: 'passed'}});
  await assert.rejects(cancelPiWorker(f), /worker_cancel_budget_or_acceptance/);
});
test('unclean target, substituted original identity or sibling stopped instead of completed cannot pass', async () => {
  for (const change of [f => f.observations[1].result.cleanup.cleaned = false,
    f => f.observations[1].result.cleanup.started = {...f.observations[1].started, executionId: 'substitute'},
    f => f.observations[2].result.status = 'cancelled', f => f.observations[2].result.cleanup.agentExit.at = f.observations[2].started.startedAt]) {
    const f = await scripted(), request = f.client.request;
    f.client.request = async (operation, options) => {const result = await request(operation, options); if (operation === 'worker.cancel') {
      await Promise.resolve(); change(f);} return result;};
    await assert.rejects(cancelPiWorker(f)); assert.equal(f.writes, 1);
  }
});

function retainedFixture() {
  const bytes = encode({region: 'west', status: 'paid', count: 2, netCents: 550}), file = {path: 'west.json', digest: digest(bytes), bytes: bytes.length};
  const expected = {taskId: 'task-a', workerId: 'worker-west', executionId: 'execution-west', planDigest: digest(Buffer.from('plan'))};
  const candidate = {profile: 'task-file-business/v1', ...expected, nodeId: 'west', reservationDigest: digest(Buffer.from('reservation')),
    layoutDigest: digest(Buffer.from('layout')), inputDigest: digest(Buffer.from('input')), files: [file], manifestDigest: digest(Buffer.from(JSON.stringify([file])))};
  delete candidate.executionId;
  const result = {...candidate, report: 'PRIVATE_REPORT_NOT_OUTPUT'}, record = {
    worker: {id: expected.workerId, taskId: expected.taskId, nodeId: 'west', attempt: 1, status: 'completed'},
    ticket: {...expected, nodeId: 'west', reservationDigest: candidate.reservationDigest}, executionId: expected.executionId,
    cleanup: {cleaned: true, started: {executionId: expected.executionId}}, candidate, interactionRefs: [], resultRef: 'result-original',
    resultDigest: digest(encode({candidate: result, interactionRefs: []}))};
  const task = {task: {id: expected.taskId, status: 'failed', code: 'worker_cancelled'}, workerIds: [expected.workerId], plan: {digest: expected.planDigest}};
  return {expected, record, result, task, bytes};
}
test('retained candidate is exact original worker/result binding, never a delivery or self-signed Decision', () => {
  const f = retainedFixture(), summary = retainedResult(f.record, f.result, f.task, f.expected);
  assert.equal(summary.resultRef, 'result-original'); assert.doesNotMatch(JSON.stringify(summary), /PRIVATE/);
  for (const mutate of [f => f.record.resultDigest = digest(Buffer.from('other')), f => f.result.workerId = 'foreign',
    f => f.record.ticket.planDigest = digest(Buffer.from('other')), f => f.record.ticket.startProtocol = 'unexpected',
    f => f.record.cleanup.cleaned = false, f => f.record.worker.status = 'cancelled', f => f.record.interactionRefs = [{}],
    f => f.task.decision = {id: 'unexpected'}, f => f.task.workerIds = [], f => f.record.candidate.files[0].path = '../west.json']) {
    const bad = retainedFixture(); mutate(bad); assert.throws(() => retainedResult(bad.record, bad.result, bad.task, bad.expected));
  }
});
test('bounded query-only fixture follows original projection refs and verifies blob without writing the database', t => {
  // Deliberately a SYNTHETIC minimal SQLite transport fixture, not Core evidence.
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'worker-cancel-read-fixture-')));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  fs.mkdirSync(path.join(root, 'store'), {mode: 0o700}); fs.mkdirSync(path.join(root, 'artifacts'), {mode: 0o700});
  const database = path.join(root, 'store/authority.sqlite'), f = retainedFixture(), db = new DatabaseSync(database);
  db.exec('CREATE TABLE metadata(singleton INTEGER,format TEXT); CREATE TABLE projections(kind TEXT,id TEXT,bytes BLOB,source_stream TEXT,source_sequence INTEGER,source_digest TEXT); CREATE TABLE events(stream TEXT,sequence INTEGER,digest TEXT,bytes BLOB)');
  db.prepare('INSERT INTO metadata VALUES(1,?)').run('marshal-node-task-sqlite/v6-worker-cancellation');
  const source = encode({type: 'synthetic-reader-fixture'}), sourceDigest = digest(source);
  db.prepare('INSERT INTO events VALUES(?,?,?,?)').run(f.expected.taskId, 1, sourceDigest, source);
  for (const [kind, id, value] of [['task', f.expected.taskId, f.task], ['attempt', f.expected.workerId, f.record], ['attempt', f.record.resultRef, f.result]])
    db.prepare('INSERT INTO projections VALUES(?,?,?,?,?,?)').run(kind, id, encode(value), f.expected.taskId, 1, sourceDigest);
  db.close(); fs.chmodSync(database, 0o600);
  const blob = path.join(root, 'artifacts', digest(f.bytes).slice(7)); fs.writeFileSync(blob, f.bytes, {flag: 'wx', mode: 0o600});
  const before = fs.readFileSync(database), result = readRetainedResult(root, f.expected);
  assert.deepEqual(encode(result.report), encode({region: 'west', status: 'paid', count: 2, netCents: 550}));
  assert.deepEqual(fs.readFileSync(database), before); assert.doesNotMatch(JSON.stringify(result), /PRIVATE/);
  fs.writeFileSync(blob, encode({region: 'west', status: 'paid', count: 1, netCents: 550}));
  assert.throws(() => readRetainedResult(root, f.expected), /worker_cancel_retained_business/);
});

test('checked-in Pi peer really writes west through the original bridge and stops only the original waiting east', {timeout: 20000}, async t => {
  // Original native bridge/guard with deterministic SDK, NOT a Core reducer test.
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'worker-cancel-native-fixture-'))), handles = [];
  let complete = false, releaseWest, questionEntered;
  const gate = new Promise(resolve => {releaseWest = resolve;}), asked = new Promise(resolve => {questionEntered = resolve;});
  const here = name => fileURLToPath(new URL(name, import.meta.url));
  const provider = createPiProvider({id: 'pi-fixture', executable: process.execPath, args: [here('./worker-cancel-peer.fixture.mjs')],
    bridge: {sdkEntry: here('../agent-provider-pi/fixtures/sdk/index.mjs')}});
  t.after(async () => {
    releaseWest(); for (const handle of handles) assert.equal((await handle.stop()).cleanup?.cleaned, true);
    if (complete) fs.rmSync(root, {recursive: true, force: true}); else t.diagnostic('Preserved private fixture: ' + root);
  });
  const deadline = Date.now() + 15000, scopes = [];
  for (const nodeId of ['east', 'west']) {
    const cwd = path.join(root, nodeId); fs.mkdirSync(cwd, {mode: 0o700}); fs.writeFileSync(path.join(cwd, 'sales.json'), encode(data), {flag: 'wx', mode: 0o600});
    handles.push(provider.start({cwd, deadline, prompt: '\nWORKER_CANCEL_FIXTURE=' + JSON.stringify({node: nodeId}),
      onPermission: async request => {if (nodeId === 'west') await gate; return filePermission({cwd, nodeId, role: 'author'}, request);},
      executionContext: {launch: (options, callbacks) => launchProtocol({...options, createClient: callbacks.createClient}), extraScope: value => scopes.push(value)},
      questionContext: {configuration: {profile: 'task-runtime-question/v1', policyDigest: questionPolicyDigest, maxWaitMs: 10000},
        ask(_request, {signal}) {questionEntered(); return new Promise(resolve => {
          if (signal.aborted) resolve(null); else signal.addEventListener('abort', () => resolve(null), {once: true});
        });}, acknowledge() {assert.fail('No answer may be acknowledged');}},
    }));
  }
  await asked; const east = await handles[0].stop();
  assert.equal(east.status, 'cancelled'); assert.equal(east.reason, 'pi_provider_stopped'); assert.equal(east.cleanup.cleaned, true);
  assert.equal(fs.existsSync(path.join(root, 'east/east.json')), false);
  releaseWest(); const west = await handles[1].completion;
  assert.equal(west.status, 'completed'); assert.equal(west.stopReason, 'end_turn'); assert.equal(west.cleanup.cleaned, true);
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(root, 'west/west.json'))), {region: 'west', status: 'paid', count: 2, netCents: 550});
  assert.deepEqual(scopes, []); complete = true;
});
