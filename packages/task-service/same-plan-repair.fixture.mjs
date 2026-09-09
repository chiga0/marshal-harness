// Test-only ORIGINAL service CLI composition. No alternate reducer, forged
// receipt/cleanup, direct SQL mutation, or real model is used by this fixture.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, encode, digest} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness, createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
import {createVerificationPort, createRepairPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {policy, bindPlan, proposal} from '../task-team-integration/scenario.fixture.mjs';
import {data} from '../task-qwen-live/driver.fixture.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1], scenario = process.env.MARSHAL_REPAIR_SCENARIO;
if (process.env.MARSHAL_REPAIR_FIXTURE !== '1' || !path.isAbsolute(root ?? '') ||
  !['positive', 'structure', 'bad-frame', 'cancel', 'before', 'after'].includes(scenario)) throw Error('test-only configuration');
const here = name => fileURLToPath(new URL(name, import.meta.url));
const journal = path.join(path.dirname(root), 'repair-observations.jsonl'), tickets = new Map();
const v5 = process.env.MARSHAL_REPAIR_V5 === '1';
const parse = row => row && JSON.parse(row.bytes.toString('utf8'));
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
}
// Transparent one-shot rendezvous around the ORIGINAL write callback/COMMIT.
// The parent may SIGKILL only its original ChildProcess. Timeout exits failed;
// it never continues a half-transaction or signals a PID loaded from disk.
let armed = ['before', 'after'].includes(scenario) && process.argv[process.argv.indexOf('--mode') + 1] === 'create';
const write = Store.prototype.write;
let v5Cut = v5 && process.env.MARSHAL_REPAIR_V5_CUT === '1';
function remember(result) {
  if (!v5 || !result?.reservationDigest || !result.input) return;
  tickets.set(path.join(root, 'executions', result.workerId), result);
  if (v5Cut && result.repairId !== null) {
    v5Cut = false; record({type: 'v5-barrier', workerId: result.workerId, taskId: result.taskId, repairId: result.repairId});
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000); process.exit(79);
  }
}
function barrier(value) {armed = false; record({type: 'barrier', point: scenario, ...value});
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000); process.exit(79);}
Store.prototype.write = function(owner, callback) {
  if (!armed) {const result = write.call(this, owner, callback); remember(result); return result;}
  let target;
  const value = write.call(this, owner, tx => {
    const result = callback(tx);
    if (armed && result?.operation?.kind === 'task.repair' && result.replayed === false) {
      const row = tx.projection('task', result.taskId), task = parse(row);
      assert.equal(task.task.status, 'queued'); assert.equal(task.attempts, 4);
      assert.equal(result.operation.status, 'accepted');
      target = {taskId: result.taskId, receipt: result, taskDigest: digest(row.bytes)};
      if (scenario === 'before') barrier(target);
    }
    return result;
  });
  if (armed && target && scenario === 'after') barrier(target);
  remember(value); return value;
};
const repair = createRepairPort({policy: {id: 'regional-local-repair-fixture', version: '1',
  description: '允许显式选择一个地区修正内容，保留未选分支；必须按原输入和完整集合重新独立验证，不增加原预算。'},
  nodeIds: ['east', 'west'], assertions: ['west-content']});
function observed(handle, ticket) {
  return {...handle, started: handle.started.then(started => {
    record({type: 'started', taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role,
      repairId: ticket.repairId, started}); return started;
  }), completion: handle.completion.then(result => {
    record({type: 'completion', taskId: ticket.taskId, workerId: ticket.workerId, status: result.status, cleanup: result.cleanup}); return result;
  })};
}
const custodyProfile = {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true};
const planner = createAcpProvider({id: 'repair-planner', executable: process.execPath,
  args: [here('./runtime-question-recovery.worker.fixture.mjs'), 'planner'], custodyProfile});
