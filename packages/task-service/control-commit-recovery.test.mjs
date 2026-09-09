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
import {custodyDigest, verifyObservation} from '../agent-runtime/custody-contract.mjs';

const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
const config = fileURLToPath(new URL('./control-commit-recovery.fixture.mjs', import.meta.url));
const data = {rows: [
  {region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100},
]};
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
async function until(observe, ms = 12000) {
  const end = Date.now() + ms;
  for (;;) { const value = await observe(); if (value) return value;
    assert.ok(Date.now() < end, 'bounded control-commit observation timed out'); await pause(10); }
}
function lines(file) {
  try { const text = fs.readFileSync(file, 'utf8'); return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
}
const decode = row => JSON.parse(Buffer.from(row.bytes, 'hex').toString('utf8'));
const projections = (state, kind) => state.projections.filter(row => row.kind === kind).map(decode);
const taskRecord = (state, id) => projections(state, 'task').find(row => row.task.id === id);
const workers = (state, id) => projections(state, 'attempt').filter(row => row.worker?.taskId === id);
const capacity = state => projections(state, 'budget').flatMap(row => row.active);
const events = (state, id) => state.events.filter(row => row.stream === id).map(row => decode(row).payload);
function durable(f) {
  assert.ok(f.services.every(service => service.exited), 'query-only inspection after all original service handles exit');
  const db = new DatabaseSync(path.join(f.root, 'store/authority.sqlite'), {readOnly: true, timeout: 100, allowExtension: false});
  try {
    db.exec('PRAGMA query_only=ON'); assert.equal(db.prepare('PRAGMA integrity_check').get().integrity_check, 'ok');
    assert.deepEqual(db.prepare('PRAGMA foreign_key_check').all(), []);
    const rows = sql => db.prepare(sql).all().map(row => Object.fromEntries(Object.entries(row).map(([key, value]) =>
      [key, value instanceof Uint8Array ? Buffer.from(value).toString('hex') : value])));
    return {generation: db.prepare('SELECT generation FROM metadata').get().generation,
      events: rows('SELECT * FROM events ORDER BY stream,sequence'), heads: rows('SELECT * FROM heads ORDER BY stream'),
      projections: rows('SELECT * FROM projections ORDER BY kind,id'), receipts: rows('SELECT * FROM receipts ORDER BY scope,operation,key_digest'),
      outbox: rows('SELECT * FROM outbox ORDER BY id')};
  } finally { db.close(); }
}
async function fixture(t, point) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-control-commit-')));
  const f = {root: path.join(parent, 'data'), services: [], complete: false, keep: false};
  f.observations = () => lines(path.join(parent, 'custody-observations.jsonl'));
  f.barriers = () => lines(path.join(parent, 'control-commit.jsonl'));
  f.gone = async () => {
    for (const row of f.observations().filter(row => row.type === 'started' && row.started))
      for (const pid of [row.started.guardPid, row.started.agentPid]) {
        assert.ok(Number.isSafeInteger(pid) && pid > 1);
        await until(() => {try {process.kill(pid, 0); return false;} catch (error) {assert.equal(error.code, 'ESRCH'); return true;}});
      }
  };
  t.after(async () => {
    for (const service of f.services) {await service.stop('SIGKILL'); service.checkOutput();}
    await f.gone();
    if (f.complete && !f.keep) fs.rmSync(parent, {recursive: true, force: true});
    else t.diagnostic('Preserved private failed/unresolved control-commit fixture: ' + parent);
  });
  f.launch = async (mode, fault = 'none') => {
    const child = spawn(process.execPath, [cli, '--root', f.root, '--mode', mode, '--config', config], {cwd: parent,
      env: {MARSHAL_CONTROL_COMMIT_FIXTURE: '1', MARSHAL_CONTROL_COMMIT_POINT: fault,
        MARSHAL_CONTROL_COMMIT_CAPACITY: point.startsWith('cancel-') ? '2' : '1'}, stdio: ['ignore', 'pipe', 'pipe']});
    let stdout = '', stderr = '', exited = false, token;
    const done = new Promise(resolve => {
      child.once('error', () => {exited = true; resolve({code: null, signal: 'spawn-error'});});
      child.once('exit', (code, signal) => {exited = true; resolve({code, signal});});
    });
    child.stdout.on('data', bytes => {stdout += bytes; if (stdout.length > 16384) child.kill('SIGKILL');});
    child.stderr.on('data', bytes => {stderr += bytes; if (stderr.length > 16384) child.kill('SIGKILL');});
    const service = {child, done, get exited() {return exited;}, checkOutput() {if (token) assert.equal((stdout + stderr).includes(token), false);},
      async stop(signal) {
        if (!exited) child.kill(signal); // Only this fixture's ORIGINAL handle.
        const timer = setTimeout(() => {if (!exited) child.kill('SIGKILL');}, 10000);
        try {await until(() => exited); return await done;} finally {clearTimeout(timer);}
      }};
    f.services.push(service);
    await until(() => {assert.equal(exited, false, 'original CLI startup failed: ' + stderr); return stdout.includes('\n');}, 20000);
    const output = JSON.parse(stdout.slice(0, stdout.indexOf('\n'))), connection = JSON.parse(fs.readFileSync(output.connectionFile));
    token = connection.token; service.client = new TaskClient({baseURL: connection.url, token}); return service;
  };
  return f;
}
async function approved(client, inputId, intent, key) {
  const body = {intent, context: {inputRefs: [inputId]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await client.createTask(body, key + '-create');
  const preview = await until(async () => {const value = await client.getTask(created.id); return value.status === 'awaiting-approval' && value;});
  const approval = {expectedRevision: preview.revision, planRevision: preview.plan.revision, planDigest: preview.plan.digest};
  const operation = await client.approveTask(created.id, approval, key + '-approve');
  return {body, created, approval, operation, key};
}
async function replay(client, old) {
  assert.deepEqual(await client.createTask(old.body, old.key + '-create'), old.created);
  assert.deepEqual(await client.approveTask(old.created.id, old.approval, old.key + '-approve'), old.operation);
  await assert.rejects(client.createTask({...old.body, intent: 'changed'}, old.key + '-create'), {code: 'idempotency_conflict'});
}
async function healthy(client, inputId) {
  const next = await approved(client, inputId, 'control-commit next healthy team', 'next');
  const completed = await until(async () => {const value = await client.getTask(next.created.id); return value.status === 'completed' && value;});
  const downloads = await Promise.all(completed.artifactIds.map(id => client.downloadArtifact(id)));
  const delivery = downloads.find(value => value.artifact.kind === 'delivery'); assert.ok(delivery);
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)), expected);
  assert.equal((await client.request('task.audit', {path: {taskId: next.created.id}})).attempts, 4);
  return {next, completed, delivery};
}
const unchanged = (a, b) => {for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(b[key], a[key], key);};

