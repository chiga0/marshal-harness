// Reuse the existing six-cut fixture's ORIGINAL Store.write rendezvous and
// checker. Only choose the actual narrow v5 factory; do not wrap its prepare.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
const original = (await import('./control-commit-recovery.fixture.mjs')).default;
const tickets = new Map(), write = Store.prototype.write;
Store.prototype.write = function(owner, callback) {
  const result = write.call(this, owner, callback);
  if (result?.workerId && result.reservationDigest && result.input) tickets.set(result.workerId, result);
  return result;
};
const root = process.argv[process.argv.indexOf('--root') + 1], journal = path.join(path.dirname(root), 'custody-observations.jsonl');
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
}
const native = createAcpProvider({id: 'fixture-acp', executable: process.execPath,
  args: [fileURLToPath(new URL('../task-team-integration/agent.fixture.mjs', import.meta.url))], env: {TEAM_FIXTURE_MODE: 'good'},
  custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
const held = createAcpProvider({id: native.id, executable: process.execPath,
  args: [fileURLToPath(new URL('../task-team-integration/agent.fixture.mjs', import.meta.url))], env: {TEAM_FIXTURE_MODE: 'hang'}, custodyProfile: native.custodyProfile});
const provider = {id: native.id, custodyProfile: native.custodyProfile, start(input) {
  const ticket = tickets.get(path.basename(input.cwd)); if (!ticket) throw Error('missing original ticket');
  const hold = process.env.MARSHAL_CONTROL_COMMIT_POINT.startsWith('cancel-') && ticket.role === 'author' && ticket.input.task.intent === 'custody interrupted authors';
  const handle = (hold ? held : native).start(input);
  return {...handle, started: handle.started.then(started => {
    record({type: 'started', taskId: ticket.taskId, workerId: ticket.workerId, role: ticket.role, started}); return started;
  }), completion: handle.completion.then(result => {
    record({type: 'completion', taskId: ticket.taskId, workerId: ticket.workerId, status: result.status, cleanup: result.cleanup}); return result;
  })};
}};
export default {...original, unpermitted: {profile: 'node-unpermitted-reservation/v1'},
  providers: new Map([[provider.id, provider]]), businessFactory: createStagingOnlyBusinessFactory()};
