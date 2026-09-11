// External installed-package consumer; excluded from the runtime inventory.
// The only configuration is the ORIGINAL installed regional-window config.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {verify} from '../task-distribution/index.mjs';

const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const same = (a, b) => assert.deepEqual(JSON.parse(JSON.stringify(a)), JSON.parse(JSON.stringify(b)));
export function parseOptions(args) {
  const names = ['package', 'source-head', 'manifest-digest', 'node', 'qwen-entry', 'run-dir', 'scenario'];
  const o = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i].slice(2);
    assert.ok(args[i].startsWith('--') && !Object.hasOwn(o, key), 'invalid_arguments');
    if (['execute-real', 'controlled-acp-test'].includes(key)) o[key] = true;
    else {assert.ok(names.includes(key) && args[i + 1] && !args[i + 1].startsWith('--'), 'invalid_arguments'); o[key] = args[++i];}
  }
  assert.ok(names.every(key => typeof o[key] === 'string'), 'missing_arguments');
  assert.equal(Number(o['execute-real'] === true) + Number(o['controlled-acp-test'] === true), 1, 'explicit_execution_kind_required');
  assert.match(o['source-head'], /^[a-f0-9]{40}$/); assert.match(o['manifest-digest'], /^sha256:[a-f0-9]{64}$/);
  for (const key of ['package', 'node', 'qwen-entry', 'run-dir']) assert.ok(path.isAbsolute(o[key]) && path.normalize(o[key]) === o[key]);
  assert.ok(['delivery', 'cancel'].includes(o.scenario)); return o;
}
export async function observe(read, predicate, deadline) {
  while (Date.now() < deadline) {const result = await read(); if (predicate(result)) return result; await pause(50);}
  throw new Error('observation_deadline');
}
export function activeAuthors(workers) {
  return workers.nextCursor === null && workers.items.length === 2 &&
    workers.items.every(w => w.role === 'author' && w.status === 'running' && typeof w.startedAt === 'string') &&
    workers.items.map(w => w.nodeId).sort().join(',') === 'east,west';
}
export function checkDelivery(content, bytes, dates) {
  const rows = JSON.parse(bytes).rows;
  const files = ['east', 'west'].map(region => {
    const selected = rows.filter(row => row.region === region && row.status === 'paid' && row.date >= dates.startDate && row.date <= dates.endDate);
    return {path: region + '.json', result: {region, ...dates, count: selected.length, netCents: selected.reduce((sum, row) => sum + row.cents, 0)}};
  });
  const actual = JSON.parse(content);
  same(Object.keys(actual).sort(), ['files', 'profile', 'sourceDigest', 'window']);
  assert.equal(actual.profile, 'regional-paid-window/v1'); same(actual.window, dates); assert.equal(actual.sourceDigest, hash(bytes));
  assert.equal(actual.files.length, 2);
  actual.files.forEach((file, i) => {same(Object.keys(file).sort(), ['content', 'path']); assert.equal(file.path, files[i].path); same(JSON.parse(file.content), files[i].result);});
  return {digest: hash(content), bytes: content.length, reports: files.map(file => file.result)};
}
function privateDirectory(root) {
  const stat = fs.lstatSync(root);
  assert.ok(stat.isDirectory() && stat.uid === process.getuid() && (stat.mode & 0o777) === 0o700 && fs.realpathSync(root) === root, 'private_directory_required');
}
export function launch(node, args, env, cwd) {
  const child = spawn(node, args, {env, cwd, stdio: ['ignore', 'pipe', 'pipe']});
  let output = '', stderrBytes = 0, closed = false, stopping, resolveReady, rejectReady;
  const ready = new Promise((resolve, reject) => {resolveReady = resolve; rejectReady = reject;});
  const exit = new Promise(resolve => {
    child.once('error', () => rejectReady(new Error('service_spawn_failed')));
    child.once('close', (code, signal) => {closed = true; rejectReady(new Error('service_closed')); resolve({code, signal, stderrBytes});});
  });
  child.stdout.on('data', bytes => {
    if (Buffer.byteLength(output) + bytes.length > 8192) {rejectReady(new Error('service_output_bound')); child.kill('SIGTERM'); return;}
    output += bytes.toString();
    if (output.includes('\n')) try {resolveReady(JSON.parse(output.split('\n')[0]));} catch {rejectReady(new Error('service_ready_invalid'));}
  });
  child.stderr.on('data', bytes => {stderrBytes += bytes.length; if (stderrBytes > 65536) child.kill('SIGTERM');});
  const timer = setTimeout(() => rejectReady(new Error('service_ready_deadline')), 10000);
  return {ready: ready.finally(() => clearTimeout(timer)), stop() {
    stopping ??= (async () => {
      if (!closed) child.kill('SIGTERM');
      const kill = setTimeout(() => {if (!closed) child.kill('SIGKILL');}, 15000);
      try {await observe(async () => closed, Boolean, Date.now() + 20000); const result = await exit;
        assert.equal(result.code, 0); assert.equal(result.signal, null); assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true); return result;
      } finally {clearTimeout(kill);}
    })(); return stopping;
  }};
}
export async function run(options) {
  const o = parseOptions(Object.entries(options).flatMap(([key, value]) => value === true ? ['--' + key] : ['--' + key, value]));
  assert.ok(Number(process.versions.node.split('.')[0]) >= 22); assert.ok(process.getuid() > 0);
  assert.equal(fs.realpathSync(o.node), fs.realpathSync(process.execPath));
  const manifest = verify({root: o.package, manifestDigest: o['manifest-digest']});
  assert.equal(manifest.sourceHead, o['source-head'], 'source_pin_mismatch');
  assert.equal(fs.realpathSync(o['qwen-entry']), o['qwen-entry']); assert.equal(path.basename(o['qwen-entry']), 'cli-entry.js');
  const qwen = JSON.parse(fs.readFileSync(path.join(path.dirname(o['qwen-entry']), 'package.json')));
  assert.equal(qwen.name, '@qwen-code/qwen-code'); assert.equal(typeof qwen.version, 'string');
  assert.ok(path.isAbsolute(process.env.HOME ?? '')); privateDirectory(path.dirname(o['run-dir']));
  fs.mkdirSync(o['run-dir'], {mode: 0o700}); privateDirectory(o['run-dir']);
  const root = o['run-dir'], state = path.join(root, 'data'), handles = [];
  const evidence = {profile: 'installed-qwen-window-consumer/v1', passed: false, scenario: o.scenario,
    executionKind: o['execute-real'] ? 'real-qwen' : 'controlled-acp-test', sourceHead: manifest.sourceHead,
    manifestDigest: o['manifest-digest'], nodeVersion: process.versions.node, platform: process.platform, arch: process.arch,
    qwenVersion: qwen.version, qwenEntryDigest: hash(fs.readFileSync(o['qwen-entry'])), stage: 'loading',
    scope: {layout: 1, production: false, leaderV7: false, publication: false, faultRecovery: false,
      processOverlapProven: false, originalProcessCancellationProven: false, tokenOrToolCancellationProven: false},
    // A command-line label cannot prove provider usage. Only the test peer's
    // independently inspected no-network implementation establishes test zero.
    modelCalls: null, usage: {tokens: null, cost: null, source: 'unavailable'}, exits: []};
  const save = (name, value) => fs.writeFileSync(path.join(root, name), JSON.stringify(value, null, 2) + '\n', {flag: 'wx', mode: 0o600});
  let interrupted = false, client;
  const interrupt = () => {interrupted = true; for (const h of handles) void h.stop().catch(() => {});};
  process.on('SIGINT', interrupt); process.on('SIGTERM', interrupt);
  try {
    const load = file => import(pathToFileURL(path.join(o.package, 'packages', file)).href);
    const [{TaskClient}, {intake, answer, complete}] = await Promise.all([load('task-client/index.mjs'), load('task-regional-window/driver.mjs')]);
    const env = {PATH: path.dirname(o.node) + ':/usr/bin:/bin:/usr/sbin:/sbin', HOME: process.env.HOME, MARSHAL_QWEN_ENTRY: o['qwen-entry']};
    for (const key of ['LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (process.env[key]) env[key] = process.env[key];
    async function start(mode) {
      assert.equal(interrupted, false); same(verify({root: o.package, manifestDigest: o['manifest-digest']}), manifest);
      const handle = launch(o.node, [path.join(o.package, manifest.entrypoint), '--root', state, '--mode', mode, '--port', '0',
        '--config', path.join(o.package, 'packages/task-regional-window/service-config.mjs')], env, root);
      handles.push(handle); const ready = await handle.ready;
      assert.ok(ready.connectionFile.startsWith(state + path.sep) && fs.realpathSync(ready.connectionFile) === ready.connectionFile);
      assert.equal(fs.statSync(ready.connectionFile).mode & 0o777, 0o600);
      const connection = JSON.parse(fs.readFileSync(ready.connectionFile));
      return {handle, client: new TaskClient({baseURL: connection.url, token: connection.token, timeoutMs: 10000})};
    }
    let handle; ({client, handle} = await start('create'));
    assert.equal(JSON.parse(fs.readFileSync(path.join(state, 'profile.json'))).layout, 1);
    const dates = {startDate: '2026-09-01', endDate: '2026-09-02'};
    const bytes = Buffer.from(JSON.stringify({rows: [
      {date: '2026-08-31', region: 'east', status: 'paid', cents: 9999},
      {date: '2026-09-01', region: 'east', status: 'paid', cents: 100},
      {date: '2026-09-02', region: 'east', status: 'paid', cents: -25},
      {date: '2026-09-02', region: 'east', status: 'paid', cents: 0},
      {date: '2026-09-02', region: 'west', status: 'paid', cents: 50},
      {date: '2026-09-02', region: 'west', status: 'cancelled', cents: 9999},
      {date: '2026-09-03', region: 'west', status: 'paid', cents: 9999}]}));
    evidence.stage = 'intake'; const session = await intake(client, {bytes, key: 'installed-window', timeoutMs: 600000});
    evidence.taskId = session.taskId; save('session.json', session);
    const preview = await answer(client, session, dates); save('preview.json', preview);
    const deadline = Math.min(Date.now() + 600000, Date.parse(preview.confirmBefore));
    let done, operation, cancelRequest, delivery;
    evidence.stage = o.scenario;
    if (o.scenario === 'delivery') {
      const result = await complete(client, session, preview.approval); done = result.task; operation = result.operation; delivery = result;
      evidence.delivery = checkDelivery(result.content, bytes, dates); assert.equal(result.artifact.digest, evidence.delivery.digest);
      save('delivery.json', JSON.parse(result.content));
    } else {
      operation = await client.approveTask(session.taskId, preview.approval, session.key + '-approve');
      const observed = await observe(async () => {
        const workers = await client.request('task.workers', {path: {taskId: session.taskId}}), task = await client.getTask(session.taskId);
        assert.ok(!['failed', 'cancelled', 'completed', 'intervention'].includes(task.status), 'cancel_window_missed');
        return {workers, task};
      }, value => value.task.status === 'running' && activeAuthors(value.workers), deadline);
      evidence.cancelObservedAuthors = observed.workers.items; evidence.cancelRequestedAt = new Date().toISOString();
      cancelRequest = {path: {taskId: session.taskId}, body: {expectedRevision: observed.task.revision}, idempotencyKey: 'installed-window-cancel'};
      const receipt = await client.request('task.cancel', cancelRequest); evidence.cancelOperation = receipt;
      done = await observe(() => client.getTask(session.taskId), task => {
        assert.ok(!['failed', 'completed', 'intervention'].includes(task.status), 'cancel_window_missed'); return task.status === 'cancelled';
      }, deadline);
      const terminal = await observe(() => client.request('operation.get', {path: {operationId: receipt.id}}), value => !['accepted', 'running'].includes(value.status), deadline);
      assert.equal(terminal.status, 'succeeded'); assert.equal(done.artifactIds.length, 0);
    }
    const audit = await client.request('task.audit', {path: {taskId: session.taskId}});
    const workers = await client.request('task.workers', {path: {taskId: session.taskId}});
    assert.equal(workers.nextCursor, null); assert.equal(audit.attempts, o.scenario === 'delivery' ? 3 : 2);
    if (o.scenario === 'cancel') {assert.notEqual(audit.acceptance.status, 'passed'); assert.ok(workers.items.every(w => w.role === 'author' && w.status === 'cancelled'));}
    save('task.json', done); save('audit.json', audit); save('workers.json', workers); evidence.audit = audit;
    evidence.stage = 'normal-cold-reopen'; evidence.exits.push(await handle.stop());
    ({client, handle} = await start('open'));
    same(await client.createTask(session.body, session.key + '-create'), session.created);
    same(await client.approveTask(session.taskId, preview.approval, session.key + '-approve'), operation);
    if (cancelRequest) same(await client.request('task.cancel', cancelRequest), evidence.cancelOperation);
    same(await client.getTask(session.taskId), done); same(await client.request('task.audit', {path: {taskId: session.taskId}}), audit);
    same(await client.request('task.workers', {path: {taskId: session.taskId}}), workers);
    if (delivery) assert.deepEqual((await client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
    evidence.exits.push(await handle.stop()); same(verify({root: o.package, manifestDigest: o['manifest-digest']}), manifest);
    evidence.coldReplay = {sameTask: true, sameAudit: true, sameWorkers: true, faultRecovery: false};
    assert.equal(interrupted, false); evidence.passed = true; evidence.stage = 'completed';
  } catch (error) {
    evidence.failure = {name: error.name, code: typeof error.code === 'string' ? error.code : 'consumer_assertion_or_observation_failed'};
    if (client && evidence.taskId) for (const [name, operation] of [['task', 'task.get'], ['audit', 'task.audit'], ['workers', 'task.workers']]) {
      try {save('failure-' + name + '.json', await client.request(operation, {path: {taskId: evidence.taskId}}));} catch {}
    }
    throw error;
  }
  finally {
    for (const handle of handles) try {await handle.stop();} catch {evidence.passed = false; evidence.cleanupFailed = true;}
    process.off('SIGINT', interrupt); process.off('SIGTERM', interrupt); save('evidence.json', evidence);
  }
  assert.equal(evidence.passed, true); return evidence;
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run(parseOptions(process.argv.slice(2))).then(result => process.stdout.write(JSON.stringify(result) + '\n'), () => {process.stderr.write('Qwen 安装包验收失败；私有 run-dir 保留证据。\n'); process.exitCode = 1;});
}
