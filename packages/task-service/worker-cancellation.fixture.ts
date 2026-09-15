// Real checked-in CLI composition: original SQLite/ACP/custody/FileBusiness/checker.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, encode, digest} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createFileBusiness, createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';
const here = value => fileURLToPath(new URL(value, import.meta.url));
export const data = {rows: [{region: 'east', status: 'paid', cents: 100}, {region: 'west', status: 'paid', cents: 75}]};
export const expected = [{region: 'east', count: 1, netCents: 100}, {region: 'west', count: 1, netCents: 75}];
export function configuration(root, {unpermitted = false} = {}) {
  const parent = path.dirname(root), releaseParent = path.join(parent, 'release');
  if (!fs.existsSync(releaseParent)) fs.mkdirSync(releaseParent, {mode: 0o700});
  const journal = value => {const fd = fs.openSync(path.join(parent, 'worker-observations.jsonl'), fs.constants.O_CREAT | fs.constants.O_APPEND | fs.constants.O_WRONLY | fs.constants.O_NOFOLLOW, 0o600);
    try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}};
  const tickets = new Map(), original = Store.prototype.write, cut = process.env.WORKER_CANCEL_CUT ?? 'none'; let cutOnce = false;
  const barrier = value => {journal({type: 'barrier', cut, value}); Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000); throw Error('fixture barrier missed');};
  Store.prototype.write = function(owner, callback) {
    let selected = false;
    const value = original.call(this, owner, tx => {
      const append = tx.append.bind(tx); tx.append = (stream, expected, events) => {
        if (events.some(event => {const payload = JSON.parse(event.bytes).payload;
          return cut.startsWith('stop-') && payload.type === 'worker.cancel-requested' ||
            cut.startsWith('settle-') && payload.type === 'worker.finished' && payload.status === 'cancelled';})) selected = true;
        return append(stream, expected, events);
      };
      const result = callback(tx);
      if (selected && !cutOnce && cut.endsWith('-before')) {cutOnce = true; barrier(null);}
      return result;
    });
    if (value?.workerId && value.reservationDigest && value.input) tickets.set(value.workerId, value);
    if (selected && !cutOnce && cut.endsWith('-after')) {cutOnce = true; barrier(value);}
    return value;
  };
  function observe(handle, ticket) {return {...handle, stop() {journal({type: 'stop', workerId: ticket.workerId}); return handle.stop();},
    started: handle.started.then(value => {journal({type: 'started', taskId: ticket.taskId, workerId: ticket.workerId, nodeId: ticket.nodeId, role: ticket.role, started: value}); return value;}),
    completion: handle.completion.then(value => {journal({type: 'completion', taskId: ticket.taskId, workerId: ticket.workerId, cleanup: value.cleanup, status: value.status}); return value;})};}
  const native = createAcpProvider({id: 'fixture-acp', executable: process.execPath, args: [here('./worker-cancellation-agent.fixture.mjs')],
    env: {WORKER_RELEASE_PARENT: releaseParent}, custodyProfile: {id: 'worker-cancel-fixture-v1', scope: 'inherited-process-group', eligible: true}});
  const agent = {id: native.id, custodyProfile: native.custodyProfile, start(input) {
    const ticket = tickets.get(path.basename(input.cwd)); if (!ticket) throw Error('original ticket missing');
    return observe(native.start(input), ticket);
  }};
  const checkerPath = here('./custody-recovery.worker.fixture.mjs');
  const command = hold => createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), env: hold ? {MARSHAL_CUSTODY_CHECKER_HOLD: '1'} : {},
    assertions: [{name: 'regions', validate: actual => digest(encode(actual)) === digest(encode(expected))}],
    delivery: ({prepared}) => ({name: 'regions.json', mediaType: 'application/json', content: encode({files: ['east', 'west'].map(region =>
      ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
  const normal = command(false), held = command(true), start = input => observe((input.ticket.input.task.intent === 'hold-verifier' ? held : normal).start(input), input.ticket);
  start.custodyProfile = normal.start.custodyProfile;
  const verification = createVerificationPort({id: 'independent-checker', policy, bindPlan, start});
  return {providers: new Map([[agent.id, agent]]), verification, custody: {profile: 'node-execution-custody/v1'},
    workerCancellation: {profile: 'task-worker-cancellation/v1'}, ...(unpermitted ? {unpermitted: {profile: 'node-unpermitted-reservation/v1'}} : {}),
    supervisorOptions: {intervalMs: 10}, applicationOptions: {execution: {maxWorkers: 2}},
    businessFactory: unpermitted ? createStagingOnlyBusinessFactory() : ports => createFileBusiness({parent: ports.executionParent, depot: ports.depot,
      approvedLayout: ports.approvedLayout, observeExecution: ports.observeExecution, layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout})};
}
const index = process.argv.indexOf('--root');
export default index === -1 ? {} : configuration(process.argv[index + 1], {unpermitted: process.env.WORKER_CANCEL_UNPERMITTED === '1'});
