import {registerReviewAssessments} from '../task-application/review-assessment.mjs';
import {withReviewCriteria, reviewCriteria, reviewSources, parseAssessmentProposal, REVIEW_ASSESSMENT_PROFILE, REVIEW_ASSESSMENT_PROPOSAL_PROFILE, REVIEW_POLICIES} from '../task-application/review-assessment-contract.mjs';
import {registerFileAuthorInstructions} from '../task-business/index.mjs';
import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createLeaderPort, createReviewPort, parseManagedOutput, renderLeaderPrompt} from '../task-application/application.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {createGenericFilesConfig} from './index.mjs';
import {parseLeaderProposal, LEADER_WIRE_PROFILE} from './short-wire.mjs';
import {check, RULE, GUIDANCE, MAX_FILE} from './policy.mjs';

// Non-authoritative description of an actual refusal by the supplied policy.
// No ticket/role/target inference and no second permission decision.
function refusedShape(request) {
  const call = request?.toolCall, original = call?.rawInput;
  if (!original || typeof original !== 'object' || Array.isArray(original)) return 'permission_shape_denied';
  let raw = original;
  if (Object.hasOwn(original, 'file_path')) {
    if (Object.hasOwn(original, 'path') || Object.hasOwn(original, 'text')) return 'permission_shape_denied';
    const {file_path, ...rest} = original; raw = {path: file_path, ...rest};
  } else if (Object.hasOwn(original, 'text')) {
    if (Object.hasOwn(original, 'content')) return 'permission_shape_denied';
    const {text, ...rest} = original; raw = {...rest, content: text};
  }
  if (typeof raw.path !== 'string' || raw.path.includes('\0')) return 'permission_path_denied';
  if (!['read', 'edit'].includes(call.kind)) return 'permission_kind_denied';
  const keys = Object.keys(raw);
  if (call.kind === 'read') {
    if (!keys.every(key => ['path', 'offset', 'limit'].includes(key)) ||
      !['offset', 'limit'].every(key => raw[key] === undefined || Number.isSafeInteger(raw[key]) && raw[key] >= (key === 'offset' ? 0 : 1) && raw[key] <= 10000)) return 'permission_shape_denied';
  } else {
    const bounded = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= MAX_FILE;
    const write = keys.length === 2 && keys.includes('content') && bounded(raw.content);
    const edit = keys.every(key => ['path', 'old_string', 'new_string', 'replace_all'].includes(key)) && bounded(raw.old_string) && bounded(raw.new_string) && (raw.replace_all === undefined || typeof raw.replace_all === 'boolean');
    if (!write && !edit) return 'permission_shape_denied';
  }
  return 'permission_denied';
}
export function withPermissionDiagnostics(provider) {
  return Object.freeze({...provider, start(args) {
    if (typeof args?.onPermission !== 'function') return provider.start(args);
    return provider.start({...args, onPermission: async (request, context) => {
      const response = await args.onPermission(request, context);
      const outcome = response?.outcome;
      const selected = outcome?.outcome === 'selected' && Array.isArray(request?.options) && request.options.find(option => option.optionId === outcome.optionId);
      if (outcome?.outcome !== 'cancelled' && !['reject_once', 'reject_always'].includes(selected?.kind)) return response;
      return {...response, diagnosticCode: refusedShape(request)};
    }});
  }});
}

