import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {bindGenericFilesPlan} from './layout.mjs';
import {RULE, GUIDANCE, check, equal, expectedFiles, deliveryFrom} from './policy.mjs';
import {filePermission} from './permission.mjs';
const here = name => fileURLToPath(new URL(name, import.meta.url));

/** Trusted deployment DI. Never accepts provider/validator configuration from a Task. */
export function createGenericConfig({provider} = {}) {
  check(provider?.id && typeof provider.start === 'function', 'generic_files_configuration');
  const code = digest(encode(['index.mjs', 'policy.mjs', 'permission.mjs', 'checker.mjs', 'layout.mjs']
    .map(file => ({file, digest: digest(fs.readFileSync(here(file)))}))));
  const policy = {id: 'generic-files-integrity', version: '1', description: RULE + ' 固定源码摘要：' + code};
  const reviewPolicy = {id: 'generic-files-content-review', version: '1', description: RULE + ' 必须独立检查原始需求和全部候选，外部效果声明不作为证据。' + code};
  let depot;
  const command = createVerificationCommand({executable: process.execPath, checkerPath: here('checker.mjs'),
    checkerDigest: digest(fs.readFileSync(here('checker.mjs'))), policyDigest: digest(encode(policy)),
    request: ({ticket}) => ({files: expectedFiles(ticket),
      ...(ticket.input.leaderReplyRefs ? {leaderReplyRefs: ticket.input.leaderReplyRefs, leaderReplies: ticket.input.leaderReplies} : {}),
      ...(ticket.input.interactionRefs ? {interactionRefs: ticket.input.interactionRefs} : {})}),
    assertions: [{name: 'exact-files', validate: (actual, {ticket}) => equal(actual, expectedFiles(ticket))}],
    delivery: ({ticket}) => ({name: 'results.json', mediaType: 'application/json', content: encode(deliveryFrom(depot, ticket))})});
  const verification = createVerificationPort({id: 'generic-files-checker', policy, bindPlan: bindGenericFilesPlan, start: command.start});
  const leaderPolicy = {profile: 'task-managed-leader/v1', maxCalls: 12, maxActions: 1, maxRequests: 4,
    repair: {nodeIds: [], maxRounds: 0}, review: {providerId: provider.id, policyDigest: digest(encode(reviewPolicy))}, publication: null};
  const prepare = render => ({input}) => ({prompt: RULE + '\n' + GUIDANCE + '\n' + render(input)});
  return {providers: new Map([[provider.id, provider]]),
    leader: createLeaderPort({id: 'generic-files-leader', providerId: provider.id, policy: leaderPolicy, prepare: prepare(renderLeaderPrompt), parseDecision: parseManagedOutput}),
    review: createReviewPort({id: 'generic-files-review', providerId: provider.id, policy: reviewPolicy, prepare: prepare(renderReviewPrompt), parseReport: parseManagedOutput}),
    verification, custody: {profile: 'node-execution-custody/v1'},
    applicationOptions: {defaultLimits: {timeoutMs: 600000, maxAttempts: 24, maxWorkers: 3}, execution: {maxWorkers: 3}},
    businessFactory(context) {
      check(!depot, 'generic_files_configuration_already_active'); depot = context.depot;
      return createFileBusiness({parent: context.executionParent, depot, approvedLayout: context.approvedLayout,
        observeExecution: context.observeExecution, layoutFor: ticket => ticket.input.fileLayout,
        authorize: (ticket, request) => filePermission(ticket, path.join(context.executionParent, ticket.workerId), request)});
    }};
}

/** No model call at construction; preserve native HOME/login without copying credentials. */
export function createGenericFileTeamConfig({executable, env} = {}) {
  check(typeof executable === 'string' && path.isAbsolute(executable), 'generic_files_executable');
  const environment = {PATH: process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin'};
  for (const key of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (typeof process.env[key] === 'string') environment[key] = process.env[key];
  check(path.isAbsolute(environment.HOME ?? ''), 'generic_files_native_home_missing');
  // Restrict the native core registry and known delegation/MCP surfaces before
  // the model chooses tools. Non-core tools may still exist: permission checks
  // and ACP extraScope remain mandatory, not replaced by these CLI settings.
  return {...createGenericConfig({provider: createAcpProvider({id: 'qwen', executable,
    args: ['--acp', '--approval-mode', 'default', '--core-tools', 'read_file', 'write_file', 'edit', '--exclude-tools', 'agent', 'mcp__*'], env: env ?? environment,
    custodyProfile: {id: 'qwen-native-files-v1', scope: 'inherited-process-group', eligible: true}})}),
    providerFacts: [{id: 'qwen', displayName: 'Qwen ACP', availability: 'unknown',
      coreCapabilities: ['input', 'execution-identity', 'terminal', 'artifacts', 'owned-stop'],
      enhancedCapabilities: ['acp', 'progress', 'tools', 'permissions']}]};
}
