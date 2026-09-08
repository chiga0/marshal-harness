import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {PROFILE, taskBody, finalValues, range, rowsFrom, equal, check, fields} from './policy.mjs';
import {consumeDelivery} from './consumer.mjs';

const id = value => typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(value);
const boundary = plan => {const {revision, digest, ...value} = plan; return value;};
function sessionData(session) {
  check(session.profile === PROFILE && id(session.key) && session.taskId === session.created.id, 'window_invalid_session');
  const bytes = Buffer.from(session.sourceBase64, 'base64'); rowsFrom(bytes);
  check(bytes.toString('base64') === session.sourceBase64 && digest(bytes) === session.sourceDigest &&
    session.uploaded.digest === session.sourceDigest && session.uploaded.bytes === bytes.length, 'window_invalid_session'); return bytes;
}
function current(session, questions) {
  check(questions.taskId === session.taskId && questions.confirmBefore === session.initial.confirmBefore && questions.preview &&
    questions.items.length === session.initial.items.length && questions.nextCursor === null &&
    questions.items.every(item => session.initial.items.some(original => original.id === item.id && original.slotId === item.slotId &&
      original.subject === item.subject && original.revision === item.revision && original.prompt === item.prompt)) &&
    equal(questions.preview.input.context.inputRefs, [session.uploaded.id]) && equal(boundary(questions.preview.plan), boundary(session.initial.preview.plan)), 'window_preview_changed');
}
/** Phase 1: original request contains NO dates. No answers, approval or model. */
export async function intake(client, {bytes, key, timeoutMs = 600000}) {
  rowsFrom(bytes); check(id(key), 'window_invalid_key');
  const uploaded = await client.request('input.create', {idempotencyKey: key + '-input',
    body: {name: 'sales.json', mediaType: 'application/json', contentBase64: Buffer.from(bytes).toString('base64')}});
  const body = taskBody(uploaded.id, timeoutMs), created = await client.createTask(body, key + '-create');
  const initial = await client.request('task.questions', {path: {taskId: created.id}});
  check(created.status === 'awaiting-answer' && created.plan && initial.items.length === 2 && initial.nextCursor === null &&
    equal(initial.items.map(q => q.slotId).sort(), ['endDate', 'startDate']) && initial.items.every(q => q.answer === null && q.status === 'open'), 'window_missing_questions');
  return {profile: PROFILE, key, taskId: created.id, body, created, uploaded, initial,
    sourceBase64: Buffer.from(bytes).toString('base64'), sourceDigest: digest(bytes)};
}
/** Phase 1 continuation: explicit operator answers. Return full preview and
 * exact approval arguments; NEVER approve from this method. */
export async function answer(client, session, values) {
  sessionData(session); range(values);
  for (const slot of ['startDate', 'endDate']) {
    const questions = await client.request('task.questions', {path: {taskId: session.taskId}}); current(session, questions);
    const question = questions.items.find(q => q.slotId === slot);
    if (question.status === 'answered') {check(question.answer === values[slot], 'window_answer_already_consumed'); continue;}
    check(question.status === 'open', 'window_question_closed');
    await client.request('task.answer', {path: {taskId: session.taskId, questionId: question.id}, idempotencyKey: session.key + '-' + slot,
      body: {expectedRevision: questions.taskRevision, previewDigest: questions.previewDigest, questionRevision: question.revision, answer: values[slot]}});
  }
  const questions = await client.request('task.questions', {path: {taskId: session.taskId}}); current(session, questions);
  const task = await client.getTask(session.taskId);
  check(task.status === 'awaiting-confirmation' && questions.items.every(q => q.status === 'answered') &&
    equal(finalValues(questions.preview.input), values) && task.revision === questions.taskRevision &&
    task.plan.digest === questions.preview.plan.digest, 'window_final_preview_mismatch');
  return {taskId: task.id, preview: questions.preview, confirmBefore: questions.confirmBefore,
    approval: {expectedRevision: task.revision, planRevision: task.plan.revision, planDigest: task.plan.digest}};
}
/** Phase 2: only a user's explicit exact final approval may create execution. */
export async function complete(client, session, approval) {
  const original = sessionData(session);
  check(fields(approval, ['expectedRevision', 'planRevision', 'planDigest']) && Number.isSafeInteger(approval.expectedRevision) && approval.expectedRevision > 0 &&
    Number.isSafeInteger(approval.planRevision) && approval.planRevision > 0 && /^sha256:[a-f0-9]{64}$/.test(approval.planDigest), 'window_invalid_approval');
  const questions = await client.request('task.questions', {path: {taskId: session.taskId}}); current(session, questions);
  check(questions.items.every(q => q.status === 'answered') && questions.preview.plan.digest === approval.planDigest &&
    questions.preview.plan.revision === approval.planRevision, 'window_approval_mismatch');
  const values = finalValues(questions.preview.input), end = Date.parse(questions.confirmBefore);
  check(Date.now() < end, 'window_deadline');
  const input = await client.downloadArtifact(session.uploaded.id);
  check(input.artifact.digest === session.sourceDigest && input.content.equals(original), 'window_source_changed');
  // One original key/body. An operator retry after response loss uses the same
  // values; Core replay precedes CAS. Never invent a replacement attempt/key.
  const operation = await client.approveTask(session.taskId, approval, session.key + '-approve');
  let task;
  for (;;) {
    check(Date.now() < end, 'window_deadline'); task = await client.getTask(session.taskId);
    if (task.status === 'completed') break;
    check(!['failed', 'cancelled', 'cancelling', 'intervention', 'awaiting-answer', 'paused'].includes(task.status), 'window_execution_stopped');
    await pause(100);
  }
  const audit = await client.request('task.audit', {path: {taskId: task.id}});
  check(audit.acceptance.status === 'passed' && audit.attempts === 3, 'window_independent_acceptance_missing');
  const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
  const deliveries = artifacts.filter(item => item.artifact.kind === 'delivery'); check(deliveries.length === 1, 'window_delivery_missing');
  const delivery = deliveries[0]; check(delivery.artifact.taskId === task.id, 'window_delivery_mismatch');
  const consumed = consumeDelivery(delivery.content, original, values);
  return {task, operation, audit, artifact: delivery.artifact, content: delivery.content, consumed};
}

