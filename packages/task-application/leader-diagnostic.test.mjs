import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {createLeaderPort, parseManagedOutput, receipt, safeManagedDiagnostic, safeRejectedOutputDiagnostic} from './leader-ports.mjs';
import {diagnosticCollector} from '../task-leader-report/live-consumer.fixture.mjs';
const hashBytes = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
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
  const reports = [], failures = () => reports.filter(report => report.code === 'managed_provider_failure');
  const parsed = await finish({...failed, status: 'completed', stopReason: 'end_turn', reason: 'PRIVATE_REASON'}, report => reports.push(report));
  assert.equal(parsed.reason, 'invalid_leader_decision'); assert.equal(failures()[0].stage, 'parse'); assert.equal(failures()[0].reason, null);
  assert.equal(failures()[0].parseCode, 'invalid_json');
  await finish({...failed, stopReason: 'PRIVATE_STOP', reason: 'PRIVATE_REASON', cleanup: {...cleanup, cleaned: false}}, report => reports.push(report));
  assert.equal(failures()[1].stage, 'cleanup'); assert.equal(failures()[1].stopReason, null); assert.equal(failures()[1].reason, null);
  for (const [outputText, code] of [['{}', 'invalid_leader_decision'], ['\uFEFF{}', 'invalid_leader_result']]) {
    await finish({...failed, status: 'completed', stopReason: 'end_turn', outputText}, report => reports.push(report));
    assert.equal(failures().at(-1).parseCode, code);
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
test('parse rejection retains a bounded base64 copy of the original rejected output', async () => {
  for (const [output, parseCode] of [['', 'invalid_leader_result'], ['A'.repeat(100), 'invalid_json'], ['A'.repeat(1536), 'invalid_json'],
    ['A'.repeat(1537), 'invalid_json'], ['A'.repeat(2048), 'invalid_json'], ['A'.repeat(2049), 'invalid_json'], ['A'.repeat(4096), 'invalid_json']]) {
    const reports = [], buffer = Buffer.from(output, 'utf8');
    const result = await finish({...failed, status: 'completed', stopReason: 'end_turn', outputText: output}, report => reports.push(report));
    assert.equal(result.status, 'failed'); assert.equal(result.reason, 'invalid_leader_decision'); assert.equal(result.value, null);
    assert.equal(reports.length, 2);
    assert.equal(reports[0].code, 'managed_provider_failure'); assert.equal(reports[0].stage, 'parse'); assert.equal(reports[0].parseCode, parseCode);
    assert.deepEqual(reports[1], {code: 'managed_provider_rejected_output', authority: false, taskId: 'task-one', workerId: 'worker-one',
      providerId: 'pi', executionType: 'leader', encoding: 'utf8-base64', wellformed: true, bytes: buffer.length, digest: hashBytes(buffer),
      truncated: buffer.length > 2048, head: buffer.subarray(0, 1536).toString('base64'),
      tail: buffer.length > 1536 ? buffer.subarray(Math.max(1536, buffer.length - 512)).toString('base64') : ''});
  }
});
test('rejected copy stays bounded for oversize, malformed or non-string output', async () => {
  for (const [outputText, expected] of [
    ['x'.repeat(1048577), {wellformed: true, bytes: 1048577, digest: null, truncated: true,
      head: Buffer.from('x'.repeat(1536)).toString('base64'), tail: Buffer.from('x'.repeat(512)).toString('base64')}],
    ['\ud800', {wellformed: false, bytes: 3, digest: hashBytes(Buffer.from('�')), truncated: false, head: Buffer.from('�').toString('base64'), tail: ''}],
    [42, {wellformed: false, bytes: null, digest: null, truncated: false, head: '', tail: ''}]]) {
    const reports = [];
    const result = await finish({...failed, status: 'completed', stopReason: 'end_turn', outputText}, report => reports.push(report));
    assert.equal(result.status, 'failed'); assert.equal(result.reason, 'invalid_leader_decision');
    assert.equal(reports.length, 2); assert.equal(reports[0].parseCode, 'invalid_leader_result');
    assert.deepEqual(reports[1], {code: 'managed_provider_rejected_output', authority: false, taskId: 'task-one', workerId: 'worker-one',
      providerId: 'pi', executionType: 'leader', encoding: 'utf8-base64', ...expected});
    assert.equal(safeRejectedOutputDiagnostic(reports[1]) !== null, true);
    assert.ok(Buffer.byteLength(JSON.stringify(reports[1])) <= 4096);
  }
});
test('rejected-output projection stays closed and bounded', () => {
  const shape = {code: 'managed_provider_rejected_output', authority: false, taskId: 'task-one', workerId: 'worker-one', providerId: 'pi',
    executionType: 'review', encoding: 'utf8-base64', wellformed: true, bytes: 2, digest: 'sha256:' + 'a'.repeat(64), truncated: false,
    head: 'e30=', tail: ''};
  assert.deepEqual(safeRejectedOutputDiagnostic(shape), shape);
  for (const bad of [{...shape, raw: 'PRIVATE'}, {...shape, extra: 'x'}, {...shape, authority: true}, {...shape, executionType: 'verify'},
    {...shape, encoding: 'text'}, {...shape, wellformed: 'yes'}, {...shape, bytes: -1}, {...shape, bytes: 2.5},
    {...shape, bytes: 'big'}, {...shape, digest: 'deadbeef'}, {...shape, truncated: 1}, {...shape, head: '%%%'}, {...shape, head: 'e30=\n'},
    {...shape, head: Buffer.alloc(1537).toString('base64')}, {...shape, tail: Buffer.alloc(513).toString('base64')}])
    assert.equal(safeRejectedOutputDiagnostic(bad), null);
});
test('consumer collector retains bounded rejected-output records under separate caps', () => {
  const failure = {code: 'managed_provider_failure', authority: false, taskId: 'task-one', workerId: 'worker-one', providerId: 'pi',
    executionType: 'leader', status: 'failed', stopReason: 'error', reason: 'pi_agent_error', stage: 'provider-result', parseCode: null};
  const rejected = {code: 'managed_provider_rejected_output', authority: false, taskId: 'task-one', workerId: 'worker-one', providerId: 'pi',
    executionType: 'review', encoding: 'utf8-base64', wellformed: true, bytes: 2, digest: 'sha256:' + 'a'.repeat(64), truncated: false,
    head: 'e30=', tail: ''};
  const items = [], collect = diagnosticCollector(items);
  const rejectedBytes = Buffer.from(JSON.stringify(rejected) + '\n');
  collect(rejectedBytes.subarray(0, 37)); collect(rejectedBytes.subarray(37));
  collect(Buffer.from(JSON.stringify(failure) + '\n'));
  assert.deepEqual(items, [rejected, failure]);
  assert.equal(Buffer.from(items[0].head, 'base64').toString(), '{}');
  for (const bad of [{...rejected, raw: 'PRIVATE'}, {...rejected, authority: true}, {...rejected, head: '%%%'},
    {...rejected, bytes: -1}, {...rejected, head: Buffer.alloc(1537).toString('base64')}])
    collect(Buffer.from(JSON.stringify(bad) + '\n'));
  collect(Buffer.from('y'.repeat(5000))); collect(Buffer.from('\n'));
  assert.equal(items.length, 2);
  for (let i = 0; i < 10; i++) collect(rejectedBytes);
  for (let i = 0; i < 40; i++) collect(Buffer.from(JSON.stringify(failure) + '\n'));
  assert.equal(items.filter(value => value.code === 'managed_provider_rejected_output').length, 8);
  assert.equal(items.filter(value => value.code === 'managed_provider_failure').length, 32);
  assert.doesNotMatch(JSON.stringify(items), /PRIVATE/);
});
