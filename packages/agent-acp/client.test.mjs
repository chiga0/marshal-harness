import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough, Writable } from 'node:stream';
import { AcpClient } from './client.mjs';

const tick = () => new Promise(resolve => setImmediate(resolve));
const promptBlocks = [{ type: 'text', text: 'fixture only' }];
const initResult = { protocolVersion: 1, agentInfo: { name: 'fixture-agent', version: '99.0' },
  agentCapabilities: { loadSession: true, promptCapabilities: { image: true } } };
const update = (sessionId, text = 'text') => ({ jsonrpc: '2.0', method: 'session/update', params: {
  sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } } } });
const permission = (sessionId, id = 'peer-1') => ({ jsonrpc: '2.0', id, method: 'session/request_permission', params: {
  sessionId, toolCall: { toolCallId: 'tool-1', status: 'pending', title: 'Fixture', _meta: {
    qwenInteractionKind: 'user_question', qwenQuestions: [{ question: 'Which?', header: 'Choice', options: [{ label: 'A', description: 'A' }, { label: 'B', description: 'B' }] }] } },
  options: [{ optionId: 'allow', name: 'Allow', kind: 'allow_once' }, { optionId: 'reject', name: 'Reject', kind: 'reject_once' }] } });
const allow = { outcome: { outcome: 'selected', optionId: 'allow' } };
const denied = { outcome: { outcome: 'cancelled' } };

function fixture(t, options = {}) {
  const readable = new PassThrough(), writable = new PassThrough(), frames = [], updates = [], failures = [];
  let output = '';
  writable.on('data', bytes => {
    output += bytes.toString();
    for (;;) {
      const index = output.indexOf('\n'); if (index < 0) break;
      frames.push(JSON.parse(output.slice(0, index))); output = output.slice(index + 1);
    }
  });
  const client = new AcpClient({ readable, writable, onUpdate: value => { updates.push(value); },
    onClose: value => failures.push(value.code), ...options });
  t.after(() => { client.close(); readable.destroy(); writable.destroy(); });
  const send = value => readable.write(Buffer.from(JSON.stringify(value) + '\n'));
  const response = (request, result) => send({ jsonrpc: '2.0', id: request.id, result });
  async function requestAt(index) {
    for (let n = 0; n < 100 && frames.length <= index; n++) await tick();
    assert.ok(frames.length > index, 'fixture expected an outbound frame');
    return frames[index];
  }
  async function initialize(result = initResult) {
    const index = frames.length, promise = client.initialize();
    response(await requestAt(index), result); await promise;
  }
  async function session(id = 's1') {
    const index = frames.length, promise = client.newSession({ cwd: '/fixture', mcpServers: [] });
    response(await requestAt(index), { sessionId: id }); await promise;
    return id;
  }
  return { readable, writable, frames, updates, failures, client, send, response, requestAt, initialize, session };
}

test('initialize is provider-neutral; two sessions and out-of-order prompt responses stay bound', async t => {
  const f = fixture(t); await f.initialize(); await f.session('s1'); await f.session('s2');
  assert.equal(f.frames[0].method, 'initialize');
  assert.deepEqual(f.frames[0].params.clientCapabilities, {});
  const first = f.client.prompt('s1', promptBlocks), second = f.client.prompt('s2', promptBlocks);
  const a = await f.requestAt(3), b = await f.requestAt(4);
  assert.notEqual(a.id, b.id);
  f.send(update('s2', 'B')); f.send(update('s1', 'A'));
  f.response(b, { stopReason: 'max_tokens' }); f.response(a, { stopReason: 'end_turn' });
  assert.equal((await first).stopReason, 'end_turn'); assert.equal((await second).stopReason, 'max_tokens');
  assert.deepEqual(f.updates.map(event => event.sessionId), ['s2', 's1']);
  assert.equal(f.client.closed, false);
});

test('initialize negotiates supported protocol versions, not Agent names or package versions', async t => {
  const f = fixture(t, { supportedProtocolVersions: [1, 2] });
  await f.initialize({ ...initResult, protocolVersion: 2, agentInfo: { name: 'another-agent', version: '1000' } });
  assert.equal(f.frames[0].params.protocolVersion, 2);
  await assert.rejects(f.client.initialize(), { code: 'acp_already_initialized' });
  const bad = fixture(t); const promise = bad.client.initialize();
  bad.response(await bad.requestAt(0), { protocolVersion: 20, agentCapabilities: {} });
  await assert.rejects(promise, { code: 'acp_invalid_initialize_result' });
  assert.equal(bad.client.closed, true);
});

