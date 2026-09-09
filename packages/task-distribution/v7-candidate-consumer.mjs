// Explicit invocation only; not a default *.test.mjs and never a pack fallback.
// MARSHAL_CANDIDATE_ROOT/MANIFEST/SOURCE are independent required pins.
// Fixed Node --test --test-concurrency=1 packages/task-distribution/v7-candidate-consumer.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import {candidateInputs, exerciseInstalledV7} from './v7-installed.fixture.mjs';
const inputs = candidateInputs(process.env);
test('wrong manifest/source pins reject before importing package or starting service', async t => {
  await assert.rejects(exerciseInstalledV7(t, {...inputs, manifestDigest: 'sha256:' + '0'.repeat(64)}), /manifest_digest_mismatch/);
  await assert.rejects(exerciseInstalledV7(t, {...inputs, sourceHead: '0'.repeat(40)}), /AssertionError/);
});
test('same installed v7 candidate: fixed configuration, two HTTP Leader teams, original publication, GET and cold replay', {timeout: 180000}, async t => {
  await exerciseInstalledV7(t, inputs);
});
