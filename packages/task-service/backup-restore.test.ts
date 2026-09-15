import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fixture, manifest, copyOffline, durable, completeTeam, events, upload} from './backup-restore.fixture.mjs';

async function originalFacts(client, original, history) {
  const taskId = original.created.id;
  assert.deepEqual(await client.getTask(taskId), original.completed);
  assert.deepEqual(await client.request('task.plan', {path: {taskId}}), original.plan);
  assert.deepEqual(await client.getAudit(taskId), original.audit);
  assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
  assert.deepEqual(await client.approveTask(taskId, original.approval, original.key + '-approve'), original.receipt);
  assert.equal((await client.request('operation.get', {path: {operationId: original.receipt.id}})).status, 'succeeded');
  await assert.rejects(client.createTask({...original.body, intent: 'changed'}, original.key + '-create'), {code: 'idempotency_conflict'});
  assert.deepEqual(await events(client, taskId), history);
  for (const download of original.downloads) assert.deepEqual(await client.downloadArtifact(download.artifact.id), download);
}

test('same-version offline full snapshot restores original HTTP facts and delivers a new team; missing data and a second writer fail closed', {timeout: 60000}, async t => {
  const f = await fixture(t), source = path.join(f.parent, 'original'), backup = path.join(f.parent, 'backup'), restored = path.join(f.parent, 'restored');
  const first = await f.launch(source, 'create'), input = await upload(first.client), original = await completeTeam(first.client, input.id, 'original');
  const taskId = original.created.id, history = await events(first.client, taskId);
  const head = await first.client.request('task.events', {path: {taskId}, query: {limit: 3}});
  assert.ok(history.length > 6); assert.ok(head.nextCursor);
  const originalStarts = f.observations().filter(value => value.type === 'started');
  assert.equal(originalStarts.length, 4); assert.equal(originalStarts.filter(value => value.kind === 'verification').length, 1);
  assert.ok(f.observations().filter(value => value.type === 'completion').every(value => value.cleanup?.cleaned === true));
  // Only this original owned process is stopped; a private file copy is never
  // made while a service, verifier or its guard can still mutate the source.
  await first.stop(); const before = durable(source, () => f.offline(source));
  assert.equal(JSON.parse(fs.readFileSync(path.join(source, 'profile.json'))).layout, 2);
  const snapshot = copyOffline(source, backup, () => f.offline(source));
  assert.deepEqual(snapshot.filter(value => value.path && !value.path.includes(path.sep)).map(value => value.path).sort(),
    ['artifacts', 'connections', 'custody', 'executions', 'profile.json', 'store']);
  assert.ok(snapshot.some(value => value.path.startsWith('connections' + path.sep)));
  assert.ok(snapshot.some(value => value.path.endsWith('.observation.json')));
  assert.ok(snapshot.some(value => value.path === path.join('store', 'authority.sqlite')));
  copyOffline(backup, restored, () => f.offline(backup));
  const second = await f.launch(restored, 'open');
  assert.notEqual(second.connectionFile, first.connectionFile);
  await originalFacts(second.client, original, history);
  assert.deepEqual([...head.items, ...await events(second.client, taskId, head.nextCursor)], history, 'original cursor survives path and process changes');
  assert.deepEqual(await upload(second.client), input, 'original input receipt remains exact');
  assert.deepEqual(f.observations().filter(value => value.type === 'started'), originalStarts, 'restore never redispatches an old attempt');
  assert.equal((await second.client.request('supervisor.get')).activeWorkers, 0);
  assert.equal((await second.client.request('ready.get')).ready, true);

  let passedNegatives = 0;
  await t.test('same physical restored root rejects a second writer without damaging the current owner', async () => {
    await f.launch(restored, 'open', false);
    assert.deepEqual(await second.client.getTask(taskId), original.completed);
    assert.equal((await second.client.request('ready.get')).ready, true);
    assert.deepEqual(f.observations().filter(value => value.type === 'started'), originalStarts);
    passedNegatives++;
  });
  assert.equal(passedNegatives, 1, 'preserve failed fixture instead of continuing after a failed nested case');
  await second.stop(); const after = durable(restored, () => f.offline(restored));
  assert.equal(after.metadata.store_id, before.metadata.store_id);
  assert.equal(after.metadata.format, before.metadata.format); assert.equal(after.metadata.version, before.metadata.version);
  assert.equal(after.metadata.generation, before.metadata.generation + 1);
  for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox'])
    assert.deepEqual(after[key], before[key], 'restore may claim a new owner, not rewrite/refund/replay original facts: ' + key);

  await t.test('missing authority database refuses open and cannot silently create a fresh store', async () => {
    const broken = path.join(f.parent, 'missing-db'); copyOffline(backup, broken, () => f.offline(backup));
    const database = path.join(broken, 'store', 'authority.sqlite'); fs.unlinkSync(database);
    const damaged = manifest(broken); await f.launch(broken, 'open', false);
    assert.equal(fs.existsSync(database), false); assert.deepEqual(manifest(broken), damaged);
    passedNegatives++;
  });
  assert.equal(passedNegatives, 2);
  await t.test('missing committed blob reports HTTP 503 without reconstructing or erasing acceptance', async () => {
    const broken = path.join(f.parent, 'missing-blob'); copyOffline(backup, broken, () => f.offline(backup));
    const blob = path.join(broken, 'artifacts', original.delivery.artifact.digest.slice('sha256:'.length));
    assert.ok(fs.statSync(blob).isFile()); fs.unlinkSync(blob);
    // The present contract validates bytes on read, not an invented eager
    // whole-depot boot scan. A valid SQL root may open, but download must fail.
    const service = await f.launch(broken, 'open');
    assert.deepEqual(await service.client.getTask(taskId), original.completed);
    await assert.rejects(service.client.request('artifact.get', {path: {artifactId: original.delivery.artifact.id}}), {code: 'application_unavailable', status: 503});
    await assert.rejects(service.client.downloadArtifact(original.delivery.artifact.id), {code: 'application_unavailable', status: 503});
    assert.deepEqual(await service.contentFailure(original.delivery.artifact.id), {code: 'application_unavailable', status: 503});
    assert.equal(fs.existsSync(blob), false); assert.deepEqual(f.observations().filter(value => value.type === 'started'), originalStarts);
    await service.stop();
    const unchanged = durable(broken, () => f.offline(broken));
    for (const key of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(unchanged[key], before[key]);
    passedNegatives++;
  });
  assert.equal(passedNegatives, 3);

  // The backup and original remain offline forever in this fixture. This is
  // not an anti-fork claim: independent copied SQLite roots are not one lock.
  const resumed = await f.launch(restored, 'open'); await originalFacts(resumed.client, original, history);
  const next = await completeTeam(resumed.client, input.id, 'after-restore'); assert.notEqual(next.created.id, taskId);
  assert.equal(f.observations().filter(value => value.type === 'started' && value.taskId === next.created.id).length, 4);
  assert.deepEqual(f.observations().filter(value => value.type === 'started' && value.taskId === taskId), originalStarts);
  await originalFacts(resumed.client, original, history); await resumed.stop();
  assert.deepEqual(manifest(backup), snapshot, 'unused backup bytes and permissions remain unchanged');
  assert.deepEqual(manifest(source), snapshot, 'source is not reopened or mutated after taking the offline snapshot');
  t.diagnostic(JSON.stringify({profile: 'node-execution-custody/v1', originalAttempts: 4, redispatchedAttempts: 0, newTaskAttempts: 4,
    copiedFiles: snapshot.filter(value => value.kind === 'file').length, restoredHistoryEvents: history.length,
    evidence: 'same-version same-host offline full-copy; not online backup, power-loss simulation or cloned-root fencing'}));
  f.completed = true;
});
