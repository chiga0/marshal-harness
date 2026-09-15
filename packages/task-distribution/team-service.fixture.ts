// Test-only trusted deployment configuration. Every production import comes
// from the installed package; only the deterministic Agent/policy is external.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {createInterface} from 'node:readline';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';

let configuration;
if (process.env.MARSHAL_PACKAGE_CHECKER === '1') {
  const {encode} = await import(pathToFileURL(path.join(process.env.MARSHAL_PACKAGE_ROOT, 'packages/task-store/store.mjs')).href);
  for await (const line of createInterface({input: process.stdin})) {
    const request = JSON.parse(line);
    const actual = ['east', 'west'].map(region => JSON.parse(fs.readFileSync(region + '.json', 'utf8')));
    process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
      assertions: [{name: 'regions', actual}]}).toString() + '\n', () => process.exit(0));
    break;
  }
} else {
  assert.equal(process.env.MARSHAL_PACKAGE_TEAM, '1');
  const root = process.env.MARSHAL_PACKAGE_ROOT, journal = process.env.MARSHAL_PACKAGE_JOURNAL;
  assert.ok(path.isAbsolute(root ?? '') && fs.realpathSync(root) === root && path.isAbsolute(journal ?? ''));
  const load = relative => import(pathToFileURL(path.join(root, relative)).href);
  const {encode, digest} = await load('packages/task-store/store.mjs');
  const {createAcpProvider} = await load('packages/agent-provider-acp/index.mjs');
  const {createFileBusiness} = await load('packages/task-business/index.mjs');
  const {createVerificationPort} = await load('packages/task-application/application.mjs');
  const {createVerificationCommand} = await load('packages/task-verification-command/index.mjs');
  const record = value => {
    const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
    try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  };
  const custody = process.env.MARSHAL_PACKAGE_CUSTODY === '1';
  const native = createAcpProvider({id: 'package-fixture-acp', executable: process.execPath,
    args: [fileURLToPath(new URL('../task-team-integration/agent.fixture.mjs', import.meta.url))], env: {TEAM_FIXTURE_MODE: 'good'},
    ...(custody ? {custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}} : {})});
  function observe(handle, role) {
    return {...handle, started: handle.started.then(value => {record({type: 'started', role, value}); return value;}),
      completion: handle.completion.then(value => {record({type: 'finished', role, status: value.status, reason: value.reason ?? null, cleanup: value.cleanup}); return value;})};
  }
  const prepared = new Map();
  const provider = {id: native.id, ...(custody ? {custodyProfile: native.custodyProfile} : {}), start(input) {
    assert.ok(prepared.has(input.cwd)); return observe(native.start(input), prepared.get(input.cwd).role);
  }};
  // Parent assertions are deliberately independent of the Agent's arithmetic.
  const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
  const checkerPath = fileURLToPath(import.meta.url);
  const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    env: {MARSHAL_PACKAGE_CHECKER: '1', MARSHAL_PACKAGE_ROOT: root}, policyDigest: digest(encode(policy)), assertions: [{name: 'regions', validate: actual => {
      try {assert.deepEqual(actual, expected); return true;} catch {return false;}
    }}], delivery: ({prepared: input}) => ({name: 'regional-report.json', mediaType: 'application/json', content: encode({files:
      ['east', 'west'].map(region => ({path: region + '.json', content: fs.readFileSync(path.join(input.cwd, region + '.json'), 'utf8')}))})})});
  function startVerification(input) {return observe(command.start(input), 'verifier');}
  if (custody) Object.defineProperty(startVerification, 'custodyProfile', {value: command.start.custodyProfile});
  const verification = createVerificationPort({id: 'package-independent-checker', policy, bindPlan, start: startVerification});
  configuration = {providers: new Map([[provider.id, provider]]), verification, supervisorOptions: {intervalMs: 10},
    ...(custody ? {custody: {profile: 'node-execution-custody/v1'}} : {}),
    onDiagnostic: value => record({type: 'diagnostic', code: value.code}),
    businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
      const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
      return {...business, async prepare(ticket, context) {
        const result = await business.prepare(ticket, context); prepared.set(result.cwd, ticket); return result;
      }, release(ticket) {
        for (const [cwd, current] of prepared) if (current.workerId === ticket.workerId) prepared.delete(cwd);
        return business.release(ticket);
      }};
    }};
}
export default configuration;
