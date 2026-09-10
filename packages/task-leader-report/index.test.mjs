import test from 'node:test';
import {withoutSQLiteRuntimeNotices} from '../task-store/runtime-notices.fixture.mjs';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {encode, digest} from '../task-store/store.mjs';
import {leaderReplyDigest} from '../task-api/contract.mjs';
import {isManagedFileBusiness} from '../task-business/index.mjs';
import {createLeaderReportConfig, createPiLeaderReportConfig} from './index.mjs';
import {PROFILE, taskBody, finalWindow, originalReport, verificationRequest, expected, initial} from './policy.mjs';
import {filePermission} from './permission.mjs';
const bytes = encode({rows: [{date: '2026-09-01', region: 'east', status: 'paid', cents: 100}, {date: '2026-09-02', region: 'east', status: 'paid', cents: -20},
  {date: '2026-09-02', region: 'east', status: 'paid', cents: 0}, {date: '2026-09-02', region: 'west', status: 'cancelled', cents: 999},
  {date: '2026-09-02', region: 'west', status: 'paid', cents: 50}]});
function fixture(t) {const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-report-unit-')));
  t.after(() => fs.rmSync(root, {recursive: true, force: true})); return root;}
function ticket(window, answer) {
  const value = {taskId: 'task-example', input: {task: taskBody('input-example', window), inputArtifacts: [{id: 'input-example', kind: 'input', status: 'ready', digest: digest(bytes), bytes: bytes.length}],
    leaderReplies: [], leaderReplyRefs: []}};
  if (answer !== undefined) {const requestDigest = 'sha256:' + 'a'.repeat(64), requestId = 'request-example', ref = {requestId, requestDigest,
    replyDigest: leaderReplyDigest(value.taskId, requestId, {requestDigest, answer})}; value.input.leaderReplyRefs.push(ref); value.input.leaderReplies.push({...ref, answer});}
  return value;
}
test('original Task dates or exact bound answer drive reports; missing/foreign/changed answers and malformed rows fail closed', () => {
  const one = {startDate: '2026-09-01', endDate: '2026-09-01'}, two = {startDate: '2026-09-02', endDate: '2026-09-02'};
  const a = ticket({startDate: null, endDate: null}, JSON.stringify(one)), b = ticket(two), depot = {get: () => bytes};
  assert.deepEqual(finalWindow(a), one); assert.deepEqual(finalWindow(b), two);
  const requested = verificationRequest(depot, a);
  assert.deepEqual(requested.leaderReplyRefs, a.input.leaderReplyRefs); assert.deepEqual(requested.leaderReplies, a.input.leaderReplies);
  assert.equal(requested.sourceBase64, bytes.toString('base64')); assert.deepEqual(verificationRequest(depot, b).leaderReplyRefs, []);
  assert.deepEqual(originalReport(depot, a), {profile: PROFILE, window: one, sourceDigest: digest(bytes), reports: expected(bytes, one)});
  assert.notDeepEqual(originalReport(depot, a).reports, originalReport(depot, b).reports);
  assert.deepEqual(expected(bytes, two).map(value => [value.count, value.netCents]), [[2, -20], [1, 50]]);
  assert.throws(() => finalWindow(ticket({startDate: null, endDate: null})), /missing_window/);
  assert.throws(() => finalWindow(ticket({...one, endDate: null}, JSON.stringify(two))), /changed_original/);
  for (const mutate of [x => {x.taskId = 'other';}, x => {x.input.leaderReplies[0].answer = JSON.stringify(two);}, x => {x.input.leaderReplyRefs[0] = {...x.input.leaderReplyRefs[0], requestId: 'other'};}]) {
    const value = structuredClone(a); mutate(value); assert.throws(() => finalWindow(value), /original_reply/);
  }
  assert.throws(() => originalReport({get: () => Buffer.from('{}')}, b));
  assert.throws(() => initial({...b.input.task, context: {...b.input.task.context, text: '{"profile":"other"}'}}));
  assert.throws(() => finalWindow(ticket(two, JSON.stringify(two))), /unrequested_reply/);
});
test('Pi file permission is exact one-time author scope; no other tools, peers, inputs, links or managed-role writes', t => {
  const root = fixture(t), worker = {role: 'author', nodeId: 'east'};
  fs.writeFileSync(path.join(root, 'sales.json'), bytes, {mode: 0o600});
  const request = (name, rawInput) => ({toolCall: {kind: name === 'read' ? 'read' : 'edit', _meta: {provider: 'pi', toolName: name}, rawInput},
    options: [{kind: 'allow_once', optionId: 'allow-once'}, {kind: 'reject_once', optionId: 'deny-once'}]});
  const yes = q => assert.equal(filePermission(worker, root, q).outcome.optionId, 'allow-once');
  const no = (q, who = worker) => assert.equal(filePermission(who, root, q).outcome.outcome, 'cancelled');
  yes(request('read', {path: 'sales.json'})); yes(request('write', {path: path.join(root, 'east.json'), content: '{}'}));
  for (const file of ['sales.json', 'west.json', '../east.json', '/tmp/east.json']) no(request('write', {path: file, content: '{}'}));
  no(request('bash', {path: 'east.json', command: 'echo x'})); no(request('write', {path: 'east.json', content: '{}', extra: true}));
  no(request('write', {path: 'east.json', content: '{}'}), {role: 'planner', nodeId: 'east'});
  no(request('write', {path: 'east.json', content: '\0'}));
  const wrong = request('write', {path: 'east.json', content: '{}'}); wrong.options[0].optionId = 'proceed_once'; no(wrong);
  fs.symlinkSync(path.join(root, 'sales.json'), path.join(root, 'east.json')); no(request('read', {path: 'east.json'})); no(request('write', {path: 'east.json', content: '{}'}));
  fs.unlinkSync(path.join(root, 'east.json')); fs.writeFileSync(path.join(root, 'east.json'), '{}', {mode: 0o600});
  yes(request('edit', {path: 'east.json', oldText: '{}', newText: '{"count":0}'}));
  fs.linkSync(path.join(root, 'east.json'), path.join(root, 'alias')); no(request('edit', {path: 'east.json', oldText: '{}', newText: '{}'}));
});
test('configuration keeps original managed FileBusiness, exact target identity and no implicit Pi fallback', t => {
  const root = fixture(t), reports = path.join(root, 'reports'); fs.mkdirSync(reports, {mode: 0o700});
  const options = {provider: {id: 'controlled-fixture', start() {throw Error('must not start');}}, reportRoot: reports, readBaseURL: 'http://127.0.0.1:19999/reports/'};
  const first = createLeaderReportConfig(options), identity = first.publication.configurationDigest;
  const business = first.businessFactory({executionParent: root, depot: {get() {}, put() {}}, approvedLayout() {}, observeExecution() {}});
  assert.equal(isManagedFileBusiness(business), true); assert.equal(first.applicationOptions.defaultLimits.maxWorkers, 3);
  assert.equal(first.applicationOptions.execution.maxWorkers, 3); assert.equal(first.clarification, undefined);
  assert.throws(() => first.businessFactory({}), /already_active/); business.close(); first.dispose();
  const second = createLeaderReportConfig(options); assert.equal(second.publication.configurationDigest, identity); second.dispose();
  assert.throws(() => createPiLeaderReportConfig({reportRoot: reports, readBaseURL: options.readBaseURL}), /pi_configuration/);
});
test('packaged independent checker recomputes original arbitrary rows; valid JSON with wrong total is not accepted', t => {
  const root = fixture(t), window = {startDate: '2026-09-02', endDate: '2026-09-02'}, reports = expected(bytes, window);
  const checker = fileURLToPath(new URL('../task-regional-window/checker.mjs', import.meta.url));
  const run = () => spawnSync(process.execPath, [checker], {cwd: root, env: {}, timeout: 5000, encoding: 'utf8', input: JSON.stringify({profile: 'test-frame', nonce: 'test-nonce', binding: {},
    input: {...window, sourceDigest: digest(bytes), sourceBase64: bytes.toString('base64')}}) + '\n'});
  for (const report of reports) fs.writeFileSync(path.join(root, report.region + '.json'), JSON.stringify(report), {mode: 0o600});
  const good = run(); assert.equal(good.status, 0); assert.deepEqual(JSON.parse(good.stdout).assertions[1].actual, reports);
  fs.writeFileSync(path.join(root, 'east.json'), JSON.stringify({...reports[0], netCents: 0})); const bad = run();
  assert.equal(bad.status, 1); assert.equal(bad.stdout, ''); assert.equal(withoutSQLiteRuntimeNotices(bad.stderr), '');
});
