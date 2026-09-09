import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {setImmediate as turn} from 'node:timers/promises';
import {Store} from '../task-store/store.mjs';
import {TaskApplication} from '../task-application/application.mjs';
import {TaskSupervisor} from './controller.mjs';

const context = {principal: 'local-operator'};
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
async function until(predicate) {
  const deadline = Date.now() + 3000;
  while (!await predicate()) { assert.ok(Date.now() < deadline, 'bounded deterministic observation timed out'); await turn(); }
}
const plan = () => ({summary: '两作者并行，直接依赖汇合',
  nodes: ['first', 'second', 'review'].map(id => ({id, role: id === 'review' ? 'reviewer' : 'author',
    goal: '交付' + id, scope: [id], providerId: null})),
  edges: [{from: 'first', to: 'review'}, {from: 'second', to: 'review'}],
  deliverables: ['数据程序和文档'], acceptance: ['独立验证最终交付'], assumptions: []});

// Controlled Provider only: these facts describe this fixture, not a real
// process/ACP/OS cleanup or independently accepted business deliverable.
class FakeProvider {
  id = 'fixture'; records = []; autoStarted = true; autoStop = true;
  start(options) {
    const started = deferred(), completion = deferred(), data = JSON.parse(options.prompt);
    const fact = {executionId: 'fixture-' + data.workerId, startedAt: new Date().toISOString()};
    const record = {options, data, fact, stopCount: 0, done: false, startObserved: false,
      announce: () => { record.startObserved = true; started.resolve(fact); },
      finish: (extra = {}) => {
        record.done = true;
        completion.resolve({providerId: this.id, status: 'completed', stopReason: 'end_turn', outputText: 'fixture response',
          cleanup: {started: fact, cleaned: true, scope: 'controlled-fixture', reason: 'fixture-completed'}, ...extra});
      },
      progress: () => options.onProgress({phase: 'running', observedAt: new Date().toISOString(), tool: null})};
    this.records.push(record);
    if (this.autoStarted) record.announce();
    return {started: started.promise, completion: completion.promise, snapshot: () => ({}), stop: () => {
      record.stopCount++;
      this.onStop?.(record);
      if (this.autoStop && !record.done) {
        record.announce(); record.finish({status: 'cancelled', stopReason: 'cancelled'});
      }
      return completion.promise;
    }};
  }
}
function fixture(t, options = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-supervisor-test-'))), root = path.join(parent, 'state');
  let store = Store.create(root), owner = store.claimOwner(0, 'supervisor-test', Date.now() + 3600000);
  const execution = {maxWorkers: options.maxWorkers ?? 2, providerIds: ['fixture'], defaultProvider: 'fixture'};
  let ids = 0, offset = 0;
  const clock = () => Date.now() + offset, makeId = prefix => prefix + '-' + String(++ids).padStart(6, '0');
  let app = new TaskApplication({store, owner, execution, clock, makeId});
  const provider = new FakeProvider(), errors = [], controllers = [];
  const makeController = extra => {
    const supervisor = new TaskSupervisor({execution: app.execution, providers: new Map([[provider.id, provider]]),
      prepare: ticket => ({cwd: parent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role, nodeId: ticket.nodeId})}),
      collect: ticket => ticket.role === 'planner' ? {plan: plan()} : {result: {nodeId: ticket.nodeId, candidate: true}},
      onError: error => errors.push(error), clock, ...extra});
    controllers.push(supervisor); return supervisor;
  };
  t.after(async () => {
    provider.autoStop = true;
    for (const record of provider.records) if (!record.done) { record.announce(); record.finish({status: 'cancelled', stopReason: 'cancelled'}); }
    for (const controller of controllers) await controller.close();
    store.close(); fs.rmSync(parent, {recursive: true, force: true});
  });
  return {parent, provider, errors, makeController, advance(milliseconds) {offset += milliseconds;},
    get app() { return app; }, get store() { return store; },
    read(callback) {return store.read(owner, callback);},
    create(key = 'create') {return app.dispatch({operation: 'task.create', key,
      body: {intent: '交付数据程序与说明', limits: {timeoutMs: 30000, maxAttempts: 8, maxWorkers: 2}}}, context);},
    get(taskId) {return app.dispatch({operation: 'task.get', taskId}, context);},
    async control(operation, taskId, extra = {}) {
      const task = await this.get(taskId);
      return app.dispatch({operation, taskId, key: operation.replace('.', '-') + '-' + task.revision,
        body: {expectedRevision: task.revision, ...extra}}, context);
    },
    async approve(taskId) {
      const task = await this.get(taskId);
      return this.control('task.approve', taskId, {planRevision: task.plan.revision, planDigest: task.plan.digest});
    },
    capacity() {return store.read(owner, tx => app.execution.capacity(tx).value.active);},
    reopen() {
      store.close(); store = Store.openExisting(root); owner = store.claimOwner(owner.generation, 'reopened-supervisor', Date.now() + 3600000);
      app = new TaskApplication({store, owner, execution, clock, makeId});
    }};
}
async function approved(f, supervisor) {
  const task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  f.provider.records[0].finish(); await until(async () => (await f.get(task.id)).status === 'awaiting-approval');
  const operation = await f.approve(task.id); return {task, operation};
}

