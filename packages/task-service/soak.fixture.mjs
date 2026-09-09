// 仅用于有限持续运行测试；原协议进程、custody、SQLite 与验收均走生产链。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {encode, digest} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1];
if (process.env.MARSHAL_SOAK_FIXTURE !== '1' || !path.isAbsolute(root ?? '')) throw Error('test-only configuration');
const here = name => fileURLToPath(new URL(name, import.meta.url));
const journal = path.join(path.dirname(root), 'soak-observations.jsonl'), prepared = new Map();
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
function observed(handle, ticket) {
  const identity = {taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role};
  return {...handle, started: handle.started.then(started => {
    record({type: 'started', ...identity, started}); return started;
  }), completion: handle.completion.then(result => {
    record({type: 'completion', ...identity, status: result.status, reason: result.reason ?? null, cleanup: result.cleanup}); return result;
  })};
}
const native = mode => createAcpProvider({id: 'fixture-acp', executable: process.execPath,
  args: [here('../task-team-integration/agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: mode},
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const good = native('good'), corrupt = native('corrupt'), held = native('hang');
const provider = {id: good.id, custodyProfile: good.custodyProfile, start(input) {
  const ticket = prepared.get(input.cwd); assert.ok(ticket);
  const selected = ticket.input.task.intent === 'soak content failure' ? corrupt : ticket.input.task.intent === 'soak held authors' ? held : good;
  return observed(selected.start(input), ticket);
}};
// 固定 checker 读取真实候选；父进程的预期值不复用作者的计算函数。
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
const checkerPath = here('../task-team-integration/checker.fixture.mjs');
const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
  policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: actual => {
    try { assert.deepEqual(actual, expected); return true; } catch { return false; }
  }}], delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json', content: encode({files: ['east', 'west']
    .map(region => ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
function startVerification(input) { return observed(command.start(input), input.ticket); }
startVerification.custodyProfile = command.start.custodyProfile;
const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan, start: startVerification});
export default {custody: {profile: 'node-execution-custody/v1'}, providers: new Map([[provider.id, provider]]), verification,
  applicationOptions: {execution: {maxWorkers: 4}}, supervisorOptions: {intervalMs: 10},
  onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
  businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
    const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
    return {...business, async prepare(ticket, context) {
      const value = await business.prepare(ticket, context); prepared.set(value.cwd, ticket); return value;
    }, release(ticket) {
      for (const [cwd, value] of prepared) if (value.workerId === ticket.workerId) prepared.delete(cwd);
      return business.release(ticket);
    }};
  }};
