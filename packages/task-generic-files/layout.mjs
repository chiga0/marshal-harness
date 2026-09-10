import {checkedGraph} from '../task-application/graph.mjs';

const identifier = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const check = value => {if (!value) throw new Error('generic_files_plan_unsupported');};
const description = '通用文件成果布局：输入按摘要引用，每个作者独立输出 result.md；仅冻结材料与交付位置，不代表业务验收通过，不授权外部写入或发布。';

/** Pure preparation for the existing trusted verification bindPlan port.
 * Not registered in serve: independent business acceptance still needs an
 * explicit profile, checker and Review integration before runtime enablement.
 * Intent/scope text never becomes a filesystem path or permission rule. */
export function bindGenericFilesPlan({inputArtifacts, proposal}) {
  check(Array.isArray(inputArtifacts) && inputArtifacts.length <= 16 && proposal);
  const graph = checkedGraph(proposal.nodes, proposal.edges, {maxNodes: 9, maxEdges: 36});
  const nodes = [...proposal.nodes].sort((a, b) => a.id.localeCompare(b.id, 'en'));
  check(new Set(nodes.map(node => node.id.toLowerCase())).size === nodes.length);
  check(nodes.every(node => ['author', 'verifier'].includes(node.role)));
  const verifiers = nodes.filter(node => node.role === 'verifier');
  check(verifiers.length === 1 && nodes.length >= 2);
  const verifier = verifiers[0];
  const sinks = nodes.filter(node => graph.outgoing.get(node.id).size === 0);
  check(sinks.length === 1 && sinks[0].id === verifier.id);
  const inputs = [...inputArtifacts].sort((a, b) => String(a?.id).localeCompare(String(b?.id), 'en'));
  check(inputs.every(item => identifier(item?.id)) && new Set(inputs.map(item => item.id.toLowerCase())).size === inputs.length);
  const authors = nodes.filter(node => node.role === 'author');
  const deliveries = authors.filter(node => graph.incoming.get(verifier.id).has(node.id))
    .map(node => ({nodeId: node.id, path: 'result.md', targetPath: `results/${node.id}.md`}));
  const layouts = authors.map(node => ({nodeId: node.id, allowedPaths: ['result.md'], inputs: [
    ...inputs.map(item => ({path: `inputs/${item.id}`, source: {kind: 'input', id: item.id}})),
    ...authors.filter(parent => graph.incoming.get(node.id).has(parent.id))
      .map(parent => ({path: `upstream/${parent.id}.md`, source: {kind: 'upstream', nodeId: parent.id, path: 'result.md'}})),
  ]}));
  layouts.push({nodeId: verifier.id, allowedPaths: [], inputs: deliveries.map(item => ({
    path: item.targetPath, source: {kind: 'upstream', nodeId: item.nodeId, path: item.path},
  }))});
  return {nodeId: verifier.id, description, layouts, deliveries};
}
