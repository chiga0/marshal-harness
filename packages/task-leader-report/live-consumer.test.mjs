import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import fs from 'node:fs';
import {parseOptions, expectedReport, waitPhase, checkAuthorization, sameJSON, checkPlan} from './live-consumer.fixture.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {TaskClient} from '../task-client/index.mjs';
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
  // Original HTTP parser returns null-prototype objects at every JSON object.
  checkAuthorization(parseJson(Buffer.from(JSON.stringify(authorization))), context);
  for (const patch of [{targetId: 'other'}, {name: '../other'}, {bytes: 3}, {artifactDigest: 'wrong'}, {reviewDigest: 'other'}, {extra: true}])
    assert.throws(() => checkAuthorization({...authorization, ...patch}, context));
  assert.throws(() => checkAuthorization(authorization, {...context, now: Infinity}));
  assert.throws(() => checkAuthorization(authorization, {...context, leader: {review: {verdict: 'reject'}}}));
});
test('consumer does not import source Core, call pack, or use fixture configuration', () => {
  const source = fs.readFileSync(new URL('./live-consumer.fixture.mjs', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /\bpack\s*\(|startTaskService|\.fixture\.mjs|import\(['"]\.\.\/task-(?:application|service|store)/);
  assert.match(source, /packages\/task-leader-report\/service-config\.mjs/);
});
const http = value => parseJson(Buffer.from(JSON.stringify(value)));
test('original HTTP nested records compare by JSON value, preserving fields and arrays', () => {
  const value = {nested: {list: [{answer: 'x'}, null, 3]}, ok: true};
  sameJSON(http(value), value);
  sameJSON(http(value), {ok: true, nested: {list: [{answer: 'x'}, null, 3]}});
  for (const changed of [{...value, extra: 1}, {...value, ok: 'true'}, {nested: {list: [null, {answer: 'x'}, 3]}, ok: true}, {ok: true}])
    assert.throws(() => sameJSON(http(value), changed), /evidence_mismatch/);
  for (const invalid of [undefined, NaN, Infinity, new Date(), {x: undefined}, [,], Object.assign([], {extra: 1}), {toJSON() {return value;}}])
    assert.throws(() => sameJSON(invalid, invalid), /comparison_not_json/);
});
test('original HTTP replay receipt and local spread compare without dropping any receipt fields', () => {
  const receipt = http({replayed: false, requestId: 'request-one', body: {answer: 'x', sequence: 2}});
  const replay = http({...receipt, replayed: true});
  sameJSON({...replay, replayed: false}, receipt);
  assert.throws(() => sameJSON({...replay, replayed: false, extra: true}, receipt));
  assert.throws(() => sameJSON({...replay, replayed: false, body: {answer: 'changed', sequence: 2}}, receipt));
  assert.throws(() => sameJSON(replay, receipt));
});
test('original HTTP Plan permits node and edge permutations but rejects membership or budget drift', () => {
  const limits = {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3};
  const plan = {nodes: [{id: 'east', role: 'author'}, {id: 'west', role: 'author'}, {id: 'verify', role: 'verifier'}],
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], budget: limits};
  checkPlan(http(plan), limits);
  const reordered = http({...plan, nodes: [plan.nodes[2], plan.nodes[1], plan.nodes[0]], edges: [...plan.edges].reverse()});
  const before = JSON.stringify(reordered); checkPlan(reordered, limits); assert.equal(JSON.stringify(reordered), before);
  for (const patch of [{nodes: plan.nodes.slice(1)}, {nodes: [...plan.nodes, plan.nodes[0]]}, {nodes: [plan.nodes[0], plan.nodes[0], plan.nodes[2]]},
    {nodes: plan.nodes.map(node => node.id === 'east' ? {...node, role: 'reviewer'} : node)}, {edges: [plan.edges[0], plan.edges[0]]},
    {edges: plan.edges.slice(1)}, {edges: [...plan.edges, {from: 'east', to: 'west'}]}, {edges: [{...plan.edges[0], extra: true}, plan.edges[1]]},
    {budget: {...limits, maxAttempts: 16}}, {budget: {...limits, extra: 1}}]) assert.throws(() => checkPlan(http({...plan, ...patch}), limits));
});
test('original TaskClient accepts serialized Plan and its parsed values pass the consumer without a socket', async () => {
  const limits = {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3};
  const plan = {taskId: 'task-one', revision: 1, digest: 'sha256:' + 'a'.repeat(64), summary: '两个地区独立汇总与核验',
    nodes: ['west', 'verify', 'east'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', goal: '完成' + id, scope: [], providerId: null})),
    edges: [{from: 'west', to: 'verify'}, {from: 'east', to: 'verify'}], budget: limits,
    deliverables: ['east.json', 'west.json'], acceptance: ['原日期窗口与流水准确'], assumptions: []};
  let calls = 0;
  const client = new TaskClient({baseURL: 'http://127.0.0.1:39877', token: 'unit-test-only-local-access-token-not-a-secret', fetch: async (url, request) => {
    calls++; assert.equal(url, 'http://127.0.0.1:39877/v1/tasks/task-one/plan'); assert.equal(request.method, 'GET');
    const bytes = Buffer.from(JSON.stringify(plan));
    return new Response(bytes, {status: 200, headers: {'Content-Type': 'application/json', 'Content-Length': String(bytes.length)}});
  }});
  const parsed = await client.request('task.plan', {path: {taskId: 'task-one'}});
  assert.equal(calls, 1); assert.equal(Object.getPrototypeOf(parsed), null); assert.equal(Object.getPrototypeOf(parsed.budget), null);
  sameJSON(parsed, plan); checkPlan(parsed, limits);
  assert.throws(() => checkPlan(parsed, {...limits, maxAttempts: 16}), /plan_budget_mismatch/);
});
