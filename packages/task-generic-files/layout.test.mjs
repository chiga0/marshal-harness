import test from 'node:test';
import assert from 'node:assert/strict';
import {bindGenericFilesPlan} from './layout.mjs';
import {TaskVerification, createVerificationPort} from '../task-application/verification.mjs';

const plan = (authors, edges = authors.map(from => ({from, to: 'verify'}))) => ({
  nodes: [...authors.map(id => ({id, role: 'author', providerId: null})), {id: 'verify', role: 'verifier', providerId: null}],
  edges, acceptance: ['覆盖用户已确认要求'],
});
function bind(input, proposal) {
  const port = createVerificationPort({id: 'generic-fixture', policy: {id: 'fixture', version: '1', description: '仅测试布局准入，不执行验收。'},
    bindPlan: bindGenericFilesPlan, start() {assert.fail('layout must not start execution');}});
  const verifier = new TaskVerification({artifacts: {requireDepot() {}}}, port);
  return verifier.bind({input, inputArtifacts: input.artifacts ?? []}, structuredClone(proposal));
}
test('different business prompts and arbitrary node names use existing Core binding, without business literals', () => {
  for (const [intent, authors] of [['起草数据开发 SQL，不执行', ['sql', 'design']], ['整理采购方案', ['requirements', 'comparison', 'risks']]]) {
    const binding = bind({intent}, plan(authors));
    assert.equal(binding.profile, 'task-verification/v1');
    assert.equal(binding.deliveries.length, authors.length);
    assert.deepEqual(new Set(binding.deliveries.map(x => x.targetPath)), new Set(authors.map(id => `results/${id}.md`)));
  }
});
test('dependencies receive upstream references; only final outputs are delivered, original inputs preserved', () => {
  const binding = bind({intent: '分阶段产出', artifacts: [{id: 'source-1'}]}, plan(['research', 'draft'], [
    {from: 'research', to: 'draft'}, {from: 'draft', to: 'verify'},
  ]));
  const draft = binding.layouts.find(x => x.nodeId === 'draft');
  assert.deepEqual(draft.inputs, [
    {path: 'inputs/source-1', source: {kind: 'input', id: 'source-1'}},
    {path: 'upstream/research.md', source: {kind: 'upstream', nodeId: 'research', path: 'result.md'}},
  ]);
  assert.deepEqual(binding.deliveries, [{nodeId: 'draft', path: 'result.md', targetPath: 'results/draft.md'}]);
  assert.deepEqual(binding.layouts.find(x => x.nodeId === 'verify').allowedPaths, []);
});
test('zero inputs and one author accepted; intent and scope cannot create filesystem authority', () => {
  const proposal = plan(['writer']); proposal.nodes[0].scope = ['/etc/passwd', '../../secret'];
  const a = bind({intent: '读取 /etc/passwd'}, proposal);
  const b = bind({intent: '只做摘要'}, plan(['writer']));
  assert.deepEqual(a, b);
  assert.deepEqual(a.layouts.find(x => x.nodeId === 'writer').allowedPaths, ['result.md']);
});
test('invalid graphs, extra sinks, role injection, path/case aliases and excessive inputs rejected', () => {
  const variants = [plan(['../escape']), plan(['A', 'a']), plan(['isolated'], []),
    plan(['x'], [{from: 'x', to: 'verify'}, {from: 'verify', to: 'x'}]),
    plan(Array.from({length: 9}, (_, i) => `author${i}`))];
  const role = plan(['x']); role.nodes[0].role = 'publisher'; variants.push(role);
  for (const proposal of variants) assert.throws(() => bind({intent: 'test'}, proposal));
  for (const artifacts of [[{id: '../escape'}], [{id: 'A'}, {id: 'a'}], Array.from({length: 17}, (_, i) => ({id: `input${i}`}))])
    assert.throws(() => bind({intent: 'test', artifacts}, plan(['x'])));
});