for (const kind of ['reservation', 'binding', 'cancel']) for (const side of ['before', 'after'])
  test(`control ${kind} ${side} COMMIT: original crash preserves budget, exact authority and no redispatch`, {timeout: 60000}, async t => {
    const point = kind + '-' + side, f = await fixture(t, point), first = await f.launch('create', point);
    const input = await first.client.request('input.create', {idempotencyKey: 'sales-input', body: {
      name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
    const old = await approved(first.client, input.id, 'custody interrupted authors', 'old');
    let cancelRequest, pending, replied = false;
    if (kind === 'cancel') {
      await until(async () => (await first.client.request('task.workers', {path: {taskId: old.created.id}})).items
        .filter(worker => worker.role === 'author' && worker.startedAt && worker.status === 'running').length === 2);
      cancelRequest = {path: {taskId: old.created.id}, body: {expectedRevision: (await first.client.getTask(old.created.id)).revision}, idempotencyKey: 'lost-cancel'};
      pending = first.client.request('task.cancel', cancelRequest).then(value => {replied = true; return value;}, error => error);
    }
    const marker = await until(() => f.barriers()[0]);
    assert.equal(marker.point, point); assert.equal(marker.taskId, old.created.id);
    assert.deepEqual(await first.stop('SIGKILL'), {code: null, signal: 'SIGKILL'});
    if (pending) {assert.equal((await pending).code, 'client_transport_error'); assert.equal(replied, false);}
    await f.gone(); assert.equal(f.barriers().length, 1);
    const before = durable(f), prior = taskRecord(before, old.created.id), oldStarts = f.observations().filter(row => row.type === 'started');
    const oldWorkers = workers(before, old.created.id), active = oldWorkers.filter(row => row.worker.status !== 'completed');
    assert.deepEqual(prior.limits, old.body.limits); assert.equal(prior.task.deadlineAt, old.created.deadlineAt);
    assert.equal(prior.retryCount, 0); assert.equal(prior.reworkCount, 0); assert.deepEqual(prior.task.artifactIds, []);
    assert.equal(projections(before, 'attempt').filter(row => row.type === 'independent-verification').length, 0);
    if (side === 'after') assert.equal(digest(Buffer.from(before.projections.find(row => row.id === old.created.id).bytes, 'hex')), marker.taskDigest);
    assert.equal(events(before, old.created.id).filter(event => event.type === (kind === 'reservation' ? 'worker.reserved' : kind === 'binding' ?
      'worker.custody-permitted' : 'task.cancel') && (kind === 'cancel' || event.workerId === marker.workerId)).length, side === 'after' ? 1 : 0);
    if (kind !== 'cancel') {
      assert.equal(oldStarts.length, 1); assert.equal(oldStarts[0].role, 'planner');
      assert.equal(prior.attempts, kind === 'reservation' && side === 'before' ? 1 : 2);
      assert.equal(active.length, prior.attempts - 1); assert.equal(capacity(before).length, active.length);
      const worker = active[0];
      if (worker) {
        assert.equal(worker.worker.id, marker.workerId); assert.equal(worker.worker.status, 'queued');
        assert.equal(worker.executionId, null); assert.equal(worker.cleanup, null);
        assert.equal(before.outbox.find(row => row.id === worker.ticket.commandId).status, 'unknown');
      }
      const bound = kind === 'binding' && side === 'after';
      assert.equal(!!worker?.custody, bound);
      assert.equal(before.receipts.filter(row => row.scope === marker.workerId && row.operation === 'execution.custody-binding').length, bound ? 1 : 0);
      if (bound) assert.deepEqual(worker.custody.descriptor, marker.value);
    } else {
      assert.equal(prior.attempts, 3); assert.equal(oldStarts.length, 3); assert.equal(active.length, 2); assert.equal(capacity(before).length, 2);
      assert.equal(prior.task.status, side === 'after' ? 'cancelling' : 'running');
      assert.equal(!!prior.cancelIntent, side === 'after');
      const receipts = before.receipts.filter(row => row.operation === 'task.cancel');
      assert.equal(receipts.length, side === 'after' ? 1 : 0);
      assert.equal(before.outbox.filter(row => JSON.parse(Buffer.from(row.payload, 'hex').toString()).action === 'cancel').length, receipts.length);
      if (side === 'after') assert.deepEqual(decode(receipts[0]), marker.value);
    }
    const second = await f.launch('open'); await replay(second.client, old);
    let terminal, recovered = kind === 'cancel' || kind === 'binding' && side === 'after';
    if (recovered) {
      terminal = kind === 'cancel' && side === 'after' ? 'cancelled' : 'failed';
      await until(async () => (await second.client.getTask(old.created.id)).status === terminal);
      assert.equal((await second.client.request('ready.get')).ready, true);
      assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
      if (kind === 'cancel') {
        if (side === 'after') {
          assert.deepEqual(encode(await second.client.request('task.cancel', cancelRequest)), encode(marker.value));
          assert.equal((await second.client.request('operation.get', {path: {operationId: marker.value.id}})).status, 'succeeded');
          await assert.rejects(second.client.request('task.cancel', {...cancelRequest, body: {expectedRevision: 1}}), {code: 'idempotency_conflict'});
        } else {
          // No committed cancellation exists. Do not fabricate its lost receipt
          // or refresh the caller's CAS automatically after recovery failed Task.
          await assert.rejects(second.client.request('task.cancel', cancelRequest), {code: 'revision_conflict'});
        }
      }
    } else {
      if (active.length) await until(async () => (await second.client.getTask(old.created.id)).status === 'intervention');
      await assert.rejects(second.client.request('ready.get'), {code: 'not_ready'});
      await assert.rejects(second.client.createTask(old.body, 'fresh-blocked'), {code: 'not_ready'});
      assert.equal((await second.client.request('supervisor.get')).activeWorkers, active.length);
      if (!active.length) {
        // Rollback left no reservation/Agent. An EXPLICIT new HTTP cancellation
        // can fence old pending commands; recovery itself never replays them.
        const task = await second.client.getTask(old.created.id);
        const op = await second.client.request('task.cancel', {path: {taskId: task.id}, body: {expectedRevision: task.revision}, idempotencyKey: 'explicit-no-worker-cancel'});
        await until(async () => (await second.client.getTask(task.id)).status === 'cancelled');
        assert.equal((await second.client.request('operation.get', {path: {operationId: op.id}})).status, 'succeeded');
        assert.equal((await second.client.request('ready.get')).ready, true); recovered = true; terminal = 'cancelled';
      } else {
        // Reservation-before-binding is explicitly unresolved in ADR0089. An
        // empty directory/no started event is NOT an authority cleanup proof.
        f.keep = true; terminal = 'intervention';
      }
    }
    const result = recovered ? await healthy(second.client, input.id) : null;
    const finalOld = await second.client.getTask(old.created.id);
    assert.equal(finalOld.status, terminal); assert.equal(finalOld.deadlineAt, old.created.deadlineAt); assert.deepEqual(finalOld.artifactIds, []);
    const audit = await second.client.request('task.audit', {path: {taskId: old.created.id}});
    assert.equal(audit.attempts, prior.attempts); assert.equal(audit.retryCount, 0); assert.equal(audit.reworkCount, 0);
    assert.deepEqual(f.observations().filter(row => row.type === 'started' && row.taskId === old.created.id), oldStarts);
    assert.deepEqual(await second.stop('SIGTERM'), {code: 0, signal: null});
    const settled = durable(f); assert.equal(settled.generation, before.generation + 1);
    for (const row of before.receipts) assert.deepEqual(settled.receipts.find(value => value.scope === row.scope && value.operation === row.operation && value.key_digest === row.key_digest), row);
    for (const row of active) {
      const current = workers(settled, old.created.id).find(value => value.worker.id === row.worker.id);
      assert.deepEqual(current.ticket, row.ticket); assert.equal(current.executionId, row.executionId);
      if (row.custody) {
        const observation = JSON.parse(fs.readFileSync(path.join(f.root, 'custody', row.custody.descriptor.custodyId + '.observation.json')));
        assert.equal(verifyObservation(row.custody.descriptor, observation), true);
        assert.equal(current.custody.settledDigest, custodyDigest(observation)); assert.deepEqual(current.cleanup, observation.payload.cleanup);
        assert.equal(current.cleanup.cleaned, true);
        if (kind === 'binding') {assert.equal(observation.payload.permitReceived, false); assert.equal(current.cleanup.scope, 'none-start');}
        assert.equal(events(settled, old.created.id).filter(event => event.type === 'worker.cleanup-reconciled' && event.workerId === row.worker.id).length, 1);
      } else {assert.equal(current.cleanup, null); assert.equal(current.worker.status, 'queued');}
    }
    assert.equal(capacity(settled).length, recovered ? 0 : active.length);
    const starts = f.observations().filter(row => row.type === 'started'), third = await f.launch('open');
    await replay(third.client, old); assert.deepEqual(await third.client.getTask(old.created.id), finalOld);
    if (kind === 'cancel' && side === 'after') assert.deepEqual(encode(await third.client.request('task.cancel', cancelRequest)), encode(marker.value));
    if (result) {
      await replay(third.client, result.next); assert.deepEqual(await third.client.getTask(result.next.created.id), result.completed);
      assert.deepEqual((await third.client.downloadArtifact(result.delivery.artifact.id)).content, result.delivery.content);
    } else await assert.rejects(third.client.request('ready.get'), {code: 'not_ready'});
    assert.deepEqual(await third.stop('SIGTERM'), {code: 0, signal: null}); unchanged(settled, durable(f));
    assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts); f.complete = true;
  });
