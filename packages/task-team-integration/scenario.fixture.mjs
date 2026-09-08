// Test-only business policy. Never a default Task template or model implementation.
export const policy = {id: 'regional-sales-fixture', version: '1', description: '两个地区独立计算，完整交付由固定检查器按原输入重新计算；不执行生产数据库写入。'};
export function proposal() {
  return {summary: '两个地区并行处理销售记录，独立检查完整报告',
    nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
      goal: id === 'verify' ? '独立重算两个地区并集成交付' : '读取 sales.json，只统计本地区 paid 记录的整数金额，输出 ' + id + '.json',
      scope: [id], providerId: null})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
    deliverables: ['east.json', 'west.json'], acceptance: ['排除 cancelled，保留零额及负数退款，逐地区与整体总额一致'], assumptions: []};
}
export function bindPlan({inputArtifacts, proposal: plan}) {
  if (inputArtifacts.length !== 1 || plan.nodes.map(node => node.id).join(',') !== 'east,west,verify') throw Error('unsupported fixture');
  return {nodeId: 'verify', description: policy.description,
    layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}],
      allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [], inputs: ['east', 'west'].map(nodeId =>
      ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
    deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
}
