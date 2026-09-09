// External deterministic ACP peer, not a production Agent or Core authority.
// The recipe is the frozen task-service/leader-agent fixture; no source Core
// imports. The two real author processes rendezvous instead of timed sleeps.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {createInterface} from 'node:readline';
import {setTimeout as pause} from 'node:timers/promises';
const canonical = value => Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']' :
  value && typeof value === 'object' ? '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}' : JSON.stringify(value);
const hash = value => 'sha256:' + createHash('sha256').update(canonical(value)).digest('hex');
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const response = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
function decision(input) {
  const s = input.snapshot, last = s.evidence.find(item => item.kind === 'verification'), review = s.evidence.find(item => item.kind === 'review');
  let actions;
  if (!s.interactions.replies.length) actions = [{type: 'ask', kind: 'business', prompt: '请明确此次业务区域', options: [],
    subject: s.readSet.find(item => item.kind === 'input').digest, nodeIds: []}];
  else if (!s.plan) actions = [{type: 'plan', proposal: {summary: s.task.input.intent,
    nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author', goal: '实现原需求的' + id, scope: [id], providerId: null})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: s.task.input.requirements.deliverables,
    acceptance: s.task.input.requirements.acceptance, assumptions: []}}];
  else if (!review) actions = [{type: 'work', kind: 'review', nodeIds: s.selection.map(item => item.nodeId), selectionDigest: hash(s.selection)}];
  else if (!last) actions = [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(s.selection)}];
  else if (s.obligation.some(item => item.reason === 'postverify-finished')) actions = [{type: 'conclude', outcome: 'succeeded',
    summary: s.task.input.intent + '\n' + s.plan.acceptance.join('\n') + '\n原报告已经独立Review、验证、明确授权发布并经真实GET后验', basisDigests: [last.digest, review.digest]}];
  else {
    const delivery = input.materials.find(item => item.kind === 'delivery'); assert.ok(delivery);
    actions = [{type: 'deliver', artifactId: delivery.id, acceptanceDigest: last.digest, reviewDigest: review.digest}];
  }
  return {profile: input.profile, callId: input.callId, inputDigest: input.inputDigest, summary: '消费原完整上下文', actions};
}
async function rendezvous(input) {
  const root = process.env.MARSHAL_V7_BARRIER;
  assert.ok(path.isAbsolute(root) && fs.realpathSync(root) === root);
  assert.ok(input.task && ['east', 'west'].includes(input.node.id));
  const file = id => path.join(root, hash(input.task).slice(7) + '-' + id);
  fs.writeFileSync(file(input.node.id), '', {flag: 'wx', mode: 0o600});
  const deadline = Date.now() + 15000;
  while (!['east', 'west'].every(id => fs.existsSync(file(id)))) {
    assert.ok(Date.now() < deadline, 'author rendezvous expired'); await pause(10);
  }
}
async function prompt(request) {
  const text = request.params.prompt[0].text; let output;
  if (text.includes('\n完整冻结输入：')) {
    const input = JSON.parse(text.split('\n完整冻结输入：').at(-1));
    assert.ok(input.snapshot.task.input.requirements.acceptance.every(value => text.includes(value)));
    if (input.profile === 'task-managed-leader/v1') output = decision(input);
    else {
      assert.equal(input.profile, 'task-independent-review/v1');
      const files = input.materials.filter(item => item.nodeId), context = JSON.parse(input.snapshot.task.input.context.text);
      assert.equal(files.length, 2);
      for (const item of files) assert.deepEqual(JSON.parse(item.content), {nodeId: item.nodeId,
        region: input.snapshot.interactions.replies[0].answer, value: context[item.nodeId]});
      output = {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
        verdict: 'accept', summary: input.snapshot.task.input.requirements.acceptance.join('\n') + '：两份原候选内容均独立检查', findings: []};
    }
  } else {
    const input = JSON.parse(text.slice(text.indexOf('{"task":')));
    assert.equal(input.leaderReplies.length, 1); assert.equal(input.leaderReplyRefs.length, 1);
    await rendezvous(input);
    const result = {nodeId: input.node.id, region: input.leaderReplies[0].answer, value: JSON.parse(input.task.context.text)[input.node.id]};
    fs.writeFileSync(input.node.id + '.json', JSON.stringify(result), {flag: 'wx', mode: 0o600});
    output = {message: '候选已写；不自签验收'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(output)}}}});
  response(request, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (request.method === 'initialize') response(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') response(request, {sessionId: 'installed-v7-fixture'});
  else if (request.method === 'session/prompt') void prompt(request).catch(() => {
    process.stderr.write('{"code":"installed_v7_peer_failed"}\n'); process.exitCode = 1;
  });
}
