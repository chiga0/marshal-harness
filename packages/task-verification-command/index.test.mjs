import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {tmpdir} from 'node:os';
import {mkdtemp, writeFile, rm, readFile, realpath} from 'node:fs/promises';
import {readFileSync} from 'node:fs';
import {createVerificationCommand} from './index.mjs';
import {encode, digest} from '../task-store/store.mjs';

const checkerPath = fileURLToPath(new URL('./checker.fixture.mjs', import.meta.url));
const checkerDigest = digest(readFileSync(checkerPath));
const policyDigest = digest(Buffer.from('fixture-sum-policy/v1'));
const planDigest = digest(Buffer.from('approved-fixture-plan'));
function ticket(deadline = Date.now() + 15000) {
  const input = {task: {goal: '求和交付'}, verification: {binding: {profile: 'task-verification/v1', policyDigest,
    providerId: 'fixture-independent', policy: 'sum/v1', nodeId: 'verify', description: 'sum and count', layouts: {}, deliveries: ['sum.json']}, manifests: []}};
  const frozen = {workerId: 'worker-verify', taskId: 'task-one', nodeId: 'verify', role: 'verifier', executionType: 'verification',
    providerId: 'fixture-independent', generation: '1', commandId: 'command-one', inputDigest: digest(encode(input)), planDigest, deadline, input};
  return {...frozen, reservationDigest: digest(encode(frozen))};
}
async function setup(t, mode = 'good', extra = {}) {
  const cwd = await realpath(await mkdtemp(path.join(tmpdir(), 'marshal-verifier-')));
  t.after(() => rm(cwd, {recursive: true, force: true}));
  await writeFile(path.join(cwd, 'candidate.json'), '{"values":[17,29,36]}', {mode: 0o400});
  const config = {executable: process.execPath, checkerPath, checkerDigest, policyDigest,
    assertions: [{name: 'sum', validate: actual => actual === 82}, {name: 'count', validate: actual => actual === 3}],
    request: () => ({mode}), delivery: ({report}) => ({name: 'sum.json', mediaType: 'application/json', content: encode(report.assertions)}), ...extra};
  return {cwd, prepared: {cwd}, config, adapter: createVerificationCommand(config)};
}
async function run(t, mode, extra) {
  const fixture = await setup(t, mode, extra), handle = fixture.adapter.start({ticket: ticket(), prepared: fixture.prepared});
  t.after(() => handle.stop());
  return {fixture, handle, result: await handle.completion};
}

test('real fixed Node checker, exact original cleanup and complete required assertions produce byte artifacts', {timeout: 20000}, async t => {
  const {handle, result} = await run(t, 'good');
  assert.equal(result.status, 'passed'); assert.equal(result.type, 'verification');
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.cleanup.agentExit.code, 0);
  assert.equal(result.cleanup.executionId, (await handle.started).executionId);
  const evidence = JSON.parse(result.evidence.content);
  assert.equal(evidence.delivery.digest, digest(result.delivery.content));
  assert.equal(evidence.binding.checkerDigest, checkerDigest); assert.equal(evidence.binding.policyDigest, policyDigest);
  assert.deepEqual(evidence.assertions, [{name: 'sum', actual: 82}, {name: 'count', actual: 3}]);
  assert.match(evidence.nonce, /^[0-9a-f-]{36}$/u);
  assert.equal(result.receipt, undefined); assert.equal(result.decision, undefined);
});

test('report nonce/binding and exact closed assertion set reject replay, omissions and pass labels', {timeout: 20000}, async t => {
  for (const mode of ['nonce', 'binding', 'missing', 'duplicate', 'unknown', 'extra']) {
    const {result} = await run(t, mode);
    assert.equal(result.status, 'failed', mode); assert.equal(result.cleanup.cleaned, true, mode);
    assert.equal(result.evidence, null); assert.equal(result.delivery, null);
  }
});

test('malicious assertion body remains data, no evaluation and no raw text in errors', {timeout: 10000}, async t => {
  const {result} = await run(t, 'body');
  assert.equal(result.status, 'failed'); assert.equal(result.reason, 'verification_assertion_failed');
  assert.ok(!JSON.stringify(result).includes('PRIVATE_VERIFIER_TOKEN'));
});

test('strict byte frame rejects duplicate JSON keys, invalid UTF8 and extra frames', {timeout: 15000}, async t => {
  for (const mode of ['duplicate-key', 'utf8', 'trailing']) {
    const {result} = await run(t, mode); assert.equal(result.status, 'failed', mode); assert.equal(result.delivery, null);
  }
});

test('valid output with nonzero exit or overflow is never accepted', {timeout: 15000}, async t => {
  for (const mode of ['nonzero', 'limit']) {
    const {result} = await run(t, mode); assert.equal(result.status, 'failed', mode); assert.equal(result.cleanup.cleaned, true);
  }
});

test('immediate bootstrap stop and running stop await original cleanup, never produce artifacts', {timeout: 15000}, async t => {
  for (const immediate of [true, false]) {
    const fixture = await setup(t, 'hang'), handle = fixture.adapter.start({ticket: ticket(), prepared: fixture.prepared});
    t.after(() => handle.stop());
    if (!immediate) assert.ok((await handle.started).agentPid > 0);
    const result = await handle.stop();
    assert.equal(result.status, 'failed'); assert.equal(result.reason, 'verification_stopped');
    assert.equal(result.cleanup.cleaned, true); assert.equal(result.evidence, null);
    assert.equal(result, await handle.completion); assert.equal(result, await handle.stop());
  }
});

