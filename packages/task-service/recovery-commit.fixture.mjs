// Test-only configuration for the original service CLI. No production hook,
// alternate reducer, forged result/cleanup or model is installed by this file.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, encode, digest} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1];
const point = process.env.MARSHAL_SERVICE_COMMIT_POINT;
if (process.env.MARSHAL_SERVICE_COMMIT_FIXTURE !== '1' || !path.isAbsolute(root ?? '') ||
    !['none', 'create-before', 'create-after', 'result-before', 'result-after'].includes(point)) throw Error('test-only configuration');
const journal = path.join(path.dirname(root), 'commit-observations.jsonl');
const intent = '事务提交恢复夹具：两个地区独立统计净销售额并验收完整报告';
const parse = row => row && JSON.parse(row.bytes.toString('utf8'));
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
let armed = point !== 'none';
function barrier(observation) {
  armed = false;
  record({type: 'barrier', point, ...observation});
  // Parent kills only its original child handle. This synchronous, finite
  // rendezvous cannot advance the event loop, return an HTTP reply or let an
  // uncommitted callback escape. Timeout is a failing exit, never continuation.
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000);
  process.exit(79);
}
const write = Store.prototype.write;
Store.prototype.write = function(owner, callback) {
  if (!armed || typeof callback !== 'function' || callback.constructor.name === 'AsyncFunction')
    return write.call(this, owner, callback);
  let target;
  const value = write.call(this, owner, tx => {
    const result = callback(tx); // Every original mutation/check runs unchanged.
    const creating = point.startsWith('create-') && result?.intent === intent && result.status === 'draft' && result.revision === 1;
    const finishing = point.startsWith('result-') && result?.role === 'verifier' && result.status === 'completed';
    if (armed && (creating || finishing)) {
      const taskId = creating ? result.id : result.taskId;
      const row = tx.projection('task', taskId), task = parse(row), head = tx.head(taskId);
      const event = JSON.parse(tx.events(taskId, head.sequence - 1n, 1)[0].bytes.toString('utf8'));
      assert.equal(task.task.intent, intent);
      assert.equal(event.payload.type, creating ? 'task.created' : 'worker.finished');
      const commands = tx.commands('', 20).filter(command => command.taskId === taskId);
      const capacity = parse(tx.projection('budget', 'service-capacity')) ?? {active: []};
      if (creating) {
        const requestDigest = digest(encode({operation: 'task.create', taskId: null, body: task.input}));
        const receipt = tx.receipt('tasks', 'task.create', digest(encode('commit-create')), requestDigest);
        assert.deepEqual(parse(receipt), result);
        assert.equal(task.attempts, 0); assert.equal(commands.length, 1);
        assert.equal(commands[0].status, 'pending'); assert.equal(capacity.active.length, 0);
      } else {
        const worker = parse(tx.projection('attempt', result.id));
        assert.equal(task.task.status, 'completed'); assert.equal(task.acceptance.status, 'passed');
        assert.equal(worker.cleanup.cleaned, true);
        assert.equal(event.payload.workerId, result.id); assert.equal(event.payload.decisionDigest, task.decision.digest);
        assert.equal(parse(tx.projection('attempt', task.decision.id)).status, 'accepted');
        assert.equal(commands.find(command => command.id === worker.ticket.commandId).status, 'observed');
        assert.equal(capacity.active.length, 0);
        assert.equal(task.task.artifactIds.length, 2);
        for (const id of task.task.artifactIds) assert.equal(parse(tx.projection('artifact', id)).artifact.status, 'ready');
      }
      // Diagnostic rendezvous only; tests independently read SQLite after exit.
      target = {taskId, result, taskDigest: digest(row.bytes), eventDigest: head.digest,
        attempts: task.attempts, limits: task.limits, deadlineAt: task.task.deadlineAt};
      if (point.endsWith('-before')) barrier(target);
    }
    return result;
  });
  if (armed && target && point.endsWith('-after')) barrier(target); // Original COMMIT has returned.
  return value;
};

const here = name => fileURLToPath(new URL(name, import.meta.url));
const prepared = new Map();
function observed(handle, ticket, kind) {
  const started = handle.started.then(value => { record({type: 'started', kind, workerId: ticket.workerId, taskId: ticket.taskId, started: value}); return value; });
  const completion = handle.completion.then(value => {
    const outputs = kind === 'verification' && value.status === 'passed' ? ['evidence', 'delivery'].map(kind =>
      ({kind, digest: digest(value[kind].content), bytes: value[kind].content.byteLength})) : [];
    record({type: 'completion', kind, workerId: ticket.workerId, taskId: ticket.taskId, status: value.status, cleanup: value.cleanup, outputs});
    return value; // Preserve original command/ACP result and cleanup identities.
  });
  return {...handle, started, completion};
}
const native = createAcpProvider({id: 'fixture-acp', executable: process.execPath,
  args: [here('../task-team-integration/agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: 'good'}});
const provider = {id: native.id, start(input) {
  const ticket = prepared.get(input.cwd); assert.ok(ticket);
  return observed(native.start(input), ticket, 'agent');
}};
// Same checked-in independent checker and deliberately separate expected totals
// as the complete team test; never import the Agent's calculation as an oracle.
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
const checkerPath = here('../task-team-integration/checker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
  policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: actual => {
    try { assert.deepEqual(actual, expected); return true; } catch { return false; }
  }}], delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json',
    content: encode({files: ['east', 'west'].map(region => ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan,
  start(input) { return observed(command.start(input), input.ticket, 'verification'); }});
export default {providers: new Map([[provider.id, provider]]), verification, supervisorOptions: {intervalMs: 10},
  onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
    const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
    return {...business, async prepare(ticket, context) {
      const value = await business.prepare(ticket, context); prepared.set(value.cwd, ticket); return value;
    }, release(ticket) {
      for (const [cwd, current] of prepared) if (current.workerId === ticket.workerId) prepared.delete(cwd);
      return business.release(ticket);
    }};
  }};
