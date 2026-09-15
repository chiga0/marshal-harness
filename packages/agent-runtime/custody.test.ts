import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import {tmpdir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {createExecutionCustody} from './custody.mjs';
import {custodyDigest, verifyObservation} from './custody-contract.mjs';
import {launchCommand} from './index.mjs';

async function fixture(t) {
  const root = await fs.realpath(await fs.mkdtemp(path.join(tmpdir(), 'marshal-custody-unit-')));
  await fs.chmod(root, 0o700);
  const manager = createExecutionCustody({root});
  t.after(async () => { await manager.close(); await fs.rm(root, {recursive: true, force: true}); });
  const binding = {storeId: randomUUID(), generation: '1', taskId: randomUUID(), workerId: randomUUID(), commandId: randomUUID(),
    reservationDigest: 'sha256:' + 'a'.repeat(64), inputDigest: 'sha256:' + 'b'.repeat(64), planDigest: null,
    deadline: Date.now() + 15000, ownerExpiresAt: Date.now() + 15000,
    executionProfile: {id: 'fixture-v1', scope: 'inherited-process-group', eligible: true}};
  return {root, manager, binding};
}
test('prepared custodian never launches before permit and seals a signed no-start observation', {timeout: 10000}, async t => {
  const {manager, binding} = await fixture(t), handle = await manager.prepare(binding);
  assert.equal(handle.descriptor.bindingDigest, custodyDigest(binding));
  await assert.rejects(handle.launch({deadline: binding.deadline}), {code: 'custody_launch_denied'});
  const cleanup = await handle.stop();
  assert.equal(cleanup.cleaned, true); assert.equal(cleanup.scope, 'none-start');
  const observation = manager.read(handle.descriptor);
  assert.equal(verifyObservation(handle.descriptor, observation), true);
  assert.equal(verifyObservation({...handle.descriptor, publicKey: 'A'.repeat(59) + '='}, observation), false);
  manager.acknowledge(handle.descriptor, custodyDigest(observation));
  manager.acknowledge(handle.descriptor, custodyDigest(observation));
});
test('command uses original custodian with exact stdout, durable signed cleanup and no duplicate launch', {timeout: 15000}, async t => {
  const {root, manager, binding} = await fixture(t), handle = await manager.prepare(binding); handle.permit();
  const runtime = await launchCommand({executable: process.execPath,
    args: [fileURLToPath(new URL('./command.fixture.mjs', import.meta.url)), 'sum'], cwd: root, env: {}, deadline: binding.deadline,
    input: Buffer.from('{"nonce":"custody-exact","values":[3,8]}\n'), executionContext: {launch: handle.launch}});
  const result = await runtime.completion;
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.outputComplete, true);
  assert.deepEqual(JSON.parse(result.stdout), {nonce: 'custody-exact', sum: 11, leaked: false});
  assert.equal(result.cleanup.executionId, handle.descriptor.executionId);
  const observation = manager.read(handle.descriptor);
  assert.equal(verifyObservation(handle.descriptor, observation), true);
  assert.equal(observation.payload.permitReceived, true);
  assert.deepEqual(await runtime.stop(), result);
  await assert.rejects(handle.launch({deadline: binding.deadline}), {code: 'custody_launch_denied'});
});
test('near-limit command bytes drain over both pipes before the independent IPC completion', {timeout: 15000}, async t => {
  const {root, manager, binding} = await fixture(t), handle = await manager.prepare(binding); handle.permit();
  const expected = Buffer.from(JSON.stringify({nonce: 'drain', sum: 0, leaked: false, payload: 'y'.repeat(240000)}) + '\n');
  const runtime = await launchCommand({executable: process.execPath,
    args: [fileURLToPath(new URL('./command.fixture.mjs', import.meta.url)), 'large'], cwd: root, env: {}, deadline: binding.deadline,
    input: Buffer.from('{"nonce":"drain","values":[],"size":240000}\n'), limits: {outputBytes: 262144},
    executionContext: {launch: handle.launch}});
  const result = await runtime.completion;
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.outputComplete, true);
  assert.deepEqual(result.stdout, expected); assert.equal(result.cleanup.outputBytes, expected.length);
});
