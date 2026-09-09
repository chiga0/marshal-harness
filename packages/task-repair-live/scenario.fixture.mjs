// Public synthetic business. No expected error, hidden requirement, answer or
// precomputed report is sent to an author. A correct first attempt is valid.
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {createRepairPort} from '../task-application/application.mjs';

export const equal = (a, b) => {try {return encode(a).equals(encode(b));} catch {return false;}};
export const regions = Object.freeze(['east', 'west']);
export const data = Object.freeze({rows: [
  {orderId: 'e1', revision: 1, region: 'east', status: 'paid', cents: 1250},
  {orderId: 'w1', revision: 1, region: 'west', status: 'paid', cents: 600},
  {orderId: 'e2', revision: 1, region: 'east', status: 'paid', cents: -75},
  {orderId: 'w2', revision: 1, region: 'west', status: 'paid', cents: 200},
  {orderId: 'e3', revision: 1, region: 'east', status: 'paid', cents: 0},
  {orderId: 'w1', revision: 2, region: 'west', status: 'paid', cents: 625},
  {orderId: 'e4', revision: 1, region: 'east', status: 'cancelled', cents: 9000},
  {orderId: 'w2', revision: 2, region: 'west', status: 'cancelled', cents: 200},
  {orderId: 'w3', revision: 1, region: 'west', status: 'paid', cents: -50},
  {orderId: 'w4', revision: 1, region: 'west', status: 'paid', cents: 0},
  {orderId: 'w5', revision: 1, region: 'west', status: 'cancelled', cents: 100},
  {orderId: 'w5', revision: 2, region: 'west', status: 'paid', cents: 100},
]});
export const rules = '先按 orderId 取 revision 最大的唯一记录，再筛选本地区 status=paid；不是先筛选 paid。' +
  '同 orderId 的 region 不变，输入不存在同 revision 冲突。count 计算去重后 paid 订单数，包括零额和负数；' +
  'netCents 对 cents 整数求和，保留负退款，不转浮点金额。输出恰有 region、count、netCents 三字段的 JSON。';
export const policy = Object.freeze({id: 'regional-latest-paid', version: '1', description: rules + '独立读取完整两地区产物并按原上传输入重算，不执行作者代码。'});
export const repairPolicy = Object.freeze({id: 'regional-latest-paid-repair', version: '1',
  description: '只允许操作者在真实内容拒收后选择一个地区，反馈不改变原规则；保留另一地区精确结果，完整重验，原六次总预算和期限不刷新。'});
export const contentAssertions = Object.freeze(regions.map(region => region + '-content'));
export function repairPort() {return createRepairPort({policy: repairPolicy, nodeIds: [...regions], assertions: [...contentAssertions]});}
export const repairPolicyDigest = repairPort().policyDigest;
export function proposal() {
  return {summary: '按最新订单版本独立汇总两个地区的已付款净额',
    nodes: [...regions, 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', providerId: null, scope: [id],
      goal: id === 'verify' ? '独立重算两地区完整交付' : '读取 sales.json，严格按业务规则计算 ' + id + '，只写 ' + id + '.json。' + rules})),
    edges: regions.map(from => ({from, to: 'verify'})), deliverables: regions.map(id => id + '.json'),
    acceptance: [rules], assumptions: []};
}
export function taskBody(inputId, timeoutMs) {
  return {intent: '按最新订单版本汇总两地区已付款流水；要求首次正确，无预设错误或故意返工。',
    context: {inputRefs: [inputId], text: '公开合成数据，固定实机验收业务。' + rules +
      '两个作者并行，各只写本地区文件。只用原生 read/write/edit，不用 shell、网络、发布或修改其他文件。' +
      'Planner 必须返回以下完整固定 proposal，不增删图、权限、预算或验收要求：\n' + encode(proposal()).toString()},
    requirements: {deliverables: proposal().deliverables, acceptance: [rules]}, limits: {timeoutMs, maxAttempts: 6, maxWorkers: 2}};
}
export function bindPlan({inputArtifacts, proposal: actual}) {
  const declared = proposal();
  if (inputArtifacts.length !== 1 || !equal(actual.nodes, declared.nodes) || !equal(actual.edges, declared.edges) ||
      !equal(actual.deliverables, declared.deliverables) || !equal(actual.assumptions, [])) throw Error('fixed_plan_mismatch');
  return {nodeId: 'verify', description: policy.description,
    layouts: regions.map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}],
      allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: regions.map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
    deliveries: regions.map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
}
export function originalInput(ticket) {
  const refs = ticket.input.inputArtifacts, bytes = encode(data);
  if (!Array.isArray(refs) || refs.length !== 1 || refs[0].kind !== 'input' || refs[0].name !== 'sales.json' ||
      refs[0].digest !== digest(bytes) || refs[0].bytes !== bytes.length) throw Error('original_input_mismatch');
  return {source: structuredClone(refs[0]), sales: structuredClone(data), verification: ticket.input.verification};
}
export function expectedReports() {
  const latest = new Map();
  for (const row of data.rows) if (!latest.has(row.orderId) || row.revision > latest.get(row.orderId).revision) latest.set(row.orderId, row);
  return regions.map(region => {const rows = [...latest.values()].filter(row => row.region === region && row.status === 'paid');
    return {region, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)};});
}
export function reportShape(value, region) {
  return value && equal(Object.keys(value).sort(), ['count', 'netCents', 'region']) && value.region === region &&
    Number.isSafeInteger(value.count) && value.count >= 0 && Number.isSafeInteger(value.netCents);
}
// A separate download consumer uses sorted first-version selection, not the
// parent's Map reducer or the checker's claimed pass/expected values.
export function consumeDelivery(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length < 1 || bytes.length > 16384) throw Error('delivery_limit');
  const value = parseJson(Buffer.from(bytes));
  if (!equal(Object.keys(value), ['files']) || !Array.isArray(value.files) || value.files.length !== 2) throw Error('delivery_shape');
  const ordered = [...data.rows].sort((a, b) => a.orderId.localeCompare(b.orderId) || b.revision - a.revision);
  const selected = ordered.filter((row, at) => at === 0 || row.orderId !== ordered[at - 1].orderId);
  for (const [at, region] of regions.entries()) {
    const file = value.files[at];
    if (!equal(Object.keys(file).sort(), ['content', 'path']) || file.path !== region + '.json' || typeof file.content !== 'string') throw Error('delivery_shape');
    const report = parseJson(Buffer.from(file.content)), rows = selected.filter(row => row.region === region && row.status === 'paid');
    let cents = 0; for (const row of rows) cents += row.cents;
    if (!reportShape(report, region) || report.count !== rows.length || report.netCents !== cents) throw Error('delivery_business_mismatch');
  }
  return {digest: digest(bytes), bytes: bytes.length, reports: 2};
}
