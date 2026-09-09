// One explicit Git/Pi+Qwen acceptance business. Not a production default.
import fs from 'node:fs';
import path from 'node:path';
import {encode} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {gitDescription, PATCH, CONTEXT} from '../task-git-business/index.mjs';

export const LIMITS = Object.freeze({timeoutMs: 600000, maxAttempts: 4, maxWorkers: 2});
export const NODES = Object.freeze(['library', 'client']);
export const MARKER = 'GIT_MIXED_DECLARED_PLAN_V1\n', END = '\nGIT_MIXED_DECLARED_PLAN_END\n';
export class LiveError extends Error {constructor(code) {super(code); this.name = 'LiveError'; this.code = code;}}
export const check = (value, code) => {if (!value) throw new LiveError(code);};
export const equal = (a, b) => {try {return encode(a).equals(encode(b));} catch {return false;}};
const text = (value, max = 65536) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
const fields = (value, required, optional = []) => value && typeof value === 'object' && !Array.isArray(value) &&
  required.every(key => Object.hasOwn(value, key)) && Object.keys(value).every(key => required.includes(key) || optional.includes(key));
const denied = request => {
  // Preserve a native explicit refusal when offered. ACP failed-only tool
  // updates otherwise cannot prove that execution was rejected before effect.
  const options = request?.options?.filter?.(option => option?.kind === 'reject_once' && text(option.optionId, 128)) ?? [];
  return options.length === 1 ? {outcome: {outcome: 'selected', optionId: options[0].optionId}} : {outcome: {outcome: 'cancelled'}};
};
export const policy = Object.freeze({id: 'git-mixed-invoice-acceptance', version: '1',
  description: '两原仓库锁定 base；只交付真实 patch，独立同 base 应用并检查 net/invoice 组合、非法输入和无关文件。'});