test('optional input audit failure does not authorize, block or retry the original execution', async t => {
  const f = fixture(t), observations = [];
  const execution = new Proxy(f.app.execution, {get(target, name) {
    if (name === 'observeInput') return (ticket, stage) => {observations.push({workerId: ticket.workerId, stage}); throw Error('private-audit-store-failure');};
    const value = target[name]; return typeof value === 'function' ? value.bind(target) : value;
  }});
  const supervisor = f.makeController({execution}), task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  f.provider.records[0].finish(); await until(async () => (await f.get(task.id)).status === 'awaiting-approval');
  assert.deepEqual(observations.map(item => item.stage), ['prepared', 'handed-off']);
  assert.equal(new Set(observations.map(item => item.workerId)).size, 1); assert.equal(f.provider.records.length, 1);
  const audit = await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context);
  assert.equal(audit.attempts, 1); assert.equal(audit.prompts[0].observation.stage, 'unavailable'); assert.equal(f.capacity().length, 0);
  assert.equal(f.errors.filter(error => error.code === 'worker_input_audit_unavailable').length, 2);
  assert.equal(JSON.stringify(f.errors).includes('private-audit-store-failure'), false);
});

test('real SQLite/Application: two Workers overlap, fan-in waits and repeated poll never redispatches', async t => {
  const f = fixture(t); let filteredPages = 0;
  const execution = new Proxy(f.app.execution, {get(target, name) {
    if (name === 'poll') return (...args) => {
      const page = target.poll(...args); if (page.items.length === 0 && page.nextCursor) filteredPages++; return page;
    };
    const value = target[name]; return typeof value === 'function' ? value.bind(target) : value;
  }});
  const tickets = [];
  const supervisor = f.makeController({execution, pageSize: 1, prepare: ticket => {
    tickets.push(ticket);
    return {cwd: f.parent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role, nodeId: ticket.nodeId})};
  }});
  const {task, operation} = await approved(f, supervisor);
  await until(async () => { await supervisor.tick(); return f.provider.records.length === 3; });
  const authors = f.provider.records.slice(1);
  assert.deepEqual(authors.map(record => record.data.nodeId).sort(), ['first', 'second']);
  assert.equal(f.capacity().length, 2);
  for (let n = 0; n < 3; n++) {
    const first = supervisor.tick(), duplicate = supervisor.tick(); assert.equal(first, duplicate); await first;
  }
  assert.equal(f.provider.records.length, 3); // Both completions are still pending.
  const query = await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context);
  assert.equal(query.items.filter(worker => worker.status === 'running').length, 2);
  await authors[0].progress(); authors[0].finish();
  await until(() => f.capacity().length === 1); await supervisor.tick(); assert.equal(f.provider.records.length, 3);
  authors[1].finish(); await until(() => f.capacity().length === 0);
  await until(async () => { await supervisor.tick(); return f.provider.records.length === 4; });
  const reviewer = f.provider.records[3]; assert.equal(reviewer.data.nodeId, 'review');
  const reviewTicket = tickets.find(ticket => ticket.nodeId === 'review');
  assert.deepEqual(reviewTicket.input.upstream.map(item => item.result.nodeId).sort(), ['first', 'second']);
  assert.equal(Object.isFrozen(reviewTicket), true); assert.equal(Object.isFrozen(reviewTicket.input), true);
  reviewer.finish(); await until(() => f.capacity().length === 0);
  assert.equal((await f.get(task.id)).status, 'running', 'rounds/candidates do not fabricate Task completed');
  assert.equal((await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context)).attempts, 4);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: operation.id}, context)).status, 'succeeded');
  assert.ok(filteredPages > 0, 'observed commands really produce empty nonfinal pages');
  assert.deepEqual(f.errors, []);
});

