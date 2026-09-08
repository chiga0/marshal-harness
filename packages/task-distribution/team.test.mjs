import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {execFileSync, spawn} from 'node:child_process';
import {setTimeout as pause} from 'node:timers/promises';
import {createHash} from 'node:crypto';
import {pack, verify, SOURCE_FILES, NODE_VERSION} from './index.mjs';

const repository = fileURLToPath(new URL('../..', import.meta.url));
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const git = (root, ...args) => execFileSync('git', ['-C', root, ...args], {encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'pipe']}).trim();
async function until(observe, predicate, milliseconds = 15000) {
  const end = Date.now() + milliseconds;
  for (;;) {const result = await observe(); if (predicate(result)) return result;
    assert.ok(Date.now() < end, 'installed team bounded observation expired'); await pause(25);}
}

for (const custody of [false, true]) test(`installed ${custody ? 'custody-v2' : 'legacy-v1'} package delivers a real process team through CLI/HTTP and cold-reopens exact results`, {timeout: 45000}, async t => {
  assert.equal(process.versions.node, NODE_VERSION);
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-installed-team-')));
  const source = path.join(parent, 'source'), installed = path.join(parent, 'package'), state = path.join(parent, 'state');
  const journal = path.join(parent, 'observations.jsonl'), children = [];
  let passed = false;
  t.after(async () => {
    for (const child of children) await child.stop();
    if (passed) fs.rmSync(parent, {recursive: true, force: true});
    else t.diagnostic('失败证据保留：' + parent);
  });
  fs.mkdirSync(source, {mode: 0o700});
  for (const file of SOURCE_FILES) {
    fs.mkdirSync(path.dirname(path.join(source, file)), {recursive: true, mode: 0o700});
    fs.copyFileSync(path.join(repository, file), path.join(source, file));
  }
  git(source, 'init', '-q'); git(source, 'add', 'packages');
  git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'package team source');
  const packed = pack({sourceRoot: source, sourceHead: git(source, 'rev-parse', 'HEAD'), target: installed});
  const report = verify({root: installed, manifestDigest: packed.manifestDigest});
  // The client also comes from the package. There are no source Core imports.
  const {TaskClient} = await import(pathToFileURL(path.join(installed, 'packages/task-client/index.mjs')).href);
  const config = fileURLToPath(new URL('./team-service.fixture.mjs', import.meta.url));
  const observations = () => fs.existsSync(journal) ? fs.readFileSync(journal, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
  async function launch(mode) {
    const child = spawn(process.execPath, [path.join(installed, report.entrypoint), '--root', state, '--mode', mode, '--config', config, '--port', '0'],
      {cwd: parent, env: {MARSHAL_PACKAGE_TEAM: '1', MARSHAL_PACKAGE_ROOT: installed, MARSHAL_PACKAGE_JOURNAL: journal,
        MARSHAL_PACKAGE_CUSTODY: custody ? '1' : '0'}, stdio: ['ignore', 'pipe', 'pipe']});
    let output = '', errors = '', connection, rejectReady, resolveReady;
    const ready = new Promise((resolve, reject) => {resolveReady = resolve; rejectReady = reject;});
    const exit = new Promise(resolve => {
      child.once('exit', (code, signal) => {rejectReady(new Error('installed service exited before ready')); resolve({code, signal});});
      child.once('error', () => {rejectReady(new Error('installed service spawn failed')); resolve({code: null, signal: 'spawn-error'});});
    });
    child.stdout.on('data', bytes => {
      output += bytes.toString();
      if (output.length > 4096) {rejectReady(new Error('installed service output limit')); child.kill('SIGTERM'); return;}
      if (!connection && output.includes('\n')) {
        try {connection = JSON.parse(output.split('\n')[0]); resolveReady(connection);} catch {rejectReady(new Error('installed service invalid ready'));}
      }
    });
    child.stderr.on('data', bytes => {errors += bytes.toString(); if (errors.length > 4096) child.kill('SIGTERM');});
    let stopping;
    const handle = {async stop() {
      stopping ??= (async () => {
        if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
        const timer = setTimeout(() => {if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');}, 8000);
        try {return await exit;} finally {clearTimeout(timer);}
      })();
      return stopping;
    }};
    children.push(handle);
    const timer = setTimeout(() => {rejectReady(new Error('installed service startup deadline')); void handle.stop();}, 10000);
    try {
      const started = await ready, credentials = JSON.parse(fs.readFileSync(started.connectionFile));
      assert.equal(fs.statSync(started.connectionFile).mode & 0o777, 0o600);
      const client = new TaskClient({baseURL: credentials.url, token: credentials.token});
      assert.equal((await client.request('ready.get')).ready, true);
      return {...handle, client, connectionFile: started.connectionFile};
    } finally {clearTimeout(timer);}
  }
  const first = await launch('create');
  assert.equal(JSON.parse(fs.readFileSync(path.join(state, 'profile.json'))).layout, custody ? 2 : 1);
  const data = {rows: [{region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
    {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
    {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100}]};
  const input = await first.client.request('input.create', {idempotencyKey: 'package-input', body: {
    name: 'sales.json', mediaType: 'application/json', contentBase64: Buffer.from(JSON.stringify(data)).toString('base64')}});
  const body = {intent: '发行包内两个地区并行计算、独立验收完整报告', context: {inputRefs: [input.id]},
    limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
  const created = await first.client.createTask(body, 'package-create');
  const task = await until(() => first.client.getTask(created.id), item => ['awaiting-approval', 'failed', 'intervention'].includes(item.status));
  assert.equal(task.status, 'awaiting-approval');
  const plan = await first.client.request('task.plan', {path: {taskId: task.id}});
  const approval = {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
  const operation = await first.client.approveTask(task.id, approval, 'package-approve');
  const done = await until(() => first.client.getTask(task.id), item => ['completed', 'failed', 'intervention'].includes(item.status));
  assert.equal(done.status, 'completed'); assert.equal(done.artifactIds.length, 2);
  const audit = await first.client.request('task.audit', {path: {taskId: task.id}});
  assert.equal(audit.acceptance.status, 'passed'); assert.equal(audit.attempts, 4);
  const downloads = await Promise.all(done.artifactIds.map(id => first.client.downloadArtifact(id)));
  const delivery = downloads.find(value => value.artifact.kind === 'delivery');
  assert.deepEqual(JSON.parse(delivery.content).files.map(file => JSON.parse(file.content)),
    [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}]);
  assert.equal(hash(delivery.content), delivery.artifact.digest);
  assert.equal((await first.client.request('operation.get', {path: {operationId: operation.id}})).status, 'succeeded');
  const events = observations(), starts = events.filter(value => value.type === 'started'), finishes = events.filter(value => value.type === 'finished');
  assert.deepEqual(starts.map(value => value.role).sort(), ['author', 'author', 'planner', 'verifier']);
  assert.equal(finishes.length, 4); assert.ok(finishes.every(value => value.cleanup.cleaned === true));
  const authors = finishes.filter(value => value.role === 'author');
  assert.ok(Math.max(...authors.map(value => Date.parse(value.cleanup.started.startedAt))) <
    Math.min(...authors.map(value => Date.parse(value.cleanup.agentExit.at))), 'original author process lifetimes overlap');
  assert.deepEqual(await first.stop(), {code: 0, signal: null});
  const second = await launch('open');
  assert.notEqual(second.connectionFile, first.connectionFile);
  assert.deepEqual(await second.client.createTask(body, 'package-create'), created);
  assert.deepEqual(await second.client.approveTask(task.id, approval, 'package-approve'), operation);
  assert.deepEqual(await second.client.getTask(task.id), done);
  assert.deepEqual(await second.client.request('task.audit', {path: {taskId: task.id}}), audit);
  assert.deepEqual((await second.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.deepEqual(await second.stop(), {code: 0, signal: null});
  assert.deepEqual(observations(), events, 'cold process reopening never starts a replacement worker');
  assert.deepEqual(verify({root: installed, manifestDigest: packed.manifestDigest}), report);
  passed = true;
});
