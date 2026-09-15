// External deterministic ACP peer. It makes no Core receipt and is never packaged.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {createInterface} from 'node:readline';
import {setTimeout as pause} from 'node:timers/promises';
const canonical = value => Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' : value && typeof value === 'object' ?
  '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}' : JSON.stringify(value);
const hash = value => 'sha256:' + createHash('sha256').update(canonical(value)).digest('hex');
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const respond = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
const pending = new Map(); let sequence = 0;
async function permission(sessionId, name, rawInput) {
  const id = 'permission-' + ++sequence;
  const result = new Promise(resolve => pending.set(id, resolve));
  send({jsonrpc: '2.0', id, method: 'session/request_permission', params: {sessionId,
    toolCall: {toolCallId: id, title: name, kind: name === 'read' ? 'read' : 'edit', rawInput},
    options: [{kind: 'allow_once', optionId: 'allow-once', name: '允许一次'}, {kind: 'reject_once', optionId: 'deny-once', name: '拒绝'}]}});
  assert.equal((await result).outcome.optionId, 'allow-once');
}
function windowFor(task, replies) {
  const {startDate, endDate} = JSON.parse(task.context.text);
  if (startDate !== null && endDate !== null) return {startDate, endDate};
  assert.equal(replies.length, 1); return JSON.parse(replies[0].answer);
}
function calculate(rows, region, window) {
  const selected = rows.filter(row => row.region === region && row.status === 'paid' && row.date >= window.startDate && row.date <= window.endDate);
  return {region, ...window, count: selected.length, netCents: selected.reduce((sum, row) => sum + row.cents, 0)};
}
function decision(input) {
  const s = input.snapshot, last = s.evidence.find(item => item.kind === 'verification'), review = s.evidence.find(item => item.kind === 'review');
  const original = JSON.parse(s.task.input.context.text); let actions;
  if ([original.startDate, original.endDate].includes(null) && !s.interactions.replies.length) actions = [{type: 'ask', kind: 'business',
    prompt: '请回复本次日期窗口 JSON（startDate/endDate）', options: [], subject: s.readSet.find(item => item.kind === 'input').digest, nodeIds: []}];
  else if (!s.plan) actions = [{type: 'plan', proposal: {summary: s.task.input.intent,
    nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', goal: '实现原日期窗口的' + id,
      scope: id === 'verify' ? ['east.json', 'west.json'] : ['sales.json', id + '.json'], providerId: null})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: ['east.json', 'west.json'],
    acceptance: s.task.input.requirements.acceptance, assumptions: []}}];
  else if (!review) actions = [{type: 'work', kind: 'review', nodeIds: s.selection.map(item => item.nodeId), selectionDigest: hash(s.selection)}];
  else if (!last) actions = [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(s.selection)}];
  else if (s.obligation.some(item => item.reason === 'postverify-finished')) actions = [{type: 'conclude', outcome: 'succeeded',
    summary: s.task.input.intent + ' 原窗口报告经独立Review、验证、用户授权与真实GET后验，完成。', basisDigests: [last.digest, review.digest]}];
  else actions = [{type: 'deliver', artifactId: input.materials.find(item => item.kind === 'delivery').id, acceptanceDigest: last.digest, reviewDigest: review.digest}];
  return {profile: input.profile, callId: input.callId, inputDigest: input.inputDigest, summary: '消费原业务要求和回答', actions};
}
async function prompt(request) {
  const text = request.params.prompt[0].text; let output;
  if (text.includes('\n完整冻结输入：')) {
    const input = JSON.parse(text.split('\n完整冻结输入：').at(-1));
    assert.ok(input.snapshot.task.input.requirements.acceptance.every(value => text.includes(value)));
    assert.ok(input.materials.some(item => item.kind === 'input' && JSON.parse(item.content).rows.length > 0), 'original upload expanded');
    if (input.profile === 'task-managed-leader/v1') output = decision(input);
    else {
      assert.equal(input.profile, 'task-independent-review/v1');
      const rows = JSON.parse(input.materials.find(item => item.kind === 'input').content).rows;
      const files = input.materials.filter(item => item.nodeId), window = windowFor(input.snapshot.task.input, input.snapshot.interactions.replies);
      assert.equal(files.length, 2);
      for (const item of files) assert.deepEqual(JSON.parse(item.content), calculate(rows, item.nodeId, window));
      output = {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
        verdict: 'accept', summary: '独立按原流水及原用户窗口核对两份候选、负额和零额计数均正确', findings: []};
    }
  } else {
    const input = JSON.parse(text.slice(text.indexOf('{"task":'))), nodeId = input.node.id;
    assert.ok(['east', 'west'].includes(nodeId)); assert.equal(input.leaderReplyRefs.length, input.leaderReplies.length);
    const root = process.env.MARSHAL_REPORT_TEST_BARRIER, file = id => path.join(root, hash(input.task).slice(7) + '-' + id);
    fs.writeFileSync(file(nodeId), '', {flag: 'wx', mode: 0o600}); const deadline = Date.now() + 15000;
    while (!['east', 'west'].every(id => fs.existsSync(file(id)))) {assert.ok(Date.now() < deadline, 'author rendezvous'); await pause(10);}
    await permission(request.params.sessionId, 'read', {path: 'sales.json'});
    const report = calculate(JSON.parse(fs.readFileSync('sales.json')).rows, nodeId, windowFor(input.task, input.leaderReplies));
    const content = JSON.stringify(report); await permission(request.params.sessionId, 'write', {path: nodeId + '.json', content});
    fs.writeFileSync(nodeId + '.json', content, {flag: 'wx', mode: 0o600}); output = {message: '仅写原节点候选；由独立验收处理'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(output)}}}});
  respond(request, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (!request.method) {pending.get(request.id)?.(request.result); pending.delete(request.id);}
  else if (request.method === 'initialize') respond(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') respond(request, {sessionId: 'report-fixture'});
  else if (request.method === 'session/prompt') void prompt(request).catch(() => {process.stderr.write('{"code":"report_peer_failed"}\n', () => process.exit(1));});
}
