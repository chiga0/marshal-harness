// Test-only deployment configuration for the ORIGINAL CLI, real Store, custody,
// native Pi bridge and command verifier. It never implements a Task reducer.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, encode, digest} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createPiProvider} from '../agent-provider-pi/index.mjs';
import {createFileBusiness, createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
import {createVerificationPort, createRuntimeQuestionPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1], scenario = process.env.MARSHAL_QUESTION_SCENARIO;
if (process.env.MARSHAL_QUESTION_FIXTURE !== '1' || !path.isAbsolute(root ?? '') ||
    !['positive', 'cancel', 'dispatch-crash', 'ack-crash', 'worker-before', 'worker-dispatched', 'worker-ack', 'worker-pair'].includes(scenario)) throw Error('test-only configuration');
const here = name => fileURLToPath(new URL(name, import.meta.url));
const journal = path.join(path.dirname(root), 'question-observations.jsonl'), tickets = new Map();
const v5 = process.env.MARSHAL_QUESTION_V5 === '1';
const targetCancellation = scenario.startsWith('worker-'), pair = scenario === 'worker-pair';
if (v5) {
  const write = Store.prototype.write;
  Store.prototype.write = function(owner, callback) {const result = write.call(this, owner, callback);
    if (result?.reservationDigest && result.input) tickets.set(path.join(root, 'executions', result.workerId), result);
    return result;};
}
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
}
const interrupted = ticket => ticket.input.task.intent === 'question interrupted ' + scenario;
const policy = {id: 'question-recovery-checker', version: '1', description: '东分支必须输出原用户选择，西分支保持独立输出；固定检查器按当前票据的已确认问答材料验证完整文件。'};
const proposal = {summary: '两个原生文件作者并行，只有东分支缺地区选择；独立验证后交付',
  nodes: [
    {id: 'east', role: 'author', providerId: 'pi-question-fixture', goal: '询问必要地区选择后写 output.txt，仅包含原答案', scope: ['output.txt']},
    {id: 'west', role: 'author', providerId: 'pi-question-fixture', goal: '独立写 output.txt 为 independent native candidate', scope: ['output.txt']},
    {id: 'verify', role: 'verifier', providerId: null, goal: policy.description, scope: []}],
  edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
  deliverables: ['east.txt', 'west.txt'], acceptance: [policy.description], assumptions: []};
const runtimeQuestions = createRuntimeQuestionPort({policy: {id: 'regional-choice-question', version: '1', description: '只有东分支需要明确的 north 或 south 地区输入，不扩大原图、权限或验收。'},
  nodeIds: pair ? ['east', 'west'] : ['east'], maxQuestions: pair ? 2 : 1, maxWaitMs: 30000, applies: () => true,
  validateQuestion: (request, input) => (input.node.id === 'east' || pair && input.node.id === 'west') && request.kind === 'select' &&
    request.prompt === '请确定本次业务区域' && JSON.stringify(request.options) === '["north","south"]',
  validateAnswer: answer => ['north', 'south'].includes(answer)});
