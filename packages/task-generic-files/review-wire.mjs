import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createLeaderPort, createReviewPort, parseManagedOutput, renderLeaderPrompt} from '../task-application/application.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {createGenericFilesConfig} from './index.mjs';
import {parseLeaderProposal, LEADER_WIRE_PROFILE} from './short-wire.mjs';
import {check, RULE, GUIDANCE} from './policy.mjs';

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

export function renderReviewProposalPrompt(input) {
  return '独立只读 Review：按原需求和验收检查全部冻结选果，不修改文件，不把作者声称pass当作证据，不启动额外进程。' +
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

export function createGenericFilesReviewWireConfig(options) {
  const config = createGenericFilesConfig(options), originalLeader = config.leader;
  const code = digest(encode(['review-wire.mjs', 'qwen-review-service-config.mjs', 'short-wire.mjs', '../task-application/leader-ports.mjs']
    .map(name => ({name, digest: digest(fs.readFileSync(fileURLToPath(new URL(name, import.meta.url))))}))));
  const observability = {profile: 'task-observation/v1', retainPrompts: true};
  const reviewPolicy = {id: 'generic-files-bound-review', version: '1',
    description: RULE + ' 原通用策略：' + config.review.policyDigest + ' 显式Review wire：' + REVIEW_WIRE_PROFILE + ' 观测策略：' + JSON.stringify(observability) + ' 源码摘要：' + code};
  config.review = createReviewPort({id: reviewPolicy.id, providerId: options.provider.id, policy: reviewPolicy,
    prepare: ({input}) => ({prompt: RULE + '\n' + renderReviewProposalPrompt(input)}), parseReport: parseReviewProposal});
  config.leader = createLeaderPort({id: 'generic-files-bound-leader', providerId: options.provider.id,
    policy: {profile: 'task-managed-leader/v1', maxCalls: 16, maxActions: 1, maxRequests: 4,
      repair: {scope: 'plan-authors', nodeIds: [], maxRounds: 1},
      review: {providerId: options.provider.id, policyDigest: digest(encode(reviewPolicy))}, publication: null},
    prepare: async ({ticket, input, prepared}, context) => {
      await originalLeader.prepare(ticket, prepared, context);
      return {prompt: RULE + '\n' + GUIDANCE + '\n' + renderLeaderPrompt(input, {wireProfile: LEADER_WIRE_PROFILE})};
    }, parseDecision: parseLeaderProposal});
  config.observability = observability;
  return config;
}
