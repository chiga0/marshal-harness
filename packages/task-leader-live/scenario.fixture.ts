// Test business oracle only; never an authority/Reviewer/production plugin.
// The real driver must separately bind Core's original input, answers and receipts.
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {data, choices, expectedReports, equal} from '../task-runtime-question-live/scenario.fixture.mjs';

export {data, choices};
const check = (value, code) => {if (!value) throw new Error(code);};
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
function decode(bytes, limit = 16384) {
  check(bytes instanceof Uint8Array && bytes.byteLength > 0 && bytes.byteLength <= limit, 'business_bytes_invalid');
  return parseJson(Buffer.from(bytes));
}
export const policy = Object.freeze({id: 'leader-regional-report', version: '1',
  description: '根据原始销售数据及用户明确的 east 状态独立重算两个地区报告，禁止执行作者代码。'});

export function taskBody(inputId, timeoutMs) {
  check(id(inputId) && Number.isSafeInteger(timeoutMs) && timeoutMs >= 60000 && timeoutMs <= 900000, 'business_task_invalid');
  return {intent: '由 Leader 澄清缺项、组织两个作者生成地区销售报告，安排独立审查和验收，在明确授权后发布报告并验证消费结果。',
    context: {inputRefs: [inputId], text: '这是合成数据的受限实机验收，不是真实客户数据。' +
      'east 的统计状态尚未给定；Leader 必须向用户询问 paid 或 cancelled，不能猜测、设置默认值或当成假设。' +
      'west 独立统计 paid。Leader 读取回复后提出一次可确认的计划。' +
      '计划包含 east、west 两个 role=author 节点和 verify(role=verifier)，按该顺序排列；providerId 均为 null。' +
      '仅 east→verify、west→verify 两条边，作者之间无依赖；独立语义 Reviewer 是受管审查，不增加 DAG 作者。' +
      '两作者读取原 sales.json，只写各自 east.json 或 west.json；每个文件恰有 region、status、count、netCents 四字段。' +
      '零额计入笔数，负数原样计入净额，所有金额使用整数 cents；不预设结果。' +
      '只使用被批准的原生文件工具；不执行 shell、网络发布或改变验收规则。' +
      'deliverables 恰为 east.json、west.json，assumptions=[]；budget 沿任务限制。' +
      '独立 Review 如有真实问题，仅修正受影响部分并保留仍有效的另一结果；首轮正确不制造返工。' +
      '发布只能走已配置的目标并等待精确成果授权；作者或 Leader 不直接写发布目录。'},
    requirements: {deliverables: ['east.json', 'west.json'],
      acceptance: ['east 使用用户明确回复的状态，west 固定 paid；原输入独立重算完整报告，计入零额及负数。',
        '当前组合须有独立 Review 和验收；按精确授权交付、独立后验，Leader 汇总，不能把进程退出当成成功。']},
    limits: {timeoutMs, maxAttempts: 17, maxWorkers: 3}};
}

export function bindPlan({inputArtifacts, proposal}) {
  check(Array.isArray(inputArtifacts) && inputArtifacts.length === 1 && id(inputArtifacts[0].id) &&
    Array.isArray(proposal?.nodes) && proposal.nodes.map(node => node.id).join(',') === 'east,west,verify', 'business_plan_invalid');
  return {nodeId: 'verify', description: policy.description,
    layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}],
      allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
    deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
}

export function reportFor(answer) {
  check(choices.includes(answer), 'explicit_business_answer_required');
  return {reports: expectedReports(answer)};
}

export function verifyRegions({sales, files, answer}) {
  check(equal(decode(sales), data), 'original_input_mismatch');
  const report = reportFor(answer);
  check(Array.isArray(files) && files.length === 2, 'business_files_invalid');
  for (let index = 0; index < 2; index++) {
    check(files[index]?.path === ['east.json', 'west.json'][index], 'business_files_invalid');
    check(equal(decode(files[index].content, 4096), report.reports[index]), 'business_report_mismatch');
  }
  return report;
}

export function consumeDelivery(bytes, answer) {
  check(equal(decode(bytes), reportFor(answer)), 'delivery_business_mismatch');
  return {digest: digest(bytes), bytes: bytes.byteLength, reports: 2};
}
