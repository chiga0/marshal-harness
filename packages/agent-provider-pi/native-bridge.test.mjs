import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createPiProvider} from './index.mjs';
import {installNativeBridge} from './native-bridge.mjs';
import {BRIDGE_PROFILE, BRIDGE_TITLE} from './bridge-contract.mjs';
import * as sdk from './fixtures/sdk/index.mjs';
import * as shell from './fixtures/sdk/utils/shell.js';
import {createFileBusiness, fileLayoutDigest} from '../task-business/index.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {digest, encode} from '../task-store/store.mjs';
import {launchProtocol} from '../agent-runtime/index.mjs';

const fixture = fileURLToPath(new URL('./bridge-agent.fixture.mjs', import.meta.url));
// Opt-in repeat with an installed native SDK exercises the same extension and
// original tools WITHOUT starting Pi, reading login or making a model request.
const sdkEntry = process.env.MARSHAL_PI_TEST_SDK ?? fileURLToPath(new URL('./fixtures/sdk/index.mjs', import.meta.url));
const provider = mode => createPiProvider({id: 'pi-native', executable: process.execPath, args: [fixture, mode], bridge: {sdkEntry}});
const allow = request => ({outcome: {outcome: 'selected', optionId: request.options.find(option => option.kind === 'allow_once').optionId}});
function directory(t) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-pi-bridge-')));
  t.after(() => fs.rmSync(root, {recursive: true, force: true})); return root;
}
const options = (t, extra = {}) => ({cwd: directory(t), deadline: Date.now() + 15000, prompt: 'approved native fixture', ...extra});
const clean = result => { assert.equal(result.cleanup?.cleaned, true); assert.equal(result.cleanup.scope, 'inherited-process-group'); };

test('custody Pi denied shell adds no obligation; allowed shell records before effect and SQL failure refuses', {timeout: 15000}, async t => {
  for (const mode of ['deny', 'allow', 'sql-failure']) {
    const input = options(t), order = [];
    input.executionContext = {
      launch: (options, callbacks) => launchProtocol({...options, createClient: callbacks.createClient}),
      extraScope(code) { order.push('durable'); assert.equal(code, 'pi_shell_extra_scope');
        assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
        if (mode === 'sql-failure') throw Error('fixture rejected transaction'); },
    };
    input.onPermission = request => { order.push('policy'); assert.deepEqual(order, ['policy']);
      return mode === 'deny' ? {outcome: {outcome: 'cancelled'}} : allow(request); };
    const handle = provider('shell').start(input); t.after(() => handle.stop());
    const result = await handle.completion; clean(result);
    assert.deepEqual(order, mode === 'deny' ? ['policy'] : ['policy', 'durable']);
    assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), mode === 'allow');
  }
});

test('actual extension wrapper prompts final immutable arguments and preserves originally active tools', async t => {
  const cwd = directory(t), handlers = new Map(), tools = new Map(), active = ['write', 'custom']; let observed, reply;
  const api = {on: (name, fn) => handlers.set(name, fn), registerTool: tool => tools.set(tool.name, tool),
    getActiveTools: () => active, setActiveTools: next => assert.deepEqual(next, active)};
  const config = {profile: BRIDGE_PROFILE, nonce: 'a'.repeat(64), cwd, deadline: Date.now() + 10000};
  const ctx = {cwd, sessionManager: {getSessionId: () => 'session'}, ui: {notify() {}, confirm(title, value) {
    assert.equal(title, BRIDGE_TITLE); observed = JSON.parse(value); return new Promise(resolve => { reply = resolve; }); }}};
  installNativeBridge(api, {config, sdk, shell}); await handlers.get('session_start')({}, ctx);
  assert.deepEqual([...tools.keys()], ['write']); assert.equal(tools.get('write').promptSnippet, 'original write');
  const input = {path: 'approved.txt', content: 'approved bytes'}, controller = new AbortController();
  handlers.get('tool_execution_start')({toolCallId: 'call-one', toolName: 'write', args: input}, ctx);
  assert.equal(tools.get('write').prepareArguments(input), input);
  const writing = tools.get('write').execute('call-one', input, controller.signal, () => {}, ctx);
  input.path = 'unapproved.txt'; input.content = 'changed after permission';
  assert.deepEqual(observed.input, {path: 'approved.txt', content: 'approved bytes'});
  reply(true); await writing;
  assert.equal(fs.readFileSync(path.join(cwd, 'approved.txt'), 'utf8'), 'approved bytes'); assert.equal(fs.existsSync(path.join(cwd, 'unapproved.txt')), false);
  await assert.rejects(handlers.get('session_start')({}, ctx), /pi_bridge_invalid_session/);
});

