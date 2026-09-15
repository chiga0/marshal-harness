import test from 'node:test';
import assert from 'node:assert/strict';
import {checkedGraph, affectedNodes} from './graph.mjs';
import {freezePlan, actions, nextRevision} from './model.mjs';

const nodes = ['api', 'client', 'integration'].map(id => ({id, role: 'author', goal: 'Implement ' + id, scope: [id], providerId: null}));
const edges = [{from: 'api', to: 'integration'}, {from: 'client', to: 'integration'}];
const record = {task: {id: 'task-1'}, limits: {timeoutMs: 10000, maxAttempts: 5, maxWorkers: 2}, plan: null};
const proposal = {summary: 'Deliver a usable application', nodes, edges,
  deliverables: ['Sources and execution instructions'], acceptance: ['Consumer integration checks'], assumptions: []};

test('graph respects dependencies while local rework excludes unrelated accepted branches', () => {
  assert.deepEqual(checkedGraph(nodes, edges).order, ['api', 'client', 'integration']);
  assert.deepEqual(affectedNodes(nodes, edges, ['api']), ['api', 'integration']);
  for (const wrong of [[...edges, {from: 'integration', to: 'api'}], [...edges, edges[0]], [{from: 'unknown', to: 'client'}]])
    assert.throws(() => checkedGraph(nodes, wrong));
  assert.throws(() => checkedGraph([...nodes, nodes[0]], edges));
});

test('plan freezes bounded business fields and cannot raise original execution budget', () => {
  let hashed;
  const plan = freezePlan(record, proposal, value => { hashed = structuredClone(value); return 'sha256:fixture'; });
  assert.equal(plan.revision, 1); assert.deepEqual(hashed.nodes, nodes);
  assert.equal(Object.hasOwn(hashed, 'digest'), false);
  proposal.nodes[0].goal = 'changed after approval';
  assert.equal(plan.nodes[0].goal, 'Implement api');
  for (const invalid of [{...proposal, budget: {...record.limits, maxWorkers: 3}},
    {...proposal, budget: {...record.limits, maxAttempts: 2}}, {...proposal, acceptance: []},
    {...proposal, nodes: [{...nodes[0], role: 'publisher'}]}]) assert.throws(() => freezePlan(record, invalid, () => ''), error => error.status === 400);
  assert.equal(freezePlan({...record, plan}, proposal, () => 'digest').revision, 2);
});

test('terminal/cancelling states never advertise another execution or approval', () => {
  for (const status of ['completed', 'failed', 'cancelled', 'intervention', 'cancelling']) assert.deepEqual(actions({status}), []);
  assert.deepEqual(actions({status: 'awaiting-approval'}), ['approve', 'cancel']);
  assert.throws(() => nextRevision(Number.MAX_SAFE_INTEGER));
});
