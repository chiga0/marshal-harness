// Trusted test-only configuration loaded by the ORIGINAL service CLI. All
// execution, custody, signatures, Store writes and recovery remain production.
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

const root = process.argv[process.argv.indexOf('--root') + 1], scenario = process.env.MARSHAL_CUSTODY_SCENARIO;
if (process.env.MARSHAL_CUSTODY_FIXTURE !== '1' || !path.isAbsolute(root ?? '') ||
    !['authors', 'cancel', 'verifier'].includes(scenario)) throw Error('test-only configuration');
const here = name => fileURLToPath(new URL(name, import.meta.url));
const journal = path.join(path.dirname(root), 'custody-observations.jsonl'), prepared = new Map();
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
const interrupted = ticket => ticket.input.task.intent === 'custody interrupted ' + scenario;
function observed(handle, ticket, kind) {
  const started = handle.started.then(value => {
    record({type: 'started', kind, taskId: ticket.taskId, workerId: ticket.workerId, role: ticket.role, started: value}); return value;
  });
  const completion = handle.completion.then(value => {
    record({type: 'completion', kind, taskId: ticket.taskId, workerId: ticket.workerId, status: value.status, cleanup: value.cleanup});
    return value;
  });
  let stopping;
  return {...handle, started, completion, stop() {
    if (stopping) return stopping;
    if (scenario === 'cancel' && interrupted(ticket) && ticket.role === 'author') {
      record({type: 'stop-barrier', taskId: ticket.taskId, workerId: ticket.workerId});
      // Keep the exact owned handle pending after the real durable cancel.
      // Service death, not this fixture, must trigger custodian cleanup. If the
      // parent misses its window, the finite fallback stops the original handle.
      stopping = new Promise(resolve => setTimeout(() => resolve(handle.stop()), 10000));
    } else stopping = handle.stop();
    return stopping;
  }};
}
const native = mode => createAcpProvider({id: 'fixture-acp', executable: process.execPath,
  args: [here('../task-team-integration/agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: mode},
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const good = native('good'), held = native('hang');
const provider = {id: good.id, custodyProfile: good.custodyProfile, start(input) {
  const ticket = prepared.get(input.cwd); assert.ok(ticket);
  const hold = interrupted(ticket) && ['authors', 'cancel'].includes(scenario);
  return observed((hold ? held : good).start(input), ticket, 'agent');
}};
// Independent expected values do not import the ACP fixture's calculation.
const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
const checkerPath = here('./custody-recovery.worker.fixture.mjs');
function checker(hold) {
  return createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), env: hold ? {MARSHAL_CUSTODY_CHECKER_HOLD: '1'} : {},
    assertions: [{name: 'regions', validate: actual => { try { assert.deepEqual(actual, expected); return true; } catch { return false; } }}],
    delivery: ({prepared}) => ({name: 'regional-report.json', mediaType: 'application/json', content: encode({files: ['east', 'west']
      .map(region => ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
}
const normalChecker = checker(false), heldChecker = checker(true);
function startVerification(input) {
  const hold = scenario === 'verifier' && interrupted(input.ticket);
  return observed((hold ? heldChecker : normalChecker).start(input), input.ticket, 'verification');
}
// Preserve the real command producer's eligibility; the wrapper cannot mint a
// new scope claim or broaden the profile of the underlying command execution.
startVerification.custodyProfile = normalChecker.start.custodyProfile;
const verification = createVerificationPort({id: 'trusted-regional-checker', policy, bindPlan, start: startVerification});
export default {custody: {profile: 'node-execution-custody/v1'}, providers: new Map([[provider.id, provider]]), verification,
  supervisorOptions: {intervalMs: 10}, onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
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
