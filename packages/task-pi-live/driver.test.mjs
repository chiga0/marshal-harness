import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {parseOptions, filePermission, expectedRegions, runLive} from './driver.fixture.mjs';
import {taskBody, validatePlan, consumeDelivery, data} from '../task-qwen-live/driver.fixture.mjs';
import {policy, proposal, bindPlan} from '../task-team-integration/scenario.fixture.mjs';
import {TaskVerification, createVerificationPort} from '../task-application/verification.mjs';
import {encode} from '../task-store/store.mjs';

const argumentsList = ['--execute-real', '--run-dir', '/private/tmp/pi-new', '--node', '/installed/node',
  '--pi-entry', '/installed/pi/dist/bundle/cli.js', '--pi-sdk', '/installed/pi/dist/index.js'];
test('Pi real driver requires one explicit execution flag and closed native entry options', () => {
  assert.deepEqual(parseOptions(['--help']), {help: true});
  assert.deepEqual(parseOptions(argumentsList), {executeReal: true, runDir: '/private/tmp/pi-new', node: '/installed/node',
    piEntry: '/installed/pi/dist/bundle/cli.js', sdkEntry: '/installed/pi/dist/index.js', timeoutMs: 600000});
  for (const args of [[], argumentsList.slice(1), argumentsList.slice(0, -2), [...argumentsList, '--execute-real'],
    [...argumentsList, '--node', '/other'], [...argumentsList, '--qwen-entry', '/qwen'], [...argumentsList, '--model', 'fallback'],
    [...argumentsList, '--mode', 'open'], [...argumentsList, '--timeout-ms', '59999'], [...argumentsList, '--timeout-ms', '900001'],
    argumentsList.map(value => value === '/private/tmp/pi-new' ? '/' : value),
    argumentsList.map(value => value === '/private/tmp/pi-new' ? '/private/tmp/a/../pi' : value)]) assert.throws(() => parseOptions(args));
});
test('the exported driver cannot create state or launch without explicit real execution authority', async () => {
  await assert.rejects(runLive({}), /explicit_real_execution_required/);
  await assert.rejects(runLive({executeReal: false}), /explicit_real_execution_required/);
});
function fixture(t) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-pi-permissions-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true}));
  fs.writeFileSync(path.join(cwd, 'sales.json'), '{}'); fs.writeFileSync(path.join(cwd, 'east.json'), '{}');
  const identity = {cwd, role: 'author', nodeId: 'east'};
  const request = (toolName, rawInput) => ({toolCall: {kind: toolName === 'read' ? 'read' : 'edit', rawInput,
    _meta: {provider: 'pi', toolName}}, options: [{kind: 'allow_once', optionId: 'allow-once'}, {kind: 'reject_once', optionId: 'reject-once'}]});
  return {cwd, identity, request};
}
test('native path/content and canonical edits preserve original Pi parameters with allow-once only', t => {
  const {cwd, identity, request} = fixture(t);
  for (const input of [request('read', {path: 'sales.json'}), request('read', {path: path.join(cwd, 'sales.json'), offset: 1, limit: 100}),
    request('read', {path: 'east.json'}), request('write', {path: 'east.json', content: '{}'}),
    request('edit', {path: 'east.json', edits: [{oldText: '{}', newText: '{"x":1}'}]}),
    request('edit', {path: path.join(cwd, 'east.json'), oldText: '{}', newText: '{"x":1}'})]) {
    const original = structuredClone(input);
    assert.deepEqual(filePermission(identity, input), {outcome: {outcome: 'selected', optionId: 'allow-once'}});
    assert.deepEqual(input, original, 'authorization never normalizes or rewrites native tool arguments');
  }
});
test('missing owned output can be created, but input/read/edit and missing or aliased cwd cannot gain authority', t => {
  const {cwd, identity, request} = fixture(t), west = {...identity, nodeId: 'west'};
  assert.equal(filePermission(west, request('write', {path: 'west.json', content: '{}'})).outcome.optionId, 'allow-once');
  for (const input of [request('read', {path: 'west.json'}), request('edit', {path: 'west.json', edits: [{oldText: 'a', newText: 'b'}]})])
    assert.equal(filePermission(west, input).outcome.outcome, 'cancelled');
  assert.equal(filePermission({...identity, cwd: path.join(cwd, 'missing')}, request('write', {path: 'east.json', content: '{}'})).outcome.outcome, 'cancelled');
  const alias = path.join(cwd, 'alias'); fs.symlinkSync(cwd, alias);
  assert.equal(filePermission({...identity, cwd: alias}, request('write', {path: 'east.json', content: '{}'})).outcome.outcome, 'cancelled');
});
test('shell, peer output, source modification, escapes, unknown native fields and wrong provider options are denied', t => {
  const {identity, request} = fixture(t);
  const bad = [request('bash', {command: 'cat sales.json'}), request('write', {path: 'sales.json', content: '{}'}),
    request('write', {path: 'west.json', content: '{}'}), request('write', {path: '../east.json', content: '{}'}),
    request('write', {path: 'east.json', content: '{}', command: 'other'}), request('read', {path: '/etc/passwd'}),
    request('read', {path: 'sales.json', limit: 0}), request('read', {path: 'sales.json', offset: 0}),
    request('write', {file_path: 'east.json', content: '{}'}), request('edit', {path: 'east.json', old_string: 'a', new_string: 'b'}),
    {...request('read', {path: 'sales.json'}), options: [{kind: 'allow_once', optionId: 'proceed_once'}]},
    {...request('read', {path: 'sales.json'}), options: [{kind: 'allow_always', optionId: 'allow-once'}]},
    {...request('read', {path: 'sales.json'}), options: [null]},
    {...request('read', {path: 'sales.json'}), options: [{kind: 'allow_once', optionId: 'allow-once'}, {kind: 'allow_once', optionId: 'allow-once'}]}];
  const foreign = request('read', {path: 'sales.json'}); foreign.toolCall._meta.provider = 'qwen'; bad.push(foreign);
  for (const value of bad) assert.equal(filePermission(identity, value).outcome.outcome, 'cancelled');
  for (const role of ['planner', 'verifier', null])
    assert.equal(filePermission({...identity, role}, request('read', {path: 'sales.json'})).outcome.outcome, 'cancelled');
});
test('permission denies links, hardlinks, directories and malformed or over-budget edits', t => {
  const {cwd, identity, request} = fixture(t);
  const errors = [request('write', {path: 'east.json', content: 'x'.repeat(4097)}),
    request('edit', {path: 'east.json', edits: []}), request('edit', {path: 'east.json', edits: [{oldText: 'x', newText: 'y', command: 'x'}]}),
    request('edit', {path: 'east.json', edits: [{oldText: 'x'.repeat(4097), newText: 'y'}]}),
    request('edit', {path: 'east.json', edits: Array.from({length: 9}, () => ({oldText: 'x', newText: 'y'}))}),
    request('edit', {path: 'east.json', edits: [{oldText: '\0', newText: 'y'}]}),
    request('write', {path: 'east.json', content: '\ud800'})];
  for (const input of errors) assert.equal(filePermission(identity, input).outcome.outcome, 'cancelled');
  const target = path.join(cwd, 'east.json'); fs.unlinkSync(target); fs.symlinkSync(path.join(cwd, 'sales.json'), target);
  assert.equal(filePermission(identity, request('write', {path: 'east.json', content: '{}'})).outcome.outcome, 'cancelled');
  fs.unlinkSync(target); fs.linkSync(path.join(cwd, 'sales.json'), target);
  assert.equal(filePermission(identity, request('read', {path: 'east.json'})).outcome.outcome, 'cancelled');
  fs.unlinkSync(target); fs.mkdirSync(target);
  assert.equal(filePermission(identity, request('write', {path: 'east.json', content: '{}'})).outcome.outcome, 'cancelled');
});
test('shared original Core plan and independent consumer are reusable without injecting expected answers or weakening oracle', () => {
  const body = taskBody('pi-input', 600000), plan = {...proposal(), taskId: 'pi-task', revision: 1, digest: 'sha256:' + '1'.repeat(64), budget: body.limits};
  const port = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan, start() { throw Error('no model or process in this test'); }});
  new TaskVerification({artifacts: {requireDepot() {}}}, port).bind({input: body, inputArtifacts: [{id: 'pi-input'}]}, plan);
  plan.acceptance[0] = '按区域处理已付款交易，包含退款与零额。';
  const task = {id: 'pi-task', status: 'awaiting-approval', revision: 4, plan: {revision: 1, digest: plan.digest}};
  assert.deepEqual(validatePlan(task, plan, body.limits, 'pi-input'), {expectedRevision: 4, planRevision: 1, planDigest: plan.digest});
  assert.doesNotMatch(JSON.stringify(body), /1275|550|1825/);
  assert.equal(data.rows.length, 6);
  const files = expectedRegions().map(value => ({path: value.region + '.json', content: JSON.stringify(value)}));
  const result = consumeDelivery(encode({files})); assert.equal(result.netCents, 1825); assert.equal(result.count, 4);
  files[0].content = '{"passed":true}'; assert.throws(() => consumeDelivery(encode({files})));
  plan.acceptance[1] = 'trusted-regional-checker'; assert.throws(() => validatePlan(task, plan, body.limits, 'pi-input'));
});
