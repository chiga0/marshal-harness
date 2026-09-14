import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createAcpProvider, MAX_OUTPUT_TEXT_BYTES} from './index.mjs';
import {launchAcp} from '../agent-runtime/index.mjs';

const fixture = fileURLToPath(new URL('./agent.fixture.mjs', import.meta.url));
const provider = mode => createAcpProvider({id: 'fixture', executable: process.execPath, args: [fixture, mode]});
function input(t, extra = {}) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'acp-provider-test-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true}));
  return {cwd, deadline: Date.now() + 10000, prompt: 'Perform the approved fixture work', ...extra};
}
function cleaned(result, allowNoStart = false) {
  assert.equal(result.cleanup.cleaned, true); assert.equal(result.cleanup.scope, 'inherited-process-group');
  assert.equal(result.cleanup.guardExit.observed, true); assert.equal(result.cleanup.guardExit.signal, 'SIGKILL');
  if (result.cleanup.started === null) {
    assert.equal(allowNoStart, true, 'only explicitly observed pre-start cancellation/deadline may lack an Agent');
    assert.equal(result.cleanup.agentExit.observed, false);
  } else assert.throws(() => process.kill(result.cleanup.started.guardPid, 0), {code: 'ESRCH'});
}

test('one Worker exposes handle immediately then initializes, runs, normalizes and cleans', {timeout: 15000}, async t => {
  const events = [], p = provider('normal');
  const handle = p.start(input(t, {onProgress: event => events.push(event)}));
  t.after(() => handle.stop()); assert.equal(typeof handle.stop, 'function'); assert.ok(handle.completion instanceof Promise);
  assert.equal(p.profile, 'ordinary-user'); assert.equal(handle.snapshot().phase, 'starting');
  const started = await handle.started; assert.ok(started.agentPid > 0);
  const result = await handle.completion; cleaned(result);
  assert.equal(result.status, 'completed'); assert.equal(result.stopReason, 'end_turn'); assert.equal(result.outputText, 'public output');
  assert.equal(result.usage.source, 'unavailable'); assert.equal(result.usage.tokens, null);
  assert.equal(result.usage.cost, null); assert.equal(result.sessionId, 'session-fixture');
  assert.ok(events.some(e => e.tool?.kind === 'read' && e.tool.status === 'in_progress'));
  assert.ok(events.some(e => e.tool?.status === 'completed'));
  assert.doesNotMatch(JSON.stringify({events, result}), /PRIVATE_|rawInput|rawOutput|_meta|accepted/);
  assert.equal(handle.snapshot().phase, 'terminal'); assert.equal(await handle.stop(), result);
});

test('fragmented text and private thinking use byte budgets without flooding Core progress', {timeout:30000}, async t => {
  for (const mode of ['budget-message','budget-thought']) {
    const events=[];
    const handle=provider(mode).start(input(t,{onProgress:event=>events.push(event)})); t.after(()=>handle.stop());
    const result=await handle.completion; cleaned(result);
    assert.equal(result.status,'completed');
    assert.equal(result.outputText,mode==='budget-message'?'x'.repeat(4097):'public');
    assert.ok(events.length < 20, `bounded public progress: ${events.length}`);
    assert.ok(events.every(event=>!JSON.stringify(event).includes('xxxx')));
  }
});

test('thought byte overflow and empty, unknown, repeated-tool floods stay bounded', {timeout:60000}, async t => {
  for (const mode of ['budget-thought-overflow','budget-empty','budget-unknown','budget-tool']) {
    const handle=provider(mode).start(input(t)); t.after(()=>handle.stop());
    const result=await handle.completion; cleaned(result);
    assert.equal(result.status,'failed',mode);
    assert.equal(result.reason,'provider_progress_limit',mode);
  }
});

test('mixed tool/text still fails closed when consumer progress budget is exhausted', {timeout:15000}, async t => {
  let count=0, tools=0;
  const handle=provider('budget-mixed').start(input(t,{onProgress:event=>{
    if (++count > 4096) throw Error('consumer progress exhausted');
    if(event.tool) tools++;
  }})); t.after(()=>handle.stop());
  const result=await handle.completion; cleaned(result);
  assert.equal(result.status,'failed'); assert.equal(result.reason,'provider_progress_failed');
  assert.equal(count,4097); assert.ok(tools>4000);
});

test('stop is available before bootstrap and during initialize without waiting for model', {timeout: 15000}, async t => {
  for (const immediate of [true, false]) {
    let initialized; const reached = new Promise(resolve => { initialized = resolve; });
    const handle = provider('hang-init').start(input(t, {onProgress: event => { if (event.phase === 'initializing') initialized(); }}));
    t.after(() => handle.stop());
    if (!immediate) await reached;
    const stopping = handle.stop(); assert.equal(handle.stop(), stopping);
    const result = await stopping; cleaned(result, immediate && await handle.started === null); assert.equal(result.status, 'cancelled'); assert.equal(result.sessionId, null);
  }
});

