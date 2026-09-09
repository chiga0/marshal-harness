import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {DatabaseSync} from 'node:sqlite';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {data as sales} from '../task-qwen-live/driver.fixture.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./same-plan-repair.fixture.mjs', import.meta.url));
const feedback = '修正 west 金额，不改变 east 或原规则';
async function until(read, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {const value = await read(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded same-plan repair observation timed out'); await pause(10);}
}
function lines(file) {
  try {const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse);}
  catch (error) {if (error.code === 'ENOENT') return []; throw error;}
}
// HTTP decoders intentionally return null-prototype objects. Only normalize
// representation, not values; Attempt is a Task-wide reservation ordinal.
const request = (client, operation, options = {}) => client.request(operation, options).then(structuredClone);
const get = (client, operation, taskId) => request(client, operation, {path: {taskId}});
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const records = (snapshot, kind) => snapshot.projections.filter(row => row.kind === kind).map(decode);
const taskRecord = (snapshot, id) => records(snapshot, 'task').find(value => value.task.id === id);
function durable(f) {
  assert.ok(f.services.every(service => service.exited), 'read SQLite only after original service processes exit');
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally {db.close();}
}
function unchanged(a, b) {for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(b[key], a[key], key);}
async function fixture(t, scenario) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-same-plan-repair-')));
  const f = {root: path.join(parent, 'data'), services: [], complete: false,
    observations: () => lines(path.join(parent, 'repair-observations.jsonl'))};
  f.gone = async () => {
    for (const row of f.observations().filter(row => row.type === 'started' && row.started)) {
      for (const pid of [row.started.guardPid, row.started.agentPid]) {
        assert.ok(Number.isSafeInteger(pid) && pid > 1);
        await until(() => {try {process.kill(pid, 0); return false;} catch (error) {assert.equal(error.code, 'ESRCH'); return true;}});
      }
    }
  };
  t.after(async () => {
    if (!f.complete) t.diagnostic('Preserved same-plan repair evidence: ' + parent);
    for (const service of f.services) {await service.stop('SIGKILL'); service.checkOutput();}
    await f.gone(); if (f.complete) fs.rmSync(parent, {recursive: true, force: true});
  });
  f.launch = async mode => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {cwd: parent,
      env: {MARSHAL_REPAIR_FIXTURE: '1', MARSHAL_REPAIR_SCENARIO: scenario}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => {exited = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('close', (code, signal) => {exited = true; resolve({code, signal});});
    });
    child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL');});
    child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL');});
    const service = {get exited() {return exited;}, checkOutput() {if (token) assert.equal((stdout + stderr).includes(token), false);}, async stop(signal) {
      if (!exited) child.kill(signal); // Only the original owned ChildProcess.
      const watchdog = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 10000);
      try {await until(() => exited, 15000); return await done;} finally {clearTimeout(watchdog);}
    }};
    f.services.push(service);
    // One open, bounded beyond the production 15s custody observation limit.
    await until(() => {assert.equal(exited, false, 'original CLI failed: ' + stderr); return stdout.includes('\n');});
    const output = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(output.connectionFile));
    assert.equal(JSON.parse(fs.readFileSync(path.join(f.root, 'profile.json'))).layout, 4);
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token});
    assert.equal((await service.client.request('ready.get')).ready, true); return service;
  };
  return f;
}
async function rejected(client, f, content = true) {
  const input = await request(client, 'input.create', {idempotencyKey: 'sales-input', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: encode(sales).toString('base64')}});
  const body = {intent: '局部修正真实协议夹具：统计两个地区已付款净额，错误只修 west 并重新独立验证完整交付',
    context: {inputRefs: [input.id]}, limits: {timeoutMs: 90000, maxAttempts: 6, maxWorkers: 2}};
  const created = await request(client, 'task.create', {idempotencyKey: 'repair-create', body});
  const ready = await until(async () => {const value = await client.getTask(created.id);
    assert.ok(!['failed', 'cancelled', 'intervention'].includes(value.status), 'planning must succeed');
    return value.status === 'awaiting-approval' && value;});
  const plan = await get(client, 'task.plan', created.id);
  assert.deepEqual(plan.budget, body.limits); assert.equal(plan.repair.profile, 'task-local-repair/v1');
  assert.deepEqual(plan.nodes.map(node => node.id), ['east', 'west', 'verify']);
  const approval = {expectedRevision: ready.revision, planRevision: plan.revision, planDigest: plan.digest};
  const operation = await request(client, 'task.approve', {path: {taskId: created.id}, idempotencyKey: 'repair-approve', body: approval});
  const failed = await until(async () => {const value = await client.getTask(created.id);
    return ['failed', 'cancelled', 'completed', 'intervention'].includes(value.status) && structuredClone(value);});
  assert.equal(failed.status, 'failed'); assert.equal(failed.deadlineAt, created.deadlineAt);
  const audit = await get(client, 'task.audit', created.id), workers = (await get(client, 'task.workers', created.id)).items;
  assert.equal(audit.attempts, 4); assert.equal(audit.reworkCount, 0); assert.equal(audit.retryCount, 0);
  assert.equal(workers.length, 4); assert.deepEqual(workers.map(worker => worker.attempt).sort(), [1, 2, 3, 4]);
  assert.equal(audit.acceptance.status, 'failed'); assert.equal(audit.decision.status, 'rejected');
  assert.equal(audit.decision.digest, audit.acceptance.digest); assert.equal(audit.decision.planDigest, plan.digest);
  assert.equal(audit.decision.contentRejection !== null, content); assert.equal(failed.allowedActions.includes('repair'), content);
  const initial = f.observations().filter(row => row.type === 'verification-input' && row.taskId === created.id);
  assert.equal(initial.length, 1); assert.equal(initial[0].repairId, null);
  assert.equal(f.observations().filter(row => row.type === 'completion' && row.taskId === created.id && row.cleanup?.cleaned).length, 4);
  let evidence;
  if (content) {
    assert.deepEqual(audit.decision.contentRejection.failedAssertions, ['west-content']);
    assert.equal(audit.decision.contentRejection.policyDigest, plan.repair.policyDigest);
    assert.equal(audit.acceptance.evidenceIds.length, 1);
    evidence = await client.downloadArtifact(audit.acceptance.evidenceIds[0]);
    const proof = JSON.parse(evidence.content);
    assert.equal(typeof proof.originalReport, 'string'); assert.ok(proof.originalReport.endsWith('\n'));
    const originalReport = JSON.parse(proof.originalReport);
    assert.equal(encode(originalReport).toString() + '\n', proof.originalReport);
    assert.equal(digest(Buffer.from(proof.originalReport)), proof.reportDigest);
    assert.equal(proof.reportDigest, audit.decision.contentRejection.reportDigest);
    assert.equal(proof.binding.planDigest, plan.digest); assert.match(proof.requestDigest, /^sha256:[a-f0-9]{64}$/);
    assert.deepEqual(originalReport.binding, proof.binding); assert.equal(originalReport.nonce, proof.nonce);
    assert.deepEqual(proof.parentAssertions, [{name: 'east-content', passed: true}, {name: 'west-content', passed: false},
      {name: 'report-structure', passed: true}]);
  }
  assert.equal(audit.decision.artifacts.some(item => item.kind === 'delivery'), false);
  return {body, created, plan, approval, operation, failed, audit, workers, initial: initial[0], evidence};
}
function repairRequest(old) {return {path: {taskId: old.created.id}, idempotencyKey: 'west-repair', body: {
  expectedRevision: old.failed.revision, planDigest: old.plan.digest, decisionDigest: old.audit.decision.digest, nodeIds: ['west'], feedback}};}