test('native bridge applies FileBusiness authorization before real file work and collects original Runtime identity', {timeout: 25000}, async t => {
  const root = directory(t), parent = path.join(root, 'executions'); fs.mkdirSync(parent, {mode: 0o700});
  const depot = ArtifactDepot.create(path.join(root, 'depot')); t.after(() => depot.close());
  const artifact = {id: 'source', kind: 'input', taskId: null, status: 'ready', ...depot.put(Buffer.from('original input'))};
  const layout = {inputs: [{path: 'input.txt', source: {kind: 'input', id: 'source'}}], allowedPaths: ['output.txt']};
  const node = {id: 'author', role: 'author', goal: 'produce file', scope: [], providerId: 'pi-native'};
  const planDigest = 'sha256:' + 'a'.repeat(64), input = {task: {intent: '文件协作交付', context: {inputRefs: ['source']}}, node,
    plan: {taskId: 'task', digest: planDigest, nodes: [node], edges: []}, upstream: [], inputArtifacts: [artifact]};
  const base = {workerId: 'worker', taskId: 'task', nodeId: node.id, role: node.role, providerId: node.providerId, commandId: 'command',
    generation: '1', inputDigest: digest(encode(input)), planDigest, deadline: Date.now() + 15000, input};
  const ticket = {...base, reservationDigest: digest(encode(base))}, context = {signal: new AbortController().signal, deadline: base.deadline};
  let started; const requests = [];
  const business = createFileBusiness({parent, depot, layoutFor: () => layout,
    approvedLayout: () => ({nodeId: node.id, planDigest, layoutDigest: fileLayoutDigest(layout)}), observeExecution: () => started,
    authorize: (_ticket, request) => { requests.push(request);
      const name = request.toolCall._meta.toolName, raw = request.toolCall.rawInput;
      if (name === 'read' && raw.path === 'input.txt' || name === 'write' && raw.path === 'output.txt') return allow(request);
      return {outcome: {outcome: 'cancelled'}};
    }});
  t.after(() => business.close()); const prepared = await business.prepare(ticket, context);
  const handle = provider('file-business').start({...prepared, deadline: ticket.deadline}); t.after(() => handle.stop());
  started = await handle.started; const result = await handle.completion; clean(result);
  assert.equal(result.status, 'completed'); assert.equal(result.cleanup.started, started);
  assert.equal(fs.readFileSync(path.join(prepared.cwd, 'input.txt'), 'utf8'), 'original input');
  const candidate = (await business.collect(ticket, result, context)).result;
  assert.equal(candidate.files.length, 1); assert.equal(candidate.files[0].path, 'output.txt');
  assert.equal(depot.get({digest: candidate.files[0].digest, bytes: candidate.files[0].bytes}).toString(), 'independent native candidate');
  assert.deepEqual(requests.map(request => request.toolCall._meta.toolName), ['read', 'write']);
  assert.equal(candidate.decision, undefined);
});

test('deny/default/invalid permission never execute writes; stale answer after stop cannot gain authority', {timeout: 30000}, async t => {
  for (const onPermission of [undefined, () => ({outcome: {outcome: 'cancelled'}}), () => ({outcome: {outcome: 'selected', optionId: 'allow-always'}})]) {
    const input = options(t, {onPermission}), handle = provider('write').start(input); t.after(() => handle.stop());
    clean(await handle.completion); assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
  }
  let arrive, answer; const entered = new Promise(resolve => { arrive = resolve; });
  const input = options(t, {onPermission: (_request, {signal}) => { arrive(signal); return new Promise(resolve => { answer = resolve; }); }});
  const handle = provider('write').start(input); t.after(() => handle.stop()); const signal = await entered;
  const stopped = handle.stop(); answer({outcome: {outcome: 'selected', optionId: 'allow-once'}});
  const result = await stopped; clean(result); assert.equal(signal.aborted, true); assert.equal(result.status, 'cancelled');
  assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
});

test('owned shell uses actual inherited execution; guard kills surviving background child before cleanup', {timeout: 30000}, async t => {
  for (const mode of ['shell', 'shell-child', 'shell-timeout']) {
    const input = options(t, {onPermission: allow}), handle = provider(mode).start(input); t.after(() => handle.stop());
    const result = await handle.completion; clean(result); assert.equal(result.status, 'completed');
    if (mode === 'shell') assert.equal(fs.readFileSync(path.join(input.cwd, 'output.txt'), 'utf8'), 'actual-shell-output');
    if (mode === 'shell-child') {
      const pid = Number(fs.readFileSync(path.join(input.cwd, 'child.pid'), 'utf8'));
      assert.ok(Number.isInteger(pid) && pid > 1);
      let alive = true; for (let count = 0; count < 30 && alive; count++) {
        try { process.kill(pid, 0); await new Promise(resolve => setTimeout(resolve, 10)); } catch (error) { assert.equal(error.code, 'ESRCH'); alive = false; }
      }
      assert.equal(alive, false, 'actual shell descendant must be absent before directory release');
    }
  }
});

