// Explicit deterministic ACP test peer, not a model. The first east execution
// deliberately writes a content error itself; no parent edits a candidate.
// Review independently reads the actual frozen materials and reports the error.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
const hash = value => digest(encode(value)), send = value => process.stdout.write(JSON.stringify(value) + '\n');
const response = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
const mode = process.argv[2];
if (!['repair', 'cancel'].includes(mode)) throw Error('explicit fixture mode required');
function plan(input) {
  const task = input.snapshot.task.input;
  return {summary: task.intent, nodes: ['east', 'west', 'verify'].map(id => ({id, role: id === 'verify' ? 'verifier' : 'author',
    goal: '完成原业务的' + id, scope: [id], providerId: null})), edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
    deliverables: task.requirements.deliverables, acceptance: task.requirements.acceptance, assumptions: []};
}
function decide(input) {
  const s = input.snapshot, review = s.evidence.find(item => item.kind === 'review'), verification = s.evidence.find(item => item.kind === 'verification');
  let actions;
  if (!s.interactions.replies.length) actions = [{type: 'ask', kind: 'business', prompt: '请明确本次业务地区', options: [],
    subject: s.readSet.find(item => item.kind === 'input').digest, nodeIds: []}];
  else if (!s.plan) actions = [{type: 'plan', proposal: plan(input)}];
  else if (review?.verdict === 'rework') actions = [{type: 'repair', nodeIds: ['east'], basis: {kind: 'review', digest: review.digest},
    feedback: '按原独立意见修正 east 的业务值；保留 west 原成果和用户已答地区。'}];
  else if (!review) actions = [{type: 'work', kind: 'review', nodeIds: s.selection.map(item => item.nodeId), selectionDigest: hash(s.selection)}];
  else if (!verification) actions = [{type: 'work', kind: 'verify', nodeIds: ['verify'], selectionDigest: hash(s.selection)}];
  else if (s.obligation.some(item => item.reason === 'delivery-ready')) actions = [{type: 'conclude', outcome: 'succeeded',
    summary: s.plan.acceptance.join('\n') + '\n已独立检查原两地区结果，修正没有缩减要求。', basisDigests: [review.digest, verification.digest]}];
  else actions = [{type: 'deliver', artifactId: input.materials.find(item => item.kind === 'delivery').id,
    acceptanceDigest: verification.digest, reviewDigest: review.digest}];
  return {profile: input.profile, callId: input.callId, inputDigest: input.inputDigest, summary: '仅根据原事实推进', actions};
}
function review(input) {
  const task = input.snapshot.task.input, expected = JSON.parse(task.context.text), answer = input.snapshot.interactions.replies[0].answer;
  const materials = input.materials.filter(item => item.nodeId), findings = [];
  if (materials.length !== 2) throw Error('Review must receive both original materials');
  for (const material of materials) {
    const actual = JSON.parse(material.content), nodeId = material.nodeId;
    if (actual.nodeId !== nodeId || actual.region !== answer || actual.value !== expected[nodeId]) findings.push({id: 'content-' + nodeId,
      nodeIds: [nodeId], requirement: task.requirements.acceptance.find(value => value.includes(nodeId)),
      observation: '实际原文件为' + material.content, requestedChange: '依据原业务数据与用户回复，值应为' + expected[nodeId] + '、地区为' + answer});
  }
  return {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
    verdict: findings.length ? 'rework' : 'accept', summary: findings.length ? '原材料内容与已确认需求不一致' : '两原分支逐项一致', findings};
}
async function prompt(request) {
  const text = request.params.prompt[0].text; let output;
  if (text.includes('\n完整冻结输入：')) {
    const input = JSON.parse(text.split('\n完整冻结输入：').at(-1));
    if (!input.snapshot.task.input.requirements.acceptance.every(value => text.includes(value))) throw Error('original requirements missing');
    output = input.profile === 'task-managed-leader/v1' ? decide(input) : review(input);
  } else {
    const input = JSON.parse(text.slice(text.indexOf('{"task":'))), nodeId = input.node.id;
    if (!input.plan || input.leaderReplies.length !== 1 || input.leaderReplyRefs.length !== 1) throw Error('original work package missing');
    if (mode === 'cancel') return new Promise(() => {}); // Explicit window until original guard stop; no synthetic cleanup.
    const expected = JSON.parse(input.task.context.text), repair = input.repair;
    if (repair && (nodeId !== 'east' || repair.basis.kind !== 'review' || repair.originalNegativeReport.report.verdict !== 'rework' ||
        !repair.originalNegativeReport.report.findings.some(finding => finding.nodeIds.includes('east')))) throw Error('original repair evidence missing');
    const value = expected[nodeId] + (nodeId === 'east' && !repair ? 1 : 0);
    fs.writeFileSync(nodeId + '.json', JSON.stringify({nodeId, region: input.leaderReplies[0].answer, value}), {flag: 'wx', mode: 0o600});
    output = {message: '候选文件已写；验收由原独立执行负责'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(output)}}}});
  response(request, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (request.method === 'initialize') response(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') response(request, {sessionId: 'leader-followup-fixture'});
  else if (request.method === 'session/prompt') void prompt(request).catch(error => {process.stderr.write(error.message); process.exit(1);});
}
