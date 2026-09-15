// Trusted independent checker fixture, outside every author's repository.
// It applies actual patches to separate locked worktrees and executes their
// combined API; this trusted-code test is not a hostile JavaScript sandbox.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {createInterface} from 'node:readline';
import {encode} from '../task-store/store.mjs';
const digest = value => 'sha256:' + createHash('sha256').update(value).digest('hex');
const git = (cwd, args) => execFileSync('/usr/bin/git', ['--no-pager', '-c', 'core.hooksPath=/dev/null', '-c', 'core.autocrlf=false',
  '-c', 'core.fsmonitor=false', '-c', 'credential.helper=', '-c', 'protocol.allow=never', ...args],
{cwd, env: {PATH: '/usr/bin:/bin', LC_ALL: 'C', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_NO_REPLACE_OBJECTS: '1'},
  encoding: 'utf8', timeout: 4000, maxBuffer: 8 * 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe']});
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line), locations = {}, kept = [], patches = []; let stage = 'binding';
  const report = actual => process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'integration', actual}]}).toString() + '\n', () => process.exit(0));
  try {
    for (const item of request.input.repositories) {
      const patchPath = path.resolve(item.nodeId + '.patch'), patch = fs.readFileSync(patchPath);
      const binding = JSON.parse(fs.readFileSync(item.nodeId + '-context.json', 'utf8'));
      assert.equal(binding.repositoryId, item.repositoryId); assert.equal(binding.base, item.base);
      assert.equal(binding.workerId, item.workerId); assert.equal(binding.reservationDigest, item.reservationDigest);
      assert.equal(binding.planDigest, item.planDigest); assert.equal(binding.patchDigest, digest(patch));
      assert.equal(digest(patch), item.patchDigest); assert.deepEqual(binding.writePaths, item.writePaths);
      stage = 'allocate'; const cwd = path.join(request.input.verificationParent, request.nonce + '-' + item.nodeId);
      fs.mkdirSync(cwd, {mode: 0o700});
      git(item.root, ['worktree', 'add', '--detach', '--lock', '--reason', 'independent-verification', cwd, item.base]);
      assert.equal(git(cwd, ['rev-parse', 'HEAD']).trim(), item.base);
      const before = fs.readFileSync(path.join(cwd, 'untouched.txt'));
      stage = 'apply'; git(cwd, ['apply', '--check', '--', patchPath]); git(cwd, ['apply', '--index', '--', patchPath]);
      stage = 'unchanged';
      assert.deepEqual(git(cwd, ['diff', '--cached', '--name-only']).trim().split('\n'), item.writePaths);
      assert.deepEqual(fs.readFileSync(path.join(cwd, 'untouched.txt')), before);
      kept.push({nodeId: item.nodeId, digest: digest(before)}); patches.push({nodeId: item.nodeId, digest: digest(patch)}); locations[item.nodeId] = cwd;
    }
    stage = 'combination'; const {net} = await import(pathToFileURL(path.join(locations.library, 'net.mjs')));
    const {invoice} = await import(pathToFileURL(path.join(locations.client, 'invoice.mjs')));
    const actual = invoice([{sku: 'desk', cents: 1000, discount: 100}, {sku: 'lamp', cents: 500, discount: 50}], net);
    stage = 'negative'; let negatives = 0;
    for (const args of [[-1, 0], [1, 2], [Number.MAX_SAFE_INTEGER + 1, 0], [1, 0.5]]) {assert.throws(() => net(...args)); negatives++;}
    assert.throws(() => invoice([{sku: 'x', cents: Number.MAX_SAFE_INTEGER, discount: 0}, {sku: 'y', cents: 1, discount: 0}], net)); negatives++;
    report({invoice: actual, negatives, kept, patches});
  } catch {report({failureStage: stage});} // Stable fixture diagnostic; never an accepted assertion.
  break;
}
