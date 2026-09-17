import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {setImmediate as turn} from 'node:timers/promises';
import {createVerificationPort} from '../task-application/application.ts';
import {createFileBusiness} from '../task-business/index.ts';
import {TaskClient} from '../task-client/index.ts';
import {encode} from '../task-store/store.ts';
import {verifyGuide} from '../../scripts/business-postconditions.ts';
import {startTaskService} from './composition.ts';

// This is an explicit, finite S01 contract. It is supplied as an input Artifact
// and is never inferred from the author's candidate or from a natural-language
// summary. The fixture does not make free-form S01 writing a product default.
const guide = {
  profile: 'finite-guide/v1',
  title: '蓝杉读书会',
  schedule: '每周六14:00',
  location: '城市图书馆二层',
  steps: [
    '查看活动时间：每周六14:00。',
    '查看活动地点：城市图书馆二层。',
    '按以上时间和地点参与蓝杉读书会。',
  ],
  notes: [
    '以上时间与地点为已提供信息。',
    '其他安排尚未提供，请勿据此推断。',
  ],
};
const source = Buffer.from(JSON.stringify(guide));
const policy = {
  id: 's01-finite-guide-postcondition-fixture',
  version: '1',
  description: '从冻结的有限 guide contract 独立核对文案；不推断未提供事实，也不执行外部操作。',
};
const proposal = {
  summary: '按已批准的有限 guide contract 生成参与说明并独立核对',
  nodes: [
    {id: 'author', role: 'author', goal: '读取 guide-contract.json，生成 guide.md', scope: ['guide-contract.json'], providerId: null},
    {id: 'verify', role: 'verifier', goal: '从原始 guide contract 和受控候选独立核对有限事实', scope: ['guide-contract.json', 'guide.md'], providerId: null},
  ],
  edges: [{from: 'author', to: 'verify'}],
  deliverables: ['guide.md'],
  acceptance: ['标题、时间、地点、三条编号步骤和两条注意事项必须严格符合已批准有限 contract；不增加费用、设施或其他事实'],
  assumptions: [],
};
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};
const expectedText = [
  '# 蓝杉读书会',
  '时间：每周六14:00',
  '地点：城市图书馆二层',
  '## 参与步骤',
  '1. 查看活动时间：每周六14:00。',
  '2. 查看活动地点：城市图书馆二层。',
  '3. 按以上时间和地点参与蓝杉读书会。',
  '## 注意事项',
  '- 以上时间与地点为已提供信息。',
  '- 其他安排尚未提供，请勿据此推断。',
].join('\n');

async function until(predicate, label) {
  const deadline = Date.now() + 10000;
  let value;
  while (!(value = await predicate())) {
    assert.ok(Date.now() < deadline, label + ': ' + JSON.stringify(value));
    await turn();
  }
}

function bindPlan({inputArtifacts, proposal: plan}) {
  assert.equal(inputArtifacts.length, 1);
  assert.deepEqual(plan.nodes.map(node => node.id), ['author', 'verify']);
  const id = inputArtifacts[0].id;
  return {
    nodeId: 'verify',
    description: policy.description,
    layouts: [
      {nodeId: 'author', inputs: [{path: 'guide-contract.json', source: {kind: 'input', id}}], allowedPaths: ['guide.md']},
      {nodeId: 'verify', inputs: [{path: 'guide.md', source: {kind: 'upstream', nodeId: 'author', path: 'guide.md'}}], allowedPaths: []},
    ],
    deliveries: [{nodeId: 'author', path: 'guide.md', targetPath: 'guide.md'}],
  };
}

function candidateFor(mode) {
  if (mode === 'extra-facts') return expectedText + '\n现场提供签到设施。\n报名费用：100元。';
  if (mode === 'time-drift') return expectedText.replace('时间：每周六14:00', '时间：每周日14:00');
  if (mode === 'location-drift') return expectedText.replace('地点：城市图书馆二层', '地点：城市图书馆三层');
  return expectedText;
}

