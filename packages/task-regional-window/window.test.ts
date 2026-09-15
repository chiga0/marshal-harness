import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {startTaskService} from '../task-service/composition.mjs';
import {encode} from '../task-store/store.mjs';
import {createRegionalWindowConfig, filePermission} from './index.mjs';
import {date, range, finalValues, rowsFrom, expected, taskBody, proposal} from './policy.mjs';
import {intake, answer, complete, parseOptions} from './driver.mjs';
import {consumeDelivery} from './consumer.mjs';

const values = {startDate: '2026-09-01', endDate: '2026-09-02'};
const bytes = encode({rows: [
  {date: '2026-08-31', region: 'east', status: 'paid', cents: 9999},
  {date: '2026-09-01', region: 'east', status: 'paid', cents: 100},
  {date: '2026-09-02', region: 'east', status: 'paid', cents: -25},
  {date: '2026-09-02', region: 'east', status: 'paid', cents: 0},
  {date: '2026-09-01', region: 'west', status: 'paid', cents: 50},
  {date: '2026-09-01', region: 'west', status: 'cancelled', cents: 777},
  {date: '2026-09-03', region: 'west', status: 'paid', cents: 8888},
]});
async function until(fn, desired) {
  const end = Date.now() + 10000;
  for (;;) {const value = await fn(); if (desired(value)) return value; assert.ok(Date.now() < end, 'bounded window observation'); await pause(20);}
}
async function fixture(t, mode = 'good') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-window-'))), root = path.join(parent, 'data');
  const executions = [], byCwd = new Map(), services = [];
  const native = createAcpProvider({id: 'window-fixture-acp', executable: process.execPath,
    args: [fileURLToPath(new URL('./agent.fixture.mjs', import.meta.url))], env: {WINDOW_FIXTURE: mode}});
  const provider = {id: native.id, start(input) {
    const prompt = mode === 'omit-planner-declaration' ? input.prompt.replace(/^REGIONAL_WINDOW_FIXED_PROPOSAL_V1\n[^\n]+\nREGIONAL_WINDOW_FIXED_PROPOSAL_END\n/m, '') : input.prompt;
    const handle = native.start({...input, prompt}); executions.push({ticket: byCwd.get(input.cwd), prompt, handle}); return handle;
  }};
  const config = createRegionalWindowConfig({provider, onExecution: (ticket, cwd) => byCwd.set(cwd, ticket)});
  const start = async mode => {const service = await startTaskService({root, mode, ...config, supervisorOptions: {intervalMs: 10}}); services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};};
  t.after(async () => {for (const service of services) await service.shutdown(); for (const {handle} of executions) await handle.stop(); fs.rmSync(parent, {recursive: true, force: true});});
  return {...await start('create'), start, executions, parent};
}

test('strict UTC dates, inclusive bounded range and original-data integer oracle', () => {
  assert.equal(date('2024-02-29'), true);
  for (const value of ['2026-02-29', '2026-02-30', '2026-9-01', '2026-09-01T00:00:00Z', '2026-09-01+08:00', ' 2026-09-01', null]) assert.equal(date(value), false);
  assert.throws(() => range({startDate: '2026-09-02', endDate: '2026-09-01'}));
  assert.throws(() => range({startDate: '2026-01-01', endDate: '2027-01-02'}));
  assert.deepEqual(expected(bytes, values), [{region: 'east', ...values, count: 3, netCents: 75}, {region: 'west', ...values, count: 1, netCents: 50}]);
  for (const changed of [{rows: [{date: '2026-09-01', region: 'east', status: 'paid', cents: 0.1}]}, {rows: []}, {rows: [{date: '2026-09-01', region: 'east', status: 'paid', cents: 1, oracle: true}]}])
    assert.throws(() => rowsFrom(encode(changed)));
  assert.throws(() => parseOptions(['intake', '--connection', '/a', '--session', '/b', '--input', '/c', '--key', 'k', '--start-date', '2026-09-01']));
  assert.throws(() => parseOptions(['complete', '--connection', '/a', '--session', '/b', '--output', '/c']));
});

