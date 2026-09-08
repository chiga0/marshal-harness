import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {Store, encode, digest} from '../task-store/store.mjs';
import {TaskApplication} from '../task-application/application.mjs';
import {createFileBusiness, fileLayoutDigest, TaskBusinessError} from './index.mjs';

const hash = value => digest(encode(value));
const frozenCopy = value => JSON.parse(encode(value).toString());
const planDigest = 'sha256:' + 'a'.repeat(64);
const started = {executionId: 'execution-one', startedAt: '2026-09-08T00:00:00.000Z'};
function fixture(t, overrides = {}) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-task-business-')));
  const parent = path.join(root, 'executions'); fs.mkdirSync(parent, {mode: 0o700});
  const depot = ArtifactDepot.create(path.join(root, 'depot'));
  const layouts = new Map(), bindings = new Map();
  const business = createFileBusiness({parent, depot, layoutFor: ticket => layouts.get(ticket.nodeId),
    approvedLayout: ticket => bindings.get(ticket.nodeId), observeExecution: () => started, ...overrides});
  t.after(() => { business.close(); depot.close(); fs.rmSync(root, {recursive: true, force: true}); });
  return {business, depot, parent, layouts, bindings,
    layout(nodeId, value) { layouts.set(nodeId, value); bindings.set(nodeId, {nodeId, planDigest, layoutDigest: fileLayoutDigest(value)}); }};
}
function ticket(options = {}) {
  const nodeId = options.nodeId ?? 'author', role = options.role ?? 'author', planner = role === 'planner';
  const node = {id: nodeId, role, goal: '完整节点目标', scope: ['描述不是权限'], providerId: 'agent'};
  const plan = planner ? null : {taskId: 'task-one', digest: planDigest, revision: 1, summary: '完整批准方案',
    nodes: [node, {id: 'source', role: 'author', goal: '准备数据', scope: [], providerId: 'agent'}],
    edges: options.upstream?.length ? [{from: 'source', to: nodeId}] : [], budget: {timeoutMs: 300000, maxWorkers: 2, maxAttempts: 8},
    deliverables: ['结果文件'], acceptance: ['独立核对数值'], assumptions: []};
  const input = {task: {intent: '请交付分析与实现，不是固定订单', context: {text: '完整业务上下文', inputRefs: (options.inputArtifacts ?? []).map(value => value.id)},
    requirements: {deliverables: ['结果文件'], acceptance: ['独立核对数值']}},
  node, plan, upstream: options.upstream ?? [], inputArtifacts: options.inputArtifacts ?? []};
  const value = {workerId: options.workerId ?? 'worker-one', taskId: 'task-one', nodeId, role, providerId: 'agent', commandId: 'command-one',
    generation: '1', inputDigest: hash(input), planDigest: planner ? null : planDigest, deadline: Date.now() + 60000, input};
  return {...value, reservationDigest: hash(value)};
}
const context = ticket => ({signal: new AbortController().signal, deadline: ticket.deadline});
const result = (extra = {}) => ({providerId: 'agent', status: 'completed', stopReason: 'end_turn', outputText: '已生成候选，尚未独立验收。',
  cleanup: {cleaned: true, started}, ...extra});
const errorCode = code => error => error instanceof TaskBusinessError && error.code === code;