test('invalid capability shape, unimplemented client capabilities and pre-init session calls reject', async t => {
  const f = fixture(t);
  await assert.rejects(f.client.newSession({ cwd: '/fixture' }), { code: 'acp_not_initialized' });
  await assert.rejects(f.client.initialize({ clientCapabilities: { terminal: true } }), { code: 'acp_invalid_initialize' });
  const promise = f.client.initialize();
  f.response(await f.requestAt(0), { protocolVersion: 1, agentCapabilities: { loadSession: 'yes' } });
  await assert.rejects(promise, { code: 'acp_invalid_initialize_result' });
});

test('fragmented UTF-8 and coalesced CRLF frames preserve exact content', async t => {
  const f = fixture(t); await f.initialize(); await f.session();
  const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
  const bytes = Buffer.from(JSON.stringify(update('s1', '中文😀')) + '\r\n' + JSON.stringify({ jsonrpc: '2.0', id: request.id, result: { stopReason: 'end_turn' } }) + '\n');
  for (const byte of bytes) f.readable.write(Buffer.from([byte]));
  await promise;
  assert.equal(f.updates[0].update.content.text, '中文😀');
});

test('bounded early session notifications are held until newSession grants their exact ID', async t => {
  const f = fixture(t); await f.initialize();
  const promise = f.client.newSession({ cwd: '/fixture' }), request = await f.requestAt(1);
  f.send(update('s1', 'early')); await tick(); assert.equal(f.updates.length, 0);
  f.response(request, { sessionId: 's1' }); await promise;
  assert.equal(f.updates[0].sessionId, 's1');
});

test('mismatched early ID or unknown-session updates never reach callbacks', async t => {
  const f = fixture(t); await f.initialize();
  const promise = f.client.newSession({ cwd: '/fixture' }), request = await f.requestAt(1);
  f.send(update('wrong')); f.response(request, { sessionId: 's1' });
  await assert.rejects(promise, { code: 'acp_invalid_session_result' }); assert.deepEqual(f.updates, []);
  const other = fixture(t); await other.initialize(); await other.session();
  const pending = other.client.prompt('s1', promptBlocks); await other.requestAt(2);
  other.send(update('wrong'));
  await assert.rejects(pending, { code: 'acp_unknown_session' }); assert.deepEqual(other.updates, []);
});

test('unknown/duplicate response and mismatched prompt result reject without cross-resolution', async t => {
  for (const variant of ['unknown', 'duplicate', 'wrong-session']) {
    const f = fixture(t); await f.initialize(); await f.session();
    const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
    if (variant === 'duplicate') f.response(f.frames[0], initResult);
    else if (variant === 'unknown') f.send({ jsonrpc: '2.0', id: 'unknown', result: { stopReason: 'end_turn' } });
    else f.response(request, { stopReason: 'end_turn', sessionId: 'wrong' });
    await assert.rejects(promise, error => ['acp_unknown_response', 'acp_invalid_prompt_result'].includes(error.code));
  }
});

test('async update consumer finishes before the correlated prompt resolves', async t => {
  let release, entered = false;
  const f = fixture(t, { onUpdate: async () => { entered = true; await new Promise(resolve => { release = resolve; }); } });
  await f.initialize(); await f.session();
  let settled = false;
  const promise = f.client.prompt('s1', promptBlocks).then(value => { settled = true; return value; });
  const request = await f.requestAt(2); f.send(update('s1')); f.response(request, { stopReason: 'end_turn' });
  await tick(); assert.equal(entered, true); assert.equal(settled, false);
  release(); await promise; assert.equal(settled, true);
});

test('permission is denied by default and unsupported reverse methods receive method-not-found', async t => {
  const f = fixture(t); await f.initialize(); await f.session();
  const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
  f.send(permission('s1'));
  assert.deepEqual((await f.requestAt(3)).result, denied);
  f.send({ jsonrpc: '2.0', id: 'peer-fs', method: 'fs/read_text_file', params: { sessionId: 's1', path: '/fixture' } });
  assert.equal((await f.requestAt(4)).error.code, -32601);
  f.response(request, { stopReason: 'end_turn' }); await promise;
});

test('explicit callback can select only an offered option and carry question answers unchanged', async t => {
  let received, signal;
  const f = fixture(t, { onPermission: (params, context) => {
    received = params; signal = context.signal;
    return { ...allow, answers: { '0': 'A, with details' } };
  } });
  await f.initialize(); await f.session();
  const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
  f.send(permission('s1')); const reply = await f.requestAt(3);
  assert.equal(received.toolCall._meta.qwenInteractionKind, 'user_question');
  assert.deepEqual(reply.result, { ...allow, answers: { '0': 'A, with details' } });
  assert.equal(signal.aborted, true);
  f.response(request, { stopReason: 'end_turn' }); await promise;
});

