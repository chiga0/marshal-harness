import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {setTimeout as pause} from 'node:timers/promises';
import {startTaskService} from './composition.mjs';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createFileBusiness} from '../task-business/index.mjs';
import {TaskClient} from '../task-client/index.mjs';
import {createLeaderPort, createReviewPort, createVerificationPort, renderLeaderPrompt, renderReviewPrompt, parseManagedOutput} from '../task-application/application.mjs';
import {createVerificationCommand} from '../task-verification-command/index.mjs';
import {createLocalReportPublication} from '../task-publication-report/index.mjs';
import {encode, digest} from '../task-store/store.mjs';
const hash = value => digest(encode(value)), here = value => fileURLToPath(new URL(value, import.meta.url));
async function until(read, predicate, label) {const until = Date.now() + 60000; for (;;) {const value = await read();
  if (predicate(value)) return value; assert.ok(Date.now() < until, label + ': ' + JSON.stringify(value)); await pause(20);}}
const expected = ticket => ['east', 'west'].map(nodeId => ({nodeId, region: ticket.input.leaderReplies[0].answer, value: JSON.parse(ticket.input.task.context.text)[nodeId]}));
const bindPlan = () => ({nodeId: 'verify', description: '原地区和两个不同业务值都须独立检查',
  layouts: ['east', 'west'].map(nodeId => ({nodeId, inputs: [], allowedPaths: [nodeId + '.json']})).concat({nodeId: 'verify', allowedPaths: [],
    inputs: ['east', 'west'].map(nodeId => ({path: nodeId + '.json', source: {kind: 'upstream', nodeId, path: nodeId + '.json'}}))}),
  deliveries: ['east', 'west'].map(nodeId => ({nodeId, path: nodeId + '.json', targetPath: nodeId + '.json'}))});