async function replay(client, old, accepted) {
  assert.deepEqual(await request(client, 'task.create', {idempotencyKey: 'repair-create', body: old.body}), old.created);
  assert.deepEqual(await request(client, 'task.approve', {path: {taskId: old.created.id}, idempotencyKey: 'repair-approve', body: old.approval}), old.operation);
  if (!accepted) return;
  const value = await request(client, 'task.repair', repairRequest(old));
  assert.equal(value.replayed, true);
  assert.deepEqual({...value, currentTask: null, replayed: false}, {...accepted, currentTask: null});
  assert.deepEqual(value.currentTask, structuredClone(await client.getTask(old.created.id)));
  await assert.rejects(client.request('task.repair', {...repairRequest(old), body: {...repairRequest(old).body, feedback: feedback + ' changed'}}), {code: 'idempotency_conflict'});
}
async function acceptRepair(client, old) {
  const receipt = await request(client, 'task.repair', repairRequest(old));
  assert.equal(receipt.acceptedRevision, old.failed.revision + 1); assert.equal(receipt.replayed, false);
  assert.equal(receipt.operation.kind, 'task.repair'); assert.equal(receipt.operation.status, 'accepted');
  assert.equal(receipt.task.status, 'queued'); assert.equal(receipt.planDigest, old.plan.digest);
  assert.equal(receipt.decisionDigest, old.audit.decision.digest); assert.deepEqual(receipt.affectedNodes, ['west', 'verify']); return receipt;
}
async function complete(client, old, accepted, f) {
  const done = await until(async () => {const task = await client.getTask(old.created.id);
    if (['failed', 'cancelled', 'intervention'].includes(task.status)) assert.fail('repair unexpectedly terminal: ' + task.status);
    return task.status === 'completed' && structuredClone(task);});
  const audit = await get(client, 'task.audit', done.id), workers = (await get(client, 'task.workers', done.id)).items;
  assert.equal(audit.attempts, 6); assert.equal(audit.reworkCount, 1); assert.equal(audit.retryCount, 0);
  assert.equal(audit.acceptance.status, 'passed'); assert.equal(audit.decision.status, 'accepted'); assert.equal(audit.decision.contentRejection, null);
  assert.notEqual(audit.decision.digest, old.audit.decision.digest);
  assert.deepEqual(audit.repairs, [{repairId: accepted.repairId, decisionDigest: old.audit.decision.digest, nodeIds: ['west'], affectedNodes: ['west', 'verify'], operationId: accepted.operation.id}]);
  assert.equal((await client.request('operation.get', {path: {operationId: accepted.operation.id}})).status, 'succeeded');
  assert.equal(done.deadlineAt, old.created.deadlineAt); assert.equal(done.createdAt, old.created.createdAt);
  assert.deepEqual(await get(client, 'task.plan', done.id), old.plan);
  assert.deepEqual(workers.filter(worker => old.workers.some(before => before.id === worker.id)), old.workers);
  assert.equal(workers.length, 6); assert.deepEqual(workers.map(worker => worker.attempt).sort(), [1, 2, 3, 4, 5, 6]);
  const facts = f.observations(), inputs = facts.filter(row => row.type === 'verification-input' && row.taskId === done.id);
  assert.equal(inputs.length, 2); assert.equal(inputs[1].repairId, accepted.repairId);
  assert.deepEqual(inputs[1].manifests.map(item => item.nodeId), ['east', 'west']);
  const east = inputs[0].manifests.find(item => item.nodeId === 'east'), west = inputs[0].manifests.find(item => item.nodeId === 'west');
  assert.deepEqual(inputs[1].manifests.find(item => item.nodeId === 'east'), east);
  const nextWest = inputs[1].manifests.find(item => item.nodeId === 'west'); assert.notEqual(nextWest.workerId, west.workerId);
  assert.notEqual(nextWest.resultDigest, west.resultDigest);
  assert.deepEqual(inputs[1].upstream.map(item => item.workerId).sort(), [east.workerId, nextWest.workerId].sort());
  const starts = facts.filter(row => row.type === 'started' && row.taskId === done.id);
  assert.equal(starts.length, 6); assert.equal(starts.filter(row => row.nodeId === 'east').length, 1);
  assert.equal(new Set(starts.map(row => row.started.executionId)).size, 6);
  const directories = facts.filter(row => row.type === 'prepared' && row.nodeId === 'west'); assert.equal(directories.length, 2);
  assert.notEqual(directories[0].cwd, directories[1].cwd);
  const downloads = await Promise.all(done.artifactIds.map(id => client.downloadArtifact(id))), deliveries = downloads.filter(item => item.artifact.kind === 'delivery');
  assert.equal(deliveries.length, 1); const delivery = deliveries[0], content = JSON.parse(delivery.content);
  // Third consumer independently reduces original uploaded data, not the
  // checker's expected results, the Worker report, or any claimed pass label.
  assert.deepEqual(content.files.map(file => file.path), ['east.json', 'west.json']);
  for (const file of content.files) {
    const region = file.path.slice(0, -5), rows = sales.rows.filter(row => row.status === 'paid' && row.region === region);
    assert.deepEqual(JSON.parse(file.content), {region, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)});
  }
  const oldEvidence = await client.downloadArtifact(old.evidence.artifact.id);
  assert.deepEqual(oldEvidence, old.evidence); await replay(client, old, accepted); return {done, delivery, audit};
}

