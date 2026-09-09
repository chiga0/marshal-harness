import test from 'node:test';
import assert from 'node:assert/strict';
import {encode, digest} from '../task-store/store.mjs';
import {data, taskBody, reportFor, verifyRegions, consumeDelivery, bindPlan} from './scenario.fixture.mjs';

const input = () => ({sales: encode(data), answer: 'paid', files: reportFor('paid').reports.map(report =>
  ({path: report.region + '.json', content: encode(report)}))});

test('Leader scenario preserves missing answer and explicit bounded original budget', () => {
  const task = taskBody('input-sales', 600000);
  assert.deepEqual(task.context.inputRefs, ['input-sales']);
  assert.deepEqual(task.limits, {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3});
  assert.match(task.context.text, /尚未给定/);
  assert.throws(() => taskBody('../input', 600000));
  assert.throws(() => taskBody('input-sales', 0));
  assert.throws(() => reportFor(undefined), /explicit_business_answer_required/);
  const bound = bindPlan({inputArtifacts: [{id: 'input-sales'}], proposal: {nodes: ['east', 'west', 'verify'].map(id => ({id}))}});
  assert.equal(bound.nodeId, 'verify');
  assert.deepEqual(bound.layouts.map(layout => layout.allowedPaths), [['east.json'], ['west.json'], []]);
});

test('independent business oracle includes zero and negative rows and preserves west for either answer', () => {
  const paid = reportFor('paid'), cancelled = reportFor('cancelled');
  assert.deepEqual(paid.reports, [{region: 'east', status: 'paid', count: 2, netCents: 1275},
    {region: 'west', status: 'paid', count: 2, netCents: 550}]);
  assert.deepEqual(cancelled.reports[0], {region: 'east', status: 'cancelled', count: 1, netCents: 9000});
  assert.deepEqual(cancelled.reports[1], paid.reports[1]);
  assert.deepEqual(verifyRegions(input()), paid);
  const bytes = encode(paid);
  assert.deepEqual(consumeDelivery(bytes, 'paid'), {digest: digest(bytes), bytes: bytes.length, reports: 2});
});

test('oracle rejects stale answer, plausible wrong total, changed source, omitted or extra report fields', () => {
  const cases = [
    value => {value.answer = 'cancelled';},
    value => {value.files[1].content = encode({...reportFor('paid').reports[1], netCents: 800});},
    value => {const copy = structuredClone(data); copy.rows[1].cents = 801; value.sales = encode(copy);},
    value => {value.files.pop();},
    value => {value.files.reverse();},
    value => {value.files[0].content = encode({...reportFor('paid').reports[0], approved: true});},
    value => {value.files[0].content = Buffer.from('{"region":"east","region":"west"}');},
    value => {value.files[0].content = Buffer.alloc(4097, 32);},
  ];
  for (const mutate of cases) {const value = input(); mutate(value); assert.throws(() => verifyRegions(value));}
  assert.throws(() => consumeDelivery(encode(reportFor('paid')), 'cancelled'));
  assert.throws(() => consumeDelivery(encode({reports: reportFor('paid').reports, verified: true}), 'paid'));
  assert.throws(() => consumeDelivery(Buffer.from([0xff]), 'paid'));
});
