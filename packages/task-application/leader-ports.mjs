import {encode, digest} from '../task-store/store.mjs';
import {clone, isText, reject} from './model.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';

export const LEADER_PROFILE = 'task-managed-leader/v1';
export const REVIEW_PROFILE = 'task-independent-review/v1';
const ports = new WeakMap(), receipts = new WeakMap();
export const hash = value => digest(encode(value));
export const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
export const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
export const closed = (value, names) => value !== null && typeof value === 'object' &&
  [null, Object.prototype].includes(Object.getPrototypeOf(value)) && Object.keys(value).length === names.length &&
  names.every(name => Object.hasOwn(value, name));
export const check = (value, code = 'unsupported_task') => {if (!value) reject(code, 422);};
const distinct = (values, max, test = id) => Array.isArray(values) && values.length <= max && values.every(test) && new Set(values).size === values.length;
const integer = (value, min, max) => Number.isSafeInteger(value) && value >= min && value <= max;

export function leaderPolicy(policy) {
  check(closed(policy, ['profile', 'maxCalls', 'maxActions', 'maxRequests', 'repair', 'review', 'publication']) &&
    policy.profile === LEADER_PROFILE && integer(policy.maxCalls, 1, 32) && integer(policy.maxActions, 1, 4) &&
    integer(policy.maxRequests, 1, 16) && closed(policy.repair, ['nodeIds', 'maxRounds']) &&
    distinct(policy.repair.nodeIds, 64) && integer(policy.repair.maxRounds, 0, 3) &&
    closed(policy.review, ['providerId', 'policyDigest']) && id(policy.review.providerId) && sha(policy.review.policyDigest) &&
    (policy.publication === null || closed(policy.publication, ['targetId', 'policyDigest']) &&
      id(policy.publication.targetId) && sha(policy.publication.policyDigest)), 'invalid_leader_config');
  return clone(policy);
}
function reviewPolicy(policy) {
  check(closed(policy, ['id', 'version', 'description']) && isText(policy.id, 128) && isText(policy.version, 128) &&
    isText(policy.description, 4096), 'invalid_leader_config');
  return clone(policy);
}
function actions(value, policy) {
  check(Array.isArray(value) && value.length >= 1 && value.length <= policy.maxActions, 'invalid_leader_decision');
  let asks = 0;
  for (const action of value) {
    check(action && typeof action === 'object', 'invalid_leader_decision');
    const names = {ask: ['type', 'kind', 'prompt', 'options', 'subject', 'nodeIds'], plan: ['type', 'proposal'],
      work: ['type', 'kind', 'nodeIds', 'selectionDigest'], repair: ['type', 'nodeIds', 'basis', 'feedback'],
      deliver: ['type', 'artifactId', 'acceptanceDigest', 'reviewDigest'], conclude: ['type', 'outcome', 'summary', 'basisDigests']}[action.type];
    check(names && closed(action, names), 'invalid_leader_decision');
    if (['ask', 'plan', 'conclude'].includes(action.type)) check(value.length === 1, 'invalid_leader_decision');
    if (action.type === 'ask') {
      check(++asks <= 1 && ['business', 'publication'].includes(action.kind) && isText(action.prompt, 4096) && sha(action.subject) &&
        distinct(action.nodeIds, 64) && Array.isArray(action.options) && action.options.length <= 16 &&
        action.options.every(option => closed(option, ['value', 'label']) && isText(option.value, 256) && isText(option.label, 1024)) &&
        new Set(action.options.map(option => option.value)).size === action.options.length, 'invalid_leader_decision');
    } else if (action.type === 'work') check(['execute', 'review', 'verify'].includes(action.kind) &&
      distinct(action.nodeIds, 64) && action.nodeIds.length > 0 && sha(action.selectionDigest), 'invalid_leader_decision');
    else if (action.type === 'repair') check(distinct(action.nodeIds, 64) && action.nodeIds.length > 0 &&
      closed(action.basis, ['kind', 'digest']) && ['review', 'content-rejection', 'execution-failure'].includes(action.basis.kind) &&
      sha(action.basis.digest) && isText(action.feedback, 8192), 'invalid_leader_decision');
    else if (action.type === 'deliver') check(id(action.artifactId) && sha(action.acceptanceDigest) && sha(action.reviewDigest), 'invalid_leader_decision');
    else if (action.type === 'conclude') check(['wait', 'succeeded', 'failed'].includes(action.outcome) && isText(action.summary, 4096) &&
      distinct(action.basisDigests, 64, sha), 'invalid_leader_decision');
  }
  const kinds = value.filter(action => action.type !== 'work' || action.kind !== 'execute').map(action => action.type === 'work' ? action.kind : action.type);
  check(new Set(kinds).size === kinds.length, 'invalid_leader_decision');
  check(value.filter(action => ['repair', 'deliver'].includes(action.type) || action.type === 'work' && ['review', 'verify'].includes(action.kind)).length <= 1,
    'invalid_leader_decision');
}
function parse(type, config, ticket, completion) {
  parseManagedOutput({completion}); // Reject duplicate keys/BOM before a trusted field mapper can discard them.
  const value = config.parse({ticket: clone(ticket), completion: clone(completion)});
  check(!value || typeof value.then !== 'function', 'invalid_leader_result');
  check(encode(value).length <= 65536, 'invalid_leader_result');
  if (type === 'leader') {
    check(closed(value, ['profile', 'callId', 'inputDigest', 'summary', 'actions']) && value.profile === LEADER_PROFILE &&
      value.callId === ticket.input.leader.callId && value.inputDigest === ticket.input.leader.inputDigest && isText(value.summary, 4096), 'invalid_leader_decision');
    actions(value.actions, config.policy);
  } else {
    const input = ticket.input.review;
    check(closed(value, ['profile', 'inputDigest', 'selectionDigest', 'verdict', 'summary', 'findings']) && value.profile === REVIEW_PROFILE &&
      value.inputDigest === input.inputDigest && value.selectionDigest === input.selectionDigest &&
      ['accept', 'rework', 'reject'].includes(value.verdict) && isText(value.summary, 4096) && Array.isArray(value.findings) &&
      value.findings.length <= 16 && (value.verdict !== 'accept' || value.findings.length === 0), 'invalid_review_report');
    const selected = new Set(input.selection.map(item => item.nodeId));
    check(value.findings.every(finding => closed(finding, ['id', 'nodeIds', 'requirement', 'observation', 'requestedChange']) && id(finding.id) &&
      distinct(finding.nodeIds, 64) && finding.nodeIds.length && finding.nodeIds.every(nodeId => selected.has(nodeId)) &&
      ['requirement', 'observation', 'requestedChange'].every(key => isText(finding[key], 2048))) &&
      new Set(value.findings.map(finding => finding.id)).size === value.findings.length, 'invalid_review_report');
  }
  return clone(value);
}
function create(type, {id: identifier, providerId, policy, prepare, parseDecision, parseReport}) {
  check(id(identifier) && id(providerId) && typeof prepare === 'function' && typeof (type === 'leader' ? parseDecision : parseReport) === 'function', 'invalid_leader_config');
  const config = {type, policy: type === 'leader' ? leaderPolicy(policy) : reviewPolicy(policy),
    prepare, parse: type === 'leader' ? parseDecision : parseReport};
  const port = Object.freeze({id: identifier, providerId, profile: type === 'leader' ? LEADER_PROFILE : REVIEW_PROFILE,
    policyDigest: hash(config.policy),
    async prepare(ticket, prepared, context) {
      const result = await prepare({ticket: clone(ticket), input: clone(ticket.input[type]), prepared: {...prepared}}, context);
      check(closed(result, ['prompt']) && isText(result.prompt, 262144), 'invalid_leader_result');
      return {...prepared, prompt: result.prompt};
    },
    start({ticket, prepared, provider, executionContext, onProgress}) {
      check(provider?.id === providerId && typeof provider.start === 'function' && ticket.executionType === type &&
        ticket.providerId === providerId, 'invalid_leader_result');
      const binding = hash(ticket), handle = provider.start({...prepared, deadline: ticket.deadline, executionContext, onProgress});
      check(handle && typeof handle.stop === 'function' && typeof handle.started?.then === 'function' && typeof handle.completion?.then === 'function', 'invalid_leader_result');
      return Object.freeze({started: handle.started, stop: (...args) => handle.stop(...args),
        completion: Promise.resolve(handle.completion).then(raw => {
          check(raw?.providerId === providerId && ['completed', 'failed', 'cancelled', 'unknown'].includes(raw.status), 'invalid_leader_result');
          let value = null, reason = null;
          if (raw.status === 'completed' && raw.stopReason === 'end_turn' && raw.cleanup?.cleaned === true && raw.cleanup.started !== null) {
            try {value = parse(type, config, ticket, raw);} catch {reason = type === 'leader' ? 'invalid_leader_decision' : 'invalid_review_report';}
          }
          const data = {type, status: value ? 'completed' : 'failed', cleanup: clone(raw.cleanup), value, reason};
          const receipt = Object.freeze(Object.create(null)); receipts.set(receipt, {port, binding, data});
          return Object.freeze({type, status: data.status, cleanup: clone(data.cleanup), receipt});
        })});
    }});
  ports.set(port, config); return port;
}
export const createLeaderPort = config => create('leader', config);
export const createReviewPort = config => create('review', config);
// The fixed effect implementation supplies original runtime observations. This
// private wrapper binds those observations to the current Core ticket; no HTTP,
// model or returned JSON can mint/clone the capability.
export function createEffectPort(type, native) {
  check(['publication', 'postverify'].includes(type) && id(native?.id) && typeof native.start === 'function', 'invalid_leader_config');
  const port = Object.freeze({id: native.id, custodyProfile: clone(native.custodyProfile), start(options) {
    const binding = hash(options.ticket), handle = native.start(options);
    check(handle && typeof handle.stop === 'function' && typeof handle.started?.then === 'function' && typeof handle.completion?.then === 'function', 'invalid_leader_result');
    return Object.freeze({started: handle.started, stop: (...args) => handle.stop(...args), completion: Promise.resolve(handle.completion).then(raw => {
      check(raw && (type === 'publication' ? raw.type === type && ['created', 'matched', 'failed', 'unknown'].includes(raw.status) :
        raw.type === 'verification' && ['passed', 'failed'].includes(raw.status)), 'invalid_leader_result');
      const data = {type, status: ['created', 'matched', 'passed'].includes(raw.status) ? 'completed' : raw.status === 'unknown' ? 'unknown' : 'failed',
        cleanup: clone(raw.cleanup), value: clone(raw)};
      const receipt = Object.freeze(Object.create(null)); receipts.set(receipt, {port, binding, data});
      return Object.freeze({type, status: data.status, cleanup: clone(data.cleanup), receipt});
    })});
  }}); ports.set(port, {type}); return port;
}
export function parseManagedOutput({completion}) {
  check(isText(completion?.outputText, 65536), 'invalid_leader_result');
  check(!completion.outputText.includes('\uFEFF'), 'invalid_leader_result');
  let text = completion.outputText.replace(/^[\x20\t\r\n]+|[\x20\t\r\n]+$/g, '');
  const fenced = /^```(?:json)?\r?\n([\s\S]*)\r?\n```$/.exec(text);
  if (fenced) text = fenced[1];
  return parseJson(Buffer.from(text));
}
const proposal = {summary: '完成原需求', nodes: [{id: 'author', role: 'author', goal: '原业务目标', scope: ['业务范围说明'], providerId: null},
  {id: 'verify', role: 'verifier', goal: '独立客观验收', scope: [], providerId: null}], edges: [{from: 'author', to: 'verify'}],
  deliverables: ['原需求成果'], acceptance: ['原需求验收'], assumptions: []};
