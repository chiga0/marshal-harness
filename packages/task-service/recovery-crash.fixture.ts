// Trusted test-only composition for the REAL service CLI. Never shipped/configured
// by HTTP. A stop barrier holds only the test's pre-cleanup crash window.
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1];
if (!root || !path.isAbsolute(root) || process.env.MARSHAL_SERVICE_CRASH_FIXTURE !== '1') throw Error('test-only configuration');
const journal = path.join(path.dirname(root), 'fixture-executions.jsonl'), prepared = new Map();
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
const native = createAcpProvider({id: 'crash-fixture', executable: process.execPath,
  args: [fileURLToPath(new URL('./recovery-worker.fixture.mjs', import.meta.url))], env: {}});
const provider = {id: native.id, start(input) {
  const ticket = prepared.get(input.cwd); if (!ticket) throw Error('fixture missing prepared ticket');
  const handle = native.start(input);
  handle.started.then(started => record({type: 'started', workerId: ticket.workerId, taskId: ticket.taskId, cwd: input.cwd, started}));
  let stopping;
  return {...handle, stop() {
    if (stopping) return stopping;
    record({type: 'stop-entered', workerId: ticket.workerId});
    // Only this injected provider barrier delays its ORIGINAL handle; it never
    // returns a fabricated result. The test kills the owning service here.
    stopping = process.env.MARSHAL_SERVICE_CRASH_STOP_BARRIER === '1' ? new Promise(() => {}) : handle.stop();
    return stopping;
  }};
}};
export default {providers: new Map([[provider.id, provider]]), supervisorOptions: {intervalMs: 10},
  businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => {
    const business = createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
      layoutFor: () => ({inputs: [], allowedPaths: []})});
    return {...business, async prepare(ticket, context) {
      const input = await business.prepare(ticket, context); prepared.set(input.cwd, ticket); return input;
    }};
  }};