test('running cancel revokes pending permission and waits for real owned cleanup', {timeout: 15000}, async t => {
  let entered, permissionSignal, release;
  const waiting = new Promise(resolve => { entered = resolve; });
  const handle = provider('permission').start(input(t, {onPermission: (request, context) => {
    permissionSignal = context.signal; entered();
    assert.equal(request.sessionId, 'session-fixture'); assert.doesNotMatch(JSON.stringify(request), /PRIVATE_META|_meta/);
    return new Promise(resolve => { release = resolve; });
  }}));
  t.after(() => handle.stop()); await waiting;
  const result = await handle.stop(); cleaned(result); assert.equal(result.status, 'cancelled'); assert.equal(permissionSignal.aborted, true);
  release({outcome: {outcome: 'selected', optionId: 'once'}});
});

test('native tool permission is denied by default and only exact callback option is selected', {timeout: 15000}, async t => {
  for (const allowed of [false, true]) {
    const handle = provider('permission').start(input(t, {onPermission: allowed ? request => {
      assert.deepEqual(request.toolCall.rawInput, {path: 'fixture.txt'});
      return {outcome: {outcome: 'selected', optionId: 'once'}};
    } : undefined}));
    t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
    const selection = JSON.parse(result.outputText);
    assert.equal(selection.outcome.outcome, allowed ? 'selected' : 'cancelled');
  }
});

test('custody permission refusal is clean; allow waits for durable scope and failed SQL never sends allow', {timeout: 15000}, async t => {
  for (const mode of ['deny', 'allow', 'sql-failure']) {
    const order = [], executionContext = {
      launch: (options, callbacks) => launchAcp({...options, onUpdate: callbacks.onUpdate, onPermission: callbacks.onPermission}),
      extraScope(code) { order.push('durable'); assert.equal(code, 'acp_tool_scope_unproven'); if (mode === 'sql-failure') throw Error('fixture rejected transaction'); },
    };
    const handle = provider('permission-execute').start(input(t, {executionContext, onPermission: () => {
      order.push('policy'); assert.deepEqual(order, ['policy']);
      return {outcome: {outcome: 'selected', optionId: mode === 'deny' ? 'deny' : 'once'}};
    }}));
    t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
    assert.deepEqual(order, mode === 'deny' ? ['policy'] : ['policy', 'durable']);
    const reply = result.outputText ? JSON.parse(result.outputText) : null;
    assert.equal(reply?.outcome?.optionId === 'once', mode === 'allow');
  }
});

test('failed-only unsafe tools are unknown unless bound to one exact unused session refusal', {timeout: 20000}, async t => {
  const cases = [
    ['failed-only-execute', 1], ['failed-only-fetch', 1], ['failed-only-other', 1],
    ['permission-execute-denied-failed', 0], ['permission-execute-foreign', 1],
    ['permission-execute-reused-call', 1], ['permission-execute-reused-permission', 1],
    ['permission-execute-repeated-failed', 1], ['permission-execute-kind-drift', 1], ['permission-execute-started', 2],
  ];
  for (const [mode, expected] of cases) {
    const durable = [], progress = [], executionContext = {
      launch: (options, callbacks) => launchAcp({...options, onUpdate: callbacks.onUpdate, onPermission: callbacks.onPermission}),
      extraScope(code) { durable.push(code); },
    };
    const handle = provider(mode).start(input(t, {executionContext, onProgress: event => progress.push(event),
      onPermission: () => ({outcome: {outcome: 'selected', optionId: 'deny'}})}));
    t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
    assert.equal(result.status, 'completed', mode);
    assert.deepEqual(durable, Array(expected).fill('acp_tool_scope_unproven'), mode);
    assert.ok(progress.some(event => event.tool?.status === 'failed'), mode);
    if (mode.startsWith('permission-')) assert.equal(JSON.parse(result.outputText).outcome.optionId, 'deny', mode);
  }
});

test('failed-only durable recording failure cannot become a successful provider result', {timeout: 10000}, async t => {
  let attempts = 0;
  const executionContext = {
    launch: (options, callbacks) => launchAcp({...options, onUpdate: callbacks.onUpdate, onPermission: callbacks.onPermission}),
    extraScope() { attempts++; throw Error('fixture rejected transaction'); },
  };
  const handle = provider('failed-only-execute').start(input(t, {executionContext}));
  t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
  assert.equal(attempts, 1); assert.equal(result.status, 'failed'); assert.equal(result.outputText, '');
});

test('output bound/refusal/deadline remain honest non-delivery terminals and clean owned group', {timeout: 15000}, async t => {
  for (const mode of ['overflow', 'refusal', 'hang-prompt']) {
    const handle = provider(mode).start(input(t, mode === 'hang-prompt' ? {deadline: Date.now() + 1200} : {}));
    t.after(() => handle.stop()); const result = await handle.completion;
    // An absolute deadline also fences bootstrap; scheduling load must not
    // force us to invent an Agent PID or change the original time budget.
    cleaned(result, mode === 'hang-prompt' && await handle.started === null);
    assert.equal(result.status, 'failed');
    assert.equal(result.reason, {overflow: 'provider_output_limit', refusal: 'agent_refusal', 'hang-prompt': 'provider_deadline'}[mode]);
    assert.ok(Buffer.byteLength(result.outputText) <= MAX_OUTPUT_TEXT_BYTES);
  }
});