test('real HTTP SQLite files and ORIGINAL guard: necessary dates, readable preview, explicit approval, two ACP authors, independent download and cold replay', {timeout: 30000}, async t => {
  const f = await fixture(t), session = await intake(f.client, {bytes, key: 'real-window'});
  assert.equal(session.created.status, 'awaiting-answer'); assert.equal(f.executions.length, 0); assert.equal(session.body.context.text, undefined);
  await f.service.shutdown(); const reopened = await f.start('open');
  assert.deepEqual(await reopened.client.request('task.questions', {path: {taskId: session.taskId}}), session.initial);
  const preview = await answer(reopened.client, session, values);
  assert.deepEqual(finalValues(preview.preview.input), values); assert.equal(f.executions.length, 0);
  assert.deepEqual(preview.preview.plan.budget, session.initial.preview.plan.budget);
  assert.deepEqual(preview.preview.plan.acceptance, session.initial.preview.plan.acceptance);
  const result = await complete(reopened.client, session, preview.approval);
  assert.equal(result.task.status, 'completed'); assert.equal(result.audit.attempts, 3); assert.equal(f.executions.length, 2);
  assert.deepEqual(result.consumed.window, values); assert.equal(result.consumed.netCents, 125); assert.equal(result.consumed.count, 4);
  const actual = await Promise.all(f.executions.map(async ({ticket, handle}) => {const result = await handle.completion;
    assert.equal(ticket.role, 'author'); assert.deepEqual(finalValues(ticket.input.task), values);
    assert.equal(result.cleanup.cleaned, true); assert.equal(result.stopReason, 'end_turn'); return result.cleanup;}));
  assert.ok(Math.max(...actual.map(c => Date.parse(c.started.startedAt))) < Math.min(...actual.map(c => Date.parse(c.agentExit.at))));
  const tampered = JSON.parse(result.content); tampered.window.endDate = '2026-09-03';
  assert.throws(() => consumeDelivery(encode(tampered), bytes, values));
  assert.throws(() => consumeDelivery(result.content, bytes, {...values, endDate: '2026-09-03'}));
  await reopened.service.shutdown(); const cold = await f.start('open');
  assert.deepEqual(await cold.client.getTask(session.taskId), result.task);
  assert.deepEqual((await cold.client.downloadArtifact(result.artifact.id)).content, result.content); assert.equal(f.executions.length, 2);
});

test('invalid or reverse dates, old preview and different-key consumed answer reject without launching', {timeout: 15000}, async t => {
  const f = await fixture(t), session = await intake(f.client, {bytes, key: 'negative-dates'}), qs = session.initial;
  const start = qs.items.find(q => q.slotId === 'startDate'), end = qs.items.find(q => q.slotId === 'endDate');
  const body = {expectedRevision: qs.taskRevision, previewDigest: qs.previewDigest, questionRevision: 1, answer: '2026-02-30'};
  const send = (question, body, key) => f.client.request('task.answer', {path: {taskId: session.taskId, questionId: question.id}, body, idempotencyKey: key});
  await assert.rejects(send(start, body, 'bad-date'), {code: 'unsupported_task'});
  await send(start, {...body, answer: '2026-09-02'}, 'start');
  const current = await f.client.request('task.questions', {path: {taskId: session.taskId}});
  await assert.rejects(send(end, {...body, answer: '2026-09-03'}, 'old'), {code: 'revision_conflict'});
  await assert.rejects(send(end, {expectedRevision: current.taskRevision, previewDigest: current.previewDigest, questionRevision: 1, answer: '2026-09-01'}, 'reverse'), {code: 'unsupported_task'});
  assert.deepEqual(await f.client.request('task.questions', {path: {taskId: session.taskId}}), current);
  assert.equal(f.executions.length, 0);
  await f.client.request('task.cancel', {path: {taskId: session.taskId}, body: {expectedRevision: current.taskRevision}, idempotencyKey: 'cancel'});
  await until(() => f.client.getTask(session.taskId), task => task.status === 'cancelled');
  await assert.rejects(answer(f.client, session, {startDate: '2026-09-02', endDate: '2026-09-03'})); assert.equal(f.executions.length, 0);
});

test('actual author reports for WRONG date range cannot pass final independent ticket-bound oracle', {timeout: 30000}, async t => {
  const f = await fixture(t, 'wrong-window'), session = await intake(f.client, {bytes, key: 'wrong-window'}), preview = await answer(f.client, session, values);
  await assert.rejects(complete(f.client, session, preview.approval), /window_execution_stopped/);
  const task = await until(() => f.client.getTask(session.taskId), task => ['failed', 'intervention'].includes(task.status));
  const audit = await f.client.request('task.audit', {path: {taskId: session.taskId}});
  assert.equal(task.status, 'failed'); assert.equal(audit.acceptance.status, 'failed'); assert.deepEqual(task.artifactIds, []);
  assert.equal(f.executions.length, 2);
});

test('offered permissions never grant shell, another branch output or input mutation', t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-window-permission-')));
  t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  fs.writeFileSync(path.join(parent, 'sales.json'), bytes);
  const ticket = {role: 'author', nodeId: 'east'}, options = [{kind: 'allow_once', optionId: 'proceed_once'}];
  const offered = (kind, rawInput) => filePermission(ticket, parent, {toolCall: {kind, rawInput}, options}).outcome.outcome;
  assert.equal(offered('read', {file_path: 'sales.json'}), 'selected');
  assert.equal(offered('edit', {file_path: 'east.json', content: '{}'}), 'selected');
  for (const [kind, input] of [['execute', {command: 'id'}], ['edit', {file_path: 'west.json', content: '{}'}],
    ['edit', {file_path: 'sales.json', content: '{}'}], ['read', {file_path: '/etc/passwd'}], ['read', {file_path: 'sales.json', policy: 'allow'}]])
    assert.equal(offered(kind, input), 'cancelled');
});