function observed(handle, ticket) {
  const started = handle.started.then(value => {
    record({type: 'started', taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role, started: value}); return value;
  });
  const completion = handle.completion.then(value => {
    record({type: 'completion', taskId: ticket.taskId, workerId: ticket.workerId, status: value.status, cleanup: value.cleanup}); return value;
  });
  return {...handle, started, completion};
}
const planner = createAcpProvider({id: 'question-planner-fixture', executable: process.execPath,
  args: [here('./runtime-question-recovery.worker.fixture.mjs'), 'planner'],
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const pi = mode => createPiProvider({id: 'pi-question-fixture', executable: process.execPath,
  args: [here('../agent-provider-pi/bridge-agent.fixture.mjs'), mode], bridge: {sdkEntry: here('../agent-provider-pi/fixtures/sdk/index.mjs')},
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const native = pi('business-select'), missing = pi('business-ack-missing'), west = pi('write');
const provider = {id: native.id, custodyProfile: native.custodyProfile, runtimeQuestions: native.runtimeQuestions, start(input) {
  const ticket = tickets.get(input.cwd); assert.ok(ticket);
  const held = interrupted(ticket), east = ticket.nodeId === 'east';
  let request = input;
  if (east) {
    const original = input.questionContext; assert.ok(original);
    request = {...input, questionContext: {...original, async acknowledge(...args) {
      const result = await original.acknowledge(...args);
      record({type: 'ack-committed', taskId: ticket.taskId, workerId: ticket.workerId});
      // Real ACK transaction already won. This test-only barrier delays only
      // its response; no synthetic ACK, cleanup, result or disk queue is created.
      if (held && ['ack-crash', 'worker-ack'].includes(scenario)) await new Promise(resolve => setTimeout(resolve, 10000));
      return result;
    }}};
  }
  return observed((!east ? pair ? native : west : held && ['cancel', 'dispatch-crash', 'worker-dispatched'].includes(scenario) ? missing : native).start(request), ticket);
}};
const plannerProvider = {id: planner.id, custodyProfile: planner.custodyProfile, start(input) {
  const ticket = tickets.get(input.cwd); assert.ok(ticket);
  return observed(planner.start(v5 ? {...input, prompt: input.prompt + '\nFIXTURE_PLAN=' + JSON.stringify(proposal)} : input), ticket);
}};
const checkerPath = here('./runtime-question-recovery.worker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
  policyDigest: digest(encode(policy)), assertions: [{name: 'answer-bound-files', validate(actual, {ticket}) {
    const refs = ticket.input.interactionRefs;
    return refs.length === 1 && actual.matches === true && actual.questionDigest === refs[0].questionDigest &&
      actual.answerDigest === refs[0].answerDigest && actual.ackDigest === refs[0].ackDigest && actual.workerId === refs[0].question.workerId &&
      actual.executionId === refs[0].question.executionId && actual.region === refs[0].answer.answer;
  }}], delivery: ({prepared}) => ({name: 'answered-region.json', mediaType: 'application/json',
    content: encode({region: fs.readFileSync(path.join(prepared.cwd, 'east.txt'), 'utf8'), west: fs.readFileSync(path.join(prepared.cwd, 'west.txt'), 'utf8')})})});
function startVerification(input) {return observed(command.start(input), input.ticket);}
startVerification.custodyProfile = command.start.custodyProfile;
const verification = createVerificationPort({id: 'question-checker', policy, interactionPolicyDigests: [runtimeQuestions.policyDigest], start: startVerification,
  bindPlan({proposal: planned}) {
    const {taskId: _taskId, revision: _revision, budget: _budget, ...body} = planned;
    assert.deepEqual(body, proposal);
    return {nodeId: 'verify', description: policy.description,
      layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: ['output.txt']})).concat({nodeId: 'verify', allowedPaths: [],
        inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.txt', source: {kind: 'upstream', nodeId, path: 'output.txt'}}))}),
      deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: 'output.txt', targetPath: nodeId + '.txt'}))};
  }});
export default {custody: {profile: 'node-execution-custody/v1'}, runtimeQuestions, verification,
  ...(targetCancellation ? {workerCancellation: {profile: 'task-worker-cancellation/v1'}} : {}),
  ...(v5 ? {unpermitted: {profile: 'node-unpermitted-reservation/v1'}} : {}),
  providers: new Map([[plannerProvider.id, plannerProvider], [provider.id, provider]]), supervisorOptions: {intervalMs: 10},
  onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: v5 ? createStagingOnlyBusinessFactory({authorize: (_ticket, request) => {
    const allowed = request.toolCall?._meta?.toolName === 'write' && request.toolCall.rawInput?.path === 'output.txt';
    return {outcome: allowed ? {outcome: 'selected', optionId: request.options.find(option => option.kind === 'allow_once').optionId} : {outcome: 'cancelled'}};
  }}) : ({depot, executionParent, approvedLayout, observeExecution}) => {
    const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
      authorize: (ticket, request) => {
        const allowed = ticket.role === 'author' && request.toolCall?._meta?.toolName === 'write' && request.toolCall.rawInput?.path === 'output.txt';
        return {outcome: allowed ? {outcome: 'selected', optionId: request.options.find(option => option.kind === 'allow_once').optionId} : {outcome: 'cancelled'}};
      }});
    return {...business, async prepare(ticket, context) {
      const prepared = await business.prepare(ticket, context); tickets.set(prepared.cwd, ticket);
      // The deterministic planner consumes the actual visible fixed proposal,
      // not an imported private answer or a fixture-side state transition.
      return ticket.role === 'planner' ? {...prepared, prompt: prepared.prompt + '\nFIXTURE_PLAN=' + JSON.stringify(proposal)} : prepared;
    }, release(ticket) {
      for (const [cwd, value] of tickets) if (value.workerId === ticket.workerId) tickets.delete(cwd);
      return business.release(ticket);
    }};
  }};
