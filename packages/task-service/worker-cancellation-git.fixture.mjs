// Negative CLI fixture: REAL Git prepare precedes the unbound cut. It never
// grants preparation qualification or invents a process/cleanup observation.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Store, encode} from '../task-store/store.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createStagingOnlyBusinessFactory} from '../task-business/index.mjs';
import {createGitBusiness} from '../task-git-business/index.mjs';
import {runGit} from '../task-git-business/git.mjs';
import {policy, bindPlan} from '../task-git-business/scenario.fixture.mjs';
const root = process.argv[process.argv.indexOf('--root') + 1], mode = process.argv[process.argv.indexOf('--mode') + 1];
if (process.env.WORKER_GIT_FIXTURE !== '1' || !path.isAbsolute(root ?? '')) throw Error('test-only configuration');
const parent = path.dirname(root), metadataPath = path.join(parent, 'git-input.json'), journal = path.join(parent, 'worker-observations.jsonl');
const record = value => {const fd = fs.openSync(journal, fs.constants.O_CREAT | fs.constants.O_APPEND | fs.constants.O_WRONLY | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}};
const run = (cwd, args) => runGit('/usr/bin/git', cwd, args);
if (mode === 'create') {
  const nodes = [];
  for (const [nodeId, filename] of [['library', 'net.mjs'], ['client', 'invoice.mjs']]) {
    const repository = path.join(parent, nodeId); fs.mkdirSync(repository, {mode: 0o700});
    await run(repository, ['init', '-b', 'main']); fs.writeFileSync(path.join(repository, filename), 'export const original = true;\n');
    await run(repository, ['add', '--', filename]);
    await run(repository, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '--no-gpg-sign', '-m', 'locked fixture']);
    nodes.push({nodeId, repositoryId: nodeId, base: (await run(repository, ['rev-parse', 'HEAD'])).toString().trim(), writePaths: [filename]});
  }
  fs.writeFileSync(metadataPath, encode({profile: 'task-git-input/v1', nodes}), {flag: 'wx', mode: 0o600});
}
const native = createAcpProvider({id: 'git-fixture-acp', executable: process.execPath,
  args: [fileURLToPath(new URL('../task-git-business/agent.fixture.mjs', import.meta.url))],
  custodyProfile: {id: 'git-fixture-inherited', scope: 'inherited-process-group', eligible: true}});
const verification = createVerificationPort({id: 'unbound-must-not-verify', policy, bindPlan,
  start() {throw Error('unbound Task must not reach verifier');}});
const original = Store.prototype.write; let armed = mode === 'create';
Store.prototype.write = function(owner, callback) {
  let stop = false;
  const result = original.call(this, owner, tx => {
    const append = tx.append.bind(tx); tx.append = (stream, expected, events) => {
      if (events.some(event => JSON.parse(event.bytes).payload.type === 'worker.cancel-requested')) stop = true;
      return append(stream, expected, events);
    }; return callback(tx);
  });
  if (stop && armed) {armed = false; record({type: 'barrier', cut: 'git-stop-after'});
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000); throw Error('fixture cut missed');}
  return result;
};
const safe = process.env.WORKER_GIT_SWITCH_SAFE === '1';
export default {custody: {profile: 'node-execution-custody/v1'}, workerCancellation: {profile: 'task-worker-cancellation/v1'},
  ...(safe ? {unpermitted: {profile: 'node-unpermitted-reservation/v1'}} : {}),
  providers: new Map([[native.id, native]]), verification, supervisorOptions: {intervalMs: 10, prepareMs: 15000},
  businessFactory: safe ? createStagingOnlyBusinessFactory() : ports => {
    const business = createGitBusiness({parent: ports.executionParent, depot: ports.depot, approvedLayout: ports.approvedLayout,
      observeExecution: ports.observeExecution, repositoryFor: (_ticket, request) => path.join(parent, request.repositoryId),
      layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
    return {...business, async prepare(ticket, context) {
      const prepared = await business.prepare(ticket, context);
      if (ticket.role === 'author') {
        record({type: 'git-prepared', workerId: ticket.workerId, cwd: prepared.cwd, reservationDigest: ticket.reservationDigest});
        // Only waits after original Git effects, before returning to bind/start.
        await new Promise(resolve => setTimeout(resolve, 10000));
      }
      return prepared;
    }};
  }};
