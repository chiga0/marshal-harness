import {supportsNode} from '../task-store/runtime.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {DatabaseSync} from 'node:sqlite';
import {fixture, durable, completeTeam, events, upload} from './backup-restore.fixture.mjs';
import {launchFull} from './storage-full.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';

const tables = ['events', 'heads', 'projections', 'receipts', 'outbox'];
const value = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const sameRef = (row, receipt) => row.source_stream === receipt.source_stream &&
  row.source_sequence === receipt.source_sequence && row.source_digest === receipt.source_digest;
function transaction(snapshot, attempt) {
  const content = Buffer.from(attempt.body.contentBase64, 'base64'), hash = digest(content);
  const receipts = snapshot.receipts.filter(row => row.scope === 'tasks' && row.operation === 'input.create' && row.key_digest === digest(encode(attempt.key)));
  const manifests = snapshot.projections.filter(row => row.kind === 'artifact' && value(row).artifact?.name === attempt.body.name);
  const blobs = snapshot.projections.filter(row => row.kind === 'artifact' && row.id === 'digest-' + hash.slice(7));
  const created = snapshot.events.filter(row => value(row).payload?.type === 'input.created' && value(row).payload.artifact.name === attempt.body.name);
  if (!receipts.length) {
    assert.equal(attempt.receipt, undefined, 'acknowledged receipt cannot disappear');
    assert.deepEqual([manifests.length, blobs.length, created.length], [0, 0, 0], 'no partial authority after FULL'); return null;
  }
  assert.equal(receipts.length, 1); const receipt = receipts[0], artifact = value(receipt);
  assert.equal(receipt.request_digest, digest(encode({operation: 'input.create', taskId: null, body: attempt.body})));
  assert.equal(artifact.name, attempt.body.name); assert.equal(artifact.kind, 'input'); assert.equal(artifact.status, 'ready');
  assert.equal(artifact.digest, hash); assert.equal(artifact.bytes, content.length);
  if (attempt.receipt) assert.deepEqual(encode(artifact), encode(attempt.receipt));
  assert.deepEqual([manifests.length, blobs.length, created.length], [1, 1, 1]);
  assert.ok(sameRef(manifests[0], receipt) && sameRef(blobs[0], receipt));
  assert.deepEqual(value(manifests[0]), {type: 'manifest', owner: 'local-operator', artifact});
  assert.deepEqual(value(blobs[0]), {type: 'blob', digest: hash, bytes: content.length});
  const event = created[0]; assert.equal(event.stream, receipt.source_stream); assert.equal(event.sequence, receipt.source_sequence);
  assert.equal(event.digest, receipt.source_digest); assert.deepEqual(value(event).payload.artifact, artifact);
  assert.deepEqual(snapshot.heads.filter(row => row.stream === artifact.id), [{stream: artifact.id, sequence: event.sequence, digest: event.digest}]);
  return artifact;
}
async function originalFacts(client, original, history, input) {
  assert.deepEqual(await client.getTask(original.created.id), original.completed);
  assert.deepEqual(await client.getAudit(original.created.id), original.audit);
  assert.deepEqual(await client.request('task.plan', {path: {taskId: original.created.id}}), original.plan);
  assert.deepEqual(await client.createTask(original.body, original.key + '-create'), original.created);
  assert.deepEqual(await client.approveTask(original.created.id, original.approval, original.key + '-approve'), original.receipt);
  assert.deepEqual(await events(client, original.created.id), history); assert.deepEqual(await upload(client), input);
  for (const artifact of original.downloads) assert.deepEqual(await client.downloadArtifact(artifact.artifact.id), artifact);
}