function readFile(filename, max, privateFile = false) {
  check(typeof filename === 'string' && path.isAbsolute(filename) && fs.realpathSync(filename) === filename, 'window_file_identity');
  const fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {const stat = fs.fstatSync(fd); check(stat.isFile() && stat.nlink === 1 && stat.size > 0 && stat.size <= max &&
    (!privateFile || stat.uid === process.getuid() && (stat.mode & 0o7777) === 0o600), 'window_file_identity');
    const bytes = fs.readFileSync(fd); check(bytes.length === stat.size, 'window_file_changed'); return bytes;
  } finally {fs.closeSync(fd);}
}
function newOutput(filename) {
  check(path.isAbsolute(filename) && path.normalize(filename) === filename && fs.realpathSync(path.dirname(filename)) === path.dirname(filename), 'window_output_path');
  check(!fs.existsSync(filename), 'window_output_exists');
}
function saveNew(filename, bytes) {
  newOutput(filename);
  const fd = fs.openSync(filename, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  try {fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  const parent = fs.openSync(path.dirname(filename), fs.constants.O_RDONLY | fs.constants.O_DIRECTORY);
  try {fs.fsyncSync(parent);} finally {fs.closeSync(parent);}
}
export function parseOptions(argv) {
  const [action, ...args] = argv; check(['intake', 'answer', 'complete'].includes(action), 'window_arguments');
  const accepted = {intake: ['connection', 'session', 'input', 'key', 'timeout-ms'], answer: ['connection', 'session', 'start-date', 'end-date'],
    complete: ['connection', 'session', 'expected-revision', 'plan-revision', 'plan-digest', 'output']}[action];
  const options = {action};
  for (let i = 0; i < args.length; i += 2) {
    const name = args[i]?.slice(2); check(args[i]?.startsWith('--') && accepted.includes(name) && !Object.hasOwn(options, name) &&
      typeof args[i + 1] === 'string' && !args[i + 1].startsWith('--'), 'window_arguments'); options[name] = args[i + 1];
  }
  check(accepted.filter(name => name !== 'timeout-ms').every(name => Object.hasOwn(options, name)), 'window_arguments'); return options;
}
export async function cli(argv) {
  const options = parseOptions(argv), connection = parseJson(readFile(options.connection, 4096, true));
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  if (options.action === 'intake') {
    // Fail existing output before any request; never overwrite the user's receipt.
    newOutput(options.session);
    const result = await intake(client, {bytes: readFile(options.input, 65536), key: options.key, timeoutMs: Number(options['timeout-ms'] ?? 600000)});
    saveNew(options.session, encode(result)); return {taskId: result.taskId, questions: result.initial, session: options.session, executionRequested: false};
  }
  const session = parseJson(readFile(options.session, 524288, true));
  if (options.action === 'answer') return {...await answer(client, session, {startDate: options['start-date'], endDate: options['end-date']}), executionRequested: false};
  newOutput(options.output);
  const result = await complete(client, session, {expectedRevision: Number(options['expected-revision']), planRevision: Number(options['plan-revision']), planDigest: options['plan-digest']});
  saveNew(options.output, result.content);
  return {taskId: result.task.id, status: result.task.status, artifact: result.artifact, consumed: result.consumed, output: options.output};
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  cli(process.argv.slice(2)).then(value => process.stdout.write(JSON.stringify(value) + '\n'), error => {
    const code = /^window_[a-z_]+$/.test(error?.message ?? '') ? error.message : 'window_request_failed';
    process.stderr.write(JSON.stringify({code, requestOutcome: 'query-original-task-or-replay-original-key', automaticRetry: false}) + '\n'); process.exitCode = 1;
  });
}
