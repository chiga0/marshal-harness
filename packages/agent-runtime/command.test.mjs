import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {launchCommand} from './index.mjs';

const fixture = fileURLToPath(new URL('./command.fixture.mjs', import.meta.url));
async function options(t, mode) {
  const cwd = await mkdtemp(path.join(tmpdir(), 'marshal-command-test-'));
  t.after(() => rm(cwd, {recursive: true, force: true}));
  return {executable: process.execPath, args: [fixture, mode], cwd, deadline: Date.now() + 10000};
}
async function gone(pid) {
  const until = Date.now() + 3000;
  while (Date.now() < until) {
    try { process.kill(pid, 0); } catch (error) { if (error.code === 'ESRCH') return; throw error; }
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.fail('owned fixture still running');
}

test('non-ACP independent command returns exact bounded bytes and original cleanup, no ambient env', {timeout: 15000}, async t => {
  process.env.PRIVATE_COMMAND_ENV = 'fixture-only'; t.after(() => delete process.env.PRIVATE_COMMAND_ENV);
  const runtime = await launchCommand({...await options(t, 'sum'), input: Buffer.from('{"nonce":"exact-run","values":[17,29,36]}\n')});
  t.after(() => runtime.stop());
  assert.equal(runtime.client, undefined);
  const result = await runtime.completion;
  assert.equal(result.outputComplete, true);
  assert.deepEqual(JSON.parse(result.stdout), {nonce: 'exact-run', sum: 82, leaked: false});
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.cleanup.agentExit.code, 0);
  assert.equal(result.cleanup.started.executionId, runtime.started.executionId);
  assert.equal(await runtime.stop(), result);
  await gone(runtime.started.agentPid); await gone(runtime.started.guardPid);
});

test('cancel/deadline owned command waits for inherited-group cleanup without successful exit', {timeout: 15000}, async t => {
  for (const cause of ['cancel', 'deadline']) {
    const config = await options(t, 'hang');
    if (cause === 'deadline') config.deadline = Date.now() + 1200;
    const runtime = await launchCommand(config); t.after(() => runtime.stop());
    const result = await (cause === 'cancel' ? runtime.stop() : runtime.completion);
    assert.equal(result.cleanup.cleaned, true, JSON.stringify({cause, cleanup: result.cleanup}));
    assert.notEqual(result.cleanup.agentExit.code, 0);
    await gone(runtime.started.agentPid); await gone(runtime.started.guardPid);
  }
});

test('nonzero command with inherited descendant cannot leave child running', {timeout: 10000}, async t => {
  const runtime = await launchCommand(await options(t, 'descendant')); t.after(() => runtime.stop());
  const result = await runtime.completion;
  assert.equal(result.outputComplete, true); assert.equal(result.cleanup.agentExit.code, 7);
  assert.equal(result.cleanup.cleaned, true); await gone(JSON.parse(result.stdout).pid);
});

test('command output and stderr limits stop owned scope, never return raw stderr', {timeout: 15000}, async t => {
  for (const mode of ['output-limit', 'stderr-limit']) {
    const runtime = await launchCommand({...await options(t, mode), limits: {outputBytes: 1024, stderrBytes: 1024}});
    t.after(() => runtime.stop()); const result = await runtime.completion;
    assert.equal(result.cleanup.cleaned, true); assert.notEqual(result.cleanup.agentExit.code, 0);
    assert.ok(result.stdout.length <= 1024); assert.ok(!JSON.stringify(result).includes('PRIVATE_COMMAND_FIXTURE'));
  }
});

test('invalid command frame rejects before creating an execution', async t => {
  await assert.rejects(launchCommand({...await options(t, 'sum'), input: 'not-bytes'}), {code: 'runtime_invalid_input'});
  await assert.rejects(launchCommand({...await options(t, 'sum'), input: Buffer.alloc(1048577)}), {code: 'runtime_invalid_input'});
  await assert.rejects(launchCommand({...await options(t, 'sum'), input: Buffer.alloc(2), limits: {inputBytes: 1}}), {code: 'runtime_invalid_input'});
});
