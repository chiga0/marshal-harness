import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {execFileSync, spawnSync, spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {pack, verify, SOURCE_FILES, NODE_VERSION} from './index.mjs';

const repository = fs.realpathSync(fileURLToPath(new URL('../..', import.meta.url)));
const git = (root, ...args) => execFileSync('git', ['-C', root, ...args], {encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']}).trim();
const hash = bytes => `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
function fixture(t) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-script-package-')));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  const source = path.join(root, 'source'); fs.mkdirSync(source);
  for (const file of SOURCE_FILES) {
    fs.mkdirSync(path.dirname(path.join(source, file)), {recursive: true});
    fs.copyFileSync(path.join(repository, file), path.join(source, file));
  }
  git(source, 'init', '-q'); git(source, 'add', 'packages');
  git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixed source');
  const sourceHead = git(source, 'rev-parse', 'HEAD');
  const target = path.join(root, 'package');
  const create = () => pack({sourceRoot: source, target, sourceHead});
  return {root, source, target, sourceHead, create};
}
test('reproducible same bytes, explicit complete runtime inventory, private fresh target and cold imports', t => {
  const f = fixture(t), first = f.create();
  const second = pack({sourceRoot: f.source, target: path.join(f.root, 'second'), sourceHead: f.sourceHead});
  assert.deepEqual(first, second);
  assert.equal(first.files, SOURCE_FILES.length);
  const report = verify({root: f.target, manifestDigest: first.manifestDigest});
  assert.equal(report.node, NODE_VERSION); assert.equal(report.sourceHead, f.sourceHead);
  assert.equal(fs.statSync(f.target).mode & 0o777, 0o700);
  for (const file of [...SOURCE_FILES, 'manifest.json']) {
    assert.equal(fs.statSync(path.join(f.target, file)).mode & 0o777, 0o600);
    assert.deepEqual(fs.readFileSync(path.join(f.target, file)), fs.readFileSync(path.join(f.root, 'second', file)));
    assert.ok(!/fixture|\.test\.|\.marshal|\.git/.test(file));
  }
  // Commands consume stdin; deployment configurations intentionally require
  // explicit local settings. They remain packaged and are exercised below,
  // rather than being treated as side-effect-free library imports.
  const entrypoints = new Set(['packages/task-service/main.mjs', 'packages/agent-runtime/guard.mjs', 'packages/agent-runtime/custody-process.mjs',
    'packages/task-regional-window/checker.mjs', 'packages/task-regional-window/service-config.mjs',
    'packages/task-publication-report/runner.mjs']);
  const imports = SOURCE_FILES.filter(file => file.endsWith('.mjs') && !entrypoints.has(file));
  const script = imports.map(file => `await import(${JSON.stringify(pathToFileURL(path.join(f.target, file)).href)});`).join('\n');
  const loaded = spawnSync(process.execPath, ['--input-type=module', '-e', script], {cwd: f.root, timeout: 10000, encoding: 'utf8'});
  assert.equal(loaded.status, 0, loaded.stderr);
  assert.equal(loaded.stdout, ''); // No server, model, or runtime process is launched by importing dependencies.
  const cli = spawnSync(process.execPath, [path.join(f.target, report.entrypoint)], {cwd: f.root, timeout: 10000, encoding: 'utf8'});
  assert.equal(cli.status, 1); assert.match(cli.stderr, /service_start_unavailable/);
  const state = path.join(f.root, 'unconfigured-window');
  const unconfigured = spawnSync(process.execPath, [path.join(f.target, report.entrypoint), '--root', state,
    '--mode', 'create', '--config', path.join(f.target, 'packages/task-regional-window/service-config.mjs')],
  {cwd: f.root, env: {}, timeout: 10000, encoding: 'utf8'});
  assert.equal(unconfigured.status, 1);
  assert.equal(unconfigured.stdout, '');
  assert.equal(unconfigured.stderr, '{"code":"service_start_unavailable"}\n');
  assert.equal(fs.existsSync(state), false); // No configuration fallback or partial service.
  const checker = spawnSync(process.execPath, [path.join(f.target, 'packages/task-regional-window/checker.mjs')],
    {cwd: f.root, env: {}, input: '{}\n', timeout: 10000, encoding: 'utf8'});
  assert.equal(checker.status, 1);
  assert.equal(checker.stdout, ''); // Invalid input cannot create a successful verification frame.
  assert.equal(checker.stderr, '');
  const publisher = spawnSync(process.execPath, [path.join(f.target, 'packages/task-publication-report/runner.mjs')],
    {cwd: f.root, env: {}, input: '{}\n', timeout: 10000, encoding: 'utf8'});
  assert.equal(publisher.status, 1);
  assert.equal(publisher.stdout, ''); // Packaged child rejects missing authority; no side effect or secret output.
  assert.equal(publisher.stderr, '');
});
test('never overwrite an existing destination and reject source-relative targets', t => {
  const f = fixture(t); f.create();
  fs.writeFileSync(path.join(f.target, 'preserve.txt'), 'existing user file');
  assert.throws(f.create, /package_io_failed/);
  assert.equal(fs.readFileSync(path.join(f.target, 'preserve.txt'), 'utf8'), 'existing user file');
  assert.throws(() => pack({sourceRoot: f.source, target: path.join(f.source, 'output'), sourceHead: f.sourceHead}), /target_inside_source/);
});
test('verified installed package serves HTTP and reopens SQLite in a fresh CLI process without model calls', async t => {
  const f = fixture(t), packed = f.create();
  const report = verify({root: f.target, manifestDigest: packed.manifestDigest});
  const {TaskClient} = await import(pathToFileURL(path.join(f.target, 'packages/task-client/index.mjs')).href);
  const config = path.join(repository, 'packages/task-service/service.fixture.mjs');
  const state = path.join(f.root, 'state');
  async function launch(mode) {
    const child = spawn(process.execPath, [path.join(f.target, report.entrypoint), '--root', state, '--mode', mode,
      '--config', config, '--port', '0'], {cwd: f.root, stdio: ['ignore', 'pipe', 'pipe']});
    let output = '', errorOutput = '', firstResolved = false, resolveFirst, rejectFirst;
    const first = new Promise((resolve, reject) => {resolveFirst = resolve; rejectFirst = reject;});
    const exit = new Promise(resolve => {
      child.once('exit', (code, signal) => {rejectFirst(new Error('service exited before ready')); resolve({code, signal});});
      child.once('error', error => {rejectFirst(error); resolve({code: null, signal: 'spawn-error'});});
    });
    const timer = setTimeout(() => {rejectFirst(new Error('installed service startup deadline')); child.kill('SIGTERM');}, 10000);
    child.stdout.on('data', bytes => {
      output += bytes.toString();
      if (output.length > 4096) {rejectFirst(new Error('service output limit')); child.kill('SIGTERM'); return;}
      if (!firstResolved && output.includes('\n')) {
        firstResolved = true;
        try {resolveFirst(JSON.parse(output.split('\n')[0]));} catch (error) {rejectFirst(error);}
      }
    });
    child.stderr.on('data', bytes => {errorOutput += bytes.toString(); if (errorOutput.length > 4096) child.kill('SIGTERM');});
    try {
      const started = await first;
      const connection = JSON.parse(fs.readFileSync(started.connectionFile));
      assert.equal(fs.statSync(started.connectionFile).mode & 0o777, 0o600);
      const client = new TaskClient({baseURL: connection.url, token: connection.token});
      assert.equal((await client.request('health.get')).status, 'ok');
      assert.equal((await client.request('ready.get')).ready, true);
      // The external test config throws on prepare/start: no fixture Worker or
      // real model may run. This tests the installed package, not source imports.
      return started.connectionFile;
    } finally {
      clearTimeout(timer);
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
      const killTimer = setTimeout(() => child.kill('SIGKILL'), 3000);
      try {
        const ended = await exit;
        assert.deepEqual(ended, {code: 0, signal: null});
        assert.equal(JSON.parse(output.trim().split('\n').at(-1)).clean, true);
      } finally {clearTimeout(killTimer);}
    }
  }
  const original = await launch('create'), reopened = await launch('open');
  assert.notEqual(original, reopened);
  assert.deepEqual(verify({root: f.target, manifestDigest: packed.manifestDigest}), report);
});
test('wrong commit, current source drift and omitted newly committed runtime dependency are rejected', t => {
  const f = fixture(t);
  assert.throws(() => pack({sourceRoot: f.source, target: f.target, sourceHead: '0'.repeat(40)}));
  fs.appendFileSync(path.join(f.source, SOURCE_FILES[0]), '\n// drift\n');
  assert.throws(f.create, /source_drift/); assert.ok(!fs.existsSync(f.target));
  fs.copyFileSync(path.join(repository, SOURCE_FILES[0]), path.join(f.source, SOURCE_FILES[0]));
  fs.writeFileSync(path.join(f.source, 'packages/task-application/new-runtime.mjs'), 'export const added = true;\n');
  git(f.source, 'add', 'packages');
  git(f.source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'new runtime');
  assert.throws(() => pack({sourceRoot: f.source, target: f.target, sourceHead: git(f.source, 'rev-parse', 'HEAD')}), /source_inventory_changed/);
});
test('missing, modified, extra files and empty extra directory fail closed', t => {
  const f = fixture(t), result = f.create();
  const check = () => verify({root: f.target, manifestDigest: result.manifestDigest});
  const file = path.join(f.target, SOURCE_FILES[0]), original = fs.readFileSync(file);
  fs.unlinkSync(file); assert.throws(check, /missing_entry/);
  fs.writeFileSync(file, original.subarray(1), {mode: 0o600}); assert.throws(check, /file_digest_mismatch/);
  fs.writeFileSync(file, original);
  fs.writeFileSync(path.join(f.target, '.secret'), 'must not be read'); assert.throws(check, /unexpected_entry/);
  fs.unlinkSync(path.join(f.target, '.secret'));
  fs.mkdirSync(path.join(f.target, 'unused')); assert.throws(check, /unexpected_entry/);
});
test('manifest pin is mandatory and path escape/duplicate aliases do not become authority', t => {
  const f = fixture(t), result = f.create();
  assert.throws(() => verify({root: f.target}), /invalid_manifest_digest/);
  assert.throws(() => verify({root: f.target, manifestDigest: 'sha256:' + '0'.repeat(64)}), /manifest_digest_mismatch/);
  const manifestPath = path.join(f.target, 'manifest.json'), original = fs.readFileSync(manifestPath);
  for (const bad of ['../outside', SOURCE_FILES[1], '/etc/passwd']) {
    const manifest = JSON.parse(original); manifest.files[0].path = bad;
    const bytes = Buffer.from(JSON.stringify(manifest, null, 2) + '\n'); fs.writeFileSync(manifestPath, bytes);
    assert.throws(() => verify({root: f.target, manifestDigest: result.manifestDigest}), /manifest_digest_mismatch/);
    assert.throws(() => verify({root: f.target, manifestDigest: hash(bytes)}), /invalid_manifest/);
  }
});
test('symlink and hardlink source/output boundaries are rejected without following outside data', t => {
  const f = fixture(t), result = f.create();
  const output = path.join(f.target, SOURCE_FILES[0]), source = path.join(f.source, SOURCE_FILES[0]);
  fs.unlinkSync(output); fs.symlinkSync(source, output);
  assert.throws(() => verify({root: f.target, manifestDigest: result.manifestDigest}), /unexpected_entry/);
  fs.unlinkSync(output); fs.linkSync(source, output);
  assert.throws(() => verify({root: f.target, manifestDigest: result.manifestDigest}), /unsafe_file|unsafe_permissions/);
  fs.unlinkSync(output);
  const parentAlias = path.join(f.root, 'alias'); fs.symlinkSync(f.source, parentAlias);
  assert.throws(() => pack({sourceRoot: parentAlias, target: path.join(f.root, 'aliased'), sourceHead: f.sourceHead}), /unsafe_directory/);
  const saved = fs.readFileSync(source); fs.unlinkSync(source);
  fs.writeFileSync(path.join(f.root, 'source-copy'), saved); fs.symlinkSync(path.join(f.root, 'source-copy'), source);
  assert.throws(() => pack({sourceRoot: f.source, target: path.join(f.root, 'linked'), sourceHead: f.sourceHead}));
});
test('CLI prints only safe summary or stable error code, never injected path text', t => {
  const f = fixture(t), result = f.create();
  const cli = fileURLToPath(new URL('./main.mjs', import.meta.url));
  const ok = spawnSync(process.execPath, [cli, 'verify', '--root', f.target, '--manifest-digest', result.manifestDigest], {encoding: 'utf8', timeout: 10000});
  assert.equal(ok.status, 0); assert.equal(JSON.parse(ok.stdout).sourceHead, f.sourceHead);
  const bad = spawnSync(process.execPath, [cli, 'verify', '--root', '/do-not-echo-secret', '--manifest-digest', result.manifestDigest], {encoding: 'utf8', timeout: 10000});
  assert.equal(bad.status, 1); assert.equal(bad.stdout, ''); assert.doesNotMatch(bad.stderr, /do-not-echo-secret/);
});
test('oversized source and unsafe package permissions fail closed', t => {
  const f = fixture(t);
  fs.appendFileSync(path.join(f.source, SOURCE_FILES[0]), Buffer.alloc(2 * 1024 * 1024));
  assert.throws(f.create, /unsafe_file/); assert.ok(!fs.existsSync(f.target));
  fs.copyFileSync(path.join(repository, SOURCE_FILES[0]), path.join(f.source, SOURCE_FILES[0]));
  const result = f.create();
  fs.chmodSync(path.join(f.target, SOURCE_FILES[0]), 0o644);
  assert.throws(() => verify({root: f.target, manifestDigest: result.manifestDigest}), /unsafe_permissions/);
});
test('write durability failure preserves partial target and does not claim success or reuse it', t => {
  const f = fixture(t), originalSync = fs.fsyncSync;
  try {
    fs.fsyncSync = () => { throw new Error('injected-disk-failure-secret'); };
    assert.throws(f.create, /package_io_failed/);
  } finally { fs.fsyncSync = originalSync; }
  assert.ok(fs.existsSync(f.target));
  assert.ok(fs.existsSync(path.join(f.target, SOURCE_FILES[0])));
  assert.ok(!fs.existsSync(path.join(f.target, 'manifest.json')));
  assert.throws(f.create, /package_io_failed/);
});
