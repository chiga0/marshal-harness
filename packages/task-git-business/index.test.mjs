import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from '../task-service/composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {allocateWorktree, runGit} from './git.mjs';
import {createGitBusiness, gitDescription, PATCH, CONTEXT} from './index.mjs';
import {policy, bindPlan} from './scenario.fixture.mjs';

const GIT = '/usr/bin/git', here = name => fileURLToPath(new URL(name, import.meta.url));
const checker = here('./checker.fixture.mjs');
const expected = {lines: [{sku: 'desk', amount: 900}, {sku: 'lamp', amount: 450}], total: 1350};
const equal = (actual, value) => assert.equal(digest(encode(actual)), digest(encode(value)));
const git = async (cwd, args, extra) => (await runGit(GIT, cwd, args, extra)).toString();
async function until(observe, predicate, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {const value = await observe(); if (predicate(value)) return value; assert.ok(Date.now() < end, 'bounded observation timeout'); await pause(20);}
}
async function repositories(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-git-business-')));
  const beforeRemove = [];
  // Test-owned repositories only. Never remove or modify a user's checkout.
  t.after(async () => {for (const close of beforeRemove) await close(); fs.rmSync(parent, {recursive: true, force: true});});
  const roots = {}, nodes = [];
  for (const [nodeId, filename, content] of [['library', 'net.mjs', 'export function net(cents, discount) { return cents; }\n'],
    ['client', 'invoice.mjs', 'export function invoice(rows, net) { return {lines: [], total: 0}; }\n']]) {
    const root = path.join(parent, nodeId); fs.mkdirSync(root, {mode: 0o700}); roots[nodeId] = root;
    await git(root, ['init', '-b', 'main']);
    fs.writeFileSync(path.join(root, filename), content, {mode: 0o644});
    fs.writeFileSync(path.join(root, 'untouched.txt'), nodeId + ' original unrelated content\n', {mode: 0o644});
    await git(root, ['add', '--', filename, 'untouched.txt']);
    await git(root, ['-c', 'user.name=Git business fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--no-gpg-sign', '-m', 'locked fixture base']);
    const base = (await git(root, ['rev-parse', 'HEAD'])).trim();
    nodes.push({nodeId, repositoryId: nodeId, base, writePaths: [filename]});
  }
  return {parent, roots, beforeRemove, description: {profile: 'task-git-input/v1', nodes}};
}
async function fixture(t, mode = 'good') {
  const repos = await repositories(t), root = path.join(repos.parent, 'data');
  const verificationParent = path.join(repos.parent, 'verification'); fs.mkdirSync(verificationParent, {mode: 0o700});
  const handles = [], services = [], executions = []; let verifierStarts = 0, verificationActual = null, verificationOutcome = null;
  const native = createAcpProvider({id: 'git-fixture-acp', executable: process.execPath, args: [here('./agent.fixture.mjs')],
    env: {GIT_BUSINESS_FIXTURE_MODE: mode}});
  const provider = {id: native.id, start(input) {
    const handle = native.start(input); handles.push(handle); executions.push(input.cwd);
    if (mode === 'unconfirmed' && input.cwd.includes(path.sep + 'git-worktrees' + path.sep)) {
      // Fault injection removes an observation; it NEVER manufactures cleanup.
      // Test teardown still waits for the original managed fixture handle.
      return {...handle, completion: handle.completion.then(value => ({...value, status: 'unknown', cleanup: null}))};
    }
    return handle;
  }};
  const command = createVerificationCommand({executable: process.execPath, checkerPath: checker, checkerDigest: digest(fs.readFileSync(checker)),
    policyDigest: digest(encode(policy)),
    request: ({ticket}) => ({verificationParent, repositories: gitDescription(ticket.input.task.context.text).nodes.map(item => {
      const upstream = ticket.input.upstream.find(value => value.nodeId === item.nodeId);
      return {...item, root: repos.roots[item.repositoryId], workerId: upstream.workerId,
        reservationDigest: upstream.result.reservationDigest, planDigest: upstream.result.planDigest,
        patchDigest: upstream.result.files.find(file => file.path === PATCH).digest};
    })}),
    assertions: [{name: 'integration', validate: actual => {
      verificationActual = JSON.parse(encode(actual));
      equal(actual.invoice, expected); assert.equal(actual.negatives, 5);
      equal(actual.kept, ['library', 'client'].map(nodeId => ({nodeId, digest: digest(Buffer.from(nodeId + ' original unrelated content\n'))})));
      assert.equal(actual.patches.length, 2); return true;
    }}], delivery: ({ticket, prepared}) => ({name: 'cross-repository-patches.json', mediaType: 'application/json', content: encode({
      profile: 'cross-repository-delivery/v1', files: gitDescription(ticket.input.task.context.text).nodes.map(item => ({repositoryId: item.repositoryId,
        base: item.base, patch: fs.readFileSync(path.join(prepared.cwd, item.nodeId + '.patch'), 'utf8'),
        context: JSON.parse(fs.readFileSync(path.join(prepared.cwd, item.nodeId + '-context.json'), 'utf8'))}))})})});
  const verification = createVerificationPort({id: 'git-independent-checker', policy, bindPlan, start(input) {
    verifierStarts++; const handle = command.start(input);
    void handle.completion.then(value => {verificationOutcome = {status: value.status, reason: value.reason,
      cleaned: value.cleanup?.cleaned, exit: value.cleanup?.agentExit, started: !!value.cleanup?.started};});
    return handle;
  }});
  const config = {root, providers: new Map([[provider.id, provider]]), verification,
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createGitBusiness({parent: executionParent, depot,
      approvedLayout, observeExecution, repositoryFor: (_ticket, request) => repos.roots[request.repositoryId],
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout}),
    supervisorOptions: {intervalMs: 10, prepareMs: 10000, collectMs: 10000}};
  repos.beforeRemove.push(async () => {for (const service of services) await service.shutdown(); for (const handle of handles) await handle.stop();});
  async function start(mode) {
    const service = await startTaskService({...config, mode}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};
  }
  const live = await start('create');
  return {...repos, ...live, start, handles, executions, get verifierStarts() {return verifierStarts;}, get verificationActual() {return verificationActual;},
    get diagnostic() {return {verificationActual, verificationOutcome, verifierStarts};}};
}
async function approve(f) {
  const body = {intent: '在 library 和 client 两个原仓库的独立工作树完成折扣 API 与发票组合，只交付 patch，不发布。',
    context: {text: JSON.stringify(f.description)}, limits: {timeoutMs: 90000, maxAttempts: 4, maxWorkers: 2}};
  const created = await f.client.createTask(body, 'git-create');
  const task = await until(() => f.client.getTask(created.id), value => ['awaiting-approval', 'failed', 'intervention'].includes(value.status));
  assert.equal(task.status, 'awaiting-approval');
  const plan = await f.client.request('task.plan', {path: {taskId: task.id}});
  for (const node of f.description.nodes) assert.ok(plan.acceptance.some(line => line.includes(node.base) && line.includes(node.repositoryId)));
  const request = {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
  const operation = await f.client.approveTask(task.id, request, 'git-approve');
  equal(await f.client.approveTask(task.id, request, 'git-approve'), operation);
  return {task, body, created, request, operation};
}
async function unchangedSources(f) {
  for (const node of f.description.nodes) {
    assert.equal((await git(f.roots[node.repositoryId], ['rev-parse', 'HEAD'])).trim(), node.base);
    assert.equal(await git(f.roots[node.repositoryId], ['status', '--porcelain']), '');
    assert.equal(await git(f.roots[node.repositoryId], ['remote']), '');
  }
}

test('actual HTTP two-repository Git worktrees produce patches, independent apply/combination, download and cold same bytes', {timeout: 60000}, async t => {
  const f = await fixture(t), approved = await approve(f);
  const done = await until(() => f.client.getTask(approved.task.id), item => ['completed', 'failed', 'intervention'].includes(item.status));
  assert.equal(done.status, 'completed', JSON.stringify(f.diagnostic)); assert.equal(f.handles.length, 3); assert.equal(f.verifierStarts, 1);
  const authors = await Promise.all(f.handles.slice(1).map(handle => handle.completion));
  for (const result of authors) assert.equal(result.cleanup.cleaned, true);
  assert.ok(Math.max(...authors.map(value => Date.parse(value.cleanup.started.startedAt))) < Math.min(...authors.map(value => Date.parse(value.cleanup.agentExit.at))));
  for (const cwd of f.executions.slice(1)) {
    assert.ok(cwd.includes(path.sep + 'git-worktrees' + path.sep)); assert.equal(fs.statSync(path.join(cwd, '.git')).isFile(), true);
    assert.match(await git(cwd, ['worktree', 'list', '--porcelain']), /locked marshal-owned-/);
  }
  const audit = await f.client.request('task.audit', {path: {taskId: done.id}});
  assert.equal(audit.attempts, 4); assert.equal(audit.acceptance.status, 'passed');
  const downloads = await Promise.all(done.artifactIds.map(id => f.client.downloadArtifact(id)));
  const delivery = downloads.find(value => value.artifact.kind === 'delivery'); assert.ok(delivery);
  const bundle = JSON.parse(delivery.content); assert.equal(bundle.files.length, 2);
  const consumers = {};
  for (const item of bundle.files) {
    const cwd = path.join(f.parent, 'consumer-' + item.repositoryId); fs.mkdirSync(cwd, {mode: 0o700});
    await git(f.roots[item.repositoryId], ['worktree', 'add', '--detach', '--lock', cwd, item.base]);
    const patchFile = path.join(f.parent, item.repositoryId + '-download.patch'); fs.writeFileSync(patchFile, item.patch, {flag: 'wx', mode: 0o600});
    assert.equal(digest(Buffer.from(item.patch)), item.context.patchDigest);
    await git(cwd, ['apply', '--check', '--', patchFile]); await git(cwd, ['apply', '--index', '--', patchFile]);
    assert.equal(fs.readFileSync(path.join(cwd, 'untouched.txt'), 'utf8'), item.repositoryId + ' original unrelated content\n'); consumers[item.repositoryId] = cwd;
  }
  const {net} = await import(pathToFileURL(path.join(consumers.library, 'net.mjs')));
  const {invoice} = await import(pathToFileURL(path.join(consumers.client, 'invoice.mjs')));
  equal(invoice([{sku: 'desk', cents: 1000, discount: 100}, {sku: 'lamp', cents: 500, discount: 50}], net), expected);
  await unchangedSources(f);
  await f.service.shutdown(); const opened = await f.start('open');
  equal(await opened.client.createTask(approved.body, 'git-create'), approved.created);
  equal(await opened.client.approveTask(done.id, approved.request, 'git-approve'), approved.operation);
  assert.deepEqual((await opened.client.downloadArtifact(delivery.artifact.id)).content, delivery.content);
  assert.equal(f.handles.length, 3); assert.equal(f.verifierStarts, 1);
});

test('valid applicable patches with wrong combined business answer are rejected by independent oracle', {timeout: 60000}, async t => {
  const f = await fixture(t, 'wrong'), {task} = await approve(f);
  const done = await until(() => f.client.getTask(task.id), item => ['failed', 'intervention', 'completed'].includes(item.status));
  assert.equal(done.status, 'failed'); assert.equal(f.verifierStarts, 1); assert.deepEqual([...done.artifactIds], []);
  const audit = await f.client.request('task.audit', {path: {taskId: task.id}});
  assert.equal(audit.acceptance.status, 'failed'); await unchangedSources(f);
});

test('out-of-scope actual Git edits never enter verifier or delivery', {timeout: 60000}, async t => {
  const f = await fixture(t, 'extra'), {task} = await approve(f);
  const done = await until(() => f.client.getTask(task.id), item => ['failed', 'intervention', 'completed'].includes(item.status));
  assert.equal(done.status, 'failed'); assert.equal(f.verifierStarts, 0); assert.deepEqual([...done.artifactIds], []); await unchangedSources(f);
});

test('HTTP cancellation preserves locked worktrees after original cleanup and has no verifier/delivery', {timeout: 60000}, async t => {
  const f = await fixture(t, 'hang'), {task} = await approve(f);
  await until(() => f.client.request('task.workers', {path: {taskId: task.id}}), value => value.items.filter(worker => worker.role === 'author' && worker.startedAt).length === 2);
  const current = await f.client.getTask(task.id);
  await f.client.request('task.cancel', {path: {taskId: task.id}, idempotencyKey: 'git-cancel', body: {expectedRevision: current.revision}});
  assert.equal((await until(() => f.client.getTask(task.id), value => value.status === 'cancelled')).status, 'cancelled');
  for (const handle of f.handles) assert.equal((await handle.completion).cleanup.cleaned, true);
  for (const cwd of f.executions.slice(1)) {assert.equal(fs.existsSync(cwd), true); assert.match(await git(cwd, ['worktree', 'list', '--porcelain']), /locked marshal-owned-/);}
  assert.equal(f.verifierStarts, 0); await unchangedSources(f);
});

test('unconfirmed execution retains real dirty worktree/lock and cannot manufacture a patch or release capacity', {timeout: 60000}, async t => {
  const f = await fixture(t, 'unconfirmed'), {task} = await approve(f);
  const done = await until(() => f.client.getTask(task.id), value => value.status === 'intervention');
  assert.deepEqual([...done.artifactIds], []); assert.equal(f.verifierStarts, 0);
  for (const handle of f.handles) await handle.stop();
  for (const cwd of f.executions.slice(1)) {
    assert.equal(fs.existsSync(cwd), true); assert.match(await git(cwd, ['worktree', 'list', '--porcelain']), /locked marshal-owned-/);
    const workerId = path.basename(cwd), stage = path.join(f.parent, 'data', 'executions', workerId);
    assert.equal(fs.existsSync(path.join(stage, PATCH)), false); assert.equal(fs.existsSync(path.join(stage, CONTEXT)), false);
  }
  const workers = await f.client.request('task.workers', {path: {taskId: task.id}});
  assert.ok(workers.items.some(worker => worker.status === 'unknown'));
  await assert.rejects(f.client.request('ready.get')); await unchangedSources(f);
});

test('allocation rejects prior paths, changed HEAD, untracked/link/out-of-scope files and empty changes', {timeout: 45000}, async t => {
  const f = await repositories(t), parent = path.join(f.parent, 'worktrees'), indexes = path.join(f.parent, 'indexes');
  fs.mkdirSync(parent, {mode: 0o700}); fs.mkdirSync(indexes, {mode: 0o700});
  const node = f.description.nodes[0], signal = new AbortController().signal;
  const options = {gitExecutable: GIT, repository: f.roots.library, base: node.base, parent, indexParent: indexes,
    writePaths: node.writePaths, deadline: Date.now() + 40000, signal};
  for (const mode of ['unchanged', 'extra', 'symlink', 'untracked', 'head']) {
    const worktree = await allocateWorktree({...options, workerId: 'worker-' + mode}); f.beforeRemove.push(() => worktree.close());
    if (mode === 'extra') fs.writeFileSync(path.join(worktree.cwd, 'untouched.txt'), 'bad');
    if (mode === 'untracked') fs.writeFileSync(path.join(worktree.cwd, 'extra.txt'), 'bad');
    if (mode === 'symlink') {fs.unlinkSync(path.join(worktree.cwd, 'net.mjs')); fs.symlinkSync(path.join(f.roots.library, 'net.mjs'), path.join(worktree.cwd, 'net.mjs'));}
    if (mode === 'head') {
      await git(worktree.cwd, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--allow-empty', '--no-gpg-sign', '-m', 'unapproved commit']);
    }
    await assert.rejects(worktree.collectPatch({deadline: options.deadline, signal}));
    worktree.close(); await assert.rejects(allocateWorktree({...options, workerId: 'worker-' + mode}));
  }
});

test('business context is explicit bounded data and cannot smuggle root/argv or traversal', () => {
  const value = {profile: 'task-git-input/v1', nodes: [{nodeId: 'a', repositoryId: 'r', base: 'a'.repeat(40), writePaths: ['src/a.mjs']}]};
  equal(gitDescription(JSON.stringify(value)), value);
  for (const modified of [{...value, root: '/private'}, {...value, nodes: [{...value.nodes[0], base: 'main'}]},
    {...value, nodes: [{...value.nodes[0], writePaths: ['../x']}]}, {...value, nodes: [{...value.nodes[0], root: '/private'}]}])
    assert.throws(() => gitDescription(JSON.stringify(modified)));
});