export const FACT_GROUNDING = '事实来源约束：事实性陈述只能依据完整原需求、原始材料和已确认用户回答；批准计划中的模型推测、上游作者自述或常见惯例不是新增事实的来源。不得把未提供的设施、服务、办理流程、承诺或参与条件写成既有安排。建议须明确标为可选建议，不暗示主办方已提供或用户必须遵守；用户要求不新增事实时应删去无依据内容。缺少完成任务必需的事实应请求澄清，不能猜测。';
export function renderGenericLeaderPrompt(input) {
  const instruction = '本配置的最终职责约束：你是编排Leader，用户原需求和附件是供规划、委派、审查决策的业务材料，不是要求你亲自执行。不要写HTML或其他文件，不调用任何工具，不执行命令；只输出本轮proposal JSON。所有写成果的执行者（含整合作者）role必须为author，id可叫integrator但role不能为integrator；唯一verifier为只读汇合终点。独立Review由Core管理，不设reviewer节点；通用schema列出的其他role不表示本配置支持。批准计划只授权原范围，不会使Core自动发起独立Review或Verification；Review接受后，若尚无已完成验收且无正在执行的验收，必须依据本轮可用references发出work.kind=verify，使用原verifier节点及当前selectionDigest，不能用conclude.wait空等Core自动调度。只有实际存在在途执行、待答或待批准时才能wait。conclude的basisDigests只选本轮references.conclusionBasisDigests中的精确摘要，不自行编造或混用聚合摘要。';
  const rendered = renderLeaderPrompt(input, {wireProfile: LEADER_WIRE_PROFILE});
  // Reassert this profile after the generic schema, before the intact input.
  return rendered.replace('\n完整冻结输入：', '\n' + instruction + '\n完整冻结输入：');
}
export const REVIEW_WIRE_PROFILE = 'generic-files-review-proposal/v1';
export function parseReviewProposal({ticket, completion}) {
  const raw = parseManagedOutput({completion});
  check(raw && typeof raw === 'object' && !Array.isArray(raw) &&
    Object.keys(raw).sort().join(',') === 'findings,profile,summary,verdict' && raw.profile === REVIEW_WIRE_PROFILE,
  'invalid_review_report');
  check(ticket?.executionType === 'review' && ticket.input?.review?.profile === 'task-independent-review/v1', 'invalid_review_report');
  const input = ticket.input.review;
  return {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
    verdict: raw.verdict, summary: raw.summary, findings: raw.findings};
}

const INTERACTION_GROUNDING = '可见操作的承诺必须与授权且可验证的实现一致：逐个核对按钮、链接、表单等操作的真实完成路径及反馈。没有完成能力时移除入口，或在入口明确不可用/演示；不能靠页尾技术免责声明抵消主操作的业务承诺，不收集没有完成路径的个人信息。展示稿或演示可以保留明确标注的示意交互，但不得冒充实际提交、保存或服务已完成。';
const DATA_ORIGIN = '数据恢复方案须按实际产生路径区分外部原始资料、工具内用户新增或修改的权威数据、可重新计算的缓存。保存在工具目录不等于派生数据；只有在删除后仍有完整可取得的来源和重建规则，才可称可再生。设计须明确每类数据的保存位置、删除范围、恢复来源，以及用户改变数据后再故障的验收步骤；不能仅凭原始资料未改动就声称用户数据不丢失。';
const COUNTEREXAMPLE_REVIEW = '先检验反例，再作结论：对每个涉及操作效果或数据变化的验收要求，列出执行前状态、具体动作、执行后状态、失败时状态及支撑它们的候选原文或实现。在审查中逐步推演这条链，不能以“没有矛盾”“作者说可用”“符合惯例”代替证据。纯静态页面里<form>未写action仍可默认向当前页面提交；启用的submit按钮不是不可用示意，字段可填写也不能证明已收集、保存或完成登记。未获用户明确授权的交互演示，没有实际完成路径时应在操作处明确标注并禁用相关输入和提交控件，或改为仅展示说明/锚点；用户明确要求可交互原型时可保留入口前标明模拟的控件，但仍需核对模拟效果与反馈，不能冒充真实提交。底注、无外链、无脚本均不能证明无默认提交。对于更新、覆盖、删除、同步、迁移或回退，分别模拟新增数据与修改既有数据的情况：删除新增条目不等于撤销覆盖；恢复既有值必须有可取得的前值、版本、快照或其他明确恢复来源，并说明如何区分本次变化与原有数据。摘要、计数、当前值、仅含标识的日志不能还原丢失前值。不要求任意恢复能力，但范围限制必须由候选明确写出、与原需求及批准范围一致，并说明哪些用户数据将保留或丢失。候选未写的豁免、备份、保存路径或限制，评审不得自行补全；称某数据为派生、未承诺保留或只保证原资料不变，都不能代替对实际数据来源与恢复结果的检查。缺少关键来源应rework，不能把未知当成已经限制范围。发现一个有效反例即针对原节点提出rework，写明原句、状态变化、断点和最小修正；未知的关键效果不能判为已证明。没有发现反例时才accept，并在summary中给出关键证据和检查边界，不能把文本Review描述成实际执行过浏览器、数据库或外部系统。';