test('cancel during preparation records explicit none-start cleanup, no spawn, and closes original Operation', async t => {
  const f = fixture(t), preparation = deferred(); let called = false;
  const supervisor = f.makeController({prepare: (ticket, {signal}) => { called = true; assert.equal(signal.aborted, false); return preparation.promise; }});
  const task = await f.create(); await supervisor.tick(); await until(() => called);
  const operation = await f.control('task.cancel', task.id); await supervisor.tick();
  await until(() => f.capacity().length === 0); await supervisor.tick();
  assert.equal(f.provider.records.length, 0); assert.equal((await f.get(task.id)).status, 'cancelled');
  const worker = (await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context)).items[0];
  const stored = f.read(tx => JSON.parse(tx.projection('attempt', worker.id).bytes));
  assert.equal(stored.cleanup.scope, 'none-start'); assert.equal(stored.cleanup.started, null); assert.equal(stored.cleanup.cleaned, true);
  assert.equal((await f.app.dispatch({operation: 'operation.get', operationId: operation.id}, context)).status, 'succeeded');
  preparation.resolve({cwd: f.parent, prompt: 'late preparation'}); await turn();
  assert.equal(f.provider.records.length, 0); assert.deepEqual(f.errors, []);
});

test('cancel during Provider bootstrap retains handle until original cleanup; close does not pretend to finish', async t => {
  const f = fixture(t); f.provider.autoStarted = false; f.provider.autoStop = false;
  const supervisor = f.makeController(), task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  const record = f.provider.records[0]; await f.control('task.cancel', task.id); await supervisor.tick();
  assert.equal(record.stopCount, 1); assert.equal(f.capacity().length, 1);
  let closed = false; const close = supervisor.close().then(value => { closed = true; return value; });
  await turn(); assert.equal(closed, false);
  record.announce(); await turn(); assert.equal(record.stopCount, 1);
  record.finish(); const result = await close;
  assert.equal(result.clean, true); assert.equal(f.capacity().length, 0);
  assert.equal((await f.get(task.id)).status, 'cancelled'); assert.equal((await f.get(task.id)).plan, null);
});

test('cold owner unresolved obligations never synthesize handles, free capacity or launch again', async t => {
  const f = fixture(t, {maxWorkers: 1}), task = await f.create();
  const command = f.app.execution.poll().items[0];
  const ticket = f.app.execution.nextWork(command.id, command.revision); assert.ok(ticket);
  f.reopen(); const supervisor = f.makeController();
  for (let n = 0; n < 3; n++) await supervisor.tick();
  assert.equal((await f.get(task.id)).status, 'intervention'); assert.equal(f.capacity().length, 1);
  assert.equal(f.provider.records.length, 0);
  await f.create('second'); await supervisor.tick(); assert.equal(f.provider.records.length, 0);
  assert.deepEqual(f.errors, []);
});

test('missing cleanup is unknown, not clean none-start or a retryable launch', async t => {
  const f = fixture(t), supervisor = f.makeController(), task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  f.provider.records[0].finish({cleanup: null});
  await until(async () => (await f.get(task.id)).status === 'intervention');
  for (let n = 0; n < 3; n++) await supervisor.tick();
  assert.equal(f.provider.records.length, 1); assert.equal(f.capacity().length, 1);
  assert.equal((await supervisor.close()).clean, false);
  assert.equal(supervisor.snapshot().owned[0].stage, 'unknown');
});

