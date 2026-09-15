// Independent command for this explicit acceptance business. Agent code only
// executes here, under the ORIGINAL bounded command guard, never in the driver.
// Trusted candidate code, NOT a hostile same-UID JavaScript sandbox.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {execFileSync} from 'node:child_process';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';

const git = (cwd, args) => execFileSync('/usr/bin/git', ['--no-pager', '-c', 'core.hooksPath=/dev/null', '-c', 'core.autocrlf=false',
  '-c', 'core.fsmonitor=false', '-c', 'core.attributesFile=/dev/null', '-c', 'credential.helper=', '-c', 'protocol.allow=never', ...args],
{cwd, env: {PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_NO_REPLACE_OBJECTS: '1'},
  timeout: 5000, maxBuffer: 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe']});
let totalInput = 0;
process.stdin.on('data', chunk => {totalInput += chunk.length; if (totalInput > 256 * 1024) process.exit(1);});
for await (const line of createInterface({input: process.stdin})) {
  let request; try {request = JSON.parse(line);} catch {process.exit(1);}
  const consuming = request.profile === 'git-mixed-consumer/v1';
  if (!consuming && request.profile !== 'task-verification-command/v1') process.exit(1);
  const report = actual => process.stdout.write(encode(consuming ? {profile: request.profile, nonce: request.nonce, actual} :
    {profile: request.profile, nonce: request.nonce, binding: request.binding, assertions: [{name: 'git-combination', actual}]}).toString() + '\n', () => process.exit(0));
  let stage = 'binding';
  try {
    assert.deepEqual(request.input.repositories.map(value => value.nodeId), ['library', 'client']);
    const locations = {}, kept = [], patches = []; let checks = 0;
    for (const item of request.input.repositories) {
      const name = item.nodeId === 'library' ? 'net.mjs' : 'invoice.mjs';
      const content = consuming ? request.input.delivery.files.find(value => value.repositoryId === item.repositoryId) : null;
      const patch = consuming ? Buffer.from(content.patch) : fs.readFileSync(item.nodeId + '.patch');
      const context = consuming ? content.context : JSON.parse(fs.readFileSync(item.nodeId + '-context.json', 'utf8'));
      assert.ok(patch.length > 0 && patch.length <= 128 * 1024);
      assert.equal(context.profile, 'task-git-context/v1');
      for (const key of ['nodeId', 'repositoryId', 'base', 'workerId', 'taskId', 'reservationDigest', 'inputDigest', 'planDigest']) assert.equal(context[key], item[key]);
      assert.deepEqual(context.writePaths, [name]); assert.equal(context.patchDigest, digest(patch)); assert.equal(context.patchDigest, item.patchDigest);
      assert.equal(context.patchBytes, patch.length); assert.equal(context.files.length, 1); assert.equal(context.files[0].path, name);
      const initial = git(item.root, ['show', item.base + ':' + name]);
      assert.equal(context.files[0].before, digest(initial));
      assert.equal(context.baseTree, git(item.root, ['rev-parse', item.base + '^{tree}']).toString().trim()); checks++;
      stage = 'apply'; const cwd = path.join(request.input.worktreeParent, request.nonce + '-' + item.nodeId);
      fs.mkdirSync(cwd, {mode: 0o700});
      git(item.root, ['worktree', 'add', '--detach', '--lock', '--reason', consuming ? 'download-consumer' : 'independent-checker', cwd, item.base]);
      assert.equal(git(cwd, ['rev-parse', 'HEAD']).toString().trim(), item.base);
      const patchPath = path.join(request.input.worktreeParent, request.nonce + '-' + item.nodeId + '.patch');
      fs.writeFileSync(patchPath, patch, {flag: 'wx', mode: 0o600});
      const before = fs.readFileSync(path.join(cwd, 'untouched.txt'));
      git(cwd, ['apply', '--check', '--', patchPath]); git(cwd, ['apply', '--index', '--', patchPath]);
      assert.deepEqual(git(cwd, ['diff', '--cached', '--name-only']).toString().trim().split('\n'), [name]);
      assert.equal(digest(fs.readFileSync(path.join(cwd, name))), context.files[0].after);
      assert.deepEqual(fs.readFileSync(path.join(cwd, 'untouched.txt')), before);
      assert.equal(digest(before), item.untouchedDigest); checks += 3;
      kept.push({nodeId: item.nodeId, digest: digest(before)}); patches.push({nodeId: item.nodeId, digest: digest(patch)}); locations[item.nodeId] = cwd;
    }
    stage = 'combination';
    const {net} = await import(pathToFileURL(path.join(locations.library, 'net.mjs')));
    const {invoice} = await import(pathToFileURL(path.join(locations.client, 'invoice.mjs')));
    const actual = invoice([{sku: 'desk', cents: 1000, discount: 100}, {sku: 'lamp', cents: 500, discount: 50}], net);
    assert.deepEqual(invoice([], net), {lines: [], total: 0});
    assert.deepEqual(invoice([{sku: ' x ', cents: 7, discount: 7}], net), {lines: [{sku: ' x ', amount: 0}], total: 0});
    assert.equal(net(Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER), 0); checks += 3;
    stage = 'negative'; let negativeChecks = 0;
    for (const args of [[-1, 0], [1, -1], [1, 2], [Number.MAX_SAFE_INTEGER + 1, 0], [1, 0.5], ['1', 0], [1, NaN]]) {
      assert.throws(() => net(...args)); negativeChecks++;
    }
    for (const rows of [null, [,], [null], [{sku: ' ', cents: 1, discount: 0}],
      [{sku: 'x', cents: Number.MAX_SAFE_INTEGER, discount: 0}, {sku: 'y', cents: 1, discount: 0}]]) {
      assert.throws(() => invoice(rows, net)); negativeChecks++;
    }
    checks += negativeChecks;
    report({invoice: actual, checks, negativeChecks, kept, patches});
  } catch {report({failureStage: stage});} // Stable diagnostic, never an accepted assertion.
  break;
}