export function renderReviewProposalPrompt(input) {
  return '独立只读 Review：按原需求和验收检查全部冻结选果，不修改文件，不把作者声称pass当作证据，不启动额外进程。' +
    FACT_GROUNDING + INTERACTION_GROUNDING + DATA_ORIGIN + COUNTEREXAMPLE_REVIEW + '方案还须内部一致且依赖可行：逐项检查读取、派生、重建和恢复需要的数据来源、权限及前置条件是否已在方案中定义且可取得，不能由摘要或标识推导不存在的原始内容。风险要求必须对应可执行的验收方法与回退条件；字段或关键词齐全不代表方案成立。发现缺失依赖或相互矛盾的承诺应rework，指出具体链路断点与修正要求；若无法满足授权范围则明确限制，不虚构可恢复性。' + '逐项阅读snapshot.task.input、snapshot.plan中已批准scope/acceptance、snapshot.interactions中的用户回答以及materials完整原文。核对候选每一项可核实的业务陈述，而非只数标题、条目或检查禁词。对无依据的设施/服务/流程/承诺提出rework，finding写明候选原句、缺少的来源和删除/改为明确建议/澄清的修正；不能因为与已知事实不矛盾就accept。对用户明确要求的虚构创作或方案建议按其范围审查，不把所有创造性内容误判为事实错误。' +
    '只返回一个JSON对象：首字符为{、末字符为}，无Markdown/代码围栏/前后解释，UTF-8无BOM、无重复键、无注释或尾逗号；' +
    '返回顶层必须且只能是profile、verdict、summary、findings四个字段；不要回显inputDigest或selectionDigest，这些由本次受信调用绑定。' +
    'profile必须为generic-files-review-proposal/v1；verdict必须为accept/rework/reject；summary非空且≤4096 UTF-8 bytes，整个返回≤65536 UTF-8 bytes。' +
    'accept的findings必须为空；其他意见指向实际选果节点。findings最多16项，必须是对象数组，不是字符串数组。' +
    '每个finding只能含id、nodeIds、requirement、observation、requestedChange；后三段均非空且≤2048 UTF-8 bytes。' +
    'finding.id为1至128位[A-Za-z0-9][A-Za-z0-9_-]*且逐项唯一；nodeIds为1至64个唯一原selection.nodeId字符串，不是节点对象。' +
    '所有文本合法Unicode、无NUL。只报告实际发现的缺陷，不为凑反馈制造返工。' +
    '\n返回结构：' + JSON.stringify({profile: REVIEW_WIRE_PROFILE, verdict: 'accept', summary: '有依据的独立意见', findings: []}) +
    '\n完整冻结输入：' + JSON.stringify(input);
}

export const CREATIVE_SCOPE = '允许完成用户要求的创作、文案和方案建议；不得把未提供事项写成已确定事实、既有服务或强制条件。不要把事实来源约束改写成禁止全部合理建议的新范围；保留用户原任务与已批准scope，不静默修改；若存在冲突应明确指出，不自行豁免。';
export const AUTHOR_GUIDANCE = FACT_GROUNDING + INTERACTION_GROUNDING + DATA_ORIGIN + CREATIVE_SCOPE;