test('original SQLite page-limit FULL rolls back HTTP authority and cold reopen preserves receipts without OS ENOSPC', {timeout: 65000}, async t => {
  assert.ok(supportsNode());
  const f = await fixture(t), root = path.join(f.parent, 'data'), initial = await f.launch(root, 'create');
  const input = await upload(initial.client), original = await completeTeam(initial.client, input.id, 'before-sqlite-full');
  const history = await events(initial.client, original.created.id); await initial.stop();
  const starts = f.observations().filter(row => row.type === 'started'); assert.equal(starts.length, 4);
  const before = durable(root, () => f.offline(root));
  const limited = await launchFull(f, root); limited.arm(); const attempts = [];
  const full = () => limited.observations().some(row => row.type === 'sqlite-error' && row.code === 'ERR_SQLITE_ERROR' &&
    row.errcode === 13 && row.errstr === 'database or disk is full');
  for (let index = 0; index < 24 && !full(); index++) {
    const attempt = {key: 'sqlite-full-' + index, body: {name: 'sqlite-full-' + index + '.bin',
      mediaType: 'application/octet-stream', contentBase64: Buffer.alloc(1024, index + 33).toString('base64')}};
    attempts.push(attempt);
    try {attempt.receipt = await limited.client.request('input.create', {body: attempt.body, idempotencyKey: attempt.key});}
    catch (error) {attempt.failure = {code: error.code, status: error.status};}
  }
  assert.ok(full(), 'must observe real SQLite FULL, not infer it from HTTP 503');
  assert.ok(attempts.some(attempt => attempt.failure?.status === 503));
  assert.equal(limited.observations().filter(row => row.type === 'page-limit').length, 1);
  const stopped = await limited.stop();
  const after = durable(root, () => f.offline(root)), committed = attempts.map(attempt => transaction(after, attempt));
  assert.ok(committed.some(row => row === null), 'FULL must leave an uncommitted original request');
  for (const row of after.events) assert.equal(digest(Buffer.from(row.bytes, 'hex')), row.digest);
  for (const table of tables) for (const row of before[table])
    assert.ok(after[table].some(current => encode(current).equals(encode(row))), 'original authority remains unchanged: ' + table);
  const accepted = committed.filter(Boolean).length;
  for (const [table, count] of [['events', 1], ['heads', 1], ['projections', 2], ['receipts', 1], ['outbox', 0]])
    assert.equal(after[table].length, before[table].length + accepted * count, 'whole transactions only: ' + table);
  assert.equal(after.metadata.store_id, before.metadata.store_id); assert.equal(after.metadata.format, before.metadata.format);
  assert.equal(after.metadata.version, before.metadata.version); assert.equal(after.metadata.generation, before.metadata.generation + 1);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts);
  const orphan = attempts.filter((_, index) => committed[index] === null).map(attempt => {
    const content = Buffer.from(attempt.body.contentBase64, 'base64'), hash = digest(content);
    const file = path.join(root, 'artifacts', hash.slice(7)); assert.deepEqual(fs.readFileSync(file), content); return {file, hash};
  });
  const db = new DatabaseSync(path.join(root, 'store/authority.sqlite'), {readOnly: true});
  let reopenedLimit;
  try {reopenedLimit = Number(db.prepare('PRAGMA max_page_count').get().max_page_count);} finally {db.close();}
  const limit = limited.observations().find(row => row.type === 'page-limit');
  assert.ok(reopenedLimit > limit.actual, 'fresh connection observes unrestricted capacity; no database repair');
  const reopened = await f.launch(root, 'open'); await originalFacts(reopened.client, original, history, input);
  assert.equal((await reopened.client.request('ready.get')).ready, true);
  assert.equal((await reopened.client.request('supervisor.get')).activeWorkers, 0); await reopened.stop();
  const cold = durable(root, () => f.offline(root));
  for (const table of tables) assert.deepEqual(cold[table], after[table], 'open must not adopt orphan or invent commit');
  const resumed = await f.launch(root, 'open');
  for (let index = 0; index < attempts.length; index++) {
    const attempt = attempts[index];
    const receipt = await resumed.client.request('input.create', {body: attempt.body, idempotencyKey: attempt.key});
    if (committed[index]) assert.deepEqual(encode(receipt), encode(committed[index]));
    attempt.restored = receipt;
    assert.deepEqual((await resumed.client.downloadArtifact(receipt.id)).content, Buffer.from(attempt.body.contentBase64, 'base64'));
    assert.deepEqual(await resumed.client.request('input.create', {body: attempt.body, idempotencyKey: attempt.key}), receipt);
    await assert.rejects(resumed.client.request('input.create', {body: {...attempt.body, name: 'conflicting-name.bin'},
      idempotencyKey: attempt.key}), {status: 409});
  }
  await resumed.stop(); const restored = durable(root, () => f.offline(root));
  for (const attempt of attempts) assert.deepEqual(encode(transaction(restored, attempt)), encode(attempt.restored));
  for (const [table, count] of [['events', 1], ['heads', 1], ['projections', 2], ['receipts', 1], ['outbox', 0]])
    assert.equal(restored[table].length, before[table].length + attempts.length * count, 'replay never duplicates authority');
  const final = await f.launch(root, 'open'); await originalFacts(final.client, original, history, input);
  const next = await completeTeam(final.client, input.id, 'after-sqlite-full'); assert.notEqual(next.created.id, original.created.id);
  assert.equal(f.observations().filter(row => row.type === 'started' && row.taskId === next.created.id).length, 4);
  assert.deepEqual(f.observations().filter(row => row.type === 'started' && row.taskId === original.created.id), starts);
  await final.stop();
  for (const blob of orphan) assert.equal(digest(fs.readFileSync(blob.file)), blob.hash);
  t.diagnostic(JSON.stringify({mechanism: 'SQLite max_page_count', limit, reopenedLimit, errors: limited.observations(), stopped,
    attempts: attempts.length, committed: accepted, rolledBack: attempts.length - accepted, duplicateStarts: 0, newTeamAttempts: 4,
    modelCalls: 0, boundary: 'real SQLITE_FULL from database page limit; not OS ENOSPC, disk exhaustion, active-worker I/O failure or power loss'}));
  f.completed = true;
});
