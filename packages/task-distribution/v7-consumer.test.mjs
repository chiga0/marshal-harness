import test from 'node:test';
import assert from 'node:assert/strict';
import {candidateInputs} from './v7-installed.fixture.mjs';
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