test('progress waits for started and callback failure stops once, retaining original cleanup', async t => {
  const f = fixture(t); f.provider.autoStarted = false;
  let progressCalls = 0;
  const execution = new Proxy(f.app.execution, {get(target, name) {
    if (name === 'progress') return () => { progressCalls++; throw new Error('private callback body'); };
    const value = target[name]; return typeof value === 'function' ? value.bind(target) : value;
  }});
  const supervisor = f.makeController({execution}), task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  const record = f.provider.records[0], progress = record.progress(); const rejected = assert.rejects(progress);
  await turn(); assert.equal(progressCalls, 0);
  record.announce(); await rejected; await until(() => f.capacity().length === 0);
  assert.equal(record.stopCount, 1); assert.equal(progressCalls, 1); assert.equal(f.errors.length, 1);
  assert.deepEqual(Object.keys(f.errors[0]).sort(), ['code', 'stage', 'taskId', 'workerId']);
  assert.equal(f.errors[0].stage, 'progress');
  const worker = (await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context)).items[0];
  const stored = f.read(tx => JSON.parse(tx.projection('attempt', worker.id).bytes));
  assert.equal(stored.cleanup.started.executionId, record.fact.executionId);
  assert.equal(stored.cleanup.scope, 'controlled-fixture'); assert.equal(stored.worker.status, 'failed');
  await supervisor.tick(); assert.equal(f.provider.records.length, 1); assert.equal(f.errors.length, 1);
});

test('preparation and collection are bounded; expired callbacks cannot later admit a result', async t => {
  for (const phase of ['prepare', 'collect']) await t.test(phase, async t => {
    const f = fixture(t), blocked = deferred(), extra = {[phase]: () => blocked.promise, [phase + 'Ms']: 20};
    const supervisor = f.makeController(extra), task = await f.create(); await supervisor.tick();
    if (phase === 'collect') { await until(() => f.provider.records.length === 1); f.provider.records[0].finish(); }
    await until(() => f.errors.length === 1); await until(() => f.capacity().length === 0);
    assert.equal(supervisor.snapshot().failure, null, 'a Worker failure is not a service failure');
    assert.equal(f.errors.length, 1); assert.equal(f.provider.records.length, phase === 'prepare' ? 0 : 1);
    blocked.resolve(phase === 'prepare' ? {cwd: f.parent, prompt: 'late'} : {plan: plan()}); await turn();
    assert.equal((await f.get(task.id)).plan, null);
    await supervisor.close(); assert.equal((await f.get(task.id)).status, 'failed');
  });
});

test('one Task prepare/collect/provider failure cannot stop another Task or close service admission', async t => {
  for (const failure of ['prepare-timeout', 'collect-exception', 'malformed-progress', 'foreign-cleanup', 'provider-failed']) await t.test(failure, async t => {
    const f = fixture(t), bad = await f.create('bad'), good = await f.create('good');
    const supervisor = f.makeController({prepareMs: 20, prepare: ticket => {
      if (ticket.taskId === bad.id && failure === 'prepare-timeout') return new Promise(() => {});
      return {cwd: f.parent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role, nodeId: ticket.nodeId, taskId: ticket.taskId})};
    }, collect: ticket => {
      if (ticket.taskId === bad.id && failure === 'collect-exception') throw new Error('private invalid candidate data');
      return {plan: plan()};
    }});
    await supervisor.tick(); await until(() => f.provider.records.some(record => record.data.taskId === good.id));
    if (failure === 'collect-exception') f.provider.records.find(record => record.data.taskId === bad.id).finish();
    if (failure === 'malformed-progress') await assert.rejects(f.provider.records.find(record => record.data.taskId === bad.id)
      .options.onProgress({phase: 'invented', tool: null}));
    if (failure === 'foreign-cleanup') f.provider.records.find(record => record.data.taskId === bad.id)
      .finish({cleanup: {started: {executionId: 'foreign-execution'}, cleaned: true}});
    if (failure === 'provider-failed') f.provider.records.find(record => record.data.taskId === bad.id)
      .finish({status: 'failed', stopReason: 'max_tokens'});
    await until(() => f.errors.length === 1);
    const unknown = failure === 'foreign-cleanup';
    await until(async () => unknown ? (await f.get(bad.id)).status === 'intervention' : f.capacity().length === 1);
    await supervisor.tick();
    const survivor = f.provider.records.find(record => record.data.taskId === good.id);
    assert.equal(survivor.stopCount, 0); assert.equal(supervisor.snapshot().failure, null);
    assert.equal((await f.get(bad.id)).status, unknown ? 'intervention' : 'failed');
    survivor.finish(); await until(async () => (await f.get(good.id)).status === 'awaiting-approval');
    await f.create('later'); await supervisor.tick();
    await until(() => f.provider.records.some(record => record.data.taskId !== good.id && record.data.taskId !== bad.id));
    assert.equal(f.errors.length, 1); assert.equal(supervisor.snapshot().failure, null);
  });
});