test('full prompt, real input materialization, candidate-only collection and serialized downstream reconstruction', async t => {
  const f = fixture(t), artifact = {id: 'input-one', kind: 'input', taskId: null, status: 'ready', ...f.depot.put(Buffer.from('甲,17\n乙,29\n'))};
  const first = ticket({nodeId: 'source', inputArtifacts: [artifact]}), ctx = context(first);
  f.layout('source', {inputs: [{path: 'inputs/data.csv', source: {kind: 'input', id: artifact.id}}], allowedPaths: ['summary.json']});
  const prepared = await f.business.prepare(first, ctx);
  for (const text of ['请交付分析与实现', '完整业务上下文', '完整批准方案', '完整节点目标', '保留并使用原生工具/Skill']) assert.ok(prepared.prompt.includes(text));
  assert.deepEqual(Object.keys(prepared).sort(), ['cwd', 'onPermission', 'prompt']);
  assert.equal(fs.readFileSync(path.join(prepared.cwd, 'inputs/data.csv'), 'utf8'), '甲,17\n乙,29\n');
  fs.writeFileSync(path.join(prepared.cwd, 'summary.json'), '{"sum":46}');
  const candidate = (await f.business.collect(first, result(), ctx)).result;
  assert.equal(candidate.profile, 'task-file-business/v1'); assert.equal(candidate.report, result().outputText);
  assert.equal(candidate.layoutDigest, fileLayoutDigest(f.layouts.get('source'))); assert.equal(Object.isFrozen(candidate.files), true);
  assert.equal(candidate.status, undefined); assert.equal(candidate.decision, undefined);
  const downstream = ticket({workerId: 'worker-two', upstream: [{workerId: first.workerId, nodeId: 'source', result: frozenCopy(candidate)}]});
  f.layout('author', {inputs: [{path: 'inputs/summary.json', source: {kind: 'upstream', workerId: first.workerId, path: 'summary.json'}}], allowedPaths: ['result.txt']});
  fs.rmSync(prepared.cwd, {recursive: true}); // Downstream must use depot, not this directory.
  const next = await f.business.prepare(downstream, context(downstream));
  assert.equal(fs.readFileSync(path.join(next.cwd, 'inputs/summary.json'), 'utf8'), '{"sum":46}');
  f.business.release(downstream); assert.equal(fs.existsSync(next.cwd), true);
});

test('missing/stale approval and changed layout never materialize a directory', async t => {
  for (const mode of ['missing', 'plan', 'node', 'layout', 'scope']) {
    const f = fixture(t), work = ticket();
    f.layout('author', {inputs: [], allowedPaths: ['out.txt']});
    if (mode === 'missing') f.bindings.delete('author');
    if (mode === 'plan') f.bindings.get('author').planDigest = 'sha256:' + 'b'.repeat(64);
    if (mode === 'node') f.bindings.get('author').nodeId = 'foreign';
    if (mode === 'layout') f.layouts.get('author').allowedPaths.push('extra.txt');
    if (mode === 'scope') f.layouts.set('author', {scope: ['**/*']});
    await assert.rejects(f.business.prepare(work, context(work)), error => error instanceof TaskBusinessError);
    assert.deepEqual(fs.readdirSync(f.parent), []);
  }
});

test('cleanup and current execution identity must match before any file collection', async t => {
  for (const mode of ['unknown', 'foreign', 'started-at', 'failed', 'report-forgery']) {
    const f = fixture(t), work = ticket(), ctx = context(work);
    f.layout('author', {inputs: [], allowedPaths: ['out.txt']});
    const prepared = await f.business.prepare(work, ctx); fs.writeFileSync(path.join(prepared.cwd, 'out.txt'), 'candidate');
    const outcome = result();
    if (mode === 'unknown') outcome.cleanup = {cleaned: false, started};
    if (mode === 'foreign') outcome.cleanup = {cleaned: true, started: {...started, executionId: 'foreign'}};
    if (mode === 'started-at') outcome.cleanup = {cleaned: true, started: {...started, startedAt: '2026-09-09T00:00:00.000Z'}};
    if (mode === 'failed') outcome.status = 'failed';
    if (mode === 'report-forgery') { outcome.cleanup = null; outcome.outputText = JSON.stringify({cleanup: {cleaned: true, started}}); }
    await assert.rejects(f.business.collect(work, outcome, ctx), error => error instanceof TaskBusinessError);
    assert.equal(fs.readFileSync(path.join(prepared.cwd, 'out.txt'), 'utf8'), 'candidate');
    assert.deepEqual(fs.readdirSync(path.join(f.parent, '../depot')), ['format.json']);
  }
});