test('cancel during actual native shell execution waits for original whole-group cleanup', {timeout: 20000}, async t => {
  const input = options(t, {onPermission: allow}), handle = provider('shell-hang').start(input); t.after(() => handle.stop());
  const file = path.join(input.cwd, 'shell.pid');
  for (let count = 0; count < 500 && !fs.existsSync(file); count++) await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(fs.existsSync(file), true, 'actual shell entered before cancellation');
  const pid = Number(fs.readFileSync(file, 'utf8')); assert.ok(Number.isSafeInteger(pid) && pid > 1);
  const result = await handle.stop(); clean(result); assert.equal(result.status, 'cancelled');
  assert.throws(() => process.kill(pid, 0), {code: 'ESRCH'});
});

test('missing/foreign bridge and bypassed native wrapper cannot manufacture permission or cleanup', {timeout: 20000}, async t => {
  for (const mode of ['bad-nonce', 'bypass', 'bypass-error', 'custom', 'no-bridge']) {
    const input = options(t, {deadline: Date.now() + (mode === 'no-bridge' ? 1500 : 10000), onPermission: allow});
    const handle = provider(mode).start(input); t.after(() => handle.stop()); const result = await handle.completion;
    if (['bad-nonce', 'bypass', 'bypass-error'].includes(mode)) { assert.equal(result.status, 'unknown'); assert.equal(result.cleanup, null); }
    else { clean(result); if (mode === 'no-bridge') assert.notEqual(result.status, 'completed'); }
    assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
  }
});

test('selected native definition is safe when schema rejection or cancellation wins before execute', {timeout: 12000}, async t => {
  for (const mode of ['invalid-arguments', 'cancel-before-execute']) {
    let permissions = 0;
    const input = options(t, {onPermission: () => { permissions++; return allow(); }}), handle = provider(mode).start(input);
    t.after(() => handle.stop()); const result = await handle.completion; clean(result);
    assert.notEqual(result.status, 'unknown'); assert.equal(permissions, 0);
    assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
  }
});

test('truncated assistant evidence binds every native unexecuted call, not error prose', async t => {
  const cwd = directory(t), handlers = new Map(), tools = new Map(), messages = [];
  const api = {on: (name, fn) => handlers.set(name, fn), registerTool: tool => tools.set(tool.name, tool), getActiveTools: () => ['write'], setActiveTools() {}};
  const ctx = {cwd, ui: {notify(value) { messages.push(JSON.parse(value)); }}};
  installNativeBridge(api, {config: {profile: BRIDGE_PROFILE, nonce: 'b'.repeat(64), cwd, deadline: Date.now() + 10000}, sdk, shell});
  await handlers.get('session_start')({}, ctx);
  handlers.get('message_end')({message: {role: 'assistant', stopReason: 'length', content:
    ['one', 'two'].map(id => ({type: 'toolCall', id, name: 'write', arguments: {path: 'output.txt'}}))}});
  for (const id of ['one', 'two', 'foreign']) {
    handlers.get('tool_execution_start')({toolCallId: id, toolName: 'write', args: {}}, ctx);
    handlers.get('tool_execution_end')({toolCallId: id, toolName: 'write', isError: true}, ctx);
    handlers.get('message_end')({message: {role: 'toolResult'}});
  }
  assert.deepEqual(messages.filter(message => message.type === 'not-executed').map(message => message.toolCallId), ['one', 'two']);
  assert.equal(fs.existsSync(path.join(cwd, 'output.txt')), false);
});

// Explicit installed SDK also tests the ORIGINAL agent-core argument validator,
// truncated-call producer and before-execute cancellation, not hand-built ends.
if (process.env.MARSHAL_PI_TEST_SDK) test('installed native agent-core rejects invalid/truncated/cancelled calls before execution; replacement remains unknown', {timeout: 25000}, async t => {
  const coreEntry = path.resolve(path.dirname(sdkEntry), '../node_modules/@earendil-works/pi-agent-core/dist/agent-loop.js');
  const peer = fileURLToPath(new URL('./native-core.fixture.mjs', import.meta.url));
  for (const mode of ['invalid-arguments', 'truncated', 'cancel-before-execute', 'bypass-error']) {
    let permissions = 0;
    const input = options(t, {onPermission: request => { permissions++; return allow(request); }});
    const p = createPiProvider({id: 'pi-core', executable: process.execPath, args: [peer, mode, coreEntry], bridge: {sdkEntry}});
    const handle = p.start(input); t.after(() => handle.stop()); const result = await handle.completion;
    const proof = JSON.parse(fs.readFileSync(path.join(input.cwd, 'core-proof.json')));
    assert.deepEqual(proof.ends, [{id: 'write-one', isError: true}]); assert.equal(proof.permission, 0); assert.equal(permissions, 0);
    assert.equal(proof.definitionSelected, ['invalid-arguments', 'cancel-before-execute'].includes(mode) ? 1 : 0);
    assert.equal(proof.notExecuted, mode === 'truncated' ? 1 : 0);
    assert.equal(proof.executeEntered, mode === 'bypass-error' ? 1 : 0);
    if (mode === 'bypass-error') { assert.equal(result.status, 'unknown'); assert.equal(result.cleanup, null); }
    else { clean(result); assert.notEqual(result.status, 'unknown'); }
    assert.equal(fs.existsSync(path.join(input.cwd, 'output.txt')), false);
  }
});
