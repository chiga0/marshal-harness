import fs from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createLeaderPort, createReviewPort, parseManagedOutput, renderLeaderPrompt, renderReviewPrompt} from '../task-application/application.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {createGenericFilesConfig} from './index.mjs';
import {check, RULE, GUIDANCE} from './policy.mjs';

export const LEADER_WIRE_PROFILE = 'generic-files-leader-proposal/v1';
// Parse original bytes before inspecting the closed wire shape. Never repair an
// old envelope or action: only this invocation's trusted transport supplies IDs.
export function parseLeaderProposal({ticket, completion}) {
  const raw = parseManagedOutput({completion});
  check(raw && typeof raw === 'object' && !Array.isArray(raw) &&
    Object.keys(raw).sort().join(',') === 'actions,profile,summary' && raw.profile === LEADER_WIRE_PROFILE,
  'invalid_leader_decision');
  check(ticket?.executionType === 'leader' && ticket.input?.leader?.profile === 'task-managed-leader/v1', 'invalid_leader_decision');
  return {profile: 'task-managed-leader/v1', callId: ticket.input.leader.callId,
    inputDigest: ticket.input.leader.inputDigest, summary: raw.summary, actions: raw.actions};
}

export function createGenericFilesShortWireConfig(options) {
  const config = createGenericFilesConfig(options), originalLeader = config.leader;
  const code = digest(encode(['short-wire.mjs', 'qwen-short-service-config.mjs', '../task-application/leader-ports.mjs']
    .map(name => ({name, digest: digest(fs.readFileSync(fileURLToPath(new URL(name, import.meta.url))))}))));
  const reviewPolicy = {id: 'generic-files-short-review', version: '1',
    description: RULE + ' 原通用策略：' + config.review.policyDigest + ' 显式模型wire：' + LEADER_WIRE_PROFILE + ' 源码摘要：' + code};
  config.review = createReviewPort({id: reviewPolicy.id, providerId: options.provider.id, policy: reviewPolicy,
    prepare: ({input}) => ({prompt: RULE + '\n' + renderReviewPrompt(input)}), parseReport: parseManagedOutput});
  config.leader = createLeaderPort({id: 'generic-files-short-leader', providerId: options.provider.id,
    policy: {profile: 'task-managed-leader/v1', maxCalls: 16, maxActions: 1, maxRequests: 4,
      repair: {scope: 'plan-authors', nodeIds: [], maxRounds: 1},
      review: {providerId: options.provider.id, policyDigest: digest(encode(reviewPolicy))}, publication: null},
    prepare: async ({ticket, input, prepared}, context) => {
      // Retain the original composition's exact input/Depot validation. The
      // old prompt is discarded, not patched or handed to the provider.
      await originalLeader.prepare(ticket, prepared, context);
      return {prompt: RULE + '\n' + GUIDANCE + '\n' + renderLeaderPrompt(input, {wireProfile: LEADER_WIRE_PROFILE})};
    }, parseDecision: parseLeaderProposal});
  return config;
}
