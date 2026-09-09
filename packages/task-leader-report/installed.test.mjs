import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {execFileSync, spawn} from 'node:child_process';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {pack, verify, SOURCE_FILES} from '../task-distribution/index.mjs';
const repository = fileURLToPath(new URL('../..', import.meta.url)), here = file => fileURLToPath(new URL(file, import.meta.url));
const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], {encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'pipe']}).trim();
async function until(read, predicate, label, ms = 60000) {
  const deadline = Date.now() + ms;
  for (;;) {const value = await read(); if (predicate(value)) return value; assert.ok(Date.now() < deadline, label); await pause(25);}
}
test('installed production report configuration: same config, two original HTTP inputs/answers, owned team, explicit publication, real GET and cold replay', {timeout: 150000}, async t => {
  assert.equal(process.versions.node, '24.15.0');
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-report-installed-'))), source = path.join(root, 'source'), installed = path.join(root, 'package');
  fs.mkdirSync(source, {mode: 0o700});
  for (const name of SOURCE_FILES) {fs.mkdirSync(path.dirname(path.join(source, name)), {recursive: true, mode: 0o700}); fs.copyFileSync(path.join(repository, name), path.join(source, name));}
  git(source, 'init', '-q'); git(source, 'add', 'packages'); git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'report installed source');
  const sourceHead = git(source, 'rev-parse', 'HEAD'), packed = pack({sourceRoot: source, sourceHead, target: installed});
  const originalPackage = verify({root: installed, manifestDigest: packed.manifestDigest}); assert.equal(originalPackage.sourceHead, sourceHead);
  const load = file => import(pathToFileURL(path.join(installed, 'packages', file)).href);
  const [{TaskClient}, {encode, digest}, {taskBody, PROFILE}, {startReportServer}, {nameFor}] = await Promise.all([
    load('task-client/index.mjs'), load('task-store/store.mjs'), load('task-leader-report/policy.mjs'), load('task-leader-report/report-server.mjs'), load('task-publication-report/index.mjs')]);
  const same = (a, b) => assert.deepEqual(encode(a), encode(b));
  const reportRoot = path.join(root, 'reports'), barrier = path.join(root, 'barrier'), journal = path.join(root, 'journal.jsonl'), state = path.join(root, 'state');
  for (const folder of [reportRoot, barrier]) fs.mkdirSync(folder, {mode: 0o700});
  const handles = []; let reader, passed = false;
  t.after(async () => {try {for (const handle of handles) await handle.stop();} finally {await reader?.close();
    t.diagnostic((passed ? '验收现场保留：' : '失败证据保留：') + root);}});
  reader = await startReportServer({root: reportRoot, port: 0});
  const observations = () => fs.existsSync(journal) ? fs.readFileSync(journal, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
  const env = {MARSHAL_REPORT_TEST_INSTALLED: installed, MARSHAL_REPORT_TEST_MANIFEST: packed.manifestDigest, MARSHAL_REPORT_TEST_SOURCE: sourceHead,
    MARSHAL_REPORT_TEST_JOURNAL: journal, MARSHAL_REPORT_TEST_BARRIER: barrier, MARSHAL_REPORT_ROOT: reportRoot, MARSHAL_REPORT_URL: reader.url};
  let launchNumber = 0;
  async function launch(mode) {
    same(verify({root: installed, manifestDigest: packed.manifestDigest}), originalPackage);
    const child = spawn(process.execPath, [path.join(installed, originalPackage.entrypoint), '--root', state, '--mode', mode,
      '--config', here('./service.fixture.mjs'), '--port', '0'], {cwd: root, env, stdio: ['ignore', 'pipe', 'pipe']});
    let output = '', stderrBytes = 0, stopping, closed = false, resolveReady, rejectReady;
    const ready = new Promise((resolve, reject) => {resolveReady = resolve; rejectReady = reject;}), number = ++launchNumber;
    const exit = new Promise(resolve => {child.once('error', error => rejectReady(new Error('spawn: ' + error.code)));
      child.once('close', (code, signal) => {closed = true; const result = {code, signal, stderrBytes};
        fs.writeFileSync(path.join(root, number + '-exit.json'), JSON.stringify(result), {flag: 'wx', mode: 0o600});
        rejectReady(new Error('CLI closed: ' + JSON.stringify(result))); resolve(result);});});
    child.stderr.on('data', bytes => {stderrBytes += bytes.length; if (stderrBytes > 8192) child.kill('SIGTERM');});
    child.stdout.on('data', bytes => {output += bytes.toString(); if (output.length > 8192) {rejectReady(new Error('stdout bound')); child.kill('SIGTERM');}
      else if (output.includes('\n')) {try {resolveReady(JSON.parse(output.split('\n')[0]));} catch {rejectReady(new Error('bad ready'));}}});
    const handle = {stop() {stopping ??= (async () => {if (!closed) child.kill('SIGTERM');
      const timer = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 10000);
      try {await until(async () => closed, Boolean, 'owned CLI shutdown', 15000); const ended = await exit;
        assert.equal(ended.code, 0); assert.equal(ended.signal, null); assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true);
      } finally {clearTimeout(timer);}})(); return stopping;}}; handles.push(handle);
    const timer = setTimeout(() => rejectReady(new Error('CLI readiness deadline')), 10000);
    try {const value = await ready, connection = JSON.parse(fs.readFileSync(value.connectionFile)); assert.equal(fs.statSync(value.connectionFile).mode & 0o777, 0o600);
      const client = new TaskClient({baseURL: connection.url, token: connection.token}); assert.equal((await client.request('ready.get')).ready, true); return {...handle, client};
    } finally {clearTimeout(timer);}
  }
  const first = await launch('create'), client = first.client, completed = [];
  assert.equal(JSON.parse(fs.readFileSync(path.join(state, 'profile.json'))).layout, 7);
  for (const [index, cents, window] of [[0, 100, {startDate: '2026-09-01', endDate: '2026-09-01'}], [1, 300, {startDate: '2026-09-02', endDate: '2026-09-03'}]]) {
    const data = {rows: [{date: '2026-09-01', region: 'east', status: 'paid', cents}, {date: '2026-09-02', region: 'east', status: 'paid', cents: -20},
      {date: '2026-09-02', region: 'east', status: 'paid', cents: 0}, {date: '2026-09-03', region: 'west', status: 'paid', cents: cents + 50},
      {date: '2026-09-03', region: 'west', status: 'cancelled', cents: 999999}]};
    const bytes = encode(data), input = await client.request('input.create', {idempotencyKey: 'report-input-' + index,
      body: {name: 'sales.json', mediaType: 'application/json', contentBase64: bytes.toString('base64')}});
    const body = taskBody(input.id, {startDate: null, endDate: null}, {intent: '处理第' + index + '批真实窗口需求', timeoutMs: 120000}), create = {body, idempotencyKey: 'report-task-' + index};
    const created = await client.request('task.create', create), taskId = created.id;
    const get = () => client.getTask(taskId), view = () => client.getLeader(taskId);
    const phase = async status => {const result = await until(get, task => [status, 'failed', 'cancelled', 'intervention'].includes(task.status), 'phase ' + status);
      assert.equal(result.status, status, JSON.stringify({status: result.status, taskId})); return result;};
    let task = await phase('awaiting-answer'), leader = await view(); assert.equal(task.plan, null); assert.equal(leader.pendingRequest.kind, 'business');
    assert.equal(leader.pendingRequest.options.length, 0);
    const answer = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'report-answer-' + index,
      body: {expectedRevision: task.revision, requestDigest: leader.pendingRequest.requestDigest, answer: JSON.stringify(window)}};
    const answerReceipt = await client.request('task.leader.reply', answer);
    task = await phase('awaiting-approval'); const plan = await client.request('task.plan', {path: {taskId}});
    same(plan.edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]); same(plan.budget, body.limits);
    assert.deepEqual(plan.nodes.map(node => node.id), ['east', 'west', 'verify']);
    const approve = {path: {taskId}, idempotencyKey: 'report-approve-' + index, body: {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}};
    const approval = await client.request('task.approve', approve);
    task = await phase('awaiting-confirmation'); leader = await view(); assert.equal(leader.review.verdict, 'accept');
    assert.equal(fs.readdirSync(reportRoot).length, index, 'no output before explicit allow');
    const authorization = leader.pendingRequest.authorization, delivery = await client.downloadArtifact(authorization.artifactId), auditBefore = await client.getAudit(taskId);
    // Independent test consumer values, not the product oracle or author output.
    const reports = index === 0 ? [{region: 'east', ...window, count: 1, netCents: 100}, {region: 'west', ...window, count: 0, netCents: 0}] :
      [{region: 'east', ...window, count: 2, netCents: -20}, {region: 'west', ...window, count: 1, netCents: 350}];
    const expected = {profile: PROFILE, window, sourceDigest: digest(bytes), reports}; same(JSON.parse(delivery.content), expected);
    same(authorization, {taskId, planDigest: plan.digest, artifactId: delivery.artifact.id, artifactDigest: digest(delivery.content), bytes: delivery.content.length,
      acceptanceDigest: auditBefore.acceptance.digest, reviewDigest: leader.review.digest, targetId: 'local-window-report',
      targetPolicyDigest: authorization.targetPolicyDigest, operation: 'create-if-absent', name: nameFor({taskId, artifactDigest: delivery.artifact.digest}), expiresAt: authorization.expiresAt});
    assert.ok(Date.now() < Date.parse(authorization.expiresAt) && Date.parse(authorization.expiresAt) <= Date.parse(created.deadlineAt));
    const allow = {path: {taskId, requestId: leader.pendingRequest.id}, idempotencyKey: 'report-allow-' + index,
      body: {expectedRevision: task.revision, requestDigest: leader.pendingRequest.requestDigest, decision: 'allow'}};
    const allowReceipt = await client.request('task.leader.reply', allow), done = await phase('completed'), final = await view(), audit = await client.getAudit(taskId);
    assert.equal(done.deadlineAt, created.deadlineAt); assert.equal(final.publication.status, 'succeeded'); assert.equal(final.postverify.status, 'succeeded');
    assert.equal(final.publication.authorizationDigest, digest(encode(authorization))); assert.equal(final.stage, 'terminal');
    assert.equal(audit.acceptance.status, 'passed'); assert.equal(audit.reworkCount, 0); assert.equal(audit.retryCount, 0); assert.ok(audit.attempts <= 17);
    assert.equal(audit.measurement.usageSource, 'unavailable');
    for (const id of [final.summaryArtifactId, final.review.evidenceIds[0], final.publication.receiptArtifactId, final.postverify.evidenceArtifactId, ...audit.acceptance.evidenceIds])
      assert.equal((await client.downloadArtifact(id)).artifact.taskId, taskId);
    const response = await fetch(new URL(authorization.name, reader.url), {redirect: 'error', signal: AbortSignal.timeout(5000)});
    assert.equal(response.status, 200); assert.equal(Number(response.headers.get('content-length')), delivery.content.length);
    assert.deepEqual(Buffer.from(await response.arrayBuffer()), Buffer.from(delivery.content));
    const workers = await client.request('task.workers', {path: {taskId}, query: {limit: 100}}), facts = observations().filter(item => item.type === 'finished' && workers.items.some(worker => worker.id === item.workerId));
    const authors = workers.items.filter(worker => worker.role === 'author').map(worker => facts.find(item => item.workerId === worker.id));
    assert.equal(authors.length, 2); assert.ok(authors.every(Boolean));
    const overlap = Math.min(...authors.map(item => Date.parse(item.cleanup.agentExit.at))) - Math.max(...authors.map(item => Date.parse(item.cleanup.started.startedAt)));
    assert.ok(overlap > 0); assert.ok(facts.every(item => item.cleanup.cleaned === true && item.status === 'completed'));
    assert.equal((await client.request('supervisor.get')).activeWorkers, 0);
    completed.push({taskId, create, created, approve, approval, answer, answerReceipt, allow, allowReceipt, done, final, audit, workers,
      artifactId: delivery.artifact.id, bytes: Buffer.from(delivery.content), overlap});
  }
  await first.stop(); const before = observations(), filesBefore = fs.readdirSync(reportRoot).map(name => ({name, stat: fs.statSync(path.join(reportRoot, name))}));
  const second = await launch('open');
  for (const item of completed) {
    const c = second.client;
    same(await c.request('task.create', item.create), item.created); same(await c.request('task.approve', item.approve), item.approval);
    for (const [request, receipt] of [[item.answer, item.answerReceipt], [item.allow, item.allowReceipt]]) {
      const replay = await c.request('task.leader.reply', request); assert.equal(replay.replayed, true); same({...replay, replayed: false}, receipt);
    }
    same(await c.getTask(item.taskId), item.done); same(await c.getLeader(item.taskId), item.final); same(await c.getAudit(item.taskId), item.audit);
    same(await c.request('task.workers', {path: {taskId: item.taskId}, query: {limit: 100}}), item.workers);
    assert.deepEqual(Buffer.from((await c.downloadArtifact(item.artifactId)).content), item.bytes);
  }
  await second.stop(); const after = observations();
  same(after.filter(item => item.type !== 'configuration'), before.filter(item => item.type !== 'configuration'));
  const configs = after.filter(item => item.type === 'configuration'); assert.equal(configs.length, 2); same(configs[0], configs[1]);
  for (const item of filesBefore) {const stat = fs.statSync(path.join(reportRoot, item.name)); assert.equal(stat.ino, item.stat.ino); assert.equal(stat.mtimeMs, item.stat.mtimeMs);}
  assert.equal(fs.readdirSync(reportRoot).length, 2); same(verify({root: installed, manifestDigest: packed.manifestDigest}), originalPackage);
  passed = true; t.diagnostic(JSON.stringify({sourceHead, manifestDigest: packed.manifestDigest, layout: 7, sameConfiguration: true, modelCalls: 0,
    duplicateStarts: 0, duplicatePublications: 0, tasks: completed.map(item => ({taskId: item.taskId, overlapMs: item.overlap, attempts: item.audit.attempts, artifactDigest: digest(item.bytes)}))}));
});
