import {supportsNode} from '../task-store/runtime.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fixture, durable, completeTeam, events, upload} from './backup-restore.fixture.mjs';
import {fileLimit, launchLimited} from './storage-write-failure.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';

const value = row => JSON.parse(Buffer.from(row.bytes, 'hex'));
const sameRef = (row, receipt) => row.source_stream === receipt.source_stream && row.source_sequence === receipt.source_sequence && row.source_digest === receipt.source_digest;
function assertOriginalRows(before, after) {
  for (const table of ['events', 'heads', 'projections', 'receipts', 'outbox']) {
    const keys = {events: ['stream', 'sequence'], heads: ['stream'], projections: ['kind', 'id'],
      receipts: ['scope', 'operation', 'key_digest'], outbox: ['id']}[table];
    for (const original of before[table]) {
      const current = after[table].filter(row => keys.every(key => row[key] === original[key]));
      assert.equal(current.length, 1); assert.deepEqual(current[0], original, 'previously accepted authority must remain unchanged: ' + table);
    }
  }
}
function inspectAttempt(snapshot, attempt) {
  const content = Buffer.from(attempt.body.contentBase64, 'base64'), hash = digest(content);
  const receipts = snapshot.receipts.filter(row => row.scope === 'tasks' && row.operation === 'input.create' && row.key_digest === digest(encode(attempt.key)));
  const manifests = snapshot.projections.filter(row => row.kind === 'artifact' && value(row).artifact?.name === attempt.body.name);
  const blobs = snapshot.projections.filter(row => row.kind === 'artifact' && row.id === 'digest-' + hash.slice(7));
  const created = snapshot.events.filter(row => value(row).payload?.type === 'input.created' && value(row).payload.artifact.name === attempt.body.name);
  if (!receipts.length) {
    assert.equal(attempt.receipt, undefined, 'an acknowledged HTTP receipt cannot disappear');
    assert.deepEqual([manifests.length, blobs.length, created.length], [0, 0, 0], 'no partial SQL prefix without the exact receipt');
    return null;
  }
  assert.equal(receipts.length, 1); const receipt = receipts[0], artifact = value(receipt);
  assert.equal(receipt.request_digest, digest(encode({operation: 'input.create', taskId: null, body: attempt.body})));
  assert.equal(artifact.kind, 'input'); assert.equal(artifact.taskId, null); assert.equal(artifact.name, attempt.body.name);
  assert.equal(artifact.digest, hash); assert.equal(artifact.bytes, content.length); assert.equal(artifact.status, 'ready');
  if (attempt.receipt) assert.deepEqual(encode(artifact), encode(attempt.receipt));
  assert.deepEqual([manifests.length, blobs.length, created.length], [1, 1, 1]);
  assert.ok(sameRef(manifests[0], receipt) && sameRef(blobs[0], receipt));
  assert.deepEqual(value(manifests[0]), {type: 'manifest', owner: 'local-operator', artifact});
  assert.deepEqual(value(blobs[0]), {type: 'blob', digest: hash, bytes: content.length});
  const event = created[0]; assert.equal(event.stream, receipt.source_stream); assert.equal(event.sequence, receipt.source_sequence);
  assert.equal(event.digest, receipt.source_digest); assert.equal(digest(Buffer.from(event.bytes, 'hex')), event.digest);
  assert.deepEqual(value(event).payload.artifact, artifact);
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

test('kernel EFBIG and SQLite I/O under child-only file limit preserve whole authority and recover new HTTP delivery', {timeout: 65000}, async t => {
  assert.ok(supportsNode()); assert.ok(['darwin', 'linux'].includes(process.platform));
  const f = await fixture(t), root = path.join(f.parent, 'data'), initial = await f.launch(root, 'create');
  const input = await upload(initial.client), original = await completeTeam(initial.client, input.id, 'before-io-failure');
  const history = await events(initial.client, original.created.id);
  const starts = f.observations().filter(row => row.type === 'started'); assert.equal(starts.length, 4);
  assert.ok(f.observations().filter(row => row.type === 'completion').every(row => row.cleanup?.cleaned === true));
  await initial.stop(); const before = durable(root, () => f.offline(root));
  const depotLimited = await launchLimited(f, root), attempts = [];
  let limited = depotLimited;
  const submit = async (name, content) => {
    const attempt = {key: name, body: {name: name + '.bin', mediaType: 'application/octet-stream', contentBase64: content.toString('base64')}};
    attempts.push(attempt);
    try {attempt.receipt = await limited.client.request('input.create', {body: attempt.body, idempotencyKey: attempt.key});}
    catch (error) {attempt.failure = {code: error.code ?? null, status: error.status ?? null};}
    return attempt;
  };
  // Real Depot write reaches the OS limit, preserving an unreferenced partial
  // .pending blob rather than publishing a fabricated ready artifact.
  const large = await submit('oversize-kernel-write', Buffer.alloc(fileLimit * 2, 0x4c));
  assert.equal(large.failure?.status, 503);
  assert.ok(limited.observations().some(row => row.source === 'fs.writeFileSync' && row.code === 'EFBIG' &&
    row.regularFile && row.requestedBytes === fileLimit * 2 && row.resultingBytes === fileLimit));
  // The original Depot is intentionally poisoned after a failed put. Only an
  // original service close/open clears that instance; never bypass its check.
  const depotStopped = await depotLimited.stop(); limited = await launchLimited(f, root);
  // Small valid uploads exercise the REAL SQLite WAL, not a fake 503. At most
  // twelve bounded requests; no tasks/worker side effects during the failure.
  const sqliteIO = () => limited.observations().some(row => row.source?.startsWith('sqlite.') &&
    row.code === 'ERR_SQLITE_ERROR' && Number.isInteger(row.errcode) && (row.errcode & 255) === 10);
  for (let index = 0; index < 12 && !sqliteIO(); index++) await submit('wal-input-' + index, Buffer.alloc(1024, index + 1));
  assert.equal(sqliteIO(), true, 'must observe original SQLite IOERR, not infer it from HTTP status');
  const stopped = await limited.stop(); f.offline(root);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts, 'failed uploads never create model/worker work');
  const after = durable(root, () => f.offline(root)); assertOriginalRows(before, after);
  const committed = attempts.map(attempt => inspectAttempt(after, attempt)); assert.equal(committed[0], null);
  assert.equal(after.metadata.store_id, before.metadata.store_id); assert.equal(after.metadata.format, before.metadata.format);
  assert.equal(after.metadata.version, before.metadata.version); assert.equal(after.metadata.generation, before.metadata.generation + 2);
  const accepted = committed.filter(Boolean).length;
  for (const [table, rowsPerInput] of [['events', 1], ['heads', 1], ['projections', 2], ['receipts', 1], ['outbox', 0]])
    assert.equal(after[table].length, before[table].length + accepted * rowsPerInput, 'no unrelated or partial additional authority: ' + table);
  const pending = fs.readdirSync(path.join(root, 'artifacts')).filter(name => name.startsWith('.pending-')).map(name => {
    const stat = fs.lstatSync(path.join(root, 'artifacts', name)); assert.ok(stat.isFile()); assert.equal(stat.nlink, 1);
    assert.ok(stat.size <= fileLimit); return {name, digest: digest(fs.readFileSync(path.join(root, 'artifacts', name)))};
  });
  assert.ok(pending.length >= 1);

  // This new child inherits the UNCHANGED parent limit, not the restricted
  // process's hard cap. No global resource limit or file is repaired by hand.
  const reopened = await f.launch(root, 'open'); await originalFacts(reopened.client, original, history, input);
  assert.equal((await reopened.client.request('ready.get')).ready, true); assert.equal((await reopened.client.request('supervisor.get')).activeWorkers, 0);
  assert.deepEqual(f.observations().filter(row => row.type === 'started'), starts);
  // First prove opening alone preserves every transaction's disposition, then
  // replay exact committed receipts OR issue the original uncommitted request.
  await reopened.stop(); const reopenedFacts = durable(root, () => f.offline(root));
  for (const table of ['events', 'heads', 'projections', 'receipts', 'outbox']) assert.deepEqual(reopenedFacts[table], after[table]);
  const resumed = await f.launch(root, 'open');
  for (let index = 0; index < attempts.length; index++) {
    const attempt = attempts[index], receipt = await resumed.client.request('input.create', {body: attempt.body, idempotencyKey: attempt.key});
    if (committed[index]) assert.deepEqual(encode(receipt), encode(committed[index]));
    assert.equal(receipt.digest, digest(Buffer.from(attempt.body.contentBase64, 'base64')));
    assert.deepEqual((await resumed.client.downloadArtifact(receipt.id)).content, Buffer.from(attempt.body.contentBase64, 'base64'));
  }
  const next = await completeTeam(resumed.client, input.id, 'after-io-failure'); assert.notEqual(next.created.id, original.created.id);
  await originalFacts(resumed.client, original, history, input);
  assert.equal(f.observations().filter(row => row.type === 'started' && row.taskId === next.created.id).length, 4);
  assert.deepEqual(f.observations().filter(row => row.type === 'started' && row.taskId === original.created.id), starts);
  assert.equal((await resumed.client.request('supervisor.get')).activeWorkers, 0); await resumed.stop();
  for (const blob of pending) assert.equal(digest(fs.readFileSync(path.join(root, 'artifacts', blob.name))), blob.digest, 'orphan is preserved, not silently adopted or deleted');
  t.diagnostic(JSON.stringify({kernelLimit: 'RLIMIT_FSIZE', bytes: fileLimit, scope: 'only original CLI child and inherited descendants',
    errors: [...depotLimited.observations(), ...limited.observations()].filter(row => row.type === 'io-error'),
    restrictedExits: [depotStopped, stopped], attempts: attempts.length,
    committedInputs: committed.filter(Boolean).length, rolledBackInputs: committed.filter(value => !value).length,
    originalAttempts: 4, duplicateStarts: 0, newTeamAttempts: 4, models: 0,
    boundary: 'EFBIG/SQLite IOERR, not ENOSPC, whole-disk exhaustion, active-worker write-failure recovery or power loss'}));
  f.completed = true;
});
