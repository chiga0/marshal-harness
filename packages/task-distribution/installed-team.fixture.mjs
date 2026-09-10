import {supportsNode} from '../task-store/runtime.mjs';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {spawn} from 'node:child_process';
import {setTimeout as pause} from 'node:timers/promises';
import {createHash} from 'node:crypto';
import {verify} from './index.mjs';

const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
async function until(observe, predicate, milliseconds = 15000) {
  const end = Date.now() + milliseconds;
  for (;;) {const result = await observe(); if (predicate(result)) return result;
    assert.ok(Date.now() < end, 'installed team bounded observation expired'); await pause(25);}
}

export async function exerciseInstalledTeam(t, {installed, manifestDigest, sourceHead, custody}) {
  assert.ok(supportsNode());
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-installed-team-')));
  const state = path.join(parent, 'state');
  const journal = path.join(parent, 'observations.jsonl'), children = [];
  let passed = false;
  t.after(async () => {
    for (const child of children) await child.stop();
    if (passed) fs.rmSync(parent, {recursive: true, force: true});
    else t.diagnostic('失败证据保留：' + parent);
  });
  const report = verify({root: installed, manifestDigest});
  assert.equal(report.sourceHead, sourceHead);
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
    let closed = false, spawnError = false;
    const exit = new Promise(resolve => {
      child.once('exit', () => rejectReady(new Error('installed service exited before ready')));
      child.once('error', () => {spawnError = true; rejectReady(new Error('installed service spawn failed'));});
      child.once('close', (code, signal) => {closed = true; resolve(spawnError ? {code: null, signal: 'spawn-error'} : {code, signal});});
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
        try {
          await until(async () => closed, Boolean, 12000);
          const result = await exit;
          assert.deepEqual(result, {code: 0, signal: null});
          assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true);
          return result;
        } finally {clearTimeout(timer);}
      })();
      return stopping;
    }};
    children.push(handle);
    // t.after still awaits this same stop promise and reports any failure.
    const timer = setTimeout(() => {rejectReady(new Error('installed service startup deadline')); void handle.stop().catch(() => {});}, 10000);
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
  assert.deepEqual(verify({root: installed, manifestDigest}), report);
  passed = true;
  const summary = {sourceHead, manifestDigest, node: process.versions.node, platform: process.platform, arch: process.arch,
    uid: process.getuid(), layout: custody ? 2 : 1, originalExecutions: starts.length, attempts: audit.attempts,
    deliveryDigest: delivery.artifact.digest, coldReplayDuplicateStarts: 0};
  t.diagnostic(JSON.stringify(summary)); return summary;
}
