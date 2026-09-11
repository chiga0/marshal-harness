import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFileSync, spawnSync} from 'node:child_process';
import {SOURCE_FILES, pack} from './index.mjs';
import {runUpgrade, validatePair, readPages, compareAnswerReplay} from './upgrade-consumer.fixture.mjs';

const repository = fs.realpathSync(fileURLToPath(new URL('../..', import.meta.url)));
const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], {encoding: 'utf8', timeout: 10000, stdio: ['ignore', 'pipe', 'pipe']}).trim();
function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-dual-package-fixture-')));
  t.diagnostic('受控同源码双包证据（保留；不是v1.0.2跨版证明）：' + parent);
  const source = path.join(parent, 'source'); fs.mkdirSync(source, {mode: 0o700});
  for (const name of SOURCE_FILES) {
    fs.mkdirSync(path.dirname(path.join(source, name)), {mode: 0o700, recursive: true});
    fs.copyFileSync(path.join(repository, name), path.join(source, name));
  }
  git(source, 'init', '-q'); git(source, 'add', 'packages');
  git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'controlled same-source package pair');
  const sourceHead = git(source, 'rev-parse', 'HEAD');
  const oldRoot = path.join(parent, 'old-api-only'), old = pack({sourceRoot: source, sourceHead, target: oldRoot});
  fs.mkdirSync(path.join(source, 'apps/task-web/dist/assets'), {recursive: true, mode: 0o700});
  fs.writeFileSync(path.join(source, 'apps/task-web/dist/index.html'), '<!doctype html><p>controlled fixture UI, not release UI</p>');
  fs.writeFileSync(path.join(source, 'apps/task-web/dist/assets/app.js'), 'export const controlledFixture = true;');
  const newRoot = path.join(parent, 'new-ui'), next = pack({sourceRoot: source, sourceHead, target: newRoot});
  fs.appendFileSync(path.join(source, 'packages/task-regional-window/policy.mjs'), '\n// controlled incompatible config identity\n');
  git(source, 'add', 'packages');
  git(source, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'controlled changed business identity');
  const otherHead = git(source, 'rev-parse', 'HEAD'), otherRoot = path.join(parent, 'changed-business');
  const other = pack({sourceRoot: source, sourceHead: otherHead, target: otherRoot});
  return {oldPackage: {root: oldRoot, sourceHead, manifestDigest: old.manifestDigest},
    newPackage: {root: newRoot, sourceHead, manifestDigest: next.manifestDigest}, runDir: path.join(parent, 'run'), assetKind: 'controlled-fixture',
    differentBusiness: {root: otherRoot, sourceHead: otherHead, manifestDigest: other.manifestDigest}};
}
test('explicit dual-package entry rejects absent pins; consumer never packs or imports source Core', () => {
  const entry = fileURLToPath(new URL('./upgrade-consumer.mjs', import.meta.url));
  const result = spawnSync(process.execPath, [entry], {env: {}, encoding: 'utf8', timeout: 5000});
  assert.notEqual(result.status, 0); assert.equal(result.stdout, '');
  const helper = fs.readFileSync(new URL('./upgrade-consumer.fixture.mjs', import.meta.url), 'utf8');
  assert.doesNotMatch(helper, /\bpack\s*\(|\bexecFileSync\b|startTaskService|from ['"]\.\.\/task-(?:application|service|client|store)/);
  assert.match(helper, /path\.join\(pkg\.root, 'packages\/task-regional-window\/service-config\.mjs'\)/);
  assert.ok(!SOURCE_FILES.some(name => name.includes('upgrade-consumer')));
});
test('complete pagination has exact task binding and rejects cursor loops, duplicates and foreign pages', async () => {
  let count = 0;
  const client = {request: async () => ({taskId: 'task-a', items: [{id: 'event-' + ++count, taskId: 'task-a'}], nextCursor: count < 3 ? 'cursor-' + count : null})};
  assert.equal((await readPages(client, 'task.events', 'task-a')).length, 3);
  for (const page of [
    {taskId: 'task-b', items: [], nextCursor: null},
    {taskId: 'task-a', items: [], nextCursor: 'loop'},
    {taskId: 'task-a', items: [{id: 'same', taskId: 'task-a'}, {id: 'same', taskId: 'task-a'}], nextCursor: null},
    {taskId: 'task-a', items: [{id: 'foreign', taskId: 'task-b'}], nextCursor: null},
  ]) await assert.rejects(readPages({request: async () => page}, 'task.events', 'task-a'));
});
test('answer replay changes only contract replay/currentTask fields, never the frozen receipt', () => {
  const original = {taskId: 'task-a', acceptedRevision: 2, acceptedPreviewDigest: 'original', replayed: false, currentTask: {revision: 2}};
  const currentTask = {revision: 9}, actual = {...original, replayed: true, currentTask};
  compareAnswerReplay(actual, original, currentTask);
  for (const change of [{replayed: false}, {taskId: 'task-b'}, {acceptedRevision: 9}, {acceptedPreviewDigest: 'new'}, {extra: true}, {currentTask: {revision: 8}}])
    assert.throws(() => compareAnswerReplay({...actual, ...change}, original, currentTask));
});
test('same physical root old API-only → new UI → old API-only preserves complete task evidence and starts no replacement execution', {timeout: 60000}, async t => {
  const o = fixture(t);
  for (const change of [
    {oldPackage: {...o.oldPackage, sourceHead: '0'.repeat(40)}},
    {newPackage: {...o.newPackage, manifestDigest: 'sha256:' + '0'.repeat(64)}},
    {newPackage: o.oldPackage}, {oldPackage: o.newPackage, newPackage: o.oldPackage},
    {assetKind: 'fixed-assets'},
    {runDir: path.join(o.oldPackage.root, 'evidence')},
  ]) assert.throws(() => validatePair({...o, ...change}));
  assert.throws(() => validatePair({...o, newPackage: o.differentBusiness}), /regional_config_identity_changed/);
  assert.equal(fs.existsSync(o.runDir), false, 'bad pins/scopes never create a state root');
  const result = await runUpgrade(o);
  assert.equal(result.passed, true); assert.equal(result.assetKind, 'controlled-fixture');
  assert.equal(result.modelCalls, 0); assert.equal(result.originalAttempts, 3); assert.equal(result.newAgentStarts, 0);
  assert.deepEqual(result.stages, ['old-api-only-completed', 'upgraded-ui', 'rollback-api-only']);
  for (const label of ['upgraded-ui', 'rollback-api-only']) {
    assert.deepEqual(fs.readFileSync(path.join(o.runDir, label + '.json')), fs.readFileSync(path.join(o.runDir, 'original.json')));
  }
  const evidence = fs.readFileSync(path.join(o.runDir, 'evidence.json'));
  assert.equal(fs.statSync(path.join(o.runDir, 'evidence.json')).mode & 0o777, 0o600);
  assert.doesNotMatch(evidence.toString(), /"token"|"authorization"/);
  await assert.rejects(runUpgrade(o), /new_evidence_directory_required/);
  assert.deepEqual(fs.readFileSync(path.join(o.runDir, 'evidence.json')), evidence);
});
