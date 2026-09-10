import {supportsNode} from '../task-store/runtime.mjs';
// Trusted external fixture configuration. Validate the original package pins
// before importing ANY installed production module. No source Core imports.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {verify} from './index.mjs';
assert.equal(process.env.MARSHAL_INSTALLED_V7, '1');
assert.ok(supportsNode());
const installed = process.env.MARSHAL_CANDIDATE_ROOT, sourceHead = process.env.MARSHAL_CANDIDATE_SOURCE;
const report = verify({root: installed, manifestDigest: process.env.MARSHAL_CANDIDATE_MANIFEST});
assert.equal(report.sourceHead, sourceHead);
const load = relative => import(pathToFileURL(path.join(installed, relative)).href), here = relative => fileURLToPath(new URL(relative, import.meta.url));
const [{encode, digest}, {createAcpProvider}, {createFileBusiness}, core, {createVerificationCommand}, {createLocalReportPublication}] = await Promise.all([
  load('packages/task-store/store.mjs'), load('packages/agent-provider-acp/index.mjs'), load('packages/task-business/index.mjs'),
  load('packages/task-application/application.mjs'), load('packages/task-verification-command/index.mjs'), load('packages/task-publication-report/index.mjs')]);
const hash = value => digest(encode(value)), journal = process.env.MARSHAL_V7_JOURNAL, byCwd = new Map();
assert.ok(path.isAbsolute(journal));
const record = value => {
  const fd = fs.openSync(journal, fs.constants.O_APPEND | fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try {assert.ok(fs.fstatSync(fd).size < 1024 * 1024); fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
};
const identity = ticket => ({taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role,
  executionType: ticket.executionType, reservationDigest: ticket.reservationDigest, inputDigest: ticket.inputDigest, planDigest: ticket.planDigest});
function observed(ticket, handle) {
  return {...handle, started: handle.started.then(value => {record({type: 'started', ...identity(ticket), value}); return value;}),
    completion: handle.completion.then(value => {record({type: 'finished', ...identity(ticket), status: value.status,
      reason: value.reason ?? null, cleanup: value.cleanup}); return value;})};
}
const native = createAcpProvider({id: 'test-agent', executable: process.execPath, args: [here('./v7-agent.fixture.mjs')],
  env: {MARSHAL_V7_BARRIER: process.env.MARSHAL_V7_BARRIER}, custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const provider = {...native, start(input) {
  const ticket = byCwd.get(input.cwd); assert.ok(ticket);
  const required = ticket.executionType === 'leader' ? [ticket.input.leader] : ticket.executionType === 'review' ? [ticket.input.review] :
    [ticket.input.plan, ticket.input.node, ticket.input.leaderReplies];
  assert.ok([ticket.input.task, ...required].every(value => value !== undefined && input.prompt.includes(JSON.stringify(value))), 'full original work package omitted');
  record({type: 'work-package', ...identity(ticket), coverage: 'original-task-and-required-context', observed: 'handed-off', agentConsumptionProven: false,
    taskDigest: hash(ticket.input.task), promptDigest: digest(Buffer.from(input.prompt)), promptBytes: Buffer.byteLength(input.prompt)});
  return observed(ticket, native.start(input));
}};
const publicationOriginal = createLocalReportPublication({id: 'reports', root: process.env.MARSHAL_V7_REPORT_ROOT,
  readBaseURL: process.env.MARSHAL_V7_REPORT_URL, policy: {profile: 'task-local-json-report/v1', id: 'report-policy', version: '1'}});
const publication = {...publicationOriginal, start: input => observed(input.ticket, publicationOriginal.start(input)),
  postverify: {...publicationOriginal.postverify, start: input => observed(input.ticket, publicationOriginal.postverify.start(input))}};
const reviewPolicy = {id: 'review', version: '1', description: '独立检查原地区、两份原始数值和完整材料'},
  leaderPolicy = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 4, maxRequests: 4,
    repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: provider.id, policyDigest: hash(reviewPolicy)},
    publication: {targetId: publication.id, policyDigest: publication.policyDigest}},
  verifyPolicy = {id: 'two-requirements', version: '1', description: '按原Task业务输入和原回答独立核对'};
const leader = core.createLeaderPort({id: 'leader', providerId: provider.id, policy: leaderPolicy, parseDecision: core.parseManagedOutput,
  prepare: ({ticket, input, prepared}) => {byCwd.set(prepared.cwd, ticket); return {prompt: core.renderLeaderPrompt(input)};}});
const review = core.createReviewPort({id: 'review', providerId: provider.id, policy: reviewPolicy, parseReport: core.parseManagedOutput,
  prepare: ({ticket, input, prepared}) => {byCwd.set(prepared.cwd, ticket); return {prompt: core.renderReviewPrompt(input)};}});
function expected(ticket) {
  const {leaderReplies, leaderReplyRefs, task} = ticket.input;
  assert.equal(leaderReplies.length, 1); assert.equal(leaderReplyRefs.length, 1);
  const {answer, ...ref} = leaderReplies[0]; assert.deepEqual(ref, leaderReplyRefs[0]);
  return ['east', 'west'].map(nodeId => ({nodeId, region: answer, value: JSON.parse(task.context.text)[nodeId]}));
}
const bindPlan = () => ({nodeId: 'verify', description: '原地区和两个不同业务值都须独立检查',
  layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))});
const checkerPath = here('./v7-checker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: hash(verifyPolicy),
  assertions: [{name: 'both-original-requirements', validate: (actual, {ticket}) => hash(actual) === hash(expected(ticket))}],
  delivery: ({prepared}) => ({name: 'report.json', mediaType: 'application/json', content: encode(['east', 'west'].map(id => JSON.parse(fs.readFileSync(path.join(prepared.cwd, id + '.json')))))})});
const start = input => observed(input.ticket, command.start(input));
Object.defineProperty(start, 'custodyProfile', {value: command.start.custodyProfile});
const verification = core.createVerificationPort({id: 'check', policy: verifyPolicy, bindPlan, start, publicationExpected: ({ticket}) => expected(ticket)});
record({type: 'configuration', sourceHead, manifestDigest: report.manifestDigest, publicationConfigurationDigest: publication.configurationDigest,
  publicationConfiguration: publication.configuration, leaderPolicyDigest: hash(leaderPolicy), reviewPolicyDigest: hash(reviewPolicy), verificationPolicyDigest: hash(verifyPolicy)});
export default {providers: new Map([[provider.id, provider]]), custody: {profile: 'node-execution-custody/v1'}, leader, review, publication, verification,
  applicationOptions: {defaultLimits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}},
  supervisorOptions: {intervalMs: 10}, dispose: () => publicationOriginal.close(), onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
    // Preserve the original WeakSet-qualified object and methods. The trusted
    // layout callback observes the original ticket; it does not wrap prepare.
    return createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => {byCwd.set(path.join(executionParent, ticket.workerId), ticket);
        return ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout;}});
  }};