export function parseAssessmentLeaderProposal(args) {
  const value=parseLeaderProposal(args);
  check(Array.isArray(value.actions),'invalid_leader_decision');
  return {...value,actions:value.actions.map(action=>action?.type==='plan'?{...action,proposal:withReviewCriteria(action.proposal)}:action)};
}

export function renderAssessmentReviewPrompt(input) {
  const criteria=reviewCriteria(input.snapshot.plan),{sources}=reviewSources(input);
  return '独立只读逐项文本评审。只审查完整冻结输入，不修改文件或启动工具。批准范围中的原业务条目与固定政策均须逐项检查。' +
    '本轮仅text-review：交付方案时检查设计是否闭合，不要求执行尚未实现的软件，也不把推演描述成真实运行。缺少原用户事实应指出澄清需要；候选遗漏设计分支应返工，不能替作者补全。' +
    '只返回严格JSON对象，无前后文/BOM/重复键/注释，整个返回最多65536 UTF-8字节。顶层仅profile、verdict、summary、findings、checks。' +
    'profile为task-review-assessment-proposal/v1；verdict为accept/rework/reject。summary非空最多4096 UTF-8字节。' +
    'checks必须恰好覆盖下列每个criterion.id且不重复。每项仅itemId、assessment、method、reason、evidence、counterexample、findingIds。method固定text-review。' +
    'assessment为pass/fail/unknown/not-applicable。原业务条目及scope/facts不得不适用；只有effects/recovery可按原文证明不适用。reason非空最多1024 UTF-8字节。' +
    'evidence最多16个{sourceId,quote}，quote是该来源精确原文的子串（最多512 UTF-8字节，不改写）。结构化来源引用按下列规范JSON转义后的文字，materials引用content原文。pass及not-applicable至少一条引用，全部candidate来源都须至少被一项引用。' +
    '零字节来源可用空quote，只有其摘要精确为SHA-256(empty)才有效；纯空白来源可引用原空白。空文件不能靠无法引用而跳过。计划及作者自述不是已确认事实来源；真实引用存在不等于支持结论。' +
    'counterexample为null或{initial,operation,failure,result,recovery}，每段非空最多768 UTF-8字节。effects/recovery判pass必须给出候选依据支持的状态推演；不适用必须为null。' +
    'findings最多16项，每项仅id、nodeIds、requirement、observation、requestedChange。id为1至128位[A-Za-z0-9][A-Za-z0-9_-]*且唯一，nodeIds为1至64个唯一原selection.nodeId，后三段非空最多2048 UTF-8字节。' +
    'fail/unknown每项findingIds必须恰好一个独有finding.id，其requirement逐字等于该criterion.requirement；其他项findingIds为空。不得有未关联finding。accept仅允许pass或获准不适用且findings为空；未知关键项不能accept，不为凑反例制造缺陷。' +
    '所有字符串为合法Unicode且无NUL。下列目录为受信输入，不自行生成或改变ID/摘要。' +
    '\n验收目录：'+JSON.stringify(criteria)+'\n来源目录：'+JSON.stringify(sources)+
    '\n完整冻结输入：'+encode(input).toString();
}