function fixture(t, mode = 'good') {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-s01-postcondition-')));
  const root = path.join(parent, 'data');
  const byCwd = new Map();
  const services = [];
  const diagnostics = [];
  let activeDepot = null;
  let verifierStarts = 0;
  const fact = ticket => ({executionId: 's01-fixture-' + ticket.workerId, startedAt: new Date().toISOString()});
  const provider = {
    id: 's01-fixture-provider',
    start(input) {
      const ticket = byCwd.get(input.cwd);
      assert.ok(ticket, 'provider receives a bound business directory');
      const started = fact(ticket);
      const completion = deferred();
      if (ticket.role === 'planner') {
        completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
          outputText: JSON.stringify(proposal), cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
      } else {
        // The author only writes bytes. It does not produce an acceptance claim.
        fs.writeFileSync(path.join(input.cwd, 'guide.md'), candidateFor(mode), {flag: 'wx', mode: 0o600});
        completion.resolve({providerId: provider.id, status: 'completed', stopReason: 'end_turn',
          outputText: '候选已写入；不声明业务验收通过。', cleanup: {started, cleaned: true, scope: 'controlled-fixture'}});
      }
      return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
    },
  };

  const verification = createVerificationPort({id: policy.id, policy, bindPlan,
    start({ticket, prepared}) {
      verifierStarts++;
      const started = fact(ticket);
      const completion = deferred();
      const inputRef = ticket.input.inputArtifacts[0];
      // The verifier reads the original input bytes from the trusted Depot. It
      // never validates a copied author input or an author-produced report.
      const contractText = activeDepot.get({digest: inputRef.digest, bytes: inputRef.bytes}).toString('utf8');
      const candidateText = fs.readFileSync(path.join(prepared.cwd, 'guide.md'), 'utf8');
      const report = verifyGuide(JSON.parse(contractText), candidateText);
      const status = report.status === 'pass' ? 'passed' : 'failed';
      const result = {
        type: 'verification',
        status,
        cleanup: {started, cleaned: true, scope: 'controlled-fixture'},
        evidence: {name: 's01-guide-postcondition.json', mediaType: 'application/json', content: encode(report)},
      };
      if (status === 'passed') result.delivery = {name: 'guide.md', mediaType: 'text/markdown', content: Buffer.from(candidateText)};
      completion.resolve(result);
      return {started: Promise.resolve(started), completion: completion.promise, stop() {return completion.promise;}};
    },
  });

  const config = {
    root,
    mode: 'create',
    providers: new Map([[provider.id, provider]]),
    verification,
    businessFactory: ports => {
      activeDepot = ports.depot;
      const business = createFileBusiness({
        parent: ports.executionParent,
        depot: ports.depot,
        layoutFor: ticket => ticket.planDigest === null ? {inputs: [], allowedPaths: []} : ticket.input.fileLayout,
        approvedLayout: ticket => ports.approvedLayout(ticket),
        observeExecution: ports.observeExecution,
      });
      return {
        ...business,
        async prepare(ticket, context) {
          const prepared = await business.prepare(ticket, context);
          byCwd.set(prepared.cwd, ticket);
          return prepared;
        },
        release(ticket) {business.release(ticket);},
        close() {business.close(); activeDepot = null;},
      };
    },
    supervisorOptions: {intervalMs: 5},
    onDiagnostic: value => diagnostics.push(value),
  };
  const start = async modeValue => {
    const service = await startTaskService({...config, mode: modeValue});
    services.push(service);
    const connection = JSON.parse(fs.readFileSync(service.connectionFile));
    return {service, client: new TaskClient({baseURL: connection.url, token: connection.token})};
  };
  t.after(async () => {
    for (const service of services) await service.shutdown();
    fs.rmSync(parent, {recursive: true, force: true});
  });
  return {start, diagnostics, get verifierStarts() {return verifierStarts;}};
}

async function approve(f, key) {
  const input = await f.client.request('input.create', {idempotencyKey: key + '-input', body: {
    name: 'guide-contract.json', mediaType: 'application/json', contentBase64: source.toString('base64'),
  }});
  const created = await f.client.createTask({
    intent: '根据已批准的有限 guide contract 生成参与说明并独立核对。',
    context: {inputRefs: [input.id]},
    limits: {timeoutMs: 30000, maxAttempts: 4, maxWorkers: 2},
  }, key);
  let task;
  await until(async () => {
    task = await f.client.getTask(created.id);
    return task.status === 'awaiting-approval';
  }, 'planner did not produce an approval preview');
  const plan = await f.client.request('task.plan', {path: {taskId: task.id}});
  assert.ok(plan.acceptance.some(value => value.includes(policy.id)));
  await f.client.approveTask(task.id, {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest}, key + '-approve');
  return task.id;
}

test('S01真实HTTP/SQLite链路从冻结guide Artifact独立验收正确文案并交付', {timeout: 30000}, async t => {
  const f = fixture(t);
  const {client} = await f.start('create');
  const taskId = await approve({...f, client}, 's01-good');
  await until(async () => ['completed', 'failed', 'intervention'].includes((await client.getTask(taskId)).status), 'S01 success terminal state');
  const task = await client.getTask(taskId);
  assert.equal(task.status, 'completed');
  assert.equal(task.artifactIds.length, 2);
  const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
  const evidence = artifacts.find(item => item.artifact.kind === 'evidence');
  assert.ok(evidence);
  assert.equal(evidence.artifact.name, 's01-guide-postcondition.json');
  const report = JSON.parse(evidence.content.toString());
  assert.equal(report.profile, 'business-postcondition-experiment/v1');
  assert.equal(report.kind, 'guide');
  assert.equal(report.status, 'pass');
  assert.equal(report.code, 'finite_guide_exact');
  assert.equal(report.authority, false);
  const delivery = artifacts.find(item => item.artifact.kind === 'delivery');
  assert.equal(delivery.artifact.name, 'guide.md');
  assert.equal(delivery.content.toString(), expectedText);
  assert.equal(f.verifierStarts, 1);
  assert.equal((await client.request('task.audit', {path: {taskId}})).acceptance.status, 'passed');
});

for (const [mode, code] of [['extra-facts', 'outside_approved_guide_language'], ['time-drift', 'explicit_fact_mismatch'], ['location-drift', 'explicit_fact_mismatch']]) {
  test(`S01独立后验拒绝${mode === 'extra-facts' ? '额外设施与费用' : mode === 'time-drift' ? '时间事实漂移' : '地点事实漂移'}`, {timeout: 30000}, async t => {
    const f = fixture(t, mode);
    const {client} = await f.start('create');
    const taskId = await approve({...f, client}, 's01-' + mode);
    await until(async () => ['completed', 'failed', 'intervention'].includes((await client.getTask(taskId)).status), 'S01 negative terminal state');
    const task = await client.getTask(taskId);
    assert.equal(task.status, 'failed');
    assert.equal(f.verifierStarts, 1);
    const audit = await client.request('task.audit', {path: {taskId}});
    assert.equal(audit.acceptance.status, 'failed');
    const artifacts = await Promise.all(task.artifactIds.map(id => client.downloadArtifact(id)));
    const evidence = artifacts.find(item => item.artifact.kind === 'evidence');
    assert.ok(evidence);
    const report = JSON.parse(evidence.content.toString());
    assert.equal(report.status, mode === 'extra-facts' ? 'not-verified' : 'fail');
    assert.equal(report.code, code);
    assert.equal(artifacts.some(item => item.artifact.kind === 'delivery'), false);
  });
}