test('unoffered selection, throw, malformed and oversized permission answers fail closed to denial', async t => {
  for (const callback of [() => ({ outcome: { outcome: 'selected', optionId: 'not-offered' } }),
    () => { throw new Error('PRIVATE'); }, () => ({ ...allow, answers: { '0': 12 } }),
    () => ({ ...allow, answers: { '0': 'x'.repeat(1048577) } })]) {
    const f = fixture(t, { onPermission: callback }); await f.initialize(); await f.session();
    const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
    f.send(permission('s1')); assert.deepEqual((await f.requestAt(3)).result, denied);
    f.response(request, { stopReason: 'end_turn' }); await promise;
  }
});

test('cancel is a notification; late updates continue and late permission acceptance cannot send', async t => {
  let release, signal, calls = 0;
  const f = fixture(t, { onPermission: (_params, context) => {
    calls++; signal = context.signal; return new Promise(resolve => { release = resolve; });
  } });
  await f.initialize(); await f.session();
  let settled = false;
  const promise = f.client.prompt('s1', promptBlocks).then(result => { settled = true; return result; });
  const request = await f.requestAt(2); f.send(permission('s1')); await tick();
  await f.client.cancel('s1');
  assert.equal(signal.aborted, true); assert.deepEqual(f.frames[3].result, denied);
  assert.equal(f.frames[4].method, 'session/cancel'); assert.equal(Object.hasOwn(f.frames[4], 'id'), false);
  assert.equal(settled, false);
  release(allow); f.send(update('s1', 'late')); f.send(permission('s1', 'peer-2'));
  assert.deepEqual((await f.requestAt(5)).result, denied); assert.equal(calls, 1);
  f.response(request, { stopReason: 'cancelled' });
  assert.equal((await promise).stopReason, 'cancelled');
  assert.equal(f.updates[0].update.content.text, 'late');
  assert.equal(f.frames.filter(frame => frame.id === 'peer-1').length, 1);
});

test('prompt completion revokes an outstanding permission and session cancellation stays isolated', async t => {
  const releases = new Map(), signals = new Map();
  const f = fixture(t, { onPermission: (params, { signal }) => {
    signals.set(params.sessionId, signal); return new Promise(resolve => releases.set(params.sessionId, resolve));
  } });
  await f.initialize(); await f.session('s1'); await f.session('s2');
  const a = f.client.prompt('s1', promptBlocks), b = f.client.prompt('s2', promptBlocks);
  const ra = await f.requestAt(3), rb = await f.requestAt(4);
  f.send(permission('s1', 'peer-a')); f.send(permission('s2', 'peer-b')); await tick();
  await f.client.cancel('s1'); assert.equal(signals.get('s1').aborted, true); assert.equal(signals.get('s2').aborted, false);
  f.response(rb, { stopReason: 'end_turn' }); await b; assert.equal(signals.get('s2').aborted, true);
  releases.get('s2')(allow); await tick();
  assert.deepEqual(f.frames.find(frame => frame.id === 'peer-b').result, denied);
  f.response(ra, { stopReason: 'cancelled' }); await a;
});

test('wrong-session and duplicate reverse permissions close without invoking callbacks', async t => {
  let calls = 0;
  const f = fixture(t, { onPermission: () => { calls++; return allow; } }); await f.initialize(); await f.session();
  const promise = f.client.prompt('s1', promptBlocks); await f.requestAt(2);
  f.send(permission('wrong')); await assert.rejects(promise, { code: 'acp_unknown_session' }); assert.equal(calls, 0);
  const duplicate = fixture(t); await duplicate.initialize(); await duplicate.session();
  const pending = duplicate.client.prompt('s1', promptBlocks); await duplicate.requestAt(2);
  duplicate.send(permission('s1')); await duplicate.requestAt(3); duplicate.send(permission('s1'));
  await assert.rejects(pending, { code: 'acp_duplicate_peer_request' });
});

test('outbound request timeout rejects all pending; permission timeout denies and ignores late callback', async t => {
  const f = fixture(t, { requestTimeoutMs: 20 });
  await assert.rejects(f.client.initialize(), { code: 'acp_request_timeout' }); assert.equal(f.client.closed, true);
  let release;
  const other = fixture(t, { permissionTimeoutMs: 20, onPermission: () => new Promise(resolve => { release = resolve; }) });
  await other.initialize(); await other.session();
  const promise = other.client.prompt('s1', promptBlocks), request = await other.requestAt(2);
  other.send(permission('s1')); await new Promise(resolve => setTimeout(resolve, 40));
  assert.deepEqual((await other.requestAt(3)).result, denied); release(allow); await tick();
  assert.equal(other.frames.length, 4);
  other.response(request, { stopReason: 'end_turn' }); await promise;
});