test('actual CLI separates answers and exact final approval; wrong digest never starts an author', {timeout: 30000}, async t => {
  const f = await fixture(t), inputFile = path.join(f.parent, 'sales.json'), sessionFile = path.join(f.parent, 'session.json'), outputFile = path.join(f.parent, 'delivery.json');
  fs.writeFileSync(inputFile, bytes, {mode: 0o600});
  const driver = fileURLToPath(new URL('./driver.mjs', import.meta.url));
  const call = async (action, args) => JSON.parse((await promisify(execFile)(process.execPath, [driver, action, '--connection', f.service.connectionFile,
    '--session', sessionFile, ...args], {timeout: 15000, maxBuffer: 1024 * 1024})).stdout);
  const created = await call('intake', ['--input', inputFile, '--key', 'cli-window']);
  assert.equal(created.executionRequested, false); assert.equal(f.executions.length, 0);
  const preview = await call('answer', ['--start-date', values.startDate, '--end-date', values.endDate]);
  assert.equal(preview.executionRequested, false); assert.equal(f.executions.length, 0);
  const args = ['--expected-revision', String(preview.approval.expectedRevision), '--plan-revision', String(preview.approval.planRevision),
    '--plan-digest', preview.approval.planDigest, '--output', outputFile];
  const wrong = [...args]; wrong[5] = 'sha256:' + 'b'.repeat(64);
  await assert.rejects(call('complete', wrong)); assert.equal(f.executions.length, 0); assert.equal(fs.existsSync(outputFile), false);
  const done = await call('complete', args); assert.equal(done.status, 'completed'); assert.equal(done.consumed.netCents, 125);
  assert.equal(consumeDelivery(fs.readFileSync(outputFile), bytes, values).netCents, 125); assert.equal(f.executions.length, 2);
});

test('already supplied dates are not re-asked; one missing slot yields only its real question', {timeout: 30000}, async t => {
  const f = await fixture(t), uploaded = await f.client.request('input.create', {idempotencyKey: 'supplied-input',
    body: {name: 'sales.json', mediaType: 'application/json', contentBase64: bytes.toString('base64')}});
  const body = taskBody(uploaded.id);
  const partial = await f.client.createTask({...body, context: {...body.context, text: JSON.stringify({startDate: values.startDate})}}, 'partial');
  const question = await f.client.request('task.questions', {path: {taskId: partial.id}});
  assert.deepEqual(question.items.map(item => item.slotId), ['endDate']); assert.equal(f.executions.length, 0);
  await f.client.request('task.cancel', {path: {taskId: partial.id}, body: {expectedRevision: partial.revision}, idempotencyKey: 'partial-cancel'});
  const created = await f.client.createTask({...body, context: {...body.context, text: JSON.stringify(values)}}, 'supplied');
  assert.deepEqual((await f.client.request('task.questions', {path: {taskId: created.id}})).items, []);
  const planned = await until(() => f.client.getTask(created.id), task => task.status === 'awaiting-approval');
  await f.client.approveTask(created.id, {expectedRevision: planned.revision, planRevision: planned.plan.revision, planDigest: planned.plan.digest}, 'supplied-approve');
  const completed = await until(() => f.client.getTask(created.id), task => ['completed', 'failed', 'intervention'].includes(task.status));
  assert.equal(completed.status, 'completed'); assert.equal(f.executions.length, 3); assert.equal(f.executions[0].ticket.role, 'planner');
  const declaration = /^REGIONAL_WINDOW_FIXED_PROPOSAL_V1\n([^\n]+)\nREGIONAL_WINDOW_FIXED_PROPOSAL_END$/m.exec(f.executions[0].prompt);
  assert.ok(declaration, 'actual Provider prompt includes the whole exact proposal');
  assert.deepEqual(JSON.parse(declaration[1]), proposal());
  assert.deepEqual(encode((await f.client.request('task.plan', {path: {taskId: created.id}})).nodes), encode(proposal().nodes));
});

test('actual Planner cannot use a private fixture answer when the declaration is missing or alter its frozen goal', {timeout: 30000}, async t => {
  for (const mode of ['omit-planner-declaration', 'alter-planner-goal']) await t.test(mode, async t => {
    const f = await fixture(t, mode), uploaded = await f.client.request('input.create', {idempotencyKey: mode + '-input',
      body: {name: 'sales.json', mediaType: 'application/json', contentBase64: bytes.toString('base64')}});
    const body = taskBody(uploaded.id);
    const created = await f.client.createTask({...body, context: {...body.context, text: JSON.stringify(values)}}, mode);
    const task = await until(() => f.client.getTask(created.id), task => ['failed', 'intervention', 'awaiting-approval'].includes(task.status));
    assert.equal(task.status, 'failed'); assert.equal(task.plan, null); assert.deepEqual(task.artifactIds, []);
    assert.equal(f.executions.length, 1); assert.equal(f.executions[0].ticket.role, 'planner');
    assert.equal((await f.executions[0].handle.completion).cleanup.cleaned, true);
    assert.equal((await f.client.request('task.audit', {path: {taskId: task.id}})).attempts, 1);
    if (mode === 'omit-planner-declaration') assert.doesNotMatch(f.executions[0].prompt, /REGIONAL_WINDOW_FIXED_PROPOSAL_V1/);
  });
});