test('same-plan repair: real A rejection, retained B, one explicit repair, exact selected inputs, independent delivery and cold replay', {timeout: 90000}, async t => {
  const f = await fixture(t, 'positive'), first = await f.launch('create'), old = await rejected(first.client, f);
  const assertions = f.observations().filter(row => row.type === 'parent-assertion');
  assert.deepEqual(assertions.map(row => row.name), ['east-content', 'west-content', 'report-structure']);
  assert.deepEqual(await first.stop('SIGTERM'), {code: 0, signal: null}); await f.gone();
  const before = durable(f), second = await f.launch('open');
  assert.deepEqual(await get(second.client, 'task.audit', old.created.id), old.audit);
  assert.deepEqual(await second.client.downloadArtifact(old.evidence.artifact.id), old.evidence); await replay(second.client, old);
  for (const body of [{...repairRequest(old).body, expectedRevision: old.failed.revision - 1},
    {...repairRequest(old).body, planDigest: 'sha256:' + 'a'.repeat(64)}, {...repairRequest(old).body, decisionDigest: 'sha256:' + 'b'.repeat(64)},
    {...repairRequest(old).body, nodeIds: ['verify']}]) {
    await assert.rejects(second.client.request('task.repair', {...repairRequest(old), idempotencyKey: 'invalid-' + Object.keys(body).length + digest(encode(body)).slice(7, 15), body}), error => error.status === 409);
  }
  const accepted = await acceptRepair(second.client, old), result = await complete(second.client, old, accepted, f);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null}); await f.gone(); const after = durable(f);
  for (const row of before.receipts) assert.deepEqual(after.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
  const retainedBefore = taskRecord(before, old.created.id).selectedResults.east;
  assert.deepEqual(taskRecord(after, old.created.id).selectedResults.east, retainedBefore);
  assert.equal(records(after, 'budget').flatMap(value => value.active).length, 0);
  const starts = f.observations().filter(row => row.type === 'started'), third = await f.launch('open');
  assert.deepEqual(structuredClone(await third.client.getTask(old.created.id)), result.done); await replay(third.client, old, accepted);
  assert.deepEqual((await third.client.downloadArtifact(result.delivery.artifact.id)).content, result.delivery.content);
  assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null}); unchanged(after, durable(f));
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.complete = true;
});

