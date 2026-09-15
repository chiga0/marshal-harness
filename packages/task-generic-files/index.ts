import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createFileBusiness} from '../task-business/index.ts';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.ts';
import {createVerificationCommand} from '../task-verification-command/index.ts';
import {encode, digest} from '../task-store/store.ts';
import {bindGenericFilesPlan} from './layout.ts';
import {check, utf8, expectedFiles, equal, RULE, GUIDANCE, MAX_INPUT} from './policy.ts';
import {filePermission} from './permission.ts';
const here = name => fileURLToPath(new URL(name, import.meta.url));
export function createGenericFilesConfig({provider, executable = process.execPath}) {
  check(provider?.id && typeof provider.start === 'function' && path.isAbsolute(executable) && fs.realpathSync(executable) === fs.realpathSync(process.execPath));
  const code = digest(encode(['index.ts', 'policy.ts', 'layout.ts', 'checker.ts', 'permission.ts'].map(name => ({name, digest: digest(fs.readFileSync(here(name)))}))));
  const policy = {id: 'generic-files-check', version: '1', description: RULE + ' 源码摘要：' + code};
  const reviewPolicy = {id: 'generic-files-review', version: '1', description: RULE + ' 独立阅读原始需求、输入与完整候选；不足集中反馈，不因润色制造返工。' + code};
  let depot;
  const originalInputs = refs => {
    check(depot && refs.reduce((n, r) => n + r.bytes, 0) <= MAX_INPUT, 'generic_files_input_limit');
    for (const ref of refs) {const bytes = depot.get({digest: ref.digest, bytes: ref.bytes}); check(bytes.length === ref.bytes && digest(bytes) === ref.digest); utf8(bytes, MAX_INPUT);}
  };
  const command = createVerificationCommand({executable, checkerPath: here('checker.ts'), checkerDigest: digest(fs.readFileSync(here('checker.ts'))), policyDigest: digest(encode(policy)),
    request: ({ticket}) => ({files: expectedFiles(ticket), ...(ticket.input.leaderReplyRefs ? {leaderReplyRefs: ticket.input.leaderReplyRefs, leaderReplies: ticket.input.leaderReplies} : {}),
      ...(ticket.input.interactionRefs ? {interactionRefs: ticket.input.interactionRefs} : {})}),
    assertions: [{name: 'exact-files', validate: (actual, {ticket}) => equal(actual, expectedFiles(ticket))}],
    delivery: ({ticket}) => ({name: 'deliverables.json', mediaType: 'application/json', content: encode({profile: 'generic-files-delivery/v1',
      scope: 'independently-reviewed-files-not-external-effects', files: expectedFiles(ticket).map(ref => ({...ref,
        content: utf8(depot.get({digest: ref.digest, bytes: ref.bytes}))}))})})});
  const verification = createVerificationPort({id: policy.id, policy, start: command.start, bindPlan: args => {
    originalInputs(args.inputArtifacts); return bindGenericFilesPlan(args);
  }});
  return {providers: new Map([[provider.id, provider]]),
    leader: createLeaderPort({id: 'generic-files-leader', providerId: provider.id,
      policy: {profile: 'task-managed-leader/v1', maxCalls: 16, maxActions: 1, maxRequests: 4,
        repair: {scope: 'plan-authors', nodeIds: [], maxRounds: 1}, review: {providerId: provider.id, policyDigest: digest(encode(reviewPolicy))}, publication: null},
      prepare: ({input}) => {originalInputs(input.snapshot.task.inputArtifacts); return {prompt: RULE + '\n' + GUIDANCE + '\n' + renderLeaderPrompt(input)};}, parseDecision: parseManagedOutput}),
    review: createReviewPort({id: 'generic-files-review', providerId: provider.id, policy: reviewPolicy,
      prepare: ({input}) => ({prompt: RULE + '\n' + renderReviewPrompt(input)}), parseReport: parseManagedOutput}),
    verification, publication: null, custody: {profile: 'node-execution-custody/v1'},
    applicationOptions: {defaultLimits: {timeoutMs: 900000, maxAttempts: 32, maxWorkers: 3}, execution: {maxWorkers: 3}},
    businessFactory(context) {
      check(!depot, 'generic_files_already_active'); depot = context.depot;
      return createFileBusiness({parent: context.executionParent, depot, approvedLayout: context.approvedLayout, observeExecution: context.observeExecution,
        layoutFor: ticket => {originalInputs(ticket.input.inputArtifacts ?? []); return ticket.input.fileLayout;},
        authorize: (ticket, request) => filePermission(ticket, path.join(context.executionParent, ticket.workerId), request)});
    }};
}
