import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {launchCommand} from '../agent-runtime/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {createLocalReportPublication, PROFILE, INPUT_NAME, nameFor} from './index.mjs';
import {HeldRoot, readHeld, json} from './io.mjs';

const sha = value => digest(encode(value));
const stamp = 'sha256:' + 'a'.repeat(64);
function reserve(input, executionType = 'publication', deadline = Date.now() + 15000) {
  const ticket = {taskId: 'task-one', workerId: 'worker-one', nodeId: 'publication-one', role: executionType === 'publication' ? 'integrator' : 'verifier',
    providerId: executionType === 'publication' ? 'target-one' : 'target-one-postverify', executionType, generation: '1', commandId: 'command-one',
    planDigest: stamp, input, inputDigest: sha(input), deadline};
  return {...ticket, reservationDigest: sha(ticket)};
}
function fixture(t, bytes = Buffer.from('{"count":2,"netCents":550}\n')) {
  const base = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-publication-')));
  fs.chmodSync(base, 0o700); const root = path.join(base, 'reports'), cwd = path.join(base, 'execution');
  fs.mkdirSync(root, {mode: 0o700}); fs.mkdirSync(cwd, {mode: 0o700});
  fs.writeFileSync(path.join(cwd, INPUT_NAME), bytes, {mode: 0o600});
  const port = createLocalReportPublication({id: 'target-one', root, readBaseURL: 'http://127.0.0.1:1/reports/',
    policy: {profile: PROFILE, id: 'exact-report', version: '1'}});
  port.assertDisjoint([cwd, path.join(base, 'service')]);
  const authorization = {taskId: 'task-one', planDigest: stamp, artifactId: 'artifact-one', artifactDigest: digest(bytes), bytes: bytes.length,
    acceptanceDigest: stamp, reviewDigest: stamp, targetId: port.id, targetPolicyDigest: port.policyDigest,
    name: nameFor({taskId: 'task-one', artifactDigest: digest(bytes)}), operation: 'create-if-absent', expiresAt: new Date(Date.now() + 60000).toISOString()};
  const binding = {actionId: 'action-one', targetId: port.id, name: authorization.name, artifactDigest: digest(bytes), bytes: bytes.length,
    authorizationDigest: sha(authorization)};
  const makeTicket = () => reserve({publication: {binding, authorization}});
  t.after(() => {port.close(); fs.rmSync(base, {recursive: true, force: true});});
  return {base, root, cwd, bytes, port, binding, authorization, makeTicket, prepared: {cwd, prompt: '原Core授权测试输入，不代表真实用户批准'}};
}
async function started(f, port = f.port, ticket = f.makeTicket(), context) {
  const handle = port.start({ticket, prepared: f.prepared, executionContext: context});
  const result = await handle.completion;
  return {handle, result};
}
test('real owned command publishes full private bytes once; repeat and read-only lookup are matched, not a second create', async t => {
  const f = fixture(t), before = f.port.lookup(f.binding, {deadline: Date.now() + 5000}); assert.equal(before.status, 'absent');
  const first = await started(f); assert.equal(first.result.status, 'created'); assert.equal(first.result.cleanup.cleaned, true);
  assert.equal(first.result.cleanup.agentExit.code, 0); assert.ok(await first.handle.started);
  const target = path.join(f.root, f.binding.name), original = fs.statSync(target);
  assert.equal(original.mode & 0o777, 0o600); assert.equal(original.nlink, 1); assert.deepEqual(fs.readFileSync(target), f.bytes);
  assert.deepEqual(fs.readdirSync(f.root), [f.binding.name]);
  const repeated = await started(f); assert.equal(repeated.result.status, 'matched'); assert.equal(fs.statSync(target).ino, original.ino);
  const lookup = f.port.lookup(f.binding, {deadline: Date.now() + 5000}); assert.equal(lookup.status, 'matched');
  assert.deepEqual(lookup.binding, f.binding); assert.equal(json(lookup.evidence.content).createdByThisExecution, false);
  const changedAction = {...f.binding, actionId: 'action-other'};
  assert.deepEqual(f.port.lookup(changedAction, {deadline: Date.now() + 5000}).binding, changedAction);
});
test('existing different bytes are conflict; symlink/special/unsafe roots are unavailable and never overwritten', async t => {
  const f = fixture(t), target = path.join(f.root, f.binding.name); fs.writeFileSync(target, '{"wrong":true}', {mode: 0o600});
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() + 5000}).status, 'conflict');
  assert.equal((await started(f)).result.status, 'failed'); assert.equal(fs.readFileSync(target, 'utf8'), '{"wrong":true}');
  fs.unlinkSync(target); fs.symlinkSync(path.join(f.cwd, INPUT_NAME), target);
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() + 5000}).status, 'unknown');
  assert.equal((await started(f)).result.status, 'unknown'); assert.ok(fs.lstatSync(target).isSymbolicLink());
  fs.unlinkSync(target); fs.mkdirSync(target, {mode: 0o700});
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() + 5000}).status, 'unknown');
  for (const root of [f.root, f.base, path.join(f.base, 'alias')]) {
    if (root.endsWith('alias')) fs.symlinkSync(f.root, root);
    if (root.endsWith('alias')) assert.throws(() => createLocalReportPublication({id: 'x', root, readBaseURL: 'http://127.0.0.1:1/', policy: {profile: PROFILE, id: 'p', version: '1'}}));
    else assert.throws(() => f.port.assertDisjoint([root]));
  }
});
test('exact original ticket, authorization and held input are mandatory; invalid JSON/size/deadline never creates', async t => {
  const f = fixture(t);
  const edits = [ticket => {ticket.input.publication.binding.actionId = '../escape';}, ticket => {ticket.input.publication.authorization.reviewDigest = 'bad';},
    ticket => {ticket.input.publication.authorization.targetId = 'another';}, ticket => {ticket.input.publication.binding.name = '../outside.json';},
    ticket => {ticket.taskId = 'task-another';}, ticket => {ticket.deadline = Date.now() - 1;}];
  for (const edit of edits) {
    const ticket = f.makeTicket(); edit(ticket);
    ticket.inputDigest = sha(ticket.input); delete ticket.reservationDigest; ticket.reservationDigest = sha(ticket);
    const result = (await started(f, f.port, ticket)).result;
    assert.equal(result.status, 'failed'); assert.equal(result.cleanup, null);
  }
  for (const raw of ['{"a":1,"a":2}', '\ufeff{}', '{"a":"\\u0000"}', '{"a":"\\ud800"}', '{"a":1e999}']) {
    const bad = fixture(t, Buffer.from(raw)); assert.equal((await started(bad)).result.status, 'failed'); assert.deepEqual(fs.readdirSync(bad.root), []);
  }
  const big = fixture(t, Buffer.alloc(1024 * 1024 + 1, 32)); assert.equal((await started(big)).result.status, 'failed');
  fs.chmodSync(path.join(f.cwd, INPUT_NAME), 0o644); assert.equal((await started(f)).result.status, 'failed'); assert.deepEqual(fs.readdirSync(f.root), []);
});
test('caller cancellation holds original cleanup; timeout/aborted lookup does not invent absent', async t => {
  const f = fixture(t), ticket = f.makeTicket();
  const handle = f.port.start({ticket, prepared: f.prepared}); const stopping = handle.stop();
  const result = await stopping; assert.equal(result, await handle.completion);
  if (result.cleanup?.started) assert.equal(result.cleanup.cleaned, true);
  assert.ok(['failed', 'unknown'].includes(result.status));
  const controller = new AbortController(); controller.abort();
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() + 5000, signal: controller.signal}).status, 'unknown');
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() - 1}).status, 'unknown');
});
for (const point of ['before-unlink', 'after-unlink', 'unlink-error', 'directory-sync-error', 'stop-before-unlink']) test(`original child failure ${point}: retain published effect and lookup without retransmission`, async t => {
  const f = fixture(t), deadline = Date.now() + 15000, directory = new HeldRoot(f.root);
  const input = readHeld(path.join(f.cwd, INPUT_NAME), {deadline, oneLink: true});
  const request = {operation: 'publish', nonce: randomUUID(), binding: f.binding, root: f.root, rootIdentity: directory.descriptor(),
    inputIdentity: input.descriptor, deadline}; input.close(); directory.close();
  const runtime = await launchCommand({executable: process.execPath, args: [fileURLToPath(new URL('./fault-runner.fixture.mjs', import.meta.url)), point],
    cwd: f.cwd, env: {}, deadline, input: Buffer.concat([encode(request), Buffer.from('\n')])});
  t.after(() => runtime.stop());
  if (point === 'stop-before-unlink') {
    while (!fs.existsSync(path.join(f.root, f.binding.name)) && Date.now() < deadline - 5000) await new Promise(resolve => setTimeout(resolve, 10));
    assert.ok(fs.existsSync(path.join(f.root, f.binding.name))); await runtime.stop();
  }
  const result = await runtime.completion; assert.equal(result.cleanup.cleaned, true);
  assert.ok(result.cleanup.agentExit.code !== 0 || result.cleanup.agentExit.signal !== null);
  const target = path.join(f.root, f.binding.name), stat = fs.statSync(target);
  assert.deepEqual(fs.readFileSync(target), f.bytes);
  const lookup = f.port.lookup(f.binding, {deadline: Date.now() + 5000}); assert.equal(lookup.status, 'matched');
  assert.equal(json(lookup.evidence.content).createdByThisExecution, false); assert.equal(fs.statSync(target).ino, stat.ino);
  assert.equal(fs.readdirSync(f.root).some(name => name.startsWith('.pending-')), point !== 'after-unlink');
});
test('postverify is a separate actual loopback GET, whole expected JSON/answer binding and original bytes', async t => {
  const f = fixture(t); assert.equal((await started(f)).result.status, 'created');
  let mode = 'ok', hits = 0;
  const server = http.createServer((request, response) => {
    hits++; assert.equal(request.method, 'GET'); assert.equal(request.headers.authorization, undefined);
    assert.equal(request.url, '/reports/' + f.binding.name);
    if (mode === 'redirect') {response.writeHead(302, {location: '/elsewhere'}); response.end(); return;}
    if (mode === 'slow') return;
    response.writeHead(200, {'content-type': 'application/json'});
    response.end(mode === 'large' ? Buffer.alloc(1024 * 1024 + 1) : mode === 'wrong' ? '{"count":3}' : fs.readFileSync(path.join(f.root, f.binding.name)));
  });
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(0, '127.0.0.1', resolve);});
  t.after(() => {server.closeAllConnections(); return new Promise(resolve => server.close(resolve));});
  const port = createLocalReportPublication({id: 'target-one', root: f.root, readBaseURL: `http://127.0.0.1:${server.address().port}/reports/`, policy: {profile: PROFILE, id: 'exact-report', version: '1'}});
  port.assertDisjoint([f.cwd]); t.after(() => port.close());
  const refs = [{requestId: 'request-one', requestDigest: stamp, replyDigest: stamp}];
  const make = (deadline = Date.now() + 15000) => reserve({leaderReplyRefs: refs, interactionRefs: [], postverify: {
    publicationReceiptDigest: stamp, targetId: port.id, name: f.binding.name, artifactDigest: f.binding.artifactDigest, bytes: f.binding.bytes,
    policyDigest: port.policyDigest, expected: JSON.parse(f.bytes), interactionRefs: [], leaderReplyRefs: refs}}, 'postverify', deadline);
  const result = (await started(f, port.postverify, make())).result;
  assert.equal(result.status, 'passed'); assert.equal(result.cleanup.cleaned, true); assert.deepEqual(result.delivery.content, f.bytes);
  assert.equal(json(result.evidence.content).observed.checks, 4); assert.equal(hits, 1);
  for (mode of ['redirect', 'wrong', 'large', 'slow']) {
    const denied = (await started(f, port.postverify, make(Date.now() + (mode === 'slow' ? 1200 : 15000)))).result;
    assert.equal(denied.status, 'failed'); assert.equal(denied.delivery, null); assert.equal(denied.cleanup.cleaned, true);
  }
  const omitted = make(); omitted.input.postverify.leaderReplyRefs = []; omitted.inputDigest = sha(omitted.input); delete omitted.reservationDigest; omitted.reservationDigest = sha(omitted);
  const count = hits; assert.equal((await started(f, port.postverify, omitted)).result.status, 'failed'); assert.equal(hits, count);
  assert.deepEqual(fs.readFileSync(path.join(f.root, f.binding.name)), f.bytes);
});
test('fixed origin and activation boundaries reject; a changed original input inode cannot publish through an actual child', async t => {
  const f = fixture(t), config = {id: 'target-one', root: f.root, policy: {profile: PROFILE, id: 'p', version: '1'}};
  for (const readBaseURL of ['https://127.0.0.1/', 'http://localhost/', 'http://user:pass@127.0.0.1/', 'http://127.0.0.1/a?x=1', 'http://127.0.0.1/a#fragment', 'http://127.0.0.1/%2e/'])
    assert.throws(() => createLocalReportPublication({...config, readBaseURL}));
  const unarmed = createLocalReportPublication({...config, readBaseURL: 'http://127.0.0.1/'});
  t.after(() => unarmed.close()); assert.equal(unarmed.lookup(f.binding, {deadline: Date.now() + 5000}).status, 'unknown');
  assert.equal(unarmed.configurationDigest, sha(unarmed.configuration)); assert.ok(Object.isFrozen(unarmed.configuration.rootIdentity));
  const otherURL = createLocalReportPublication({...config, readBaseURL: 'http://127.0.0.1:2/'});
  t.after(() => otherURL.close()); assert.equal(otherURL.policyDigest, unarmed.policyDigest);
  assert.notEqual(otherURL.configurationDigest, unarmed.configurationDigest);
  const context = {launch(options, callbacks) {
    const filename = path.join(f.cwd, INPUT_NAME); fs.renameSync(filename, filename + '.original');
    fs.writeFileSync(filename, f.bytes, {mode: 0o600});
    return launchCommand({...options, input: callbacks.input});
  }};
  const result = (await started(f, f.port, f.makeTicket(), context)).result;
  assert.equal(result.status, 'unknown'); assert.equal(result.cleanup.cleaned, true); assert.deepEqual(fs.readdirSync(f.root), []);
  const original = f.root + '-original'; fs.renameSync(f.root, original); fs.mkdirSync(f.root, {mode: 0o700});
  assert.equal(f.port.lookup(f.binding, {deadline: Date.now() + 5000}).status, 'unknown'); assert.deepEqual(fs.readdirSync(f.root), []);
});