test('failure is durable before stop; delayed cleanup cannot admit same-Task downstream while another Task progresses', async t => {
  const f = fixture(t, {maxWorkers: 3});
  const supervisor = f.makeController({collect: ticket => {
    if (ticket.role !== 'planner') return {result: {nodeId: ticket.nodeId}};
    // The downstream depends ONLY on good; waiting for bad's own result cannot
    // accidentally mask the missing Task-wide failure fence.
    const proposal = plan(); proposal.edges = [{from: 'second', to: 'review'}];
    return {plan: proposal};
  }});
  const {task} = await approved(f, supervisor);
  await until(async () => { await supervisor.tick(); return f.provider.records.length === 3; });
  const bad = f.provider.records.find(record => record.data.nodeId === 'first');
  const good = f.provider.records.find(record => record.data.nodeId === 'second');
  const unrelated = await f.create('unrelated'); await supervisor.tick();
  await until(() => f.provider.records.length === 4);
  const survivor = f.provider.records[3];
  const attempts = (await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context)).attempts;
  f.provider.autoStop = false;
  const stopObservations = [];
  f.provider.onStop = record => {
    if (record === survivor) assert.fail('unrelated Task must not be stopped');
    const stored = f.read(tx => JSON.parse(tx.projection('task', task.id).bytes));
    stopObservations.push(stored.task.status);
    assert.equal(stored.task.status, 'cancelling', 'stop callback must see already committed fence');
    assert.equal(stored.failureCode, 'worker_failed');
  };
  await assert.rejects(bad.options.onProgress({phase: 'invalid', tool: null}));
  assert.deepEqual(stopObservations, ['cancelling', 'cancelling']);
  assert.equal(bad.stopCount, 1); assert.equal(good.stopCount, 1); assert.equal(survivor.stopCount, 0);
  assert.equal(f.capacity().length, 3, 'failure fence is not a cleanup or refund');
  // A sibling's late successful completion cannot reopen eligibility.
  good.finish(); await until(() => f.capacity().length === 2);
  for (let n = 0; n < 3; n++) await supervisor.tick();
  assert.equal(f.provider.records.some(record => record.data.nodeId === 'review'), false);
  assert.equal((await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context)).attempts, attempts);
  assert.equal((await f.get(task.id)).status, 'cancelling');
  survivor.finish(); await until(async () => (await f.get(unrelated.id)).status === 'awaiting-approval');
  assert.equal(supervisor.snapshot().failure, null); assert.equal(f.capacity().length, 1);
  bad.finish(); await until(() => f.capacity().length === 0); await supervisor.tick();
  assert.equal((await f.get(task.id)).status, 'failed');
  const workers = (await f.app.dispatch({operation: 'task.workers', taskId: task.id}, context)).items;
  assert.equal(workers.find(worker => worker.id === bad.data.workerId).status, 'failed');
  assert.equal(workers.find(worker => worker.id === good.data.workerId).status, 'cancelled');
  assert.equal(f.errors.length, 1); f.provider.onStop = null;
});