test('a stuck progress sink is bounded and cannot prevent cleanup', {timeout: 15000}, async t => {
  const handle = provider('normal').start(input(t, {onProgress: () => new Promise(() => {})}));
  t.after(() => handle.stop()); const result = await handle.completion; cleaned(result);
  assert.equal(result.status, 'failed'); assert.equal(result.reason, 'provider_progress_timeout');
});

test('invalid HTTP-style launch choices fail before execution; missing executable remains failed with cleanup', {timeout: 15000}, async t => {
  assert.throws(() => createAcpProvider({id: 'x', executable: 'relative'}), {code: 'provider_invalid_configuration'});
  const p = provider('normal');
  assert.throws(() => p.start(input(t, {deadline: 0})), {code: 'provider_invalid_input'});
  assert.throws(() => p.start(input(t, {prompt: 'x'.repeat(256 * 1024 + 1)})), {code: 'provider_invalid_input'});
  const i = input(t), handle = createAcpProvider({id: 'missing', executable: path.join(i.cwd, 'missing')}).start(i);
  const result = await handle.completion;
  assert.equal(result.status, 'failed'); assert.equal(result.cleanup.cleaned, true); assert.equal(await handle.started, null);
});


test('opt-in typed activity keeps hidden thought private and waits for whole output before redacted snippet', {timeout:15000}, async t => {
  const events = [], handle = provider('observed').start(input(t, {observability: true, onProgress: event => events.push(event)}));
  t.after(() => handle.stop()); const result = await handle.completion; cleaned(result); assert.equal(result.status, 'completed');
  assert.ok(events.some(event => event.activity === 'output')); assert.ok(events.some(event => event.activity === 'thinking'));
  assert.ok(events.filter(event => event.publicText).length === 1);
  assert.doesNotMatch(JSON.stringify(events), /PRIVATE_|fixture-secret|Bearer fixture-/);
  const final = events.at(-1); assert.match(final.publicText, /已隐藏/); assert.match(final.publicText, /公开输出/);
  assert.deepEqual(final.model, {id: 'fixture-model', source: 'provider-reported'});
  assert.deepEqual(final.usage, {inputTokens:20, outputTokens:4, totalTokens:24, source:'provider-reported', complete:true});
});

test('explicit Qwen transcript extension reports only latest response and never converts metadata into output or cumulative usage', {timeout:15000}, async t => {
  const events = [], p = createAcpProvider({id:'fixture',executable:process.execPath,args:[fixture,'qwen-usage'],usageExtension:'qwen-transcript/v1'});
  const handle = p.start(input(t,{observability:true,onProgress:event=>events.push(event)})); t.after(()=>handle.stop());
  const result = await handle.completion; cleaned(result); assert.equal(result.status,'completed');
  const first = events.find(event=>event.lastResponseUsage);
  assert.equal(first.activity,'tool'); assert.equal(first.publicText,'');
  const last = events.at(-1).lastResponseUsage;
  assert.deepEqual(last,{inputTokens:30,outputTokens:6,totalTokens:36,source:'qwen-acp-meta',scope:'last-response',complete:false,zeroMayBeDefault:true});
  assert.ok(events.every(event=>event.usage === null)); assert.equal(result.usage.source,'unavailable');
  assert.doesNotMatch(JSON.stringify(events),/PRIVATE_|_meta|parentToolCallId/);
});

test('Qwen extension rejects malformed and nested readings, preserves ambiguous zero and leaves generic ACP unchanged', {timeout:30000}, async t => {
  for (const mode of ['qwen-usage-negative','qwen-usage-fraction','qwen-usage-overflow','qwen-usage-missing','qwen-usage-subagent','qwen-usage-zero','qwen-usage']) {
    const events=[], p=createAcpProvider({id:'fixture',executable:process.execPath,args:[fixture,mode],...(mode === 'qwen-usage' ? {} : {usageExtension:'qwen-transcript/v1'})});
    const handle=p.start(input(t,{observability:true,onProgress:event=>events.push(event)}));t.after(()=>handle.stop());const result=await handle.completion;cleaned(result);
    assert.equal(result.status,'completed');
    if(mode === 'qwen-usage-zero') {assert.equal(events.at(-1).lastResponseUsage.totalTokens,0);assert.equal(events.at(-1).lastResponseUsage.complete,false);assert.equal(events.at(-1).lastResponseUsage.zeroMayBeDefault,true);}
    else assert.ok(events.every(event=>!Object.hasOwn(event,'lastResponseUsage')),mode);
  }
  assert.throws(()=>createAcpProvider({id:'fixture',executable:process.execPath,usageExtension:'invented'}),{code:'provider_invalid_configuration'});
});