const agent = mode => createAcpProvider({id: 'repair-author', executable: process.execPath,
  args: [here('../task-team-integration/agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: mode}, custodyProfile});
const good = agent('good'), wrong = agent('corrupt'), hanging = agent('hang');
const author = {id: good.id, custodyProfile: good.custodyProfile, start(input) {
  const ticket = tickets.get(input.cwd); assert.ok(ticket);
  const native = ticket.repairId !== null && scenario === 'cancel' ? hanging :
    ticket.nodeId === 'west' && ticket.repairId === null && (!v5 || ticket.input.task.intent === 'v5 repair interruption') ? wrong : good;
  return observed(native.start(input), ticket);
}};
const planning = {id: planner.id, custodyProfile: planner.custodyProfile, start(input) {
  const ticket = tickets.get(input.cwd); assert.ok(ticket);
  return observed(planner.start(v5 ? {...input, prompt: input.prompt + '\nFIXTURE_PLAN=' + JSON.stringify(declared)} : input), ticket);
}};
const declared = proposal(); for (const node of declared.nodes) if (node.role === 'author') node.providerId = author.id;
const checkerPath = here('./same-plan-repair-checker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath,
  checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: digest(encode(policy)),
  env: {MARSHAL_REPAIR_CHECKER_MODE: scenario}, repair: {policyDigest: repair.policyDigest, assertions: ['west-content']},
  request({ticket}) {
    const refs = ticket.input.inputArtifacts; assert.equal(refs.length, 1);
    assert.equal(refs[0].digest, digest(encode(data))); assert.equal(refs[0].bytes, encode(data).length);
    return {verification: ticket.input.verification, fileLayout: ticket.input.fileLayout, sales: data, source: refs[0]};
  }, assertions: ['east', 'west'].map(region => ({name: region + '-content', validate(value, {ticket}) {
    record({type: 'parent-assertion', workerId: ticket.workerId, name: region + '-content'});
    return encode(value.actual).equals(encode(value.expected));
  }})).concat({name: 'report-structure', validate(value, {ticket}) {
    record({type: 'parent-assertion', workerId: ticket.workerId, name: 'report-structure'});
    return scenario !== 'structure' && value.complete === true;
  }}), delivery({prepared}) {
    return {name: 'repaired-sales.json', mediaType: 'application/json', content: encode({files: ['east', 'west'].map(region =>
      ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})};
  }});
function startVerification(input) {
  record({type: 'verification-input', taskId: input.ticket.taskId, workerId: input.ticket.workerId,
    repairId: input.ticket.repairId, manifests: input.ticket.input.verification.manifests, upstream: input.ticket.input.upstream});
  return observed(command.start(input), input.ticket);
}
startVerification.custodyProfile = command.start.custodyProfile;
startVerification.repairBinding = command.start.repairBinding;
const verification = createVerificationPort({id: 'repair-independent-checker', policy, bindPlan,
  repairPolicyDigests: [repair.policyDigest], start: startVerification});
export default {custody: {profile: 'node-execution-custody/v1'}, repair, verification,
  ...(v5 ? {unpermitted: {profile: 'node-unpermitted-reservation/v1'}} : {}),
  providers: new Map([[planning.id, planning], [author.id, author]]), supervisorOptions: {intervalMs: 10},
  onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: v5 ? createStagingOnlyBusinessFactory() : ({depot, executionParent, approvedLayout, observeExecution}) => {
    const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
    return {...business, async prepare(ticket, context) {
      const prepared = await business.prepare(ticket, context); tickets.set(prepared.cwd, ticket);
      record({type: 'prepared', taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId,
        repairId: ticket.repairId, cwd: prepared.cwd, inputDigest: ticket.inputDigest});
      assert.ok(ticket.repairId === null || typeof ticket.repairId === 'string');
      if (ticket.role !== 'planner' && ticket.repairId !== null) {
        assert.equal(ticket.input.repair.repairId, ticket.repairId);
        assert.equal(ticket.input.repair.feedback, '修正 west 金额，不改变 east 或原规则');
        assert.deepEqual(ticket.input.repair.failedAssertions, ['west-content']);
        assert.deepEqual(ticket.input.repair.affectedNodes, ['west', 'verify']);
        assert.ok(prepared.prompt.includes('修正 west 金额，不改变 east 或原规则'));
        // Inspect the actual original renderer output; do not append diagnostic
        // input in this wrapper and thereby conceal a missing producer field.
        const sent = JSON.parse(prepared.prompt.slice(prepared.prompt.indexOf('{"task":')));
        assert.equal(sent.repair.repairId, ticket.repairId); assert.equal(sent.repair.diagnosticOnly, true);
        assert.equal(sent.repair.originalNegativeReport.binding.planDigest, ticket.planDigest);
        assert.equal(digest(Buffer.from(sent.repair.originalNegativeReport.originalReport)), sent.repair.originalNegativeReport.reportDigest);
      }
      return ticket.role === 'planner' ? {...prepared, prompt: prepared.prompt + '\nFIXTURE_PLAN=' + JSON.stringify(declared)} : prepared;
    }, release(ticket) {for (const [cwd, value] of tickets) if (value.workerId === ticket.workerId) tickets.delete(cwd); return business.release(ticket);}};
  }};