for (const scenario of ['structure', 'bad-frame']) test(`same-plan repair: ${scenario} failure is not an eligible content rejection`, {timeout: 45000}, async t => {
  const f = await fixture(t, scenario), service = await f.launch('create'), old = await rejected(service.client, f, false);
  if (scenario === 'structure') assert.deepEqual(f.observations().filter(row => row.type === 'parent-assertion').map(row => row.name),
    ['east-content', 'west-content', 'report-structure']);
  else assert.equal(f.observations().filter(row => row.type === 'parent-assertion').length, 0);
  await assert.rejects(service.client.request('task.repair', repairRequest(old)), error => error.status === 409);
  assert.deepEqual(await get(service.client, 'task.audit', old.created.id), old.audit);
  assert.deepEqual(await service.stop('SIGTERM'), {code: 0, signal: null}); await f.gone();
  const saved = durable(f); assert.equal(saved.receipts.some(row => row.operation === 'task.repair'), false);
  assert.equal(taskRecord(saved, old.created.id).attempts, 4); f.complete = true;
});

test('same-plan repair: cancel after admission fences the new A, retains B and never starts another verifier', {timeout: 60000}, async t => {
  const f = await fixture(t, 'cancel'), service = await f.launch('create'), old = await rejected(service.client, f);
  const accepted = await acceptRepair(service.client, old);
  const launched = await until(() => f.observations().find(row => row.type === 'started' && row.repairId === accepted.repairId && row.nodeId === 'west'));
  // Provider started resolution precedes the reducer's started transaction.
  // Observe that public fact before capturing the cancel CAS revision.
  await until(async () => (await service.client.request('worker.get', {path: {workerId: launched.workerId}})).status === 'running');
  const cancellation = {path: {taskId: old.created.id}, idempotencyKey: 'cancel-repair', body: {expectedRevision: (await service.client.getTask(old.created.id)).revision}};
  const operation = await request(service.client, 'task.cancel', cancellation);
  await until(async () => (await service.client.getTask(old.created.id)).status === 'cancelled');
  assert.equal((await service.client.request('operation.get', {path: {operationId: operation.id}})).status, 'succeeded');
  assert.equal((await service.client.request('operation.get', {path: {operationId: accepted.operation.id}})).status, 'failed');
  const audit = await get(service.client, 'task.audit', old.created.id); assert.equal(audit.attempts, 5); assert.equal(audit.reworkCount, 1);
  assert.equal(f.observations().filter(row => row.type === 'verification-input').length, 1);
  assert.equal(f.observations().filter(row => row.type === 'started' && row.nodeId === 'east').length, 1);
  await replay(service.client, old, accepted);
  assert.deepEqual(await service.stop('SIGTERM'), {code: 0, signal: null}); await f.gone(); const before = durable(f);
  assert.equal(records(before, 'budget').flatMap(value => value.active).length, 0);
  const reopened = await f.launch('open'); assert.equal((await reopened.client.getTask(old.created.id)).status, 'cancelled');
  await replay(reopened.client, old, accepted); assert.deepEqual(await request(reopened.client, 'task.cancel', cancellation), operation);
  assert.deepEqual(await reopened.stop('SIGTERM'), {code: 0, signal: null}); unchanged(before, durable(f)); f.complete = true;
});

