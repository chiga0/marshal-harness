// Explicit no-model ACP peer; never shipped as a runtime provider.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import {createInterface} from 'node:readline';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const reply = (request, result) => send({jsonrpc: '2.0', id: request.id, result});
const pending = new Map(); let serial = 0;
function expected(task, nodeId) {return task.intent + '\n' + nodeId + '\n' + task.context.text;}
async function prompt(request) {
  const text = request.params.prompt[0].text; let output;
  if (text.includes('\n完整冻结输入：')) {
    const input = JSON.parse(text.split('\n完整冻结输入：').at(-1)), s = input.snapshot;
    if (input.profile === 'task-independent-review/v1') {
      const files = input.materials.filter(item => item.nodeId), good = files.every(item => item.content === expected(s.task.input, item.nodeId));
      assert.equal(files.length, s.selection.length);
      output = {profile: input.profile, inputDigest: input.inputDigest, selectionDigest: input.selectionDigest,
        verdict: good ? 'accept' : 'reject', summary: good ? '独立核对原需求与真实正文一致' : '实际正文不符合原要求', findings: good ? [] : [{id: 'content-mismatch',
          nodeIds: [files[0].nodeId], requirement: '交付原需求指定正文', observation: '正文被错误替换', requestedChange: '重新明确要求；当前任务不能宣告成功'}]};
    } else {
      const review = s.evidence.find(item => item.kind === 'review'), verification = s.evidence.find(item => item.kind === 'verification');
      const selected = s.readSet.find(item => item.kind === 'selected').digest;
      let actions;
      if (!s.plan) {
        const authors = s.task.input.context.text.includes('two') ? ['draft', 'critique'] : ['compose'];
        actions = [{type: 'plan', proposal: {summary: s.task.input.intent,
          nodes: [...authors.map(id => ({id, role: 'author', goal: expected(s.task.input, id), scope: ['result.md'], providerId: null})),
            {id: 'check', role: 'verifier', goal: '独立核验实际文件摘要及完整交付', scope: [], providerId: null}],
          edges: authors.map(from => ({from, to: 'check'})), deliverables: ['原要求的完整文本文件'], acceptance: s.task.input.requirements.acceptance, assumptions: []}}];
      } else if (!review) actions = [{type: 'work', kind: 'review', nodeIds: s.selection.map(item => item.nodeId), selectionDigest: selected}];
      else if (review.verdict !== 'accept') actions = [{type: 'conclude', outcome: 'failed', summary: '独立内容审查拒绝，未交付成功', basisDigests: [review.digest]}];
      else if (!verification) actions = [{type: 'work', kind: 'verify', nodeIds: ['check'], selectionDigest: selected}];
      else if (s.obligation.some(item => item.reason === 'delivery-ready'))
        actions = [{type: 'conclude', outcome: 'succeeded', summary: '文件交付与独立内容审查、字节校验完成；未执行外部操作', basisDigests: [review.digest, verification.digest]}];
      else actions = [{type: 'deliver', artifactId: input.materials.find(item => item.kind === 'delivery').id,
        acceptanceDigest: verification.digest, reviewDigest: review.digest}];
      output = {profile: input.profile, callId: input.callId, inputDigest: input.inputDigest, summary: '按当前需求和原证据推进', actions};
    }
  } else {
    const input = JSON.parse(text.slice(text.indexOf('{"task":')));
    if (process.env.GENERIC_TEST_MODE === 'wait') await new Promise(() => {});
    const content = process.env.GENERIC_TEST_MODE === 'bad' ? '错误正文' : expected(input.task, input.node.id);
    const id = 'permission-' + ++serial, result = new Promise(resolve => pending.set(id, resolve));
    send({jsonrpc: '2.0', id, method: 'session/request_permission', params: {sessionId: request.params.sessionId,
      toolCall: {toolCallId: id, title: 'Write result.md', kind: 'edit', status: 'pending', rawInput: {file_path: 'result.md', content}},
      options: [{kind: 'allow_once', optionId: 'proceed_once', name: 'Allow once'}, {kind: 'reject_once', optionId: 'cancel', name: 'Reject'}]}});
    assert.equal((await result).outcome.optionId, 'proceed_once');
    fs.writeFileSync('result.md', content, {flag: 'wx', mode: 0o600});
    if (process.env.GENERIC_TEST_MODE === 'extra') fs.writeFileSync('extra.md', 'unexpected');
    output = {summary: '作者完成候选，不提供验收权威'};
  }
  send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
    update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(output)}}}});
  reply(request, {stopReason: 'end_turn'});
}
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (!request.method) {pending.get(request.id)?.(request.result); pending.delete(request.id);}
  else if (request.method === 'initialize') reply(request, {protocolVersion: 1, agentCapabilities: {loadSession: false}});
  else if (request.method === 'session/new') reply(request, {sessionId: 'generic-fixture'});
  else if (request.method === 'session/prompt') void prompt(request).catch(() => process.exit(1));
}