export function proposal() {
  return {summary: 'Pi 与 Qwen 分别实现两个仓库的折扣 API 和发票 API，独立应用 patch 验收', nodes: [
    {id: 'library', role: 'author', providerId: 'pi-rpc', scope: ['net.mjs'], goal:
      '修改已有 net.mjs，导出 function net(cents,discount)。两参数必须是非负 safe integer，discount<=cents，否则 throw；返回 cents-discount。只使用原生 read/write/edit 文件工具，不 shell/commit/push。'},
    {id: 'client', role: 'author', providerId: 'qwen-acp', scope: ['invoice.mjs'], goal:
      '修改已有 invoice.mjs，导出 function invoice(rows,net)。rows 必须是数组，每项非空对象且 sku 是 trim 后非空 string（返回时原样保留，不 trim）；稀疏数组 hole 必须 throw，不得 map/forEach 跳过。逐项调用传入的 net(row.cents,row.discount)，由 net 校验金额参数；amount 必须是非负 safe integer，累加 total 也必须 safe integer。返回 {lines:[{sku,amount}],total}，保持原次序，空数组为 lines:[]/total:0；非法 throw。不 import 另一仓库，net 由独立消费者传入。只使用原生 read/write/edit 文件工具，不 shell/commit/push。'},
    {id: 'verify', role: 'verifier', providerId: null, scope: [], goal: '由预配置独立 checker 在另一组同 base worktree 应用真实 patch，验证组合、负例和无关文件。'},
  ], edges: [{from: 'library', to: 'verify'}, {from: 'client', to: 'verify'}],
  deliverables: ['两个精确 base 的 patch 和上下文，供独立下载消费'],
  acceptance: [policy.description], assumptions: []};
}
export function taskBody(description) {
  gitDescription(encode(description).toString());
  return {intent: '在公开合成的 library/client 两个 Git 仓库中完成固定 net/invoice 业务。按已声明完整计划由 Pi 与 Qwen 并行修改原文件，只交付 patch；不发布、不自行批准、不额外启动工具。',
    context: {text: encode(description).toString()}, requirements: {deliverables: proposal().deliverables, acceptance: proposal().acceptance}, limits: {...LIMITS}};
}
export function bindPlan({taskInput, proposal: plan}) {
  const description = gitDescription(taskInput.context?.text), fixed = proposal();
  check(equal(description.nodes.map(node => node.nodeId), NODES) && equal(plan.nodes, fixed.nodes) &&
    equal(plan.edges, fixed.edges) && equal(plan.deliverables, fixed.deliverables) &&
    equal(plan.assumptions, fixed.assumptions) && equal(plan.acceptance, fixed.acceptance), 'plan_boundary');
  const names = nodeId => [{name: PATCH, target: nodeId + '.patch'}, {name: CONTEXT, target: nodeId + '-context.json'}];
  return {nodeId: 'verify', description: policy.description + '\n原冻结仓库/base/写范围：' + encode(description),
    layouts: NODES.map(nodeId => ({nodeId, inputs: [], allowedPaths: [PATCH, CONTEXT]})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: NODES.flatMap(nodeId => names(nodeId).map(item => ({path: item.target, source: {kind: 'upstream', nodeId, path: item.name}})))}),
    deliveries: NODES.flatMap(nodeId => names(nodeId).map(item => ({nodeId, path: item.name, targetPath: item.target})))};
}
export function validatePlan(task, plan, body) {
  const fixed = proposal(), binding = bindPlan({taskInput: body, proposal: fixed});
  const visible = [{policy, description: binding.description},
    ...binding.layouts.map(layout => ({...layout, inputs: [...layout.inputs].sort((a, b) => a.path < b.path ? -1 : 1),
      allowedPaths: [...layout.allowedPaths].sort()})).sort((a, b) => a.nodeId < b.nodeId ? -1 : 1).map(layout => ({layout})),
    ...binding.deliveries.sort((a, b) => a.targetPath < b.targetPath ? -1 : 1).map(delivery => ({delivery}))];
  check(task.status === 'awaiting-approval' && task.id === plan.taskId && task.plan.digest === plan.digest &&
    task.plan.revision === plan.revision && equal(plan.nodes, fixed.nodes) && equal(plan.edges, fixed.edges) &&
    equal(plan.deliverables, fixed.deliverables) && equal(plan.assumptions, []) && equal(plan.budget, LIMITS) &&
    plan.acceptance.length === fixed.acceptance.length + visible.length && equal(plan.acceptance.slice(0, fixed.acceptance.length), fixed.acceptance), 'plan_boundary');
  let suffix; try {suffix = plan.acceptance.slice(-visible.length).map(line => parseJson(Buffer.from(line)));} catch {throw new LiveError('plan_boundary');}
  check(equal(suffix, visible), 'plan_boundary');
  return {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
}

/** Actual prepared parameters, offered one-time selection, original worker cwd.
 * No shell, .git, other repository, additional file, URI or broad allow. */
export function filePermission(identity, request) {
  const reject = () => denied(request);
  const {cwd, role, nodeId, providerId} = identity ?? {};
  const filename = nodeId === 'library' ? 'net.mjs' : nodeId === 'client' ? 'invoice.mjs' : null;
  if (role !== 'author' || !filename || !path.isAbsolute(cwd ?? '') ||
      providerId !== (nodeId === 'library' ? 'pi-rpc' : 'qwen-acp') || !Array.isArray(request?.options)) return reject();
  const call = request.toolCall, input = call?.rawInput; let target, valid = false;
  if (providerId === 'pi-rpc') {
    if (call?._meta?.provider !== 'pi') return reject();
    target = input?.path;
    if (call.kind === 'read' && call._meta.toolName === 'read' && fields(input, ['path'], ['offset', 'limit']))
      valid = ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 1 && input[key] <= 10000);
    else if (call.kind === 'edit' && call._meta.toolName === 'write' && fields(input, ['path', 'content'])) valid = text(input.content);
    else if (call.kind === 'edit' && call._meta.toolName === 'edit') {
      const edits = fields(input, ['path', 'edits']) ? input.edits : fields(input, ['path', 'oldText', 'newText']) ? [{oldText: input.oldText, newText: input.newText}] : null;
      valid = Array.isArray(edits) && edits.length > 0 && edits.length <= 8 && edits.every(edit => fields(edit, ['oldText', 'newText']) &&
        text(edit.oldText) && text(edit.newText)) && Buffer.byteLength(JSON.stringify(edits)) <= 65536;
    }
  } else {
    target = input?.file_path;
    if (call?.kind === 'read' && fields(input, ['file_path'], ['offset', 'limit']))
      valid = ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 0 && input[key] <= 10000);
    else if (call?.kind === 'edit' && fields(input, ['file_path', 'content'])) valid = text(input.content);
    else if (call?.kind === 'edit' && fields(input, ['file_path', 'old_string', 'new_string'], ['replace_all']))
      valid = text(input.old_string) && text(input.new_string) && (input.replace_all === undefined || typeof input.replace_all === 'boolean');
  }
  if (!valid || target !== filename && target !== path.join(cwd, filename)) return reject();
  try {
    const root = fs.lstatSync(cwd), targetPath = path.join(cwd, filename), stat = fs.lstatSync(targetPath);
    if (!root.isDirectory() || fs.realpathSync(cwd) !== cwd || !stat.isFile() || stat.nlink !== 1 || fs.realpathSync(targetPath) !== targetPath) return reject();
  } catch {return reject();} // Both files must already exist at the locked base.
  const once = providerId === 'pi-rpc' ? 'allow-once' : 'proceed_once';
  const allowed = request.options.filter(option => option?.kind === 'allow_once' && option.optionId === once);
  return allowed.length === 1 ? {outcome: {outcome: 'selected', optionId: once}} : reject();
}