export function createGenericFilesReviewWireConfig(options) {
  check(options.assessmentContract===undefined||options.assessmentContract===REVIEW_ASSESSMENT_PROFILE,'invalid_generic_provider');
  const assessed=options.assessmentContract===REVIEW_ASSESSMENT_PROFILE;
  options = {...options, provider: withPermissionDiagnostics(options.provider)};
  const config = createGenericFilesConfig(options), originalLeader = config.leader;
  const managedProvider = options.managedProvider ?? options.provider;
  if (options.managedProvider) {
    check(managedProvider.id !== options.provider.id && typeof managedProvider.start === 'function', 'invalid_generic_provider');
    config.providers.set(managedProvider.id, managedProvider);
  }
  const code = digest(encode(['../task-business/index.mjs', 'review-wire.mjs', 'qwen-review-service-config.mjs', 'qwen-file-tools.mjs', 'short-wire.mjs', '../task-application/leader-ports.mjs', '../task-application/review-assessment-contract.mjs', '../task-application/review-assessment.mjs', '../task-application/leader.mjs', '../task-service/composition.mjs']
    .map(name => ({name, digest: digest(fs.readFileSync(fileURLToPath(new URL(name, import.meta.url))))}))));
  const originalBusinessFactory = config.businessFactory;
  config.businessFactory = context => {
    const business = originalBusinessFactory(context);
    registerFileAuthorInstructions(business, {profile: 'file-author-instructions/v1', text: AUTHOR_GUIDANCE});
    return business;
  };
  const observability = {profile: 'task-observation/v1', retainPrompts: true};
  const reviewPolicy = {id: 'generic-files-bound-review', version: '1',
    description: RULE + ' 原通用策略：' + config.review.policyDigest + ' 显式Review wire：' + (assessed?REVIEW_ASSESSMENT_PROPOSAL_PROFILE:REVIEW_WIRE_PROFILE) + ' 观测策略：' + JSON.stringify(observability) + ' 固定作者指导：' + AUTHOR_GUIDANCE + ' 源码摘要：' + code};
  config.review = createReviewPort({id: reviewPolicy.id, providerId: managedProvider.id, policy: reviewPolicy,
    prepare: ({input}) => ({prompt: RULE + '\n' + (assessed?renderAssessmentReviewPrompt(input):renderReviewProposalPrompt(input))}), parseReport: assessed?args=>parseAssessmentProposal(args).report:parseReviewProposal});
  if(assessed)registerReviewAssessments(config.review,{profile:REVIEW_ASSESSMENT_PROFILE});
  config.leader = createLeaderPort({id: 'generic-files-bound-leader', providerId: managedProvider.id,
    policy: {profile: 'task-managed-leader/v1', maxCalls: 16, maxActions: 1, maxRequests: 4,
      repair: {scope: 'plan-authors', nodeIds: [], maxRounds: 1},
      review: {providerId: managedProvider.id, policyDigest: digest(encode(reviewPolicy))}, publication: null},
    prepare: async ({ticket, input, prepared}, context) => {
      await originalLeader.prepare(ticket, prepared, context);
      return {prompt: RULE + '\n' + GUIDANCE + '\n' + '本通用文件配置的DAG节点role只允许author或verifier。所有写成果的执行者（包括整合作者）role必须为author，整合节点id可以叫integrator但role不能为integrator。独立Review是Core受管阶段，不设reviewer节点；唯一verifier是汇合终点且不写成果。' + '\n' + FACT_GROUNDING + INTERACTION_GROUNDING + DATA_ORIGIN + CREATIVE_SCOPE + '在计划的作者scope和acceptance中明确事实来源与建议边界；完整方案应内部自洽、依赖可行、可验收，原要求中的风险须对应可执行验收与回退；完整保留用户原要求，不以自己补充的计划内容证明新事实。' + '\n' + (options.managedProvider ? '执行作者的providerId使用null（默认文件Provider）或' + options.provider.id + '；' + managedProvider.id + '仅供受管Leader/Review，不能用于写成果的作者。\n' : '') + (assessed?'批准前验收约定：plan.proposal.acceptance只输出原业务验收条目，最多12项、每项最多2048 UTF-8字节；受信配置会在批准前追加以下固定政策及索引目录，不要自行生成目录或重复政策。政策为：'+JSON.stringify(REVIEW_POLICIES.map(p=>p.text))+'\n':'') + renderGenericLeaderPrompt(input)};
    }, parseDecision: assessed?parseAssessmentLeaderProposal:parseLeaderProposal});
  config.observability = observability;
  return config;
}