test('explicit close and broken streams reject pending and abort callbacks without owning streams', async t => {
  const f = fixture(t); const promise = f.client.initialize(); f.client.close();
  await assert.rejects(promise, { code: 'acp_closed' }); assert.equal(f.readable.destroyed, false); assert.equal(f.writable.destroyed, false);
  assert.equal(f.readable.listenerCount('data'), 0); assert.deepEqual(f.failures, ['acp_closed']); f.client.close();
  const broken = fixture(t); const pending = broken.client.initialize(); broken.readable.end();
  await assert.rejects(pending, { code: 'acp_disconnected' });
  const errored = fixture(t); const waiting = errored.client.initialize(); errored.writable.emit('error', new Error('PRIVATE'));
  await assert.rejects(waiting, { code: 'acp_stream_error' });
});

test('partial EOF, malformed JSON/UTF-8, arrays and oversize frames reject without raw payload errors', async t => {
  for (const [bytes, end, code] of [
    [Buffer.from('{"private":"secret"'), true, 'acp_truncated_frame'],
    [Buffer.from('PRIVATE\n'), false, 'acp_invalid_frame'],
    [Buffer.from([0xff, 10]), false, 'acp_invalid_frame'],
    [Buffer.from('[]\n'), false, 'acp_invalid_message'],
    [Buffer.from('x'.repeat(1048577)), false, 'acp_frame_limit'],
  ]) {
    const f = fixture(t); const promise = f.client.initialize(); f.readable.write(bytes); if (end) f.readable.end();
    await assert.rejects(promise, error => error.code === code && !error.message.includes('PRIVATE'));
  }
});

test('a final complete response is dispatched before immediate EOF rejects other pending work', async t => {
  const f = fixture(t); await f.initialize(); await f.session('s1'); await f.session('s2');
  const a = f.client.prompt('s1', promptBlocks), b = f.client.prompt('s2', promptBlocks);
  const request = await f.requestAt(3); await f.requestAt(4);
  const rejected = assert.rejects(b, { code: 'acp_disconnected' });
  f.readable.end(Buffer.from(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: { stopReason: 'end_turn' } }) + '\n'));
  assert.equal((await a).stopReason, 'end_turn'); await rejected;
  assert.equal(f.client.closed, true);
});

test('bounded request/session/peer capacities and local argument validation do not send extra work', async t => {
  const f = fixture(t, { maxPending: 1, maxSessions: 2, maxPeerRequests: 1 }); await f.initialize(); await f.session('s1'); await f.session('s2');
  await assert.rejects(f.client.newSession({ cwd: '/fixture' }), { code: 'acp_session_limit' });
  await assert.rejects(f.client.prompt('wrong', promptBlocks), { code: 'acp_unknown_session' });
  await assert.rejects(f.client.prompt('s1', []), { code: 'acp_invalid_prompt' });
  const a = f.client.prompt('s1', promptBlocks); await f.requestAt(3);
  await assert.rejects(f.client.prompt('s1', promptBlocks), { code: 'acp_session_busy' });
  await assert.rejects(f.client.prompt('s2', promptBlocks), { code: 'acp_pending_limit' });
  f.send(permission('s1')); await f.requestAt(4); f.send(permission('s1', 'another'));
  await assert.rejects(a, { code: 'acp_peer_request_limit' });
});

test('slow update callbacks and blocked writable streams are bounded', async t => {
  let release;
  const f = fixture(t, { maxQueueBytes: 1024, onUpdate: () => new Promise(resolve => { release = resolve; }) });
  await f.initialize(); await f.session();
  const pending = f.client.prompt('s1', promptBlocks); await f.requestAt(2);
  f.send(update('s1')); await tick(); f.send(update('s1', 'x'.repeat(1100)));
  await assert.rejects(pending, { code: 'acp_read_limit' }); release();
  const readable = new PassThrough(); let flush;
  const writable = new Writable({ write(_chunk, _encoding, callback) { flush = callback; } });
  const client = new AcpClient({ readable, writable, requestTimeoutMs: 20 });
  const init = client.initialize(); await assert.rejects(init, { code: 'acp_request_timeout' });
  flush(); readable.destroy(); writable.destroy();
  assert.equal(client.closed, true);
});

test('remote errors retain only numeric code; terminal errors do not authorize success', async t => {
  const f = fixture(t); await f.initialize(); await f.session();
  const promise = f.client.prompt('s1', promptBlocks), request = await f.requestAt(2);
  f.send({ jsonrpc: '2.0', id: request.id, error: { code: -32001, message: 'PRIVATE', data: { secret: 'PRIVATE' } } });
  await assert.rejects(promise, error => error.code === 'acp_remote_error' && error.rpcCode === -32001 && !JSON.stringify(error).includes('PRIVATE'));
  assert.equal(f.client.closed, false);
  const next = f.client.prompt('s1', promptBlocks), nextRequest = await f.requestAt(3);
  f.response(nextRequest, { stopReason: 'unknown' });
  await assert.rejects(next, { code: 'acp_invalid_prompt_result' });
});
