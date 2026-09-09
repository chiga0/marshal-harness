// Real ACP transport under the original guard/custody; deterministic replies,
// no model, provider credentials, private receipt or direct Store access.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
const hash = value => digest(encode(value)), send = value => process.stdout.write(JSON.stringify(value) + '\n');
const response = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
function proposal(input) {
  return {summary: input.snapshot.task.input.intent, nodes: ['east', 'west', 'verify'].map(id => ({id,
    role: id === 'verify' ? 'verifier' : 'author', goal: '实现原需求的' + id, scope: [id], providerId: null})),
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}], deliverables: ['east报告', 'west报告'],
    acceptance: input.snapshot.task.input.requirements.acceptance, assumptions: []};
}
function decision(input) {
  const s = input.snapshot, last = s.evidence.find(item => item.kind === 'verification'), review = s.evidence.find(item => item.kind === 'review');
  let actions;
  if (!s.interactions.replies.length) actions = [{type: 'ask', kind: 'business', prompt: '请明确此次业务区域', options: [],
    subject: s.readSet.find(item => item.kind === 'input').digest, nodeIds: []}];
  else if (!s.plan) actions = [{type: 'plan', proposal: proposal(input)}];
  else if (!review) actions = [{type: 'work', kind: 'review', nodeIds: s.selection.map(item => item.nodeId), selectionDigest: hash(s.selection)}];
  else if (!last) actions = [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(s.selection)}];
  else if (s.obligation.some(item => ['delivery-ready', 'postverify-finished'].includes(item.reason))) actions = [{type: 'conclude', outcome: 'succeeded',
    summary: s.plan.acceptance.join('\n') + '\n对应两原报告和独立Review/Verification证据', basisDigests: [last.digest, review.digest]}];
  else {
    const delivery = input.materials.find(item => item.kind === 'delivery');
    if (!delivery) throw Error('actual delivery reference must be in original Leader input');
    actions = [{type: 'deliver', artifactId: delivery.id, acceptanceDigest: last.digest, reviewDigest: review.digest}];
  }
  return {profile: input.profile, callId: input.callId, inputDigest: input.inputDigest, summary: '原完整上下文决定', actions};
}
async function prompt(request) {
  const text = request.params.prompt[0].text; let output;
  if (text.includes('\n完整冻结输入：')) {
    const input = JSON.parse(text.split('\n完整冻结输入：').at(-1));
    if (!input.snapshot.task.input.requirements.acceptance.every(value => text.includes(value))) throw Error('requirements omitted');
    if (input.profile === 'task-managed-leader/v1') output = decision(input);
    else {
      const files = input.materials.filter(item => item.nodeId);
      if (files.length !== 2 || !files.every(item => JSON.parse(item.content).region === input.snapshot.interactions.replies[0].answer)) throw Error('independent Review observed wrong content');
      output = {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
        verdict: 'accept', summary: input.snapshot.task.input.requirements.acceptance.join('\n') + '：两份原材料都已检查', findings: []};
    }
  } else {
    const input = JSON.parse(text.slice(text.indexOf('{"task":')));
    if (!input.plan.acceptance.every(value => text.includes(value.replaceAll('"', '\\"'))) && !text.includes('原完整业务要求')) throw Error('plan omitted');
    if (input.leaderReplies.length !== 1 || input.leaderReplyRefs.length !== 1) throw Error('original answer omitted from real work package');
    output = {nodeId: input.node.id, region: input.leaderReplies[0].answer, value: JSON.parse(input.task.context.text)[input.node.id]};
    fs.writeFileSync(input.node.id + '.json', JSON.stringify(output), {flag: 'wx', mode: 0o600});
    output = {message: '候选已写；不自签验收'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(output)}}}});
  response(request, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (request.method === 'initialize') response(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') response(request, {sessionId: 'leader-protocol-fixture'});
  else if (request.method === 'session/prompt') void prompt(request).catch(error => {process.stderr.write(error.message); process.exitCode = 1;});
}
