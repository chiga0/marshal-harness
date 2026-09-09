// Independent, fixed Node checker. No model or worker-reported verdict is used.
import fs from 'node:fs';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
// Original command transport keeps stdin open; one bounded LF frame, not EOF.
for await (const input of createInterface({input: process.stdin})) {
if (Buffer.byteLength(input) > 262144) throw Error('bounded');
const request = JSON.parse(input), refs = request.input.interactionRefs;
if (!Array.isArray(refs) || refs.length !== 1 || refs[0].question.request.prompt !== '请确定本次业务区域' ||
  refs[0].answer.answer !== 'north' || digest(encode(refs[0].answer)) !== refs[0].answerDigest ||
  digest(encode(refs[0].question)) !== refs[0].questionDigest) throw Error('question evidence mismatch');
const code = fs.readFileSync('code.txt', 'utf8'), docs = fs.readFileSync('docs.txt', 'utf8');
const actual = {answer: refs[0].answer.answer, code, docs, count: refs.length};
process.stdout.write(encode({profile: request.profile, nonce: request.nonce, binding: request.binding,
  assertions: [{name: 'business-answer', actual}]}).toString() + '\n', () => process.exit(0));
break;
}
