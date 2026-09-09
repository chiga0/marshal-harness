import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import fs from 'node:fs';
import {parseOptions, expectedReport, waitPhase, checkAuthorization} from './live-consumer.mjs';
const args = ['--package', '/package', '--manifest-digest', 'sha256:' + 'a'.repeat(64), '--source-head', 'b'.repeat(40),
  '--node', '/node', '--pi-entry', '/pi/dist/bundle/cli.js', '--pi-sdk', '/pi/dist/index.js', '--run-dir', '/private/new', '--execute-real', '--allow-local-publication'];
test('explicit finite arguments; neither execution nor publication is implicit', () => {
  assert.equal(parseOptions(args).package, '/package');
  for (const flag of ['--execute-real', '--allow-local-publication']) assert.throws(() => parseOptions(args.filter(value => value !== flag)));
  for (const suffix of [['--package', '/other'], ['--unknown', 'x'], ['--execute-real']]) assert.throws(() => parseOptions([...args, ...suffix]));
  assert.throws(() => parseOptions(args.map(value => value === '/node' ? 'node' : value)));
  assert.throws(() => parseOptions(args.map(value => value === 'b'.repeat(40) ? 'HEAD' : value)));
});
test('independent input oracle includes refunds, zero, date bounds and excludes cancellation', () => {
  const window = {startDate: '2026-09-02', endDate: '2026-09-03'};
  const data = {rows: [{date: '2026-09-02', region: 'east', status: 'paid', cents: -20},
    {date: '2026-09-03', region: 'east', status: 'paid', cents: 0}, {date: '2026-09-03', region: 'west', status: 'cancelled', cents: 80},
    {date: '2026-09-01', region: 'west', status: 'paid', cents: 100}]};
  assert.deepEqual(expectedReport(data, window, 'source').reports, [{region: 'east', ...window, count: 2, netCents: -20}, {region: 'west', ...window, count: 0, netCents: 0}]);
});
test('bounded observation never retries terminal failure or missed expected phase', async () => {
  for (const status of ['failed', 'cancelled', 'intervention', 'completed']) {
    let calls = 0; await assert.rejects(waitPhase(async () => {calls++; return {status};}, 'awaiting-answer', Date.now() + 500), /unexpected_terminal/); assert.equal(calls, 1);
  }
  await assert.rejects(waitPhase(async () => assert.fail('must not call'), 'completed', 0), /phase_deadline/);
  assert.deepEqual(await waitPhase(async () => ({status: 'completed'}), 'completed', Date.now() + 500), {status: 'completed'});
});
test('publication authorization is exact and acceptance/expiry bound', () => {
  const content = Buffer.from('{}'), artifactDigest = 'sha256:' + createHash('sha256').update(content).digest('hex');
  const context = {taskId: 'task-one', plan: {digest: 'plan'}, report: {content, artifact: {id: 'artifact-one'}},
    audit: {acceptance: {status: 'passed', digest: 'acceptance'}}, leader: {review: {verdict: 'accept', digest: 'review'}}, now: 0};
  const authorization = {taskId: 'task-one', planDigest: 'plan', artifactId: 'artifact-one', artifactDigest, bytes: 2, acceptanceDigest: 'acceptance',
    reviewDigest: 'review', targetId: 'local-window-report', targetPolicyDigest: 'sha256:' + 'a'.repeat(64), operation: 'create-if-absent',
    name: 'task-one-' + artifactDigest.slice(7) + '.json', expiresAt: '2026-09-09T00:00:00.000Z'};
  checkAuthorization(authorization, context);
  for (const patch of [{targetId: 'other'}, {name: '../other'}, {bytes: 3}, {artifactDigest: 'wrong'}, {reviewDigest: 'other'}, {extra: true}])
    assert.throws(() => checkAuthorization({...authorization, ...patch}, context));
  assert.throws(() => checkAuthorization(authorization, {...context, now: Infinity}));
  assert.throws(() => checkAuthorization(authorization, {...context, leader: {review: {verdict: 'reject'}}}));
});
test('consumer does not import source Core, call pack, or use fixture configuration', () => {
  const source = fs.readFileSync(new URL('./live-consumer.mjs', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /\bpack\s*\(|startTaskService|\.fixture\.mjs|import\(['"]\.\.\/task-(?:application|service|store)/);
  assert.match(source, /packages\/task-leader-report\/service-config\.mjs/);
});
