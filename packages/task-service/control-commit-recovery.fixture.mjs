// Test-only rendezvous around ORIGINAL SQLite transactions. No lifecycle,
// custody signature, result, authority receipt or external cleanup is forged.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {Store, encode, digest} from '../task-store/store.mjs';

const root = process.argv[process.argv.indexOf('--root') + 1];
const point = process.env.MARSHAL_CONTROL_COMMIT_POINT;
const points = ['none', ...['reservation', 'binding', 'cancel'].flatMap(kind => [kind + '-before', kind + '-after'])];
if (process.env.MARSHAL_CONTROL_COMMIT_FIXTURE !== '1' || !path.isAbsolute(root ?? '') || !points.includes(point))
  throw Error('test-only configuration');
const targetIntent = 'custody interrupted authors', journal = path.join(path.dirname(root), 'control-commit.jsonl');
const parse = row => row && JSON.parse(row.bytes.toString('utf8'));
function record(value) {
  const fd = fs.openSync(journal, fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_NOFOLLOW, 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
}
let armed = point !== 'none';
function barrier(value) {
  armed = false; record({type: 'barrier', point, ...value});
  // The test kills its ORIGINAL service ChildProcess here. A finite timeout
  // fails the fixture; it cannot resume the transaction or send a false 202.
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10000);
  process.exit(79);
}
const write = Store.prototype.write;
Store.prototype.write = function(owner, callback) {
  if (!armed || typeof callback !== 'function' || callback.constructor.name === 'AsyncFunction') return write.call(this, owner, callback);
  let target;
  const result = write.call(this, owner, tx => {
    const value = callback(tx);
    const reserved = point.startsWith('reservation-') && value?.role === 'author' && value?.reservationDigest && value?.input;
    const bound = point.startsWith('binding-') && value?.profile === 'node-execution-custody/v1' && value.binding?.planDigest;
    const cancelled = point.startsWith('cancel-') && value?.kind === 'task.cancel';
    if (!reserved && !bound && !cancelled) return value;
    const taskId = bound ? value.binding.taskId : value.taskId;
    const row = tx.projection('task', taskId), task = parse(row);
    if (task.task.intent !== targetIntent) return value;
    const head = tx.head(taskId), event = JSON.parse(tx.events(taskId, head.sequence - 1n, 1)[0].bytes.toString('utf8'));
    assert.equal(event.payload.type, reserved ? 'worker.reserved' : bound ? 'worker.custody-permitted' : 'task.cancel');
    const workerId = reserved ? value.workerId : bound ? value.binding.workerId : null;
    if (workerId) {
      const worker = parse(tx.projection('attempt', workerId)), command = tx.command(worker.ticket.commandId);
      assert.equal(worker.worker.role, 'author'); assert.equal(worker.worker.status, 'queued');
      assert.equal(worker.executionId, null); assert.equal(worker.cleanup, null); assert.equal(command.status, 'unknown');
      assert.equal(task.attempts, 2);
      assert.deepEqual(parse(tx.projection('budget', 'service-capacity')).active.map(item => item.workerId), [workerId]);
      if (bound) {
        assert.deepEqual(worker.custody.descriptor, value);
        assert.deepEqual(parse(tx.receipt(workerId, 'execution.custody-binding', digest(encode('binding')), digest(encode(value)))), value);
      } else assert.equal(worker.custody, undefined);
    } else {
      assert.equal(task.task.status, 'cancelling'); assert.equal(task.attempts, 3);
      const operation = value;
      assert.equal(operation.status, 'accepted'); assert.equal(task.cancelIntent.operationId, operation.id);
      const requestDigest = digest(encode({operation: 'task.cancel', taskId, body: {expectedRevision: task.task.revision - 1}}));
      assert.deepEqual(parse(tx.receipt(taskId, 'task.cancel', digest(encode('lost-cancel')), requestDigest)), operation);
      const command = tx.command(task.cancelIntent.commandId);
      assert.equal(command.status, 'pending'); assert.equal(command.kind, 'stop');
      assert.equal(parse(tx.projection('budget', 'service-capacity')).active.length, 2);
    }
    target = {taskId, workerId, value, taskDigest: digest(row.bytes), eventDigest: head.digest,
      eventSequence: head.sequence.toString(), attempts: task.attempts, deadlineAt: task.task.deadlineAt};
    if (point.endsWith('-before')) barrier(target);
    return value;
  });
  if (armed && target && point.endsWith('-after')) barrier(target);
  return result;
};

// Reuse the original real ACP/command/custodian/FileBusiness producer chain.
// Only the interrupted team's authors hang; the next Task uses the same oracle
// and completes normally. These are owned Node fixtures, never model calls.
process.env.MARSHAL_CUSTODY_FIXTURE = '1'; process.env.MARSHAL_CUSTODY_SCENARIO = 'authors';
const {default: original} = await import('./custody-recovery.fixture.mjs');
const maxWorkers = process.env.MARSHAL_CONTROL_COMMIT_CAPACITY;
if (!['1', '2'].includes(maxWorkers)) throw Error('test-only capacity');
export default {...original, applicationOptions: {execution: {maxWorkers: Number(maxWorkers)}}};
