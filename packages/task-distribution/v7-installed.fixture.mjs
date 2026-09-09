// Explicit same-package v7 consumer. No pack, fallback, private receipt, model,
// source Core import or mutation of the supplied installed package.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {createHash} from 'node:crypto';
import {spawn} from 'node:child_process';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {verify, NODE_VERSION} from './index.mjs';
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const here = relative => fileURLToPath(new URL(relative, import.meta.url));
const readJson = file => JSON.parse(fs.readFileSync(file, 'utf8'));
export function candidateInputs(env) {
  const result = {installed: env.MARSHAL_CANDIDATE_ROOT, manifestDigest: env.MARSHAL_CANDIDATE_MANIFEST,
    sourceHead: env.MARSHAL_CANDIDATE_SOURCE, artifactId: env.MARSHAL_CANDIDATE_ARTIFACT_ID ?? null};
  assert.ok(path.isAbsolute(result.installed ?? ''), 'explicit installed root required');
  assert.match(result.sourceHead ?? '', /^[a-f0-9]{40}$/); assert.match(result.manifestDigest ?? '', /^sha256:[a-f0-9]{64}$/);
  if (result.artifactId !== null || env.GITHUB_ACTIONS === 'true') assert.match(result.artifactId ?? '', /^[1-9][0-9]*$/);
  return result;
}
// Export only the successful bounded summary, never the private journal, HTTP
// credentials, stdout/stderr of Agents, or a directory selected by TAP text.
// This records supplied artifact identity; it does not re-query GitHub or mint
// release approval. The workflow supplies the original producer's exact pins.
export function v7ResultFromTap(tap, expected) {
  assert.equal(typeof tap, 'string'); assert.ok(Buffer.byteLength(tap) <= 128 * 1024);
  const lines = tap.split(/\r?\n/);
  for (const line of ['# tests 2', '# pass 2', '# fail 0', '# cancelled 0', '# skipped 0']) {
    assert.equal(lines.filter(value => value === line).length, 1, 'incomplete v7 test result');
  }
  const observations = lines.filter(line => line.startsWith('# {')).map(line => JSON.parse(line.slice(2)));
  assert.equal(observations.length, 1); const result = observations[0];
  assert.deepEqual(Object.keys(result).sort(), ['sourceHead', 'manifestDigest', 'artifactId', 'files', 'node', 'platform', 'arch', 'uid',
    'layout', 'sameConfiguration', 'tasks', 'modelCalls', 'coldReplayDuplicateStarts', 'coldReplayDuplicatePublications', 'proofScope'].sort());
  for (const key of ['sourceHead', 'manifestDigest', 'artifactId']) assert.equal(result[key], expected[key], 'v7 candidate pin mismatch');
  assert.equal(result.node, NODE_VERSION); assert.equal(result.layout, 7); assert.equal(result.sameConfiguration, true);
  assert.ok(Number.isSafeInteger(result.files) && result.files > 0 && result.files <= 256);
  assert.ok(Number.isSafeInteger(result.uid) && result.uid > 0);
  assert.ok(['darwin-arm64', 'linux-x64'].includes(result.platform + '-' + result.arch));
  assert.equal(result.modelCalls, 0); assert.equal(result.coldReplayDuplicateStarts, 0); assert.equal(result.coldReplayDuplicatePublications, 0);
  assert.equal(result.proofScope, 'installed-v7-fixture-consumption-not-model-or-release-approval');
  assert.equal(result.tasks.length, 2); assert.equal(new Set(result.tasks.map(item => item.taskId)).size, 2);
  for (const task of result.tasks) {
    assert.deepEqual(Object.keys(task).sort(), ['taskId', 'attempts', 'overlapMs', 'deliveryDigest', 'deliveryBytes', 'executions',
      'reviewDigest', 'publicationReceiptArtifactId', 'postverifyEvidenceArtifactId'].sort());
    for (const key of ['taskId', 'publicationReceiptArtifactId', 'postverifyEvidenceArtifactId']) assert.match(task[key], /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$/);
    for (const key of ['deliveryDigest', 'reviewDigest']) assert.match(task[key], /^sha256:[a-f0-9]{64}$/);
    assert.ok(Number.isSafeInteger(task.deliveryBytes) && task.deliveryBytes > 0 && task.deliveryBytes <= 1048576);
    assert.ok(Number.isSafeInteger(task.attempts) && task.attempts > 0 && task.attempts <= 17 && Number.isFinite(task.overlapMs) && task.overlapMs > 0);
    assert.deepEqual(Object.keys(task.executions).sort(), ['leader', 'agent', 'review', 'verification', 'publication', 'postverify'].sort());
    assert.ok(Number.isSafeInteger(task.executions.leader) && task.executions.leader > 0 && task.executions.leader <= 9);
    assert.equal(task.executions.agent, 2);
    for (const type of ['review', 'verification', 'publication', 'postverify']) assert.equal(task.executions[type], 1);
    assert.equal(Object.values(task.executions).reduce((sum, value) => sum + value, 0), task.attempts);
  }
  return result;
}
async function until(read, predicate, label, milliseconds = 60000) {
  const deadline = Date.now() + milliseconds;
  for (;;) {const value = await read(); if (predicate(value)) return value;
    assert.ok(Date.now() < deadline, label + ' timed out'); await pause(25);}
}
async function reports(root) {
  const identity = fs.statSync(root), reads = [];
  const server = http.createServer((request, response) => {
    response.setHeader('Connection', 'close'); let fd;
    try {
      assert.equal(request.method, 'GET'); assert.equal(request.headers.host, '127.0.0.1:' + server.address().port);
      assert.ok(!request.headers.authorization && !request.headers.cookie && !request.headers.origin);
      assert.match(request.url, /^\/[A-Za-z0-9][A-Za-z0-9_-]{0,127}-[a-f0-9]{64}\.json$/);
      const current = fs.lstatSync(root); assert.equal(current.dev, identity.dev); assert.equal(current.ino, identity.ino);
      fd = fs.openSync(path.join(root, request.url.slice(1)), fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
      const before = fs.fstatSync(fd); assert.ok(before.isFile() && before.nlink === 1 && before.size > 0 && before.size <= 1048576);
      assert.equal(before.mode & 0o777, 0o600);
      const bytes = Buffer.alloc(before.size + 1), length = fs.readSync(fd, bytes, 0, bytes.length, 0), after = fs.fstatSync(fd);
      assert.equal(length, before.size); assert.equal(after.size, before.size); assert.equal(after.ctimeMs, before.ctimeMs);
      const body = bytes.subarray(0, length); reads.push({name: request.url.slice(1), digest: hash(body), bytes: length});
      response.writeHead(200, {'Content-Type': 'application/json', 'Content-Length': length}); response.end(body);
    } catch {response.writeHead(404, {'Content-Length': '0'}); response.end();}
    finally {if (fd !== undefined) fs.closeSync(fd);}
  });
  server.headersTimeout = 3000; server.requestTimeout = 3000; server.maxConnections = 8;
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(0, '127.0.0.1', resolve);});
  return {url: 'http://127.0.0.1:' + server.address().port + '/', reads, close: () => new Promise((resolve, reject) => {
    server.close(error => error ? reject(error) : resolve()); server.closeAllConnections();
  })};
}
async function downloadReport(url, name) {
  const response = await fetch(new URL(name, url), {redirect: 'error', signal: AbortSignal.timeout(5000)});
  assert.equal(response.status, 200); assert.equal(response.headers.get('content-type'), 'application/json');
  const expected = Number(response.headers.get('content-length')); assert.ok(expected > 0 && expected <= 1048576);
  const chunks = []; let size = 0;
  for await (const chunk of response.body) {size += chunk.length; assert.ok(size <= 1048576); chunks.push(chunk);}
  assert.equal(size, expected); return Buffer.concat(chunks);
}
export async function exerciseInstalledV7(t, options) {
  assert.equal(process.versions.node, NODE_VERSION); assert.ok(process.getuid() > 0);
  const {installed, manifestDigest, sourceHead} = options;
  // All bytes and the caller's independent source pin precede any package import.
  const originalPackage = verify({root: installed, manifestDigest}); assert.equal(originalPackage.sourceHead, sourceHead);
  const load = relative => import(pathToFileURL(path.join(installed, relative)).href);
  const [{TaskClient}, {encode}, {nameFor}] = await Promise.all([load('packages/task-client/index.mjs'),
    load('packages/task-store/store.mjs'), load('packages/task-publication-report/index.mjs')]);
  const same = (actual, expected) => assert.deepEqual(encode(actual), encode(expected)), hashValue = value => hash(encode(value));
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-installed-v7-'))), state = path.join(parent, 'state');
  const reportRoot = path.join(parent, 'reports'), barrier = path.join(parent, 'barrier'), journal = path.join(parent, 'observations.jsonl');
  fs.mkdirSync(reportRoot, {mode: 0o700}); fs.mkdirSync(barrier, {mode: 0o700});
  let reader; const children = [], completed = []; let passed = false;
  t.after(async () => {try {for (const handle of children) await handle.stop();} finally {if (reader) await reader.close();
    t.diagnostic((passed ? '验收现场保留：' : '失败证据保留：') + parent);}});
  reader = await reports(reportRoot);
  const observations = () => {
    if (!fs.existsSync(journal)) return [];
    assert.ok(fs.statSync(journal).size <= 1024 * 1024);
    return fs.readFileSync(journal, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  };
  const save = (name, value) => {const fd = fs.openSync(path.join(parent, name), fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY, 0o600);
    try {fs.writeFileSync(fd, JSON.stringify(value, null, 2) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}};
  async function launch(mode) {
    assert.deepEqual(verify({root: installed, manifestDigest}), originalPackage);
    const child = spawn(process.execPath, [path.join(installed, originalPackage.entrypoint), '--root', state, '--mode', mode,
      '--config', here('./v7-service.fixture.mjs'), '--port', '0'], {cwd: parent, env: {
      MARSHAL_INSTALLED_V7: '1', MARSHAL_CANDIDATE_ROOT: installed, MARSHAL_CANDIDATE_SOURCE: sourceHead,
      MARSHAL_CANDIDATE_MANIFEST: manifestDigest, MARSHAL_V7_JOURNAL: journal, MARSHAL_V7_BARRIER: barrier,
      MARSHAL_V7_REPORT_ROOT: reportRoot, MARSHAL_V7_REPORT_URL: reader.url}, stdio: ['ignore', 'pipe', 'pipe']});
    let output = '', stderrBytes = 0, resolved = false, resolveReady, rejectReady, closed = false, stopping;
    const ready = new Promise((resolve, reject) => {resolveReady = resolve; rejectReady = reject;});
    const exit = new Promise(resolve => {
      child.once('error', error => rejectReady(new Error('installed CLI spawn: ' + error.code)));
      child.once('close', (code, signal) => {closed = true; const result = {code, signal, stderrBytes};
        save(mode + '-exit.json', result); rejectReady(new Error('installed CLI closed before ready: ' + JSON.stringify(result))); resolve(result);});
    });
    child.stdout.on('data', bytes => {output += bytes.toString();
      if (output.length > 8192) {rejectReady(new Error('installed CLI stdout bound')); child.kill('SIGTERM'); return;}
      if (!resolved && output.includes('\n')) {resolved = true; try {resolveReady(JSON.parse(output.split('\n')[0]));} catch {rejectReady(new Error('invalid ready'));}}
    });
    child.stderr.on('data', bytes => {stderrBytes += bytes.length; if (stderrBytes > 8192) child.kill('SIGTERM');});
    const handle = {stop() {stopping ??= (async () => {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
      const timer = setTimeout(() => {if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');}, 8000);
      try {await until(async () => closed, Boolean, 'owned CLI close', 12000); const result = await exit;
        assert.equal(result.code, 0); assert.equal(result.signal, null); assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true);
      } finally {clearTimeout(timer);}
    })(); return stopping;}}; children.push(handle);
    const timeout = setTimeout(() => rejectReady(new Error('installed CLI ready timeout')), 10000);
    try {const started = await ready, connection = readJson(started.connectionFile);
      assert.equal(fs.statSync(started.connectionFile).mode & 0o777, 0o600);
      const client = new TaskClient({baseURL: connection.url, token: connection.token}); assert.equal((await client.request('ready.get')).ready, true);
      return {...handle, client, connectionFile: started.connectionFile};
    } finally {clearTimeout(timeout);}
  }
  async function events(client, taskId) {
    const items = []; let cursor;
    for (let page = 0; page < 20; page++) {const result = await client.request('task.events', {path: {taskId}, query: {limit: 100, ...(cursor ? {cursor} : {})}});
      items.push(...result.items); if (result.nextCursor === null) return items; assert.notEqual(result.nextCursor, cursor); cursor = result.nextCursor;}
    assert.fail('event history bound');
  }
  const first = await launch('create'), client = first.client;
  assert.equal(readJson(path.join(state, 'profile.json')).layout, 7);
  for (const [caseId, region, east, west] of [['one', 'north', 10, 20], ['two', 'south', 30, 50]]) {
    const body = {intent: '完整交付' + caseId + '的原始两地区业务要求', context: {text: JSON.stringify({east, west})},
      requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']},
      limits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}};
    const create = {idempotencyKey: 'v7-create-' + caseId, body}, created = await client.request('task.create', create), taskId = created.id;
    const get = () => client.request('task.get', {path: {taskId}}), view = () => client.request('task.leader', {path: {taskId}});
    const phase = async status => {const task = await until(get, value => [status, 'failed', 'intervention', 'cancelled'].includes(value.status), status);
      assert.equal(task.status, status); return task;};
    let current = await phase('awaiting-answer'), leader = await view();
    assert.equal(leader.pendingRequest.kind, 'business'); assert.equal(current.plan, null);
    const answer = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'v7-answer-' + caseId,
      body: {expectedRevision: current.revision, requestDigest: leader.pendingRequest.requestDigest, answer: region}};
    const answerReceipt = await client.request('task.leader.reply', answer); assert.equal(answerReceipt.replayed, false);
    current = await phase('awaiting-approval'); const plan = await client.request('task.plan', {path: {taskId}});
    same(plan.edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]); same(plan.budget, body.limits);
    const approve = {path: {taskId}, idempotencyKey: 'v7-approve-' + caseId,
      body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}};
    const approval = await client.request('task.approve', approve);
    current = await phase('awaiting-confirmation'); leader = await view();
    assert.equal(leader.pendingRequest.kind, 'publication'); assert.equal(leader.review.verdict, 'accept');
    assert.equal(fs.readdirSync(reportRoot).length, completed.length, 'no report before explicit authorization');
    const auditBefore = await client.request('task.audit', {path: {taskId}}); assert.equal(auditBefore.acceptance.status, 'passed');
    const authorization = leader.pendingRequest.authorization;
    const delivery = await client.downloadArtifact(authorization.artifactId);
    const expected = [{nodeId: 'east', region, value: east}, {nodeId: 'west', region, value: west}];
    same(JSON.parse(delivery.content), expected); assert.equal(hash(delivery.content), delivery.artifact.digest);
    same(authorization, {taskId, planDigest: plan.digest, artifactId: delivery.artifact.id, artifactDigest: delivery.artifact.digest,
      bytes: delivery.artifact.bytes, acceptanceDigest: auditBefore.acceptance.digest, reviewDigest: leader.review.digest, targetId: 'reports',
      targetPolicyDigest: hashValue(observations().find(item => item.type === 'configuration').publicationConfiguration.policy),
      name: nameFor({taskId, artifactDigest: delivery.artifact.digest}), operation: 'create-if-absent', expiresAt: authorization.expiresAt});
    assert.ok(Date.now() < Date.parse(authorization.expiresAt) && Date.parse(authorization.expiresAt) <= Date.parse(current.deadlineAt));
    const allow = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'v7-publish-' + caseId,
      body: {expectedRevision: current.revision, requestDigest: leader.pendingRequest.requestDigest, decision: 'allow'}};
    const allowReceipt = await client.request('task.leader.reply', allow); assert.equal(allowReceipt.replayed, false);
    const done = await phase('completed'), final = await view(), audit = await client.request('task.audit', {path: {taskId}});
    assert.equal(final.stage, 'terminal'); assert.equal(final.review.digest, authorization.reviewDigest);
    assert.equal(final.publication.status, 'succeeded'); assert.equal(final.publication.authorizationDigest, hashValue(authorization));
    assert.equal(final.postverify.status, 'succeeded'); assert.equal(done.deadlineAt, created.deadlineAt);
    assert.equal(audit.acceptance.status, 'passed'); assert.equal(audit.reworkCount, 0); assert.equal(audit.retryCount, 0);
    assert.equal(audit.measurement.usageSource, 'unavailable');
    const reviewReport = await client.downloadArtifact(final.review.evidenceIds[0]); assert.equal(reviewReport.artifact.taskId, taskId);
    assert.equal(JSON.parse(reviewReport.content).report.selectionDigest, final.review.selectionDigest);
    assert.equal(JSON.parse(reviewReport.content).report.verdict, 'accept');
    const summary = await client.downloadArtifact(final.summaryArtifactId), conclusion = JSON.parse(summary.content).report;
    assert.equal(final.lastDecision.evidenceId, summary.artifact.id); assert.equal(final.lastDecision.digest, hashValue(conclusion));
    assert.ok(conclusion.actions.some(action => action.type === 'conclude' && action.outcome === 'succeeded'));
    for (const id of [final.publication.receiptArtifactId, final.postverify.evidenceArtifactId]) assert.equal((await client.downloadArtifact(id)).artifact.taskId, taskId);
    assert.ok(reader.reads.some(item => item.name === authorization.name && item.digest === delivery.artifact.digest), 'postverify made real HTTP GET');
    const published = await downloadReport(reader.url, authorization.name); assert.deepEqual(published, Buffer.from(delivery.content));
    const facts = observations().filter(item => item.taskId === taskId), starts = facts.filter(item => item.type === 'started'), finishes = facts.filter(item => item.type === 'finished');
    assert.equal(starts.length, audit.attempts); assert.equal(finishes.length, starts.length); assert.ok(audit.attempts <= 17);
    assert.equal(new Set(starts.map(item => item.value.executionId)).size, starts.length);
    for (const end of finishes) {const start = starts.find(item => item.workerId === end.workerId);
      assert.ok(start); assert.equal(end.cleanup.cleaned, true); same(end.cleanup.started, start.value); assert.equal(end.cleanup.executionId, start.value.executionId);
      assert.equal(end.cleanup.agentExit.observed, true);
      if (['leader', 'review', 'agent'].includes(end.executionType)) {
        // ACP end_turn is a protocol terminal; the original runtime then stops
        // this still-listening peer. Do not fabricate process exit(0).
        assert.equal(end.status, 'completed'); assert.equal(end.reason, 'agent_end_turn');
        assert.equal(end.cleanup.agentExit.code, null); assert.equal(end.cleanup.agentExit.signal, 'SIGTERM');
      } else {
        assert.equal(end.status, end.executionType === 'publication' ? 'created' : 'passed');
        assert.equal(end.cleanup.agentExit.code, 0); assert.equal(end.cleanup.agentExit.signal, null);
      }}
    const authors = finishes.filter(item => item.role === 'author'); assert.equal(authors.length, 2);
    const overlap = Math.min(...authors.map(item => Date.parse(item.cleanup.agentExit.at))) - Math.max(...authors.map(item => Date.parse(item.cleanup.started.startedAt)));
    assert.ok(overlap > 0, 'two real author lifetimes overlap');
    for (const type of ['leader', 'review', 'verification', 'publication', 'postverify']) assert.ok(finishes.some(item => item.executionType === type));
    assert.ok(finishes.filter(item => item.executionType === 'leader').length <= 9);
    assert.equal(facts.filter(item => item.type === 'work-package').length, finishes.filter(item => !['verification', 'publication', 'postverify'].includes(item.executionType)).length);
    const workers = await client.request('task.workers', {path: {taskId}, query: {limit: 100}}); assert.equal(workers.nextCursor, null); assert.equal(workers.items.length, audit.attempts);
    assert.equal((await client.request('operation.get', {path: {operationId: approval.id}})).status, 'succeeded');
    assert.equal((await client.request('supervisor.get')).activeWorkers, 0);
    const history = await events(client, taskId);
    completed.push({taskId, create, created, approve, approval, answer, answerReceipt, allow, allowReceipt, done, final, audit, workers, history,
      deliveryId: delivery.artifact.id, deliveryDigest: delivery.artifact.digest, deliveryBytes: delivery.content.length, report: expected,
      reportName: authorization.name, attempts: audit.attempts, overlapMs: overlap,
      executions: Object.fromEntries(['leader', 'agent', 'review', 'verification', 'publication', 'postverify'].map(type => [type,
        finishes.filter(item => item.executionType === type).length])), reviewDigest: final.review.digest,
      publicationReceiptArtifactId: final.publication.receiptArtifactId, postverifyEvidenceArtifactId: final.postverify.evidenceArtifactId});
  }
  await first.stop(); const beforeOpen = observations(), originalReads = reader.reads.length;
  const second = await launch('open'); assert.notEqual(second.connectionFile, first.connectionFile);
  for (const item of completed) {
    const c = second.client, taskId = item.taskId;
    same(await c.request('task.create', item.create), item.created); same(await c.request('task.approve', item.approve), item.approval);
    for (const [request, receipt] of [[item.answer, item.answerReceipt], [item.allow, item.allowReceipt]]) {
      const replay = await c.request('task.leader.reply', request); assert.equal(replay.replayed, true); same({...replay, replayed: false}, receipt);
    }
    same(await c.request('task.get', {path: {taskId}}), item.done); same(await c.request('task.leader', {path: {taskId}}), item.final);
    same(await c.request('task.audit', {path: {taskId}}), item.audit); same(await c.request('task.workers', {path: {taskId}, query: {limit: 100}}), item.workers);
    same(await events(c, taskId), item.history);
    const result = await c.downloadArtifact(item.deliveryId); assert.equal(hash(result.content), item.deliveryDigest); same(JSON.parse(result.content), item.report);
  }
  assert.equal(reader.reads.length, originalReads, 'cold open never repeats publication/postverify');
  await second.stop();
  const afterOpen = observations(), config = afterOpen.filter(item => item.type === 'configuration'); assert.equal(config.length, 2); same(config[0], config[1]);
  same(afterOpen.filter(item => item.type !== 'configuration'), beforeOpen.filter(item => item.type !== 'configuration'));
  assert.equal(fs.readdirSync(reportRoot).length, 2); assert.deepEqual(verify({root: installed, manifestDigest}), originalPackage);
  const result = {sourceHead, manifestDigest, artifactId: options.artifactId ?? null, files: originalPackage.files, node: process.versions.node, platform: process.platform, arch: process.arch,
    uid: process.getuid(), layout: 7, sameConfiguration: true, tasks: completed.map(({taskId, attempts, overlapMs, deliveryDigest, deliveryBytes,
      executions, reviewDigest, publicationReceiptArtifactId, postverifyEvidenceArtifactId}) =>
      ({taskId, attempts, overlapMs, deliveryDigest, deliveryBytes, executions, reviewDigest, publicationReceiptArtifactId, postverifyEvidenceArtifactId})),
    modelCalls: 0, coldReplayDuplicateStarts: 0, coldReplayDuplicatePublications: 0,
    proofScope: 'installed-v7-fixture-consumption-not-model-or-release-approval'};
  save('result.json', result); passed = true; t.diagnostic(JSON.stringify(result)); return result;
}
