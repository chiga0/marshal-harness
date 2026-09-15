// Explicit test business only. The operator's selected answer never enters Task input.
import {data} from '../task-qwen-live/driver.fixture.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {createRuntimeQuestionPort} from '../task-application/application.mjs';
export {data};
export const equal = (a, b) => {try {return encode(a).equals(encode(b));} catch {return false;}};
export const choices = Object.freeze(['paid', 'cancelled']);
export const policy = Object.freeze({id: 'regional-runtime-answer', version: '1', description: '从冻结销售输入及 Core 已消费答案独立重算完整双地区报告；不执行作者代码。'});
export const questionPolicy = Object.freeze({id: 'east-status', version: '1', description: 'east 统计状态尚未确定，只能明确选择 paid 或 cancelled；不得改变权限和验收。'});
const descriptor = {profile: 'task-runtime-question/v1', policy: questionPolicy, nodeIds: ['east'], maxQuestions: 1, maxWaitMs: 120000};
export const questionPolicyDigest = digest(encode(descriptor));
export function validQuestion(value) {return value?.kind === 'select' && typeof value.prompt === 'string' &&
  value.prompt.trim().length > 0 && Buffer.byteLength(value.prompt) <= 2048 && equal(value.options, choices);}
export function questions() {const port = createRuntimeQuestionPort({...descriptor, policy: questionPolicy,
  applies: () => true, validateQuestion: validQuestion, validateAnswer: value => choices.includes(value)});
  if (port.policyDigest !== questionPolicyDigest) throw Error('policy_drift'); return port;}
export function taskBody(inputId, timeoutMs) {
  return {intent: '两个作者并行分析销售数据；east 执行时向用户明确缺失的过滤状态，随后独立验收并交付报告。',
    context: {inputRefs: [inputId], text: '受限的实机验收业务，非真实客户数据。只规划三个节点并按 east、west、verify 顺序。' +
      'east、west 均 role=author，verify role=verifier，由已配置独立检查器执行；providerId 全部 null。' +
      '仅两条边 east→verify、west→verify，作者间无依赖。两个作者读取 sales.json，分别只写 east.json、west.json。' +
      'east 的过滤状态尚未提供；east 作者必须在实际执行中调用 marshal_ask_user，kind=select、options=["paid","cancelled"]，' +
      '收到用户明确答案后只统计 east 且 status 等于答案的记录，禁止自行猜测或提前填默认值。' +
      'west 独立统计 west 且 status=paid 的记录，不等待 east。每个文件是恰有 region、status、count、netCents 四字段的 JSON 对象。' +
      '计数包括零额，负数原样参与整数 cents 求和。没有预设统计结果。只用原生 read/write/edit 工具，不用 shell、网络或发布。' +
      '计划 deliverables 恰为 east.json、west.json，assumptions=[]；不能把缺少的 east 状态写成假设。'},
    requirements: {deliverables: ['east.json', 'west.json'], acceptance: ['east 采用原运行中问答答案，west 固定 paid；完整输入独立重算，计入零额和负数。']},
    limits: {timeoutMs, maxAttempts: 4, maxWorkers: 2}};
}
export function bindPlan({inputArtifacts, proposal}) {
  if (inputArtifacts.length !== 1 || proposal.nodes.map(node => node.id).join(',') !== 'east,west,verify') throw Error('unsupported_live_plan');
  return {nodeId: 'verify', description: policy.description,
    layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}],
      allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
      inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
    deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
}
// Original verifier layout contains exactly delivery branches. The fixed
// business input instead travels in the bounded command frame, but only after
// matching the original ticket's uploaded object identity/bytes/digest.
export function verificationRequest({ticket}) {
  const refs = ticket.input.inputArtifacts, bytes = encode(data);
  if (!Array.isArray(refs) || refs.length !== 1 || refs[0].kind !== 'input' || refs[0].name !== 'sales.json' ||
      refs[0].digest !== digest(bytes) || refs[0].bytes !== bytes.length) throw Error('original_input_mismatch');
  return {verification: ticket.input.verification, fileLayout: ticket.input.fileLayout, interactionRefs: ticket.input.interactionRefs,
    sales: structuredClone(data), source: structuredClone(refs[0])};
}
export function expectedReports(answer) {
  if (!choices.includes(answer)) throw Error('invalid_business_answer');
  return ['east', 'west'].map(region => {const status = region === 'east' ? answer : 'paid';
    const rows = data.rows.filter(row => row.region === region && row.status === status);
    return {region, status, count: rows.length, netCents: rows.reduce((sum, row) => sum + row.cents, 0)};});
}
export function answerFromRefs(refs, verification, planDigest) {
  if (!Array.isArray(refs) || refs.length !== 1) throw Error('question_proof_missing');
  const ref = refs[0], q = ref.question, a = ref.answer;
  if (!q || !a || q.profile !== 'task-runtime-question/v1' || q.nodeId !== 'east' || q.planDigest !== planDigest ||
      q.policyDigest !== questionPolicyDigest || !validQuestion(q.request) || q.sequence !== 1 ||
      !choices.includes(a.answer) || digest(encode(q)) !== ref.questionDigest || digest(encode(a)) !== ref.answerDigest ||
      a.questionDigest !== ref.questionDigest || a.taskId !== q.taskId ||
      !/^sha256:[a-f0-9]{64}$/.test(ref.dispatchDigest ?? '') || !/^sha256:[a-f0-9]{64}$/.test(ref.ackDigest ?? '') ||
      verification?.manifests?.filter(item => item.nodeId === 'east' && item.workerId === q.workerId).length !== 1)
    throw Error('question_proof_mismatch');
  return a.answer;
}
export function verifyBusiness({sales, files, refs, verification, planDigest, source}) {
  if (!equal(parseJson(Buffer.from(sales)), data) || !Array.isArray(files) || files.length !== 2) throw Error('original_input_mismatch');
  if (!source || source.kind !== 'input' || source.name !== 'sales.json' || source.digest !== digest(encode(data)) || source.bytes !== encode(data).length)
    throw Error('original_input_mismatch');
  const answer = answerFromRefs(refs, verification, planDigest), expected = expectedReports(answer);
  for (let at = 0; at < 2; at++) {
    if (files[at]?.path !== ['east.json', 'west.json'][at] || typeof files[at].content !== 'string' ||
        Buffer.byteLength(files[at].content) > 4096 || !equal(parseJson(Buffer.from(files[at].content)), expected[at])) throw Error('business_report_mismatch');
  }
  return {answer, reports: expected, questionDigest: refs[0].questionDigest, answerDigest: refs[0].answerDigest};
}
export function consumeDelivery(bytes, answer, questionDigest, answerDigest) {
  if (!(bytes instanceof Uint8Array) || bytes.length > 16384 || bytes.length === 0) throw Error('delivery_limit');
  const value = parseJson(Buffer.from(bytes));
  if (!equal(value, {answer, reports: expectedReports(answer), questionDigest, answerDigest})) throw Error('delivery_business_mismatch');
  return {digest: digest(bytes), bytes: bytes.length, reports: 2};
}