test('planner returns only bounded proposal for Core, with zero unapproved output paths', async t => {
  const f = fixture(t), work = ticket({role: 'planner', nodeId: 'planning'}), ctx = context(work);
  f.layout('planning', {inputs: [], allowedPaths: []});
  const prepared = await f.business.prepare(work, ctx);
  assert.ok(prepared.prompt.includes('不固定作者数量'));
  const proposal = {summary: '两个独立交付点', nodes: [{id: 'only-node', role: 'author', goal: '实现', scope: [], providerId: null}],
    edges: [], deliverables: ['out.txt'], acceptance: ['实际正确'], assumptions: []};
  const collected = await f.business.collect(work, result({outputText: '```json\n' + JSON.stringify(proposal) + '\n```'}), ctx);
  assert.deepEqual(collected.plan, proposal); assert.deepEqual(collected.result.files, []);
  assert.equal(collected.plan.digest, undefined);
});

test('planner ambiguous/oversized/control output and filesystem writes are rejected', async t => {
  for (const mode of ['duplicate', 'authority', 'oversize', 'file']) {
    const f = fixture(t), work = ticket({role: 'planner', nodeId: 'planning'}), ctx = context(work);
    f.layout('planning', {inputs: [], allowedPaths: []});
    const prepared = await f.business.prepare(work, ctx);
    let raw = '{"summary":"a","nodes":[{}],"edges":[],"deliverables":[],"acceptance":[]}';
    if (mode === 'duplicate') raw = raw.replace('"summary":"a"', '"summary":"a","summary":"b"');
    if (mode === 'authority') raw = raw.replace('"summary":"a"', '"summary":"a","approved":true');
    if (mode === 'oversize') raw = 'a'.repeat(65537);
    if (mode === 'file') fs.writeFileSync(path.join(prepared.cwd, 'hidden-work.txt'), 'unauthorized');
    await assert.rejects(f.business.collect(work, result({outputText: raw}), ctx), error => error instanceof TaskBusinessError);
  }
});

test('cancel/deadline and release do not accept late permission or late candidate', async t => {
  let accept;
  const f = fixture(t, {authorize: () => new Promise(resolve => { accept = resolve; })}), work = ticket(), abort = new AbortController();
  const ctx = {signal: abort.signal, deadline: work.deadline};
  f.layout('author', {inputs: [], allowedPaths: ['out.txt']});
  const prepared = await f.business.prepare(work, ctx);
  const permission = prepared.onPermission({toolCall: {title: 'read'}, options: []}, {});
  abort.abort(); accept({outcome: {outcome: 'selected', optionId: 'allow'}});
  assert.deepEqual(await permission, {outcome: {outcome: 'cancelled'}});
  await assert.rejects(f.business.collect(work, result(), ctx), errorCode('business_stopped'));
  f.business.release(work); assert.equal(fs.existsSync(prepared.cwd), true);
  const other = fixture(t), expired = ticket(); expired.deadline = Date.now() - 1;
  expired.reservationDigest = hash(Object.fromEntries(Object.entries(expired).filter(([key]) => key !== 'reservationDigest')));
  await assert.rejects(other.business.prepare(expired, context(expired)), errorCode('business_stopped'));
  assert.deepEqual(fs.readdirSync(other.parent), []);
});

test('default permissions deny and release forbids collecting even if execution later ends', async t => {
  const f = fixture(t), work = ticket(), ctx = context(work);
  f.layout('author', {inputs: [], allowedPaths: []});
  const prepared = await f.business.prepare(work, ctx);
  assert.deepEqual(await prepared.onPermission({}, {}), {outcome: {outcome: 'cancelled'}});
  f.business.release(work); f.business.release(work);
  await assert.rejects(f.business.collect(work, result(), ctx), errorCode('business_ticket_mismatch'));
  assert.equal(fs.existsSync(prepared.cwd), true);
});

