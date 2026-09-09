import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createPiProvider} from './index.mjs';
import {launchProtocol} from '../agent-runtime/index.mjs';

const fixture = fileURLToPath(new URL('./bridge-agent.fixture.mjs', import.meta.url));
const sdkEntry = process.env.MARSHAL_PI_TEST_SDK ?? fileURLToPath(new URL('./fixtures/sdk/index.mjs', import.meta.url));
const config = {profile: 'task-runtime-question/v1', policyDigest: 'sha256:' + 'a'.repeat(64), maxWaitMs: 3000};
function setup(t, mode, overrides = {}) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-pi-question-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true}));
  const calls = [], scopes = [], deadline = Date.now() + 10000;
  const delivery = {questionId: 'question-original', questionDigest: 'sha256:' + 'b'.repeat(64), answerDigest: 'sha256:' + 'c'.repeat(64),
    deliveryNonce: 'd'.repeat(64), answer: 'north', deadlineAt: new Date(deadline - 1000).toISOString()};
  const provider = createPiProvider({id: 'pi', executable: process.execPath, args: [fixture, mode], bridge: {sdkEntry}});
  const input = {cwd, deadline, prompt: '有界业务提问测试，不访问真实模型。',
    onPermission: request => {calls.push(['permission', request.toolCall._meta.toolName]); return {outcome: {outcome: 'selected', optionId: 'allow-once'}};},
    executionContext: {launch: (options, callbacks) => launchProtocol({...options, createClient: callbacks.createClient}), extraScope: code => scopes.push(code)},
    questionContext: {configuration: config,
      ask(request) {calls.push(['question', request]); return delivery;},
      acknowledge(id, receipt) {calls.push(['ack', id, receipt]); return true;}},
  };
  if (overrides.questionContext) Object.assign(input.questionContext, overrides.questionContext);
  const handle = provider.start(input); t.after(() => handle.stop());
  return {cwd, handle, calls, scopes, delivery};
}

test('original native definition/ready/RPC waits for explicit business ACK before writing answer, without tool permission promotion', {timeout: 15000}, async t => {
  const f = setup(t, 'business'); const result = await f.handle.completion;
  assert.equal(result.status, 'completed'); assert.equal(result.cleanup.cleaned, true); assert.deepEqual(f.scopes, []);
  assert.equal(fs.readFileSync(path.join(f.cwd, 'output.txt'), 'utf8'), 'north');
  assert.deepEqual(f.calls.map(item => item[0]), ['question', 'ack', 'permission']);
  assert.equal(f.calls[0][1].kind, 'select'); assert.equal(f.calls[0][1].sessionId, 'native-session');
  assert.match(f.calls[0][1].questionNonce, /^[a-f0-9]{64}$/);
  assert.deepEqual(f.calls[1].slice(1), ['question-original', {questionDigest: f.delivery.questionDigest,
    answerDigest: f.delivery.answerDigest, deliveryNonce: f.delivery.deliveryNonce}]);
  assert.equal(f.calls[2][1], 'write');
});

test('foreign ACK, rejected ACK transaction and invalid answer cannot reach native write', {timeout: 15000}, async t => {
  for (const mode of ['business-ack-foreign', 'ack-rejected', 'answer-invalid']) {
    const f = setup(t, mode === 'business-ack-foreign' ? mode : 'business', {questionContext: mode === 'ack-rejected' ?
      {acknowledge() {throw Error('controlled SQL failure');}} : mode === 'answer-invalid' ? {ask() {return {answer: 'north'};}} : {}});
    const result = await f.handle.completion;
    assert.notEqual(result.status, 'completed'); assert.equal(result.cleanup.cleaned, true); assert.deepEqual(f.scopes, []);
    assert.equal(fs.existsSync(path.join(f.cwd, 'output.txt')), false);
    assert.equal(f.calls.some(item => item[0] === 'permission'), false);
  }
});

test('cancel while native input awaits answer retains original owned cleanup and never sends permission', {timeout: 15000}, async t => {
  let entered; const asked = new Promise(resolve => {entered = resolve;});
  const f = setup(t, 'business', {questionContext: {ask(_question, {signal}) {entered(); return new Promise(resolve => signal.addEventListener('abort', () => resolve(null), {once: true}));}}});
  await asked; const first = f.handle.stop(), second = f.handle.stop();
  assert.equal(first, second); const result = await first;
  assert.equal(result.status, 'cancelled'); assert.equal(result.cleanup.cleaned, true); assert.deepEqual(f.scopes, []);
  assert.equal(fs.existsSync(path.join(f.cwd, 'output.txt')), false);
});
