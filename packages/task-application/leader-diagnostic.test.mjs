import test from 'node:test';
import assert from 'node:assert/strict';
import {createLeaderPort, parseManagedOutput, receipt, safeManagedDiagnostic} from './leader-ports.mjs';
import {diagnosticCollector} from '../task-leader-report/live-consumer.fixture.mjs';
const digest = 'sha256:' + 'a'.repeat(64);
const ticket = {executionType: 'leader', providerId: 'pi', taskId: 'task-one', workerId: 'worker-one', deadline: Date.now() + 10000,
  input: {leader: {callId: 'call-one', inputDigest: digest}}};
const cleanup = {cleaned: true, started: {executionId: 'execution-one', startedAt: '2026-09-09T00:00:00.000Z'}};
function port() {return createLeaderPort({id: 'leader', providerId: 'pi', prepare: () => ({prompt: 'test'}), parseDecision: parseManagedOutput,
  policy: {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 1, maxRequests: 4, repair: {nodeIds: ['east', 'west'], maxRounds: 1},
    review: {providerId: 'pi', policyDigest: digest}, publication: null}});}
async function finish(raw, onDiagnostic) {
  const leader = port(), provider = {id: 'pi', start: () => ({started: Promise.resolve(cleanup.started), stop() {}, completion: Promise.resolve(raw)})};
  const handle = leader.start({ticket, prepared: {}, provider, onDiagnostic});
  assert.deepEqual(await handle.started, cleanup.started);
  const result = await handle.completion;
  return receipt(leader, ticket, result);
}
const failed = {providerId: 'pi', status: 'failed', stopReason: 'error', reason: 'pi_agent_error', outputText: 'PRIVATE_OUTPUT', cleanup};
test('raw failure metadata survives without changing receipt or callback failure semantics', async () => {
  const baseline = await finish(failed), observed = [];
  assert.deepEqual(await finish(failed, report => observed.push(report)), baseline);
  for (const callback of [() => {throw new Error('PRIVATE_EXCEPTION');}, () => Promise.reject(new Error('PRIVATE_EXCEPTION')), () => new Promise(() => {})])
    assert.deepEqual(await finish(failed, callback), baseline);
  assert.deepEqual(observed, [{code: 'managed_provider_failure', authority: false, taskId: 'task-one', workerId: 'worker-one', providerId: 'pi',
    executionType: 'leader', status: 'failed', stopReason: 'error', reason: 'pi_agent_error', stage: 'provider-result', parseCode: null}]);
  assert.equal(baseline.reason, null); assert.deepEqual(baseline.cleanup, cleanup);
});
test('parse and cleanup failure stages remain distinct; unknown raw enums do not leak', async () => {
  const reports = [];
  const parsed = await finish({...failed, status: 'completed', stopReason: 'end_turn', reason: 'PRIVATE_REASON'}, report => reports.push(report));
  assert.equal(parsed.reason, 'invalid_leader_decision'); assert.equal(reports[0].stage, 'parse'); assert.equal(reports[0].reason, null);
  assert.equal(reports[0].parseCode, 'invalid_json');
  await finish({...failed, stopReason: 'PRIVATE_STOP', reason: 'PRIVATE_REASON', cleanup: {...cleanup, cleaned: false}}, report => reports.push(report));
  assert.equal(reports[1].stage, 'cleanup'); assert.equal(reports[1].stopReason, null); assert.equal(reports[1].reason, null);
  for (const [outputText, code] of [['{}', 'invalid_leader_decision'], ['\uFEFF{}', 'invalid_leader_result']]) {
    await finish({...failed, status: 'completed', stopReason: 'end_turn', outputText}, report => reports.push(report));
    assert.equal(reports.at(-1).parseCode, code);
  }
  assert.doesNotMatch(JSON.stringify(reports), /PRIVATE_/);
});
test('successful original port completion does not emit a failure observation', async () => {
  const outputText = JSON.stringify({profile: 'task-managed-leader/v1', callId: 'call-one', inputDigest: digest, summary: '原受控建议',
    actions: [{type: 'conclude', outcome: 'wait', summary: '等待原操作', basisDigests: []}]});
  const reports = [], result = await finish({...failed, status: 'completed', stopReason: 'end_turn', outputText}, report => reports.push(report));
  assert.equal(result.status, 'completed'); assert.equal(result.reason, null); assert.deepEqual(reports, []);
});
test('diagnostic projection and consumer bound both fields and bytes; no raw or arbitrary metadata', () => {
  const shape = {code: 'managed_provider_failure', authority: false, taskId: 'task-one', workerId: 'worker-one', providerId: 'pi',
    executionType: 'leader', status: 'failed', stopReason: 'error', reason: 'pi_agent_error', stage: 'provider-result', parseCode: null};
  assert.equal(safeManagedDiagnostic({...shape, raw: 'PRIVATE'}), null);
  assert.equal(safeManagedDiagnostic({...shape, authority: true}), null);
  assert.equal(safeManagedDiagnostic({...shape, workerId: '/PRIVATE/path'}), null);
  assert.equal(safeManagedDiagnostic({...shape, status: 'PRIVATE'}).status, null);
  assert.equal(safeManagedDiagnostic({...shape, parseCode: 'PRIVATE'}).parseCode, null);
  const items = [], collect = diagnosticCollector(items), bytes = Buffer.from(JSON.stringify(shape) + '\n');
  collect(bytes.subarray(0, 20)); collect(bytes.subarray(20)); assert.deepEqual(items, [shape]);
  for (const value of [{...shape, raw: 'PRIVATE'}, {...shape, reason: 'PRIVATE'}, {...shape, authority: true}, {...shape, workerId: '/PRIVATE'}])
    collect(Buffer.from(JSON.stringify(value) + '\n'));
  collect(Buffer.from('x'.repeat(4096))); collect(Buffer.from('\n')); assert.equal(items.length, 1);
  for (let i = 0; i < 40; i++) collect(bytes); assert.equal(items.length, 32);
  assert.ok(Buffer.byteLength(JSON.stringify(items)) <= 32 * 2048);
  assert.doesNotMatch(JSON.stringify(items), /PRIVATE|raw/);
});