for (const publishing of [false, true]) test('real HTTP + SQLite + original ACP/guard: same fixed business config, two requirements; publication=' + publishing,
  {timeout: 150000}, async t => {
    const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-http-'))), root = path.join(parent, 'service');
    const reportParent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-leader-target-'))), reportRoot = path.join(reportParent, 'reports');
    fs.mkdirSync(reportRoot, {mode: 0o700});
    let service, reader, publication; const diagnostics = [];
    t.after(async () => {if (service) await service.shutdown(); publication?.close(); if (reader) await new Promise(resolve => reader.close(resolve));
      fs.rmSync(parent, {recursive: true, force: true}); fs.rmSync(reportParent, {recursive: true, force: true});});
    if (publishing) {reader = http.createServer((request, response) => {
      if (!/^\/[A-Za-z0-9_-]+\.json$/.test(request.url)) {response.writeHead(404); response.end(); return;}
      try {const content = fs.readFileSync(path.join(reportRoot, request.url.slice(1))); response.writeHead(200, {'Content-Type': 'application/json'}); response.end(content);}
      catch {response.writeHead(404); response.end();}
    }); await new Promise(resolve => reader.listen(0, '127.0.0.1', resolve));}
    const readBaseURL = publishing ? `http://127.0.0.1:${reader.address().port}/` : null;
    const createPublication = () => createLocalReportPublication({id: 'reports', root: reportRoot, readBaseURL,
      policy: {profile: 'task-local-json-report/v1', id: 'report-policy', version: '1'}});
    if (publishing) publication = createPublication();
    const reviewPolicy = {id: 'review', version: '1', description: '独立只读逐项业务要求和完整材料'},
      leaderPolicy = {profile: 'task-managed-leader/v1', maxCalls: 9, maxActions: 4, maxRequests: 4,
        repair: {nodeIds: ['east', 'west'], maxRounds: 1}, review: {providerId: 'test-agent', policyDigest: hash(reviewPolicy)},
        publication: publishing ? {targetId: publication.id, policyDigest: publication.policyDigest} : null};
    const leader = createLeaderPort({id: 'leader', providerId: 'test-agent', policy: leaderPolicy, parseDecision: parseManagedOutput,
      prepare: ({input}) => ({prompt: renderLeaderPrompt(input)})});
    const review = createReviewPort({id: 'review', providerId: 'test-agent', policy: reviewPolicy, parseReport: parseManagedOutput,
      prepare: ({input}) => ({prompt: renderReviewPrompt(input)})});
    const native = createAcpProvider({id: 'test-agent', executable: process.execPath, args: [here('./leader-agent.fixture.mjs')], env: {},
      custodyProfile: {id: 'fixture-inherited-v1', scope: 'inherited-process-group', eligible: true}});
    const verifyPolicy = {id: 'two-requirements', version: '1', description: '固定机制按本Task原业务输入独立核对'}, checkerPath = here('./leader-checker.fixture.mjs');
    const command = createVerificationCommand({executable: process.execPath, checkerPath, checkerDigest: digest(fs.readFileSync(checkerPath)), policyDigest: hash(verifyPolicy),
      assertions: [{name: 'both-original-requirements', validate: (actual, {ticket}) => hash(actual) === hash(expected(ticket))}],
      delivery: ({prepared}) => ({name: 'report.json', mediaType: 'application/json', content: encode(['east', 'west'].map(id => JSON.parse(fs.readFileSync(path.join(prepared.cwd, id + '.json')))))})});
    const verification = createVerificationPort({id: 'check', policy: verifyPolicy, bindPlan, start: command.start,
      publicationExpected: ({ticket}) => expected(ticket)});
    const config = {root, providers: new Map([[native.id, native]]), custody: {profile: 'node-execution-custody/v1'}, leader, review, verification,
      applicationOptions: {defaultLimits: {timeoutMs: 120000, maxAttempts: 17, maxWorkers: 3}, execution: {maxWorkers: 3}},
      supervisorOptions: {intervalMs: 10}, onDiagnostic: value => diagnostics.push(value),
      businessFactory: ({depot, executionParent, approvedLayout, observeExecution}) => createFileBusiness({parent: executionParent, depot, approvedLayout, observeExecution,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout})};
    const connect = async mode => {service = await startTaskService({...config, mode, ...(publication ? {publication} : {})});
      const connection = JSON.parse(fs.readFileSync(service.connectionFile)); return new TaskClient({baseURL: connection.url, token: connection.token});};
    let client = await connect('create'); const completed = [];
    for (const [caseId, region, east, west] of [['one', 'north', 10, 20], ['two', 'south', 30, 50]]) {
      const task = await client.request('task.create', {idempotencyKey: 'create-' + caseId,
        body: {intent: '完整交付' + caseId + '的原始两地区业务要求', context: {text: JSON.stringify({east, west})},
          requirements: {deliverables: ['east报告', 'west报告'], acceptance: ['必须保留east原业务值', '必须保留west原业务值']}}});
      const get = () => client.request('task.get', {path: {taskId: task.id}}), leaderView = () => client.request('task.leader', {path: {taskId: task.id}});
      let current = await until(get, value => value.status === 'awaiting-answer' || value.status === 'failed', 'leader intake');
      assert.equal(current.status, 'awaiting-answer', JSON.stringify(diagnostics)); let view = await leaderView();
      const answer = {path: {taskId: task.id, requestId: view.pendingRequest.id}, idempotencyKey: 'answer-' + caseId,
        body: {expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest, answer: region}};
      await client.request('task.leader.reply', answer); assert.equal((await client.request('task.leader.reply', answer)).replayed, true);
      current = await until(get, value => ['awaiting-approval', 'failed'].includes(value.status), 'plan'); assert.equal(current.status, 'awaiting-approval');
      const plan = await client.request('task.plan', {path: {taskId: task.id}});
      await client.request('task.approve', {path: {taskId: task.id}, idempotencyKey: 'approve-' + caseId,
        body: {expectedRevision: current.revision, planRevision: plan.revision, planDigest: plan.digest}});
      if (publishing) {
        current = await until(get, value => ['awaiting-confirmation', 'failed', 'intervention'].includes(value.status), 'publication consent');
        assert.equal(current.status, 'awaiting-confirmation', JSON.stringify(diagnostics)); view = await leaderView();
        assert.equal(view.pendingRequest.kind, 'publication'); assert.equal(fs.readdirSync(reportRoot).length, completed.length);
        await client.request('task.leader.reply', {path: {taskId: task.id, requestId: view.pendingRequest.id}, idempotencyKey: 'publish-' + caseId,
          body: {expectedRevision: current.revision, requestDigest: view.pendingRequest.requestDigest, decision: 'allow'}});
      }
      current = await until(get, value => ['completed', 'failed', 'intervention'].includes(value.status), 'whole delivery');
      assert.equal(current.status, 'completed', JSON.stringify(diagnostics)); view = await leaderView(); assert.equal(view.review.verdict, 'accept');
      const artifacts = await Promise.all(current.artifactIds.map(artifactId => client.request('artifact.get', {path: {artifactId}})));
      const delivery = artifacts.find(value => value.kind === 'delivery'), content = (await client.downloadArtifact(delivery.id)).content;
      assert.equal(digest(content), delivery.digest); assert.deepEqual(JSON.parse(content), [{nodeId: 'east', region, value: east}, {nodeId: 'west', region, value: west}]);
      if (publishing) {assert.equal(view.publication.status, 'succeeded'); assert.equal(view.postverify.status, 'succeeded');}
      completed.push({taskId: task.id, view: encode(view), content: Buffer.from(content), deliveryId: delivery.id});
    }
    assert.equal((await service.shutdown()).shutdownClean, true); service = null;
    if (publication) {const pinned = publication.configurationDigest; publication.close(); publication = createPublication(); assert.equal(publication.configurationDigest, pinned);}
    client = await connect('open');
    for (const item of completed) {assert.equal((await client.request('task.get', {path: {taskId: item.taskId}})).status, 'completed');
      assert.deepEqual(encode(await client.request('task.leader', {path: {taskId: item.taskId}})), item.view); assert.deepEqual(Buffer.from((await client.downloadArtifact(item.deliveryId)).content), item.content);}
    assert.equal((await service.shutdown()).shutdownClean, true); service = null;
  });