test('cancel during collection discards a late candidate but preserves original cleanup', async t => {
  const f = fixture(t), collection = deferred(); let signal;
  const supervisor = f.makeController({collect: (_ticket, _result, context) => { signal = context.signal; return collection.promise; }});
  const task = await f.create(); await supervisor.tick(); await until(() => f.provider.records.length === 1);
  const record = f.provider.records[0]; record.finish(); await until(() => signal);
  await f.control('task.cancel', task.id); await supervisor.tick();
  await until(() => f.capacity().length === 0); assert.equal(signal.aborted, true);
  collection.resolve({plan: plan()}); await turn(); await supervisor.tick();
  assert.equal((await f.get(task.id)).status, 'cancelled'); assert.equal((await f.get(task.id)).plan, null);
  assert.equal(record.stopCount, 1); assert.deepEqual(f.errors, []);
});

test('original Task deadline is reconciled before fresh dispatch and stops only owned handles', async t => {
  const f = fixture(t), supervisor = f.makeController(), task = await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  const originalDeadline = f.provider.records[0].options.deadline;
  assert.equal(originalDeadline, Date.parse(task.deadlineAt));
  f.advance(30001); await supervisor.tick(); await until(() => f.capacity().length === 0); await supervisor.tick();
  assert.equal(f.provider.records[0].stopCount, 1); assert.equal(f.provider.records.length, 1);
  assert.equal((await f.get(task.id)).status, 'failed'); assert.equal((await f.get(task.id)).code, 'task_deadline');
});

test('progress queue is bounded before started and notifies one failure without losing cleanup', async t => {
  const f = fixture(t); f.provider.autoStarted = false;
  const supervisor = f.makeController(); await f.create(); await supervisor.tick();
  await until(() => f.provider.records.length === 1);
  const record = f.provider.records[0], observations = Array.from({length: 129}, () => record.progress());
  const results = await Promise.allSettled(observations);
  assert.equal(results.filter(result => result.status === 'rejected').length, 1);
  await until(() => f.capacity().length === 0);
  assert.equal(record.stopCount, 1); assert.equal(f.errors.length, 1); assert.equal(f.errors[0].stage, 'progress');
});

test('paused reserved preparation waits without killing live work or consuming a replacement attempt', async t => {
  const f = fixture(t), gate = deferred();
  const supervisor = f.makeController({prepare: ticket => ticket.role === 'planner'
    ? {cwd: f.parent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role, nodeId: ticket.nodeId})}
    : gate.promise.then(() => ({cwd: f.parent, prompt: JSON.stringify({workerId: ticket.workerId, role: ticket.role, nodeId: ticket.nodeId})}))});
  const {task} = await approved(f, supervisor);
  await until(async () => { await supervisor.tick(); return f.capacity().length === 2; });
  await f.control('task.pause', task.id); gate.resolve(); await turn(); await supervisor.tick();
  assert.equal(f.provider.records.length, 1); assert.equal(f.capacity().length, 2);
  assert.equal((await f.get(task.id)).status, 'paused');
  await f.control('task.resume', task.id); await supervisor.tick();
  await until(() => f.provider.records.length === 3);
  assert.equal((await f.app.dispatch({operation: 'task.audit', taskId: task.id}, context)).attempts, 3);
  assert.deepEqual(f.errors, []);
});

test('self-driven timer makes progress without an external watchdog, then closes all owned cleanup', async t => {
  const f = fixture(t), supervisor = f.makeController({intervalMs: 5});
  const task = await f.create(); supervisor.start();
  await until(() => f.provider.records.length === 1);
  f.provider.records[0].finish(); await until(async () => (await f.get(task.id)).status === 'awaiting-approval');
  await f.approve(task.id); await until(() => f.provider.records.length === 3);
  const report = await supervisor.close(); assert.equal(report.clean, true);
  assert.equal(f.provider.records.slice(1).every(record => record.stopCount === 1), true);
  assert.equal(f.capacity().length, 0); assert.deepEqual(f.errors, []);
});
