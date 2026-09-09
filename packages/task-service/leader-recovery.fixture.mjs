// Trusted test configuration for the ORIGINAL CLI. The journal is observation
// only, never an execution/cleanup authority or a replacement reducer.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
const root = process.argv[process.argv.indexOf('--root') + 1];
assert.equal(process.env.MARSHAL_LEADER_RECOVERY_FIXTURE, '1'); assert.ok(path.isAbsolute(root ?? ''));
const here = name => fileURLToPath(new URL(name, import.meta.url)), hash = value => digest(encode(value));
const journal = path.join(path.dirname(root), 'leader-recovery-observations.jsonl'), tickets = new Map();
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
}
const reviewPolicy = {id: 'review', version: '1', description: '原始需求和两个原候选的独立只读检查'};
const leader = createLeaderPort({id: 'leader', providerId: 'test-agent', policy: {
  profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 4, maxRequests: 4,
  repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: 'test-agent', policyDigest: hash(reviewPolicy)}, publication: null},
  parseDecision: parseManagedOutput, prepare: ({ticket, input, prepared}) => {
    tickets.set(prepared.cwd, ticket); return {prompt: renderLeaderPrompt(input)};
  }});
const review = createReviewPort({id: 'review', providerId: 'test-agent', policy: reviewPolicy, parseReport: parseManagedOutput,
  prepare: ({ticket, input, prepared}) => {tickets.set(prepared.cwd, ticket); return {prompt: renderReviewPrompt(input)};}});
const native = file => createAcpProvider({id: 'test-agent', executable: process.execPath, args: [here(file)], env: {},
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const good = native('./leader-agent.fixture.mjs'), held = native('./leader-recovery-agent.fixture.mjs');
const provider = {id: good.id, custodyProfile: good.custodyProfile, start(input) {
  const ticket = tickets.get(input.cwd), hold = ticket?.executionType === 'leader' && ticket.planDigest !== null &&
    ticket.input.task.intent === 'leader recovery interrupted';
  const identity = ticket ? {taskId: ticket.taskId, workerId: ticket.workerId, executionType: ticket.executionType} : {executionType: 'agent'};
  const handle = (hold ? held : good).start({...input, onProgress: async value => {
    await input.onProgress?.(value);
    if (hold && value.tool?.id === 'leader-recovery-hold') record({type: 'prompt-held', ...identity});
  }});
  return {...handle, started: handle.started.then(value => {record({type: 'started', ...identity, started: value}); return value;}),
    completion: handle.completion.then(value => {record({type: 'completion', ...identity, status: value.status, cleanup: value.cleanup}); return value;})};
}};
const verifyPolicy = {id: 'two-requirements', version: '1', description: '固定检查器按原需求和原答复独立核对'}, checkerPath = here('./leader-checker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: hash(verifyPolicy),
  assertions: [{name: 'both-original-requirements', validate: (actual, {ticket}) => hash(actual) === hash(['east', 'west'].map(nodeId => ({
    nodeId, region: ticket.input.leaderReplies[0].answer, value: JSON.parse(ticket.input.task.context.text)[nodeId]})))}],
  delivery: ({prepared}) => ({name: 'report.json', mediaType: 'application/json', content: encode(['east', 'west'].map(id => JSON.parse(fs.readFileSync(path.join(prepared.cwd, id + '.json')))))})});
const verification = createVerificationPort({id: 'check', policy: verifyPolicy, start: command.start, bindPlan: () => ({nodeId: 'verify',
  description: '独立核对原答复和两个原始业务值', layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']}))
    .concat({nodeId: 'verify', allowedPaths: [], inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))})});
export default {leader, review, verification, providers: new Map([[provider.id, provider]]), custody: {profile: 'node-execution-custody/v1'},
  applicationOptions: {defaultLimits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}}, supervisorOptions: {intervalMs: 10},
  onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
    layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout})};