for (const point of ['before', 'after']) test(`same-plan repair: ${point} COMMIT service crash preserves atomic receipt/choices/budget and never respawns old commands`, {timeout: 60000}, async t => {
  const f = await fixture(t, point), first = await f.launch('create'), old = await rejected(first.client, f);
  let responded = false;
  const pending = first.client.request('task.repair', repairRequest(old)).then(value => {responded = true; return value;}, error => error);
  const marker = await until(() => f.observations().find(row => row.type === 'barrier'));
  assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'}); await pending; assert.equal(responded, false); await f.gone();
  const before = durable(f), task = taskRecord(before, old.created.id);
  assert.equal(task.attempts, 4); assert.deepEqual(task.limits, old.body.limits); assert.equal(task.task.deadlineAt, old.created.deadlineAt);
  assert.equal(task.task.status, point === 'before' ? 'failed' : 'queued'); assert.equal(task.reworkCount, point === 'before' ? 0 : 1);
  assert.equal(before.receipts.filter(row => row.operation === 'task.repair').length, point === 'before' ? 0 : 1);
  assert.equal(records(before, 'budget').flatMap(value => value.active).length, 0);
  assert.equal(task.selectedResults.east.workerId, old.initial.manifests.find(item => item.nodeId === 'east').workerId);
  if (point === 'after') {
    assert.equal(task.selectedResults.west, null); assert.equal(task.activeRepair.repairId, marker.receipt.repairId);
    assert.equal(digest(Buffer.from(before.projections.find(row => row.id === old.created.id).bytes, 'hex')), marker.taskDigest);
    assert.deepEqual(decode(before.receipts.find(row => row.operation === 'task.repair')), marker.receipt);
  } else assert.equal(task.selectedResults.west.workerId, old.initial.manifests.find(item => item.nodeId === 'west').workerId);
  const starts = f.observations().filter(row => row.type === 'started'), second = await f.launch('open');
  const stopped = await until(async () => {const value = await second.client.getTask(old.created.id); return value.status === 'failed' && structuredClone(value);});
  assert.equal((await get(second.client, 'task.audit', old.created.id)).attempts, 4);
  if (point === 'after') {
    assert.equal(stopped.code, 'service_interrupted'); await replay(second.client, old, marker.receipt);
    assert.equal((await second.client.request('operation.get', {path: {operationId: marker.receipt.operation.id}})).status, 'failed');
  } else {assert.deepEqual(stopped, old.failed); await replay(second.client, old);}
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
  assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null}); const settled = durable(f);
  assert.equal(settled.outbox.filter(row => row.status !== 'observed').length, 0);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts);
  const third = await f.launch('open'); assert.deepEqual(structuredClone(await third.client.getTask(old.created.id)), stopped);
  if (point === 'after') await replay(third.client, old, marker.receipt);
  assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null}); unchanged(settled, durable(f)); f.complete = true;
});
