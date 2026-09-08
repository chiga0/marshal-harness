// Deterministic business test, not a production default or a model answer.
import {gitDescription, PATCH, CONTEXT} from './index.mjs';
export const policy = {id: 'cross-repository-invoice-fixture', version: '1',
  description: '两个独立仓库锁定 base，只改已批准文件；独立应用 patch 后验证跨仓库组合与未改文件。'};
export function proposal() {
  return {summary: 'library 折扣计算与 client 发票组合，两个仓库并行修改后独立验收',
    nodes: [
      {id: 'library', role: 'author', goal: 'net(cents,discount) 校验非负 safe integer 且 discount<=cents，返回差值，非法 throw。', scope: ['net.mjs'], providerId: null},
      {id: 'client', role: 'author', goal: 'invoice(rows,net) 返回 {lines,total}，逐行调用 net，累计金额必须为 safe integer，非法 throw。', scope: ['invoice.mjs'], providerId: null},
      {id: 'verify', role: 'verifier', goal: '在两个同 base 新 worktree 应用 patch，独立检查组合及无关内容。', scope: [], providerId: null},
    ], edges: [{from: 'library', to: 'verify'}, {from: 'client', to: 'verify'}],
    deliverables: ['两个精确 base 的 patch、上下文及消费说明'], acceptance: ['原仓库不变，锁定 base，保留未授权文件，独立组合金额与负例通过'], assumptions: []};
}
export function bindPlan({taskInput, proposal: plan}) {
  const description = gitDescription(taskInput.context?.text);
  if (description.nodes.map(node => node.nodeId).sort().join(',') !== 'client,library' ||
      plan.nodes.map(node => node.id).join(',') !== 'library,client,verify') throw Error('unsupported_git_fixture');
  const names = nodeId => [{name: PATCH, target: nodeId + '.patch'}, {name: CONTEXT, target: nodeId + '-context.json'}];
  return {nodeId: 'verify', description: policy.description + '\n冻结仓库/base/写范围：' + JSON.stringify(description),
    layouts: ['library', 'client'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [PATCH, CONTEXT]})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: ['library', 'client'].flatMap(nodeId => names(nodeId).map(item => ({path: item.target, source: {kind: 'upstream', nodeId, path: item.name}})))}),
    deliveries: ['library', 'client'].flatMap(nodeId => names(nodeId).map(item => ({nodeId, path: item.name, targetPath: item.target})))};
}
