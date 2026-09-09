import test from 'node:test';
import assert from 'node:assert/strict';
import {PassThrough} from 'node:stream';
import {setImmediate as tick} from 'node:timers/promises';
import {PiRpcClient} from './client.mjs';

function fixture(t, options = {}) {
  const readable = new PassThrough(), writable = new PassThrough(), writes = [];
  let raw = '';
  writable.on('data', chunk => { raw += chunk.toString(); let index; while ((index = raw.indexOf('\n')) >= 0) {
    writes.push(JSON.parse(raw.slice(0, index))); raw = raw.slice(index + 1); }});
  const client = new PiRpcClient({readable, writable, ...options});
  t.after(() => { client.close(); readable.destroy(); writable.destroy(); });
  const send = value => readable.write(Buffer.from(JSON.stringify(value) + '\n'));
  const reply = (request, data, extra = {}) => send({type: 'response', id: request.id, command: request.type, success: true, data, ...extra});
  return {client, readable, writable, writes, send, reply};
}
function ending(f, value = 'real\u2028text\u2029content', reason = 'stop', settle = true) {
  f.send({type: 'message_end', message: {role: 'assistant', stopReason: reason, content: [{type: 'text', text: value}]}});
  f.send({type: 'agent_end', messages: [], willRetry: false}); if (settle) f.send({type: 'agent_settled'});
}
const state = {sessionId: 'session-one', isStreaming: false, isCompacting: false, pendingMessageCount: 0, messageCount: 0};
test('state is request-correlated and strips native session/model details', async t => {
  const f = fixture(t), reading = f.client.getState(); f.reply(f.writes[0], {...state, sessionFile: 'PRIVATE', model: {token: 'PRIVATE'}});
  assert.deepEqual(await reading, state);
});
test('LF bytes may split UTF8/CRLF; Unicode line separators inside JSON are not delimiters', async t => {
  const f = fixture(t), result = f.client.prompt('task'); f.reply(f.writes[0]);
  const bytes = Buffer.from(JSON.stringify({type: 'message_end', message: {role: 'assistant', stopReason: 'stop', content: [{type: 'text', text: '中\u2028文\u2029字'}]}}) + '\r\n');
  for (const byte of bytes) f.readable.write(Buffer.from([byte]));
  f.send({type: 'agent_end'}); f.send({type: 'agent_settled'});
  assert.equal((await result).outputText, '中\u2028文\u2029字');
});
test('prompt ack and agent_end never finish; retry/compaction are allowed until final settled', async t => {
  const f = fixture(t); let completed = false;
  const result = f.client.prompt('task').then(value => { completed = true; return value; });
  f.reply(f.writes[0]); ending(f, 'failed', 'error', false); await tick(); assert.equal(completed, false);
  f.send({type: 'auto_retry_start'}); f.send({type: 'compaction_start'}); f.send({type: 'compaction_end'});
  f.send({type: 'agent_start'}); ending(f, 'final'); assert.deepEqual(await result, {stopReason: 'stop', outputText: 'final'});
});
test('early native events wait for exact prompt acknowledgement; one live prompt only', async t => {
  const f = fixture(t); let finished = false;
  const result = f.client.prompt('task').then(value => { finished = true; return value; });
  await assert.rejects(f.client.prompt('second'), {code: 'pi_busy'}); ending(f, 'early'); await tick(); assert.equal(finished, false);
  f.reply(f.writes[0]); assert.equal((await result).outputText, 'early');
});
test('declared native session chatter passes as progress; undeclared peer events stay terminal failures', async t => {
  for (const mode of ['bash_execution_update', 'entry_appended', 'thinking_level_changed', 'session_info_changed']) {
    const f = fixture(t);
    const result = f.client.prompt('task'); f.reply(f.writes[0]);
    f.send({type: 'agent_start'}); f.send({type: mode, attempt: 1}); ending(f, 'ok-' + mode);
    assert.equal((await result).outputText, 'ok-' + mode);
  }
  const f = fixture(t), result = f.client.prompt('task');
  const rejected = assert.rejects(result, {code: 'pi_unexpected_event'}); f.reply(f.writes[0]);
  f.send({type: 'telemetry_session'}); await rejected; assert.equal(f.client.closed, true);
});
test('cancel clears native queue before abort and acknowledgement is not execution cleanup', async t => {
  const f = fixture(t), result = f.client.prompt('task'); f.reply(f.writes[0]);
  await tick(); // Flush the prior prompt write before observing the next write.
  const cancelling = f.client.cancel(); assert.equal(f.client.cancel(), cancelling);
  assert.equal(f.writes[1].type, 'clear_queue'); assert.equal(f.writes.length, 2);
  f.reply(f.writes[1], {steering: [], followUp: []}); await tick(); assert.equal(f.writes[2].type, 'abort');
  f.reply(f.writes[2]); await cancelling; assert.equal(f.client.closed, false);
  ending(f, 'late success'); assert.deepEqual(await result, {stopReason: 'aborted', outputText: ''});
});
test('unknown, duplicate, wrong-command and remote rejected responses never satisfy another request', async t => {
  for (const mode of ['unknown', 'wrong-command', 'rejected', 'duplicate']) {
    const f = fixture(t), reading = f.client.getState(), rejected = mode === 'duplicate' ? null : assert.rejects(reading);
    if (mode === 'unknown') f.reply({...f.writes[0], id: 'other'}, state);
    else if (mode === 'wrong-command') f.reply({...f.writes[0], type: 'prompt'}, state);
    else if (mode === 'rejected') f.reply(f.writes[0], undefined, {success: false, error: 'PRIVATE'});
    else { f.reply(f.writes[0], state); await reading; f.reply(f.writes[0], state); }
    if (rejected) await rejected; await tick(); if (mode !== 'rejected') assert.equal(f.client.closed, true);
  }
});
test('bad UTF8/truncated/frame/event limits fail closed; absent settled times out', async t => {
  for (const mode of ['utf8', 'truncated', 'frame', 'events', 'no-settled']) {
    const f = fixture(t, {maxFrameBytes: 512, maxEvents: 2}), result = f.client.prompt('task', {timeoutMs: 35});
    const rejected = assert.rejects(result); f.reply(f.writes[0]);
    if (mode === 'utf8') f.readable.write(Buffer.from([0xff, 0x0a]));
    if (mode === 'truncated') f.readable.end(Buffer.from('{"type":'));
    if (mode === 'frame') f.readable.write(Buffer.from('x'.repeat(513)));
    if (mode === 'events') { f.send({type: 'agent_start'}); f.send({type: 'turn_start'}); f.send({type: 'turn_start'}); }
    await rejected; assert.equal(f.client.closed, true);
  }
});
test('a denied extension question cannot become a successful settled answer', async t => {
  const f = fixture(t), result = f.client.prompt('task'), rejected = assert.rejects(result, {code: 'pi_interaction_required'});
  f.reply(f.writes[0]); f.send({type: 'extension_ui_request', id: 'question', method: 'confirm', title: 'PRIVATE'});
  await tick(); assert.deepEqual(f.writes[1], {type: 'extension_ui_response', id: 'question', cancelled: true});
  ending(f, 'pretend question resolved'); await rejected;
});
test('late queued work, missing final message, invalid stop and overlong output cannot be accepted', async t => {
  for (const mode of ['queue', 'missing', 'invalid-stop', 'overflow', 'tool-terminal']) {
    const f = fixture(t), result = f.client.prompt('task'), rejected = assert.rejects(result); f.reply(f.writes[0]);
    if (mode === 'queue') f.send({type: 'queue_update', steering: ['not-approved'], followUp: []});
    if (mode === 'missing') { f.send({type: 'agent_end'}); f.send({type: 'agent_settled'}); }
    if (mode === 'invalid-stop') ending(f, 'x', 'invented');
    if (mode === 'overflow') ending(f, 'x'.repeat(65537));
    if (mode === 'tool-terminal') { f.send({type: 'message_end', message: {role: 'assistant', stopReason: 'stop', content: [{type: 'toolCall'}]}});
      f.send({type: 'agent_end'}); f.send({type: 'agent_settled'}); }
    await rejected;
  }
});
test('wire event callback is bounded and queue overflow does not grow unboundedly', async t => {
  const f = fixture(t, {callbackTimeoutMs: 20, onEvent: () => new Promise(() => {})});
  const result = f.client.prompt('task'), rejected = assert.rejects(result, {code: 'pi_callback_timeout'});
  f.reply(f.writes[0]); f.send({type: 'agent_start'}); await rejected;
  const g = fixture(t, {maxQueueBytes: 128, onEvent: () => new Promise(() => {})});
  const result2 = g.client.prompt('task'), rejected2 = assert.rejects(result2); g.reply(g.writes[0]);
  for (let index = 0; index < 8; index++) g.send({type: 'turn_start', text: 'x'.repeat(40)}); await rejected2;
});

