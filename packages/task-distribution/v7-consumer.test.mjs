import test from 'node:test';
import assert from 'node:assert/strict';
import {candidateInputs, v7ResultFromTap} from './v7-installed.fixture.mjs';
const valid = {MARSHAL_CANDIDATE_ROOT: '/private/explicit-package', MARSHAL_CANDIDATE_SOURCE: 'a'.repeat(40),
  MARSHAL_CANDIDATE_MANIFEST: 'sha256:' + 'b'.repeat(64)};
test('v7 consumer requires all independent pins, no implicit source or pack fallback', () => {
  for (const key of Object.keys(valid)) {const input = {...valid}; delete input[key]; assert.throws(() => candidateInputs(input));}
  assert.throws(() => candidateInputs({...valid, MARSHAL_CANDIDATE_ROOT: './relative'}));
  assert.throws(() => candidateInputs({...valid, MARSHAL_CANDIDATE_SOURCE: 'main'}));
});
test('v7 consumer does not manufacture artifact identity for local evidence', () => {
  assert.equal(candidateInputs(valid).artifactId, null);
  assert.throws(() => candidateInputs({...valid, GITHUB_ACTIONS: 'true'}));
  assert.throws(() => candidateInputs({...valid, MARSHAL_CANDIDATE_ARTIFACT_ID: '0'}));
  assert.equal(candidateInputs({...valid, GITHUB_ACTIONS: 'true', MARSHAL_CANDIDATE_ARTIFACT_ID: '123'}).artifactId, '123');
});
const pins = candidateInputs({...valid, MARSHAL_CANDIDATE_ARTIFACT_ID: '123'});
const result = () => ({sourceHead: pins.sourceHead, manifestDigest: pins.manifestDigest, artifactId: pins.artifactId, files: 55,
  node: '24.15.0', platform: 'linux', arch: 'x64', uid: 1001, layout: 7, sameConfiguration: true,
  tasks: ['task-one', 'task-two'].map(taskId => ({taskId, attempts: 12, overlapMs: 400, deliveryDigest: 'sha256:' + 'c'.repeat(64), deliveryBytes: 93,
    executions: {leader: 6, agent: 2, review: 1, verification: 1, publication: 1, postverify: 1}, reviewDigest: 'sha256:' + 'd'.repeat(64),
    publicationReceiptArtifactId: 'artifact-publication-' + taskId, postverifyEvidenceArtifactId: 'artifact-postverify-' + taskId})),
  modelCalls: 0, coldReplayDuplicateStarts: 0, coldReplayDuplicatePublications: 0,
  proofScope: 'installed-v7-fixture-consumption-not-model-or-release-approval'});
const tap = value => '# ' + JSON.stringify(value) + '\n# tests 2\n# pass 2\n# fail 0\n# cancelled 0\n# skipped 0\n';
test('CI summary preserves original three pins and limited proof scope for both platforms', () => {
  for (const [platform, arch] of [['linux', 'x64'], ['darwin', 'arm64']]) {
    const value = {...result(), platform, arch}; assert.deepEqual(v7ResultFromTap(tap(value), pins), value);
  }
});
test('CI cannot archive changed pins, incomplete runs, omitted delivery chain or private extra fields as success', () => {
  for (const key of ['sourceHead', 'manifestDigest', 'artifactId']) assert.throws(() => v7ResultFromTap(tap({...result(), [key]: 'wrong'}), pins));
  for (const [from, to] of [['# pass 2', '# pass 1'], ['# fail 0', '# fail 1'], ['# cancelled 0', '# cancelled 1'], ['# skipped 0', '# skipped 1']])
    assert.throws(() => v7ResultFromTap(tap(result()).replace(from, to), pins));
  assert.throws(() => v7ResultFromTap(tap(result()) + tap(result()), pins));
  assert.throws(() => v7ResultFromTap('x'.repeat(128 * 1024 + 1), pins));
  for (const change of [value => {value.rawLog = 'not an allowed evidence field';}, value => {value.tasks[0].executions.postverify = 0;},
    value => {value.tasks[0].overlapMs = 0;}, value => {value.coldReplayDuplicateStarts = 1;}, value => {value.layout = 2;}]) {
    const value = result(); change(value); assert.throws(() => v7ResultFromTap(tap(value), pins));
  }
});