test('original absolute deadline bounds checker; expired ticket never launches', {timeout: 10000}, async t => {
  const fixture = await setup(t, 'hang');
  const handle = fixture.adapter.start({ticket: ticket(Date.now() + 900), prepared: fixture.prepared});
  t.after(() => handle.stop());
  const result = await handle.completion;
  assert.equal(result.status, 'failed'); assert.equal(result.cleanup.cleaned, true);
  // Runtime deadline control is sent by its owner; preserve the guard's actual
  // owner_stop/deadline reason, not a synthetic normalized cleanup receipt.
  assert.ok(['owner_stop', 'deadline'].includes(result.cleanup.reason));
  assert.notEqual(result.cleanup.agentExit.code, 0);
  const expired = fixture.adapter.start({ticket: ticket(Date.now() - 1), prepared: fixture.prepared});
  assert.equal((await expired.completion).cleanup, null); assert.equal(await expired.started, null);
});

test('source digest, input digest, reservation and policy mismatch fail before launch', async t => {
  const fixture = await setup(t);
  for (const edit of [value => { value.input.task.goal = 'changed'; }, value => { value.reservationDigest = planDigest; },
    value => { value.executionType = 'agent'; }, value => { value.input.verification.binding.policyDigest = planDigest; }]) {
    const value = ticket(); edit(value);
    const handle = fixture.adapter.start({ticket: value, prepared: fixture.prepared});
    assert.equal((await handle.completion).status, 'failed'); assert.equal(await handle.started, null);
  }
  const wrong = createVerificationCommand({...fixture.config, checkerDigest: planDigest});
  assert.equal((await wrong.start({ticket: ticket(), prepared: fixture.prepared}).completion).cleanup, null);
  assert.deepEqual(JSON.parse(await readFile(path.join(fixture.cwd, 'candidate.json'))), {values: [17,29,36]});
});

test('trusted callbacks cannot accidentally accept Promise, false assertions or unbounded delivery', {timeout: 20000}, async t => {
  for (const extra of [{assertions: [{name: 'sum', validate: () => true}, {name: 'count', validate: () => false}]},
    {delivery: () => Promise.resolve({})}, {delivery: () => ({name: 'x', mediaType: 'text/plain', content: Buffer.alloc(8388609)})},
    {delivery: () => ({name: '../escape', mediaType: 'text/plain', content: Buffer.from('x')})}]) {
    const {result} = await run(t, 'good', extra); assert.equal(result.status, 'failed'); assert.equal(result.cleanup.cleaned, true);
  }
});

test('no ambient environment is inherited; fixed env copied and config shape rejects invalid policy', {timeout: 10000}, async t => {
  process.env.PRIVATE_VERIFIER_TEST = 'not-a-secret-fixture';
  t.after(() => delete process.env.PRIVATE_VERIFIER_TEST);
  const env = {}, fixture = await setup(t, 'env', {env});
  env.CHANGED_AFTER_FACTORY = 'must-not-reach-checker';
  const handle = fixture.adapter.start({ticket: ticket(), prepared: fixture.prepared}); t.after(() => handle.stop());
  assert.equal((await handle.completion).status, 'passed');
  assert.throws(() => createVerificationCommand({...fixture.config, assertions: []}), /verification_config_invalid/);
  assert.throws(() => createVerificationCommand({...fixture.config, checkerPath: 'checker.mjs'}), /verification_config_invalid/);
  assert.throws(() => createVerificationCommand({...fixture.config, env: {TOKEN: '\0'}}), /verification_config_invalid/);
  assert.throws(() => createVerificationCommand({...fixture.config, assertions: [fixture.config.assertions[0], fixture.config.assertions[0]]}), /verification_config_invalid/);
});

test('checker inside candidate and failed executable never yield a passed result or fake cleanup', {timeout: 10000}, async t => {
  const fixture = await setup(t);
  const copy = path.join(fixture.cwd, 'checker.mjs');
  await writeFile(copy, readFileSync(checkerPath), {mode: 0o600});
  const local = createVerificationCommand({...fixture.config, checkerPath: copy});
  const rejected = await local.start({ticket: ticket(), prepared: fixture.prepared}).completion;
  assert.equal(rejected.reason, 'verification_checker_in_candidate'); assert.equal(rejected.cleanup, null);
  const broken = createVerificationCommand({...fixture.config, executable: '/nonexistent-marshal-verification-node'});
  const handle = broken.start({ticket: ticket(), prepared: fixture.prepared}); t.after(() => handle.stop());
  const result = await handle.completion;
  assert.equal(result.status, 'failed'); assert.equal(result.evidence, null); assert.equal(result.delivery, null);
  assert.equal(await handle.started, null);
  if (result.cleanup) assert.notEqual(result.cleanup.agentExit.code, 0);
});

test('original guard without cleanup receipt remains unconfirmed, never accepted', {timeout: 10000}, async t => {
  const {result} = await run(t, 'unconfirmed');
  assert.equal(result.status, 'failed'); assert.equal(result.cleanup.cleaned, false);
  assert.equal(result.cleanup.reason, 'cleanup_unconfirmed'); assert.equal(result.delivery, null);
});

test('checker source drift during held execution invalidates otherwise good assertions', {timeout: 10000}, async t => {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'marshal-verifier-source-')));
  t.after(() => rm(root, {recursive: true, force: true}));
  const copy = path.join(root, 'checker.mjs'), source = readFileSync(checkerPath);
  await writeFile(copy, source, {mode: 0o600});
  const fixture = await setup(t, 'delay', {checkerPath: copy});
  const handle = fixture.adapter.start({ticket: ticket(), prepared: fixture.prepared}); t.after(() => handle.stop());
  assert.ok(await handle.started);
  await writeFile(copy, Buffer.concat([source, Buffer.from('\n// changed deployment\n')]));
  const result = await handle.completion;
  assert.equal(result.status, 'failed'); assert.equal(result.reason, 'verification_checker_identity');
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.delivery, null);
});
