import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
import {parseOptions, observe, activeAuthors, checkDelivery, run} from './installed-consumer.fixture.mjs';
import {SOURCE_FILES} from '../task-distribution/index.mjs';
const pins = {package: process.env.MARSHAL_QWEN_TEST_PACKAGE, 'source-head': process.env.MARSHAL_QWEN_TEST_SOURCE,
  'manifest-digest': process.env.MARSHAL_QWEN_TEST_MANIFEST};
const args = ['--package', '/package', '--source-head', 'a'.repeat(40), '--manifest-digest', 'sha256:' + 'b'.repeat(64),
  '--node', '/node', '--qwen-entry', '/qwen/cli-entry.js', '--run-dir', '/private/new', '--scenario', 'delivery', '--execute-real'];
test('explicit execution mode, exact pins, finite scenarios, absolute paths', () => {
  assert.equal(parseOptions(args).scenario, 'delivery');
  assert.equal(parseOptions(args.map(v => v === '--execute-real' ? '--controlled-acp-test' : v))['controlled-acp-test'], true);
  for (const changed of [args.slice(0, -1), [...args, '--controlled-acp-test'], [...args, '--execute-real'],
    [...args, '--extra', 'x'], args.map(v => v === 'delivery' ? 'opencode' : v), args.map(v => v === '/node' ? 'node' : v),
    args.map(v => v === 'a'.repeat(40) ? 'HEAD' : v)]) assert.throws(() => parseOptions(changed));
});
test('bounded observation and HTTP author predicate do not invent process evidence', async () => {
  await assert.rejects(observe(() => assert.fail('expired read'), Boolean, 0), /observation_deadline/);
  const workers = {nextCursor: null, items: ['east', 'west'].map(nodeId => ({nodeId, role: 'author', status: 'running', startedAt: '2026-09-09T00:00:00Z'}))};
  assert.equal(activeAuthors(workers), true);
  for (const changed of [{...workers, nextCursor: 'cursor'}, {...workers, items: workers.items.slice(1)},
    {...workers, items: [...workers.items, workers.items[0]]}, {...workers, items: workers.items.map(w => ({...w, status: 'completed'}))}])
    assert.equal(activeAuthors(changed), false);
});
test('independent original-row oracle rejects wrong totals, extra fields, window and source', () => {
  const dates = {startDate: '2026-09-01', endDate: '2026-09-02'}, bytes = Buffer.from(JSON.stringify({rows: [
    {date: '2026-09-01', region: 'east', status: 'paid', cents: -20}, {date: '2026-09-02', region: 'east', status: 'paid', cents: 0},
    {date: '2026-09-02', region: 'west', status: 'cancelled', cents: 900}, {date: '2026-09-03', region: 'west', status: 'paid', cents: 900}]}));
  const value = {profile: 'regional-paid-window/v1', window: dates, sourceDigest: 'sha256:' + createHash('sha256').update(bytes).digest('hex'),
    files: ['east', 'west'].map(region => ({path: region + '.json', content: JSON.stringify({region, ...dates, count: region === 'east' ? 2 : 0, netCents: region === 'east' ? -20 : 0})}))};
  assert.equal(checkDelivery(Buffer.from(JSON.stringify(value)), bytes, dates).reports[0].count, 2);
  for (const changed of [{...value, extra: 1}, {...value, sourceDigest: 'bad'}, {...value, window: {...dates, endDate: '2026-09-03'}},
    {...value, files: value.files.map(f => ({...f, content: JSON.stringify({...JSON.parse(f.content), netCents: 123})}))}])
    assert.throws(() => checkDelivery(Buffer.from(JSON.stringify(changed)), bytes, dates));
});
test('external consumer and tests are absent from runtime; only installed business/client imports', () => {
  const source = fs.readFileSync(new URL('./installed-consumer.fixture.mjs', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /\bpack\s*\(|startTaskService|from ['"]\.\.\/task-(?:application|service|client|store)|from ['"]\.\/driver/);
  assert.match(source, /packages\/task-regional-window\/service-config\.mjs/);
  assert.ok(!SOURCE_FILES.some(file => /installed-consumer/.test(file)));
});

// A test-only ACP peer with NO network/model. The ORIGINAL installed config
// selects it by its explicit public test package metadata, not a fake config.
const peer = `import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createInterface} from 'node:readline';
const directory = path.dirname(fileURLToPath(import.meta.url));
const mode = fs.readFileSync(path.join(directory, 'mode'), 'utf8');
const send = x => process.stdout.write(JSON.stringify(x) + '\\n');
for await (const line of createInterface({input: process.stdin})) {
 const m = JSON.parse(line), reply = result => send({jsonrpc:'2.0', id:m.id, result});
 if(m.method === 'initialize') reply({protocolVersion:1,agentCapabilities:{loadSession:false}});
 else if(m.method === 'session/new') reply({sessionId:'controlled-window'});
 else if(m.method === 'session/prompt') {
  const text=m.params.prompt[0].text, input=JSON.parse(text.slice(text.indexOf('{"task":')));
  fs.appendFileSync(path.join(directory,'starts.jsonl'), JSON.stringify({role:input.node.role,nodeId:input.node.id})+'\\n',{mode:0o600});
  if(mode === 'cancel') continue;
  setTimeout(() => {
   const dates=finalValues(input.task);
   const rows=JSON.parse(fs.readFileSync('sales.json')).rows.filter(r=>r.status==='paid'&&r.region===input.node.id&&r.date>=dates.startDate&&r.date<=dates.endDate);
   fs.writeFileSync(input.node.id+'.json',JSON.stringify({region:input.node.id,...dates,count:rows.length,netCents:mode==='wrong'?123456:rows.reduce((s,r)=>s+r.cents,0)}),{mode:0o600,flag:'wx'});
   send({jsonrpc:'2.0',method:'session/update',params:{sessionId:m.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'{"message":"test candidate"}'}}}});
   reply({stopReason:'end_turn'});
  },500);
 }
}
`;
for (const scenario of ['delivery', 'cancel', 'wrong']) test('original installed config with controlled ACP: ' + scenario,
  {skip: !pins.package, timeout: 60000}, async t => {
    const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-qwen-installed-test-')));
    fs.chmodSync(parent, 0o700);
    const qwen = path.join(parent, 'qwen'); fs.mkdirSync(qwen, {mode: 0o700});
    fs.writeFileSync(path.join(qwen, 'package.json'), JSON.stringify({name: '@qwen-code/qwen-code', version: '0.0.0-controlled-test', type: 'module'}), {mode: 0o600});
    const policy = pathToFileURL(path.join(pins.package, 'packages/task-regional-window/policy.mjs')).href;
    fs.writeFileSync(path.join(qwen, 'cli-entry.js'), 'import {finalValues} from ' + JSON.stringify(policy) + ';\n' + peer, {mode: 0o600});
    fs.writeFileSync(path.join(qwen, 'mode'), scenario, {mode: 0o600});
    t.diagnostic('受控 ACP 现场（保留，不是实机模型）：' + parent);
    const options = {...pins, node: fs.realpathSync(process.execPath), 'qwen-entry': path.join(qwen, 'cli-entry.js'),
      'run-dir': path.join(parent, 'run'), scenario: scenario === 'wrong' ? 'delivery' : scenario, 'controlled-acp-test': true};
    await assert.rejects(run({...options, 'source-head': '0'.repeat(40)}), /source_pin_mismatch/);
    await assert.rejects(run({...options, 'manifest-digest': 'sha256:' + '0'.repeat(64)}));
    assert.equal(fs.existsSync(options['run-dir']), false, 'pin failures precede state creation or process launch');
    assert.equal(fs.existsSync(path.join(qwen, 'starts.jsonl')), false);
    if (scenario === 'wrong') {
      await assert.rejects(run(options), /window_execution_stopped/);
      const evidence = JSON.parse(fs.readFileSync(path.join(parent, 'run/evidence.json')));
      assert.equal(evidence.passed, false); assert.equal(evidence.stage, 'delivery');
      const failed = JSON.parse(fs.readFileSync(path.join(parent, 'run/failure-task.json')));
      assert.equal(failed.status, 'failed'); assert.deepEqual(failed.artifactIds, []);
      assert.equal(JSON.parse(fs.readFileSync(path.join(parent, 'run/failure-audit.json'))).attempts, 3);
      assert.equal(fs.readFileSync(path.join(qwen, 'starts.jsonl'), 'utf8').trim().split('\n').length, 2, 'no replacement execution after failure');
      assert.equal(evidence.cleanupFailed, undefined); return;
    }
    const result = await run(options);
    assert.equal(result.passed, true); assert.equal(result.executionKind, 'controlled-acp-test'); assert.equal(result.modelCalls, null);
    const frozen = fs.readFileSync(path.join(parent, 'run/evidence.json'));
    await assert.rejects(run(options), {code: 'EEXIST'});
    assert.deepEqual(fs.readFileSync(path.join(parent, 'run/evidence.json')), frozen, 'never overwrite an earlier outcome');
    assert.equal(result.scope.originalProcessCancellationProven, false); assert.equal(result.scope.faultRecovery, false);
    assert.equal(fs.statSync(path.join(parent, 'run/evidence.json')).mode & 0o777, 0o600);
    const journal = path.join(qwen, 'starts.jsonl');
    const starts = fs.existsSync(journal) ? fs.readFileSync(journal, 'utf8').trim().split('\n').map(JSON.parse) : [];
    if (scenario === 'delivery') assert.deepEqual(starts.map(x => x.nodeId).sort(), ['east', 'west']);
    else assert.ok(starts.length <= 2, 'HTTP running may precede ACP prompt; no original-process cancellation claim');
    assert.ok(starts.every(x => x.role === 'author'));
    t.diagnostic(JSON.stringify({scenario, sourceHead: result.sourceHead, manifestDigest: result.manifestDigest, modelCalls: 0, attempts: result.audit.attempts}));
  });