test('altered ticket, unknown artifacts, input drift and excess/missing outputs preserve evidence', async t => {
  for (const mode of ['ticket', 'unknown', 'drift', 'extra', 'missing']) {
    const f = fixture(t), artifact = {id: 'input-one', kind: 'input', taskId: null, status: 'ready', ...f.depot.put(Buffer.from('input'))};
    const work = ticket({inputArtifacts: [artifact]}), ctx = context(work);
    f.layout('author', {inputs: [{path: 'inputs/source.txt', source: {kind: 'input', id: mode === 'unknown' ? 'unknown' : artifact.id}}], allowedPaths: ['out.txt']});
    if (mode === 'ticket') work.input.task.intent = 'changed';
    if (['ticket', 'unknown'].includes(mode)) {
      await assert.rejects(f.business.prepare(work, ctx), error => error instanceof TaskBusinessError); continue;
    }
    const prepared = await f.business.prepare(work, ctx);
    if (mode !== 'missing') fs.writeFileSync(path.join(prepared.cwd, 'out.txt'), 'candidate');
    if (mode === 'drift') { const file = path.join(prepared.cwd, 'inputs/source.txt'); fs.chmodSync(file, 0o600); fs.writeFileSync(file, 'changed'); }
    if (mode === 'extra') fs.writeFileSync(path.join(prepared.cwd, 'extra.txt'), 'unapproved');
    await assert.rejects(f.business.collect(work, result(), ctx), error => error instanceof TaskBusinessError);
    assert.equal(fs.existsSync(prepared.cwd), true);
  }
});

test('real Application reservation -> business planner -> original Core awaits explicit approval', async t => {
  const f = fixture(t), store = Store.create(path.join(f.parent, '../state'));
  t.after(() => store.close());
  const owner = store.claimOwner(0, 'server', Date.now() + 300000);
  const app = new TaskApplication({store, owner, execution: {providerIds: ['agent'], defaultProvider: 'agent'}});
  const created = await app.dispatch({operation: 'task.create', key: 'create', body: {intent: '开发任意文件交付'}}, {principal: 'local-operator'});
  const command = app.execution.poll().items[0], work = app.execution.nextWork(command.id, command.revision), ctx = context(work);
  f.layout('planning', {inputs: [], allowedPaths: []});
  await f.business.prepare(work, ctx); app.execution.started(work, started);
  const plan = {summary: '可审查方案', nodes: [{id: 'author', role: 'author', goal: '交付说明', scope: [], providerId: 'agent'}],
    edges: [], budget: {timeoutMs: 300000, maxWorkers: 1, maxAttempts: 4}, deliverables: ['说明文档'], acceptance: ['内容准确']};
  const outcome = result({outputText: JSON.stringify(plan)});
  const collected = await f.business.collect(work, outcome, ctx);
  app.execution.finish(work, {...outcome, ...collected});
  const observed = await app.dispatch({operation: 'task.get', taskId: created.id}, {principal: 'local-operator'});
  assert.equal(observed.status, 'awaiting-approval');
  assert.deepEqual(observed.allowedActions, ['approve', 'cancel']);
  assert.equal(app.execution.poll().items.length, 0);
});

test('approval drift after preparation rejects before any candidate blob is persisted', async t => {
  const f = fixture(t), work = ticket(), ctx = context(work);
  f.layout('author', {inputs: [], allowedPaths: ['out.txt']});
  const prepared = await f.business.prepare(work, ctx); fs.writeFileSync(path.join(prepared.cwd, 'out.txt'), 'candidate');
  f.bindings.get('author').layoutDigest = 'sha256:' + 'f'.repeat(64);
  await assert.rejects(f.business.collect(work, result(), ctx), errorCode('business_unapproved_layout'));
  assert.deepEqual(fs.readdirSync(path.join(f.parent, '../depot')), ['format.json']);
  assert.equal(fs.existsSync(path.join(prepared.cwd, 'out.txt')), true);
});

test('unsafe explicit output paths never enter a prompt or create an execution directory', async t => {
  for (const names of [['../escape'], ['/absolute'], ['.env'], ['A/x', 'a/y'], ['a', 'a/b'], ['a\\b'], ['e\u0301']]) {
    const f = fixture(t), work = ticket(), layout = {inputs: [], allowedPaths: names};
    f.layouts.set('author', layout);
    assert.throws(() => fileLayoutDigest(layout), errorCode('business_invalid_layout'));
    await assert.rejects(f.business.prepare(work, context(work)), errorCode('business_invalid_layout'));
    assert.deepEqual(fs.readdirSync(f.parent), []);
  }
});
