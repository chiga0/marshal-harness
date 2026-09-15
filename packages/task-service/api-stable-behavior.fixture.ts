// Test-only composition; real HTTP/SQLite/Supervisor/ACP/owned Node processes.
// No models, credentials, hand-written terminal facts or production defaults.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {setTimeout as delay} from 'node:timers/promises';
import {performance} from 'node:perf_hooks';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createVerificationPort} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
import {policy, bindPlan} from '../task-team-integration/scenario.fixture.mjs';
import {startTaskService} from './composition.mjs';

const here = value => fileURLToPath(new URL(value, import.meta.url));
export const data = {rows: [{region: 'east', status: 'paid', cents: 1275}, {region: 'west', status: 'paid', cents: 800},
  {region: 'east', status: 'cancelled', cents: 9000}, {region: 'west', status: 'paid', cents: -250},
  {region: 'east', status: 'paid', cents: 0}, {region: 'west', status: 'cancelled', cents: 100}]};
export const expected = [{region: 'east', count: 2, netCents: 1275}, {region: 'west', count: 2, netCents: 550}];
export async function until(observe, milliseconds = 12000) {
  const deadline = performance.now() + milliseconds;
  for (;;) {const value = await observe(); if (value) return value;
    assert.ok(performance.now() < deadline, 'bounded API behavior observation'); await delay(10);}
}
export async function fixture(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-api-behavior-'))), root = path.join(parent, 'data');
  const prepared = new Map(), executions = [], transports = [];
  let service, client, connection, completed = false;
  const provider = mode => createAcpProvider({id: 'fixture-acp', executable: process.execPath,
    args: [here('../task-team-integration/agent.fixture.mjs')], env: {TEAM_FIXTURE_MODE: mode}});
  const good = provider('good'), held = provider('hang');
  function observe(handle, ticket, kind) {
    const record = {ticket, kind, started: null, completion: null}; executions.push(record);
    return {...handle, started: handle.started.then(value => {record.started = value; return value;}),
      completion: handle.completion.then(value => {record.completion = value; return value;})};
  }
  const agent = {id: good.id, start(input) {
    const ticket = prepared.get(input.cwd); assert.ok(ticket);
    return observe((ticket.input.task.intent === 'api authors held' ? held : good).start(input), ticket, 'agent');
  }};
  const checkerPath = here('./custody-recovery.worker.fixture.mjs');
  const checker = hold => createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)),
    policyDigest: digest(encode(policy)), env: hold ? {MARSHAL_CUSTODY_CHECKER_HOLD: '1'} : {},
    assertions: [{name: 'regions', validate: actual => {try {assert.deepEqual(actual, expected); return true;} catch {return false;}}}],
    delivery: ({prepared}) => ({name: 'regions.json', mediaType: 'application/json', content: encode({files: ['east', 'west']
      .map(region => ({path: region + '.json', content: fs.readFileSync(path.join(prepared.cwd, region + '.json'), 'utf8')}))})})});
  const normalChecker = checker(false), heldChecker = checker(true);
  const verification = createVerificationPort({id: 'independent-checker', policy, bindPlan,
    start(input) {return observe((input.ticket.input.task.intent === 'api verifier held' ? heldChecker : normalChecker).start(input), input.ticket, 'verification');}});
  function newClient() {
    return new TaskClient({baseURL: connection.url, token: connection.token, timeoutMs: 10000, fetch: async (url, init) => {
      // Force actual connection turnover; this is ordinary fetch, not a new SDK.
      const response = await fetch(url, {...init, headers: {...init.headers, Connection: 'close'}});
      transports.push({url: new URL(url).pathname, status: response.status, connection: response.headers.get('connection')});
      return response;
    }});
  }
  async function open(mode) {
    service = await startTaskService({root, mode, providers: new Map([[agent.id, agent]]), verification,
      applicationOptions: {execution: {maxWorkers: 3}}, supervisorOptions: {intervalMs: 10}, requestTimeoutMs: 10000,
      businessFactory: ports => {
        const business = createFileBusiness({parent: ports.executionParent, depot: ports.depot,
          approvedLayout: ports.approvedLayout, observeExecution: ports.observeExecution,
          layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout});
        return {...business, async prepare(ticket, context) {const result = await business.prepare(ticket, context); prepared.set(result.cwd, ticket); return result;},
          release(ticket) {for (const [cwd, entry] of prepared) if (entry.workerId === ticket.workerId) prepared.delete(cwd); business.release(ticket);}};
      }});
    connection = JSON.parse(fs.readFileSync(service.connectionFile)); client = newClient();
  }
  t.after(async () => {
    const shutdown = await service?.shutdown();
    const clean = shutdown?.shutdownClean === true && executions.every(entry => entry.completion?.cleanup?.cleaned === true);
    if (completed && clean) fs.rmSync(parent, {recursive: true, force: true});
    else t.diagnostic('保留私有 API 行为失败现场：' + parent);
    assert.equal(clean, true, 'only original real cleanup permits fixture cleanup');
  });
  await open('create');
  const input = await client.request('input.create', {idempotencyKey: 'sales', body: {name: 'sales.json', mediaType: 'application/json', contentBase64: encode(data).toString('base64')}});
  return {get client() {return client;}, get service() {return service;}, executions, transports,
    get root() {return root;}, newClient,
    async reopen() {assert.equal((await service.shutdown()).shutdownClean, true); await open('open');},
    complete() {completed = true;},
    async approve(intent, key) {
      const body = {intent, context: {inputRefs: [input.id]}, limits: {timeoutMs: 45000, maxAttempts: 4, maxWorkers: 2}};
      const created = await client.createTask(body, key + '-create');
      const task = await until(async () => {const value = await client.getTask(created.id); return value.status === 'awaiting-approval' && value;});
      const plan = await client.request('task.plan', {path: {taskId: task.id}});
      const approval = {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
      const receipt = await client.approveTask(task.id, approval, key + '-approve'); return {created, body, approval, receipt, key};
    }};
}