test('trusted native confirm callback is exact-once and does not block clear_queue/abort processing', async t => {
  let decide, permissionSignal;
  const f = fixture(t, {onInteraction: (_message, {signal}) => { permissionSignal = signal; return new Promise(resolve => { decide = resolve; }); }});
  const running = f.client.prompt('task'); f.reply(f.writes[0]);
  f.send({type: 'extension_ui_request', id: 'permission-one', method: 'confirm', title: 'trusted bridge', message: '{}'});
  await tick(); const cancellation = f.client.cancel(); await tick();
  assert.equal(permissionSignal.aborted, true);
  const clear = f.writes.find(message => message.type === 'clear_queue'); assert.ok(clear); f.reply(clear);
  await tick(); const abort = f.writes.find(message => message.type === 'abort'); assert.ok(abort); f.reply(abort); await cancellation;
  decide({handled: true, confirmed: true}); await tick();
  assert.deepEqual(f.writes.filter(message => message.type === 'extension_ui_response'), [{type: 'extension_ui_response', id: 'permission-one', cancelled: true}]);
  ending(f, 'not authority'); await assert.rejects(running, {code: 'pi_interaction_required'});
});

test('only handled confirms can continue; pending or timed-out interactions never become terminal success', async t => {
  for (const mode of ['allowed', 'pending', 'timeout', 'invalid', 'duplicate']) {
    const f = fixture(t, {interactionTimeoutMs: 20, onInteraction: () => mode === 'pending' || mode === 'timeout' ? new Promise(() => {}) :
      mode === 'invalid' ? {confirmed: true} : {handled: true, confirmed: true}});
    const running = f.client.prompt('task'); running.catch(() => {}); f.reply(f.writes[0]);
    const request = {type: 'extension_ui_request', id: 'permission-one', method: 'confirm'}; f.send(request);
    if (mode === 'timeout') await new Promise(resolve => setTimeout(resolve, 30)); else await tick();
    if (mode === 'duplicate') f.send(request);
    ending(f, 'candidate');
    if (mode === 'allowed') assert.equal((await running).outputText, 'candidate');
    else await assert.rejects(running);
  }
});
