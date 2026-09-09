import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {runFixture, runLive, parseOptions, overlap} from './driver.fixture.mjs';
import {filePermission, proposal, bindPlan, taskBody, LIMITS} from './business.fixture.mjs';

function directory(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-git-mixed-selftest-')));
  // runFixture resolves only after original owned service/provider/command
  // cleanup. These are exclusively this test's repositories, never user data.
  t.after(() => fs.rmSync(parent, {recursive: true, force: true})); return {parent, runDir: path.join(parent, 'run')};
}
test('real HTTP/SQLite, original Pi bridge RPC plus ACP peer, two actual Git writers and independent download/reopen', {timeout: 60000}, async t => {
  const {runDir} = directory(t), result = await runFixture({runDir, signal: t.signal});
  assert.equal(result.passed, true, JSON.stringify(result));
  assert.equal(result.mode, 'deterministic-fixture'); assert.equal(result.production, false); assert.equal(result.publisherSeparationProven, false);
  assert.equal(result.lastTaskStatus, 'completed'); assert.equal(result.executions.length, 3); assert.equal(result.verifierStarts, 1);
  assert.equal(result.executions.find(item => item.nodeId === 'library').providerId, 'pi-rpc');
  assert.equal(result.executions.find(item => item.nodeId === 'client').providerId, 'qwen-acp');
  assert.ok(result.executions.every(item => item.cleaned && item.status === 'completed' && item.executionId));
  assert.equal(result.verifiers.length, 1); assert.equal(result.verifiers[0].status, 'passed'); assert.equal(result.verifiers[0].cleaned, true);
  assert.equal(result.workers.length, 4); assert.deepEqual(result.workers.map(item => item.attempt).sort(), [1, 2, 3, 4]);
  assert.deepEqual(result.permissions, {pi: {allowed: 2, denied: 1}, qwen: {allowed: 2, denied: 1}});
  assert.ok(result.overlapMs > 0); assert.equal(result.consumer.checks, 23); assert.equal(result.consumer.negativeChecks, 12);
  assert.equal(result.consumer.total, 1350); assert.equal(result.consumer.cleanup, true); assert.equal(result.originalRepositoriesUnchanged, true);
  assert.deepEqual(result.restart, {mode: 'graceful-same-version', originalReceipts: true, sameBytes: true, duplicateStarts: 0});
  assert.equal(JSON.parse(fs.readFileSync(path.join(runDir, 'data/profile.json'))).layout, 2);
  assert.equal(fs.statSync(runDir).mode & 0o777, 0o700);
  for (const name of ['evidence.json', 'git-mixed-patches.json']) assert.equal(fs.statSync(path.join(runDir, name)).mode & 0o777, 0o600);
  const persisted = fs.readFileSync(path.join(runDir, 'evidence.json'), 'utf8');
  assert.deepEqual(JSON.parse(persisted), result);
  assert.doesNotMatch(persisted, /"token"|"prompt"|"rawInput"|"rawOutput"|"content"|not implemented|\/Users\//);
  assert.equal(fs.readdirSync(path.join(runDir, 'data/executions/git-worktrees')).length, 2);
  assert.ok(fs.readdirSync(path.join(runDir, 'verification')).some(name => name.endsWith('-library')));
  assert.ok(fs.readdirSync(path.join(runDir, 'consumer')).some(name => name.endsWith('-client')));
});
test('applicable but wrong native-writer implementation fails independent oracle and keeps failed evidence', {timeout: 60000}, async t => {
  const {runDir} = directory(t), result = await runFixture({runDir, scenario: 'wrong', signal: t.signal});
  assert.equal(result.passed, false); assert.equal(result.lastTaskStatus, 'failed'); assert.equal(result.verifierStarts, 1);
  assert.equal(result.verifiers[0].status, 'failed'); assert.equal(result.verifiers[0].cleaned, true);
  assert.equal(result.failure.code, 'task_not_completed'); assert.equal(result.executions.length, 3);
  assert.equal(fs.existsSync(path.join(runDir, 'git-mixed-patches.json')), false);
  assert.equal(fs.existsSync(path.join(runDir, 'evidence.json')), true);
  assert.equal(fs.readdirSync(path.join(runDir, 'data/executions/git-worktrees')).length, 2);
});
test('planner cannot silently turn a mixed approved business into two Pi authors or consume another Attempt', {timeout: 30000}, async t => {
  const {runDir} = directory(t), result = await runFixture({runDir, scenario: 'bad-plan', signal: t.signal});
  assert.equal(result.passed, false); assert.equal(result.lastTaskStatus, 'failed'); assert.equal(result.verifierStarts, 0);
  assert.equal(result.executions.length, 1); assert.equal(result.executions[0].role, 'planner');
  assert.equal(fs.readdirSync(path.join(runDir, 'data/executions/git-worktrees')).length, 0);
  assert.equal(fs.existsSync(path.join(runDir, 'git-mixed-patches.json')), false);
});
test('native one-time permissions bind brand, worker cwd and existing file; shell/extra fields/escape/links cannot pass', t => {
  const {parent} = directory(t), cwd = path.join(parent, 'worktree'); fs.mkdirSync(cwd);
  const target = path.join(cwd, 'net.mjs'); fs.writeFileSync(target, 'base');
  const identity = {cwd, nodeId: 'library', role: 'author', providerId: 'pi-rpc'};
  const pi = (toolName, rawInput, kind = toolName === 'read' ? 'read' : 'edit') => ({toolCall: {kind, _meta: {provider: 'pi', toolName}, rawInput},
    options: [{optionId: 'allow-once', kind: 'allow_once'}]});
  const allowed = request => filePermission(identity, request).outcome.outcome === 'selected';
  assert.equal(allowed(pi('read', {path: 'net.mjs'})), true);
  assert.equal(allowed(pi('write', {path: target, content: 'new'})), true);
  assert.equal(allowed(pi('edit', {path: 'net.mjs', edits: [{oldText: 'old', newText: 'new'}]})), true);
  for (const request of [pi('bash', {command: 'git push'}, 'execute'), pi('read', {path: '.git'}),
    pi('write', {path: '../net.mjs', content: 'x'}), pi('write', {path: 'invoice.mjs', content: 'x'}),
    pi('write', {path: 'net.mjs', content: 'x', arbitrary: true}), pi('write', {path: 'net.mjs', content: 'x'.repeat(65537)}),
    pi('read', {path: 'net.mjs', offset: 0}), pi('read', {file_path: 'net.mjs'})]) assert.equal(allowed(request), false);
  assert.equal(filePermission({...identity, role: 'planner'}, pi('read', {path: 'net.mjs'})).outcome.outcome, 'cancelled');
  const duplicate = pi('read', {path: 'net.mjs'}); duplicate.options.push({...duplicate.options[0]}); assert.equal(allowed(duplicate), false);
  fs.unlinkSync(target); fs.symlinkSync(path.join(parent, 'outside'), target); assert.equal(allowed(pi('write', {path: 'net.mjs', content: 'x'})), false);
  fs.unlinkSync(target); fs.writeFileSync(path.join(parent, 'outside'), 'outside'); fs.linkSync(path.join(parent, 'outside'), target);
  assert.equal(allowed(pi('write', {path: 'net.mjs', content: 'x'})), false);
  fs.writeFileSync(path.join(cwd, 'invoice.mjs'), 'base');
  const qwen = {toolCall: {kind: 'edit', rawInput: {file_path: 'invoice.mjs', content: 'new'}}, options: [{optionId: 'proceed_once', kind: 'allow_once'}]};
  assert.equal(filePermission({cwd, role: 'author', nodeId: 'client', providerId: 'qwen-acp'}, qwen).outcome.outcome, 'selected');
  qwen.toolCall.rawInput.path = 'invoice.mjs';
  assert.equal(filePermission({cwd, role: 'author', nodeId: 'client', providerId: 'qwen-acp'}, qwen).outcome.outcome, 'cancelled');
  const shell = {toolCall: {kind: 'execute', rawInput: {command: 'never execute'}}, options: [
    {optionId: 'proceed_once', kind: 'allow_once'}, {optionId: 'cancel', kind: 'reject_once'}]};
  assert.deepEqual(filePermission({cwd, role: 'author', nodeId: 'client', providerId: 'qwen-acp'}, shell),
    {outcome: {outcome: 'selected', optionId: 'cancel'}});
});
test('fixed plan bounds and strict Git Context stay original public API data, not hidden repo/schema or oracle answers', () => {
  const description = {profile: 'task-git-input/v1', nodes: ['library', 'client'].map((nodeId, i) =>
    ({nodeId, repositoryId: nodeId, base: String(i + 1).repeat(40), writePaths: [i ? 'invoice.mjs' : 'net.mjs']}))};
  const body = taskBody(description); assert.deepEqual(body.limits, LIMITS); assert.deepEqual(Object.keys(body.context), ['text']);
  assert.deepEqual(JSON.parse(body.context.text), description);
  const binding = bindPlan({taskInput: body, proposal: proposal()});
  assert.ok(binding.description.includes(description.nodes[0].base)); assert.equal(binding.layouts.length, 3);
  const changed = proposal(); changed.nodes[1].providerId = 'pi-rpc'; assert.throws(() => bindPlan({taskInput: body, proposal: changed}), {code: 'plan_boundary'});
  assert.doesNotMatch(JSON.stringify(proposal()), /1350|900|450/);
});
test('explicit real flag, fixed budget, exact clean source and positive original lifetimes are mandatory', async t => {
  const {runDir} = directory(t);
  assert.throws(() => parseOptions([]), {code: 'explicit_real_execution_required'});
  assert.throws(() => parseOptions(['--execute-real', '--timeout-ms', '900000']), {code: 'invalid_arguments'});
  assert.throws(() => parseOptions(['--execute-real', '--fixture', 'good']), {code: 'invalid_arguments'});
  await assert.rejects(runLive({executeReal: true, runDir, node: process.execPath, sourceHead: '0'.repeat(40)}), {code: 'source_not_clean_exact'});
  assert.equal(fs.existsSync(runDir), false);
  const facts = [{role: 'author', nodeId: 'library', executionId: 'a', startedAt: '2026-09-09T00:00:01Z', agentExitedAt: '2026-09-09T00:00:02Z'},
    {role: 'author', nodeId: 'client', executionId: 'b', startedAt: '2026-09-09T00:00:02Z', agentExitedAt: '2026-09-09T00:00:03Z'}];
  assert.throws(() => overlap(facts), {code: 'authors_did_not_overlap'});
  facts[1].startedAt = '2026-09-09T00:00:01Z'; assert.equal(overlap(facts), 1000);
});
