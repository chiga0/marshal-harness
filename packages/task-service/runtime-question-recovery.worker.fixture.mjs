// No model or login: one ACP planner peer and one independently executed checker.
// Both consume the ORIGINAL parent's input, never the HTTP test's answer closure.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
for await (const line of createInterface({input: process.stdin})) {
  const request = JSON.parse(line);
  if (process.argv[2] === 'planner') {
    const respond = result => send({jsonrpc: '2.0', id: request.id, result});
    if (request.method === 'initialize') respond({protocolVersion: 1, agentCapabilities: {loadSession: false}});
    else if (request.method === 'session/new') respond({sessionId: 'runtime-question-planner'});
    else if (request.method === 'session/prompt') {
      const prompt = request.params.prompt[0].text, at = prompt.lastIndexOf('\nFIXTURE_PLAN=');
      if (at < 0) throw Error('fixture plan was not declared to planner');
      const plan = JSON.parse(prompt.slice(at + '\nFIXTURE_PLAN='.length));
      send({jsonrpc: '2.0', method: 'session/update', params: {sessionId: request.params.sessionId,
        update: {sessionUpdate: 'agent_message_chunk', content: {type: 'text', text: JSON.stringify(plan)}}}});
      respond({stopReason: 'end_turn'});
    }
    continue;
  }
  const refs = request.input.interactionRefs, ref = refs?.[0];
  const region = fs.readFileSync('east.txt', 'utf8'), west = fs.readFileSync('west.txt', 'utf8');
  const actual = {region, workerId: ref?.question.workerId ?? null, executionId: ref?.question.executionId ?? null,
    questionDigest: ref?.questionDigest ?? null, answerDigest: ref?.answerDigest ?? null, ackDigest: ref?.ackDigest ?? null,
    matches: refs?.length === 1 && ref.question.nodeId === 'east' && ref.question.request.kind === 'select' &&
      JSON.stringify(ref.question.request.options) === '["north","south"]' && ref.questionDigest === digest(encode(ref.question)) &&
      ref.answerDigest === digest(encode(ref.answer)) && ref.answer.questionDigest === ref.questionDigest &&
      /^sha256:[a-f0-9]{64}$/.test(ref.ackDigest) && ['north', 'south'].includes(ref.answer.answer) && region === ref.answer.answer &&
      west === 'independent native candidate'};
  process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
    assertions: [{name: 'answer-bound-files', actual}]}).toString() + '\n', () => process.exit(0));
  break;
}
