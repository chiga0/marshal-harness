// Explicit CI/local candidate entry, deliberately NOT part of the default
// *.test.mjs glob. Missing candidate inputs fail; there is no pack fallback.
import test from 'node:test';
import assert from 'node:assert/strict';
import {exerciseInstalledTeam} from './installed-team.fixture.mjs';

const installed = process.env.MARSHAL_CANDIDATE_ROOT;
const manifestDigest = process.env.MARSHAL_CANDIDATE_MANIFEST;
const sourceHead = process.env.MARSHAL_CANDIDATE_SOURCE;
const artifactId = process.env.MARSHAL_CANDIDATE_ARTIFACT_ID ?? null;
assert.match(sourceHead ?? '', /^[a-f0-9]{40}$/);
assert.match(manifestDigest ?? '', /^sha256:[a-f0-9]{64}$/);
assert.equal(typeof installed, 'string');
assert.ok(process.getuid() > 0, 'candidate consumer must run as an ordinary user');
assert.ok(['darwin-arm64', 'linux-x64'].includes(process.platform + '-' + process.arch));
if (process.env.GITHUB_ACTIONS === 'true') assert.match(artifactId ?? '', /^[1-9][0-9]*$/);
for (const custody of [false, true]) test(`same candidate ${custody ? 'custody-v2' : 'legacy-v1'} original CLI team, download and cold reopen`, {timeout: 45000}, async t => {
  await exerciseInstalledTeam(t, {installed, manifestDigest, sourceHead, custody});
  t.diagnostic(JSON.stringify({artifactId, source: artifactId === null ? 'local-carrier-test' : 'current-workflow-artifact', modelCalls: 0}));
});
