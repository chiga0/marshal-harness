import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createLocalReportPublication} from '../task-publication-report/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {check, equal, regions, RULE, GUIDANCE, initial, finalWindow, originalReport, verificationRequest, sourceBytes, sourceRef} from './policy.mjs';
import {filePermission} from './permission.mjs';
export {taskBody} from './policy.mjs';
const here = file => fileURLToPath(new URL(file, import.meta.url));
const checkerPath = here('../task-regional-window/checker.mjs');
/** Trusted deployment DI, not HTTP input. Returning the original FileBusiness
 * object preserves its private managed identity; no prepare wrapper/claim. */
export function createLeaderReportConfig({provider, reportRoot, readBaseURL, executable = process.execPath}) {
  check(process.versions.node === '24.15.0' && path.isAbsolute(executable) && fs.realpathSync(executable) === fs.realpathSync(process.execPath) &&
    provider?.id && typeof provider.start === 'function', 'report_configuration');
  const code = digest(encode(['./policy.mjs', './permission.mjs', './index.mjs', '../task-regional-window/policy.mjs', '../task-regional-window/checker.mjs']
    .map(file => ({file, digest: digest(fs.readFileSync(here(file)))}))));
  const policy = {id: 'leader-window-checker', version: '1', description: RULE + ' 配置及独立数据规则源码：' + code};
  const reviewPolicy = {id: 'leader-window-review', version: '1', description: RULE + ' 独立消费原流水、原日期或原回答与完整两份候选；不为润色制造 rework。 ' + code};
  const publication = createLocalReportPublication({id: 'local-window-report', root: reportRoot, readBaseURL,
    policy: {profile: 'task-local-json-report/v1', id: 'leader-window-report', version: '1'}});
  let depot;
  try {
    const bindPlan = ({taskInput, inputArtifacts, proposal}) => {
      initial(taskInput); check(inputArtifacts.length === 1 && proposal.nodes.length === 3 &&
        regions.every(id => proposal.nodes.some(node => node.id === id && node.role === 'author' && [null, provider.id].includes(node.providerId))) &&
        proposal.nodes.some(node => node.id === 'verify' && node.role === 'verifier' && node.providerId === null) && proposal.edges.length === 2 &&
        regions.every(from => proposal.edges.some(edge => edge.from === from && edge.to === 'verify')), 'report_plan_boundary');
      return {nodeId: 'verify', description: policy.description,
        layouts: regions.map(nodeId => ({nodeId, inputs: [{path: 'sales.json', source: {kind: 'input', id: inputArtifacts[0].id}}], allowedPaths: [nodeId + '.json']}))
          .concat({nodeId: 'verify', allowedPaths: [], inputs: regions.map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
        deliveries: regions.map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))};
    };
    const original = ticket => {check(depot, 'report_business_unavailable'); return originalReport(depot, ticket);};
    const command = createVerificationCommand({executable, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: digest(encode(policy)),
      request: ({ticket}) => verificationRequest(depot, ticket),
      assertions: [{name: 'input-window', validate: (actual, {ticket}) => equal(actual, {...finalWindow(ticket), sourceDigest: sourceRef(ticket).digest})},
        {name: 'regions', validate: (actual, {ticket}) => equal(actual, original(ticket).reports)}],
      delivery: ({ticket, report}) => {
        const reports = report.assertions.find(item => item.name === 'regions').actual;
        for (const [index, nodeId] of regions.entries()) {
          const manifest = ticket.input.verification.manifests.find(item => item.nodeId === nodeId)?.manifest;
          check(manifest?.files.length === 1 && manifest.files[0].path === nodeId + '.json', 'report_candidate_binding');
          const ref = manifest.files[0], bytes = depot.get({digest: ref.digest, bytes: ref.bytes});
          check(bytes.length === ref.bytes && digest(bytes) === ref.digest && equal(parseJson(bytes), reports[index]), 'report_candidate_binding');
        }
        return {name: 'regional-window.json', mediaType: 'application/json', content: encode({...original(ticket), reports})};
      }});
    const verification = createVerificationPort({id: 'leader-window-checker', policy, bindPlan, start: command.start, publicationExpected: ({ticket}) => original(ticket)});
    const leaderPolicy = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 1, maxRequests: 4,
      repair: {nodeIds: regions, maxRounds: 1}, review: {providerId: provider.id, policyDigest: digest(encode(reviewPolicy))},
      publication: {targetId: publication.id, policyDigest: publication.policyDigest}};
    const prepare = render => ({input}) => {initial(input.snapshot.task.input); return {prompt: RULE + '\n' + GUIDANCE + '\n' + render(input)};};
    return {providers: new Map([[provider.id, provider]]), leader: createLeaderPort({id: 'window-leader', providerId: provider.id,
      policy: leaderPolicy, prepare: prepare(renderLeaderPrompt), parseDecision: parseManagedOutput}),
      review: createReviewPort({id: 'window-review', providerId: provider.id, policy: reviewPolicy, prepare: prepare(renderReviewPrompt), parseReport: parseManagedOutput}),
      verification, publication, custody: {profile: 'node-execution-custody/v1'},
      applicationOptions: {defaultLimits: {timeoutMs: 600000, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}},
      businessFactory(context) {
        check(!depot, 'report_configuration_already_active'); depot = context.depot;
        return createFileBusiness({parent: context.executionParent, depot, approvedLayout: context.approvedLayout, observeExecution: context.observeExecution,
          layoutFor: ticket => {initial(ticket.input.task); finalWindow(ticket); sourceBytes(depot, ticket); return ticket.input.fileLayout;},
          authorize: (ticket, request) => filePermission(ticket, path.join(context.executionParent, ticket.workerId), request)});
      }, dispose() {publication.close();}};
  } catch (error) {publication.close(); throw error;}
}
/** Only the original Pi reads its native login/configuration. No auth copying,
 * fallback, no-tools or replacement HOME. HTTP cannot select these paths. */
export function createPiLeaderReportConfig({piEntry, sdkEntry, reportRoot, readBaseURL, node = process.execPath}) {
  check(process.versions.node === '24.15.0' && path.isAbsolute(piEntry ?? '') && path.isAbsolute(sdkEntry ?? '') &&
    fs.realpathSync(node) === fs.realpathSync(process.execPath), 'report_pi_configuration');
  const root = path.resolve(path.dirname(piEntry), '../..');
  check(piEntry === path.join(root, 'dist/bundle/cli.js') && sdkEntry === path.join(root, 'dist/index.js') &&
    fs.realpathSync(piEntry) === piEntry && fs.realpathSync(sdkEntry) === sdkEntry &&
    parseJson(fs.readFileSync(path.join(root, 'package.json'))).name === '@earendil-works/pi-coding-agent', 'report_pi_identity');
  const env = {PATH: path.dirname(node) + ':' + (process.env.PATH ?? '/usr/bin:/bin:/usr/sbin:/sbin')};
  for (const key of ['HOME', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) if (typeof process.env[key] === 'string' && !process.env[key].includes('\0')) env[key] = process.env[key];
  check(path.isAbsolute(env.HOME ?? ''), 'report_native_home_missing');
  return createLeaderReportConfig({reportRoot, readBaseURL, executable: node, provider: createPiProvider({id: 'pi', executable: node,
    args: [piEntry, '--mode', 'rpc', '--no-session'], env, bridge: {sdkEntry},
    custodyProfile: {id: 'pi-native-file-v1', scope: 'inherited-process-group', eligible: true}})});
}