export function renderLeaderPrompt(input) {
  const shapes = [{type: 'ask', kind: 'business', prompt: '原需求缺项', options: [], subject: '原缺项摘要', nodeIds: []},
    {type: 'plan', proposal}, {type: 'work', kind: 'review', nodeIds: ['原节点'], selectionDigest: '原选果摘要'},
    {type: 'repair', nodeIds: ['原节点'], basis: {kind: 'review', digest: '原独立意见摘要'}, feedback: '精确修正意见'},
    {type: 'deliver', artifactId: '原delivery标识', acceptanceDigest: '原验收摘要', reviewDigest: '原Review摘要'},
    {type: 'conclude', outcome: 'succeeded', summary: '基于原证据说明完成', basisDigests: ['原证据摘要']}];
  return '你是受管 Leader，只决定原任务的业务推进，不能启动进程、写文件、批准计划或提升权限。只返回一个 JSON 对象，无 Markdown。' +
    '回显 profile/callId/inputDigest，summary≤4096 UTF-8 bytes，actions 为1至 policy.maxActions项。下列是字段类型示例，不是已批准任务或业务答案。' +
    'ask、plan、conclude必须独占该决定；work.kind为execute/review/verify；ask.kind为business/publication；conclude.outcome为wait/succeeded/failed；' +
    'repair.basis.kind为review/content-rejection/execution-failure。所有摘要必须来自完整输入的原事实，不发明。' +
    'plan.proposal的scope必须为字符串数组，不是权限对象；可选budget={timeoutMs,maxAttempts,maxWorkers}只能减少原限额。' +
    '只在真实信息缺失时ask；独立Review先于原客观Verification；阶段验收通过不是任务完成，仍需deliver及最终conclude。' +
    '\n字段示例：' + JSON.stringify({profile: LEADER_PROFILE, callId: input.callId, inputDigest: input.inputDigest, summary: '业务理由', actions: shapes}) +
    '\n完整冻结输入：' + JSON.stringify(input);
}
export function renderReviewPrompt(input) {
  return '独立只读 Review：按原需求和验收检查全部冻结选果，不修改文件，不把作者声称pass当作证据，不启动额外进程。' +
    '只返回一个JSON对象，verdict为accept/rework/reject；accept的findings必须为空，其余意见必须指向实际选果节点。' +
    '每个finding包含唯一id、nodeIds以及每段≤2048 UTF-8 bytes的requirement/observation/requestedChange；最多16项。' +
    '\n返回结构：' + JSON.stringify({profile: REVIEW_PROFILE, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
      verdict: 'accept', summary: '有依据的独立意见', findings: []}) + '\n完整冻结输入：' + JSON.stringify(input);
}
export function configuration(port, type) {
  const config = ports.get(port); check(config?.type === type, 'invalid_leader_config');
  return {id: port.id, providerId: port.providerId, policy: clone(config.policy), policyDigest: port.policyDigest};
}
export function receipt(port, ticket, result) {
  const value = receipts.get(result?.receipt);
  check(ports.has(port) && value?.port === port && value.binding === hash(ticket) && result.type === value.data.type &&
    result.status === value.data.status && hash(result.cleanup) === hash(value.data.cleanup), 'invalid_leader_receipt');
  return value.data;
}
