import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {leaderReplyDigest} from '../task-api/contract.mjs';
import {check, fields, equal, range, expected, sourceBytes, sourceRef, regions} from '../task-regional-window/policy.mjs';
export {check, fields, equal, range, expected, sourceBytes, sourceRef, regions};
export const PROFILE = 'leader-regional-window/v1';
export const RULE = '以原 sales.json 为唯一流水来源。日期为 2000–2099 年 YYYY-MM-DD UTC 日历日，起止均包含且最多366日；只计 paid，排除 cancelled；整数 cents，保留负数退款与零额笔数。分别由 east、west 两个并行 author 节点写 east.json、west.json，每份恰有 region,startDate,endDate,count,netCents 五字段；唯一 verify 节点依赖两个作者，使用受信独立 checker，不让作者自签验收。最终由独立 Review、checker 和用户对精确报告/目标的一次显式 allow 决定本地报告交付；禁止作者自行发布、执行代码或访问其它文件。';
export const GUIDANCE = '本业务只支持上述日期窗口区域销售报告，不支持任意任务。读取原 context.text 中 profile/startDate/endDate；任一日期为 null 时，先用一个 options:[] 的 business ask 请求用户回复 JSON 字符串 {"startDate":"YYYY-MM-DD","endDate":"YYYY-MM-DD"}，不得猜日期或把示例作为答案。收到原 leaderReplies 后保留原已给日期，按原回答规划上述两个互补作者与独立 verifier；请自己组织有界提案，不需要逐字复述中文。完整日期无需多问。已正确的结果直接进入独立 Review/验证/交付，不制造修正轮次。报告授权仅由 Core 的 publication request 获取，不发业务问题替代发布授权。';
export function initial(input) {
  check(typeof input?.intent === 'string' && input.context && fields(input.context, ['text', 'inputRefs']) &&
    Array.isArray(input.context.inputRefs) && input.context.inputRefs.length === 1, 'report_input_boundary');
  const value = parseJson(Buffer.from(input.context.text));
  check(fields(value, ['profile', 'startDate', 'endDate']) && value.profile === PROFILE, 'report_input_boundary');
  return range({startDate: value.startDate, endDate: value.endDate}, false);
}
export function finalWindow(ticket) {
  const original = initial(ticket.input.task), refs = ticket.input.leaderReplyRefs ?? [], replies = ticket.input.leaderReplies ?? [];
  check(refs.length === replies.length && refs.length <= 1, 'report_original_reply');
  if (original.startDate !== null && original.endDate !== null) {check(refs.length === 0, 'report_unrequested_reply'); return original;}
  check(refs.length === 1, 'report_missing_window');
  const {answer, ...ref} = replies[0];
  check(typeof answer === 'string' && equal(ref, refs[0]) && ref.replyDigest === leaderReplyDigest(ticket.taskId, ref.requestId,
    {requestDigest: ref.requestDigest, answer}), 'report_original_reply');
  const value = range(parseJson(Buffer.from(answer)));
  for (const name of ['startDate', 'endDate']) check(original[name] === null || original[name] === value[name], 'report_changed_original_date');
  return value;
}
export function originalReport(depot, ticket) {
  const window = finalWindow(ticket), bytes = sourceBytes(depot, ticket);
  return {profile: PROFILE, window, sourceDigest: sourceRef(ticket).digest, reports: expected(bytes, window)};
}
export function verificationRequest(depot, ticket) {
  return {...finalWindow(ticket), sourceDigest: sourceRef(ticket).digest, sourceBase64: sourceBytes(depot, ticket).toString('base64'),
    ...(ticket.input.leaderReplyRefs ? {leaderReplyRefs: ticket.input.leaderReplyRefs, leaderReplies: ticket.input.leaderReplies} : {}),
    ...(ticket.input.interactionRefs ? {interactionRefs: ticket.input.interactionRefs} : {})};
}
export function taskBody(inputId, window = {startDate: null, endDate: null}, {intent = '汇总指定日期窗口的东、西地区已付款流水并交付本地报告', timeoutMs = 600000} = {}) {
  check(typeof inputId === 'string' && inputId.length > 0 && Number.isSafeInteger(timeoutMs) && timeoutMs >= 60000 && timeoutMs <= 900000);
  return {intent, context: {inputRefs: [inputId], text: JSON.stringify({profile: PROFILE, ...range(window, false)})},
    requirements: {deliverables: ['east.json', 'west.json'], acceptance: [RULE, GUIDANCE]}, limits: {timeoutMs, maxAttempts: 17, maxWorkers: 3}};
}
