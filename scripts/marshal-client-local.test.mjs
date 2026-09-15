import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {run, connectLocal} from '../packages/task-local/main.mjs';
import {contract, TaskApiError} from '../packages/task-api/contract.mjs';
import {createTaskApiHandler} from '../packages/task-api/http-handler.mjs';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
function fixture(t) {
  const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-client-local-')));
  t.after(() => fs.rmSync(dir, {recursive: true, force: true}));
  return dir;
}
test('init records discovered paths; repeat preserves explicit deployment configuration', async t => {
  const home = fixture(t), output = [];
  const config = path.join(home, 'config.mjs');
  fs.writeFileSync(config, 'export default {};');
  await run(['init', '--install-root', root, '--config', config], {home, output: x => output.push(x)});
  await run(['init'], {home, output: x => output.push(x)});
  assert.equal(output[0].state, 'initialized');
  assert.equal(output[1].serviceConfigured, true);
  const file = path.join(home, '.marshal-client/local.json');
  const settings = JSON.parse(fs.readFileSync(file));
  assert.equal(settings.config, config);
  assert.equal(settings.installRoot, root);
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
});
test('recorded connection supports env-free status and client; no server/agent starts', async t => {
  const home = fixture(t), output = [], token = 'local-bootstrap-fixture-secret-token-001';
  const ready = contract.components.schemas.Readiness.examples[0];
  let handler, calls = 0;
  const server = createServer((req, res) => handler(req, res));
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => {server.closeAllConnections(); server.close();});
  const url = `http://127.0.0.1:${server.address().port}`;
  handler = createTaskApiHandler({token, expectedHost: new URL(url).host, application: async () => {calls++; return ready;}});
  const connection = path.join(home, 'connection.json');
  fs.writeFileSync(connection, JSON.stringify({url, token}), {mode: 0o600});
  await run(['init', '--install-root', root, '--connection-file', connection], {home, output: x => output.push(x)});
  await run(['serve'], {home, output: x => output.push(x)});
  const client = await connectLocal({settingsDir: path.join(home, '.marshal-client')});
  assert.deepEqual({...await client.request('ready.get')}, ready);
  assert.equal(calls, 4);
  assert.deepEqual(output.map(x => x.state), ['connected', 'connected']);
  assert.ok(!JSON.stringify(output).includes(token));
  assert.ok(!fs.readFileSync(path.join(home, '.marshal-client/local.json'), 'utf8').includes(token));
});
test('missing selected agent is explicit; unsafe settings rejected; own install root needs no search', async t => {
  const home = fixture(t);
  await run(['init', '--install-root', root], {home, output() {}});
  await assert.rejects(run(['serve', '--agent-executable', path.join(home, 'missing-qwen')], {home}), /agent_unavailable/);
  const file = path.join(home, '.marshal-client/local.json');
  fs.chmodSync(file, 0o644);
  await assert.rejects(run(['status'], {home}), /unsafe_settings/);
  const otherHome = fixture(t);
  const output = [];
  await run(['init'], {home: otherHome, output: x => output.push(x)});
  assert.equal(output[0].installRoot, root);
});

test('live service cannot be reported as a newly requested configuration', async t => {
  const home = fixture(t), token = 'local-bootstrap-fixture-secret-token-001';
  let handler;
  const server = createServer((req, res) => handler(req, res));
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => {server.closeAllConnections(); server.close();});
  const url = `http://127.0.0.1:${server.address().port}`;
  handler = createTaskApiHandler({token, expectedHost: new URL(url).host, application: async () => contract.components.schemas.Readiness.examples[0]});
  const connection = path.join(home, 'connection.json');
  fs.writeFileSync(connection, JSON.stringify({url, token}), {mode: 0o600});
  await run(['init', '--install-root', root, '--connection-file', connection], {home, output() {}});
  const file = path.join(home, '.marshal-client/local.json'), before = fs.readFileSync(file);
  for (const args of [['--config', path.join(home, 'different.mjs')], ['--ui', path.join(home, 'ui')], ['--port', '34567'], ['--generic']]) {
    await assert.rejects(run(['serve', ...args], {home, output() {assert.fail('no false connection');}}), /running_configuration_conflict/);
    await assert.rejects(run(['init', ...args], {home, output() {assert.fail('no live reconfiguration');}}), /running_configuration_conflict/);
    assert.deepEqual(fs.readFileSync(file), before);
  }
  await assert.rejects(run(['init', '--replace-launcher'], {home, output() {}}), /running_configuration_conflict/);
});

test('live 503 readiness blocks launch, reconfiguration and launcher replacement', async t => {
  const home = fixture(t), token = 'local-live-not-ready-fixture-token-001';
  let handler, ready = true, healthCalls = 0;
  const server = createServer((req, res) => handler(req, res));
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => {server.closeAllConnections(); server.close();});
  const url = `http://127.0.0.1:${server.address().port}`;
  handler = createTaskApiHandler({token, expectedHost: new URL(url).host, application: async request => {
    if (request.operation === 'health.get') {healthCalls++; return contract.components.schemas.Health.examples[0];}
    if (!ready) throw new TaskApiError('not_ready');
    return contract.components.schemas.Readiness.examples[0];
  }});
  const connection = path.join(home, 'connection.json');
  fs.writeFileSync(connection, JSON.stringify({url, token}), {mode: 0o600});
  await run(['init', '--install-root', root, '--connection-file', connection], {home, output() {}});
  const settings = path.join(home, '.marshal-client/local.json'), launcher = path.join(home, '.local/bin/marshal');
  const before = fs.readFileSync(settings), beforeLauncher = fs.readFileSync(launcher); ready = false;
  for (const args of [['serve'], ['status'], ['serve', '--generic'], ['init', '--replace-launcher'], ['init', '--config', path.join(home, 'other.mjs')]]) {
    await assert.rejects(run(args, {home, output() {assert.fail('not connected');}}), /service_not_ready/);
    assert.deepEqual(fs.readFileSync(settings), before); assert.deepEqual(fs.readFileSync(launcher), beforeLauncher);
  }
  assert.equal(healthCalls, 5);
});

for (const failure of ['unauthorized', 'timeout']) test(`recorded ${failure} service preserves settings and launcher without spawning`, {timeout: 20000}, async t => {
  const home = fixture(t), token = 'local-uncertain-fixture-secret-token-001';
  let handler, broken = false;
  const server = createServer((req, res) => {if (broken && failure === 'timeout') return; handler(req, res);});
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => {server.closeAllConnections(); server.close();});
  const url = `http://127.0.0.1:${server.address().port}`;
  const makeHandler = secret => createTaskApiHandler({token: secret, expectedHost: new URL(url).host,
    application: async () => {if (broken) throw new TaskApiError('unauthorized'); return contract.components.schemas.Readiness.examples[0];}});
  handler = makeHandler(token);
  const connection = path.join(home, 'connection.json');
  fs.writeFileSync(connection, JSON.stringify({url, token}), {mode: 0o600});
  await run(['init', '--install-root', root, '--connection-file', connection], {home, output() {}});
  const file = path.join(home, '.marshal-client/local.json'), launcher = path.join(home, '.local/bin/marshal');
  const before = fs.readFileSync(file), command = fs.readFileSync(launcher);
  broken = true; handler = makeHandler('a-different-valid-fixture-secret-token');
  for (const args of [['serve', '--generic'], ['init', '--replace-launcher']]) {
    await assert.rejects(run(args, {home, output() {assert.fail('must not launch or replace');}}), /connection_uncertain/);
    assert.deepEqual(fs.readFileSync(file), before); assert.deepEqual(fs.readFileSync(launcher), command);
    assert.equal(fs.existsSync(path.join(home, '.marshal-node')), false);
  }
});

test('damaged recorded connection cannot authorize replacing settings or launcher', async t => {
  const home = fixture(t); await run(['init', '--install-root', root], {home, output() {}});
  const file = path.join(home, '.marshal-client/local.json'), settings = JSON.parse(fs.readFileSync(file));
  const connection = path.join(home, 'missing.json'); settings.connectionFile = connection;
  fs.writeFileSync(file, JSON.stringify(settings), {mode: 0o600}); const before = fs.readFileSync(file);
  await assert.rejects(run(['init', '--replace-launcher'], {home, output() {assert.fail();}}));
  assert.deepEqual(fs.readFileSync(file), before);
});

for (const {legacy,upgrade} of [{legacy:false,upgrade:false},{legacy:true,upgrade:false},{legacy:false,upgrade:true},{legacy:true,upgrade:true}]) test('generic default and install upgrade consume same-package configuration; legacy='+legacy+' upgrade='+upgrade, {timeout: 10000}, async t => {
  const home = fixture(t), capture = path.join(home, 'launch.json');
  const connectionFile = path.join(home, 'live.json');
  const installed = fakeInstall(home, `
    import fs from 'node:fs'; import {createServer} from 'node:http';
    import {createTaskApiHandler} from '../task-api/http-handler.mjs';
    import {contract} from '../task-api/contract.mjs';
    await import(process.argv[process.argv.indexOf('--config')+1]);
    fs.writeFileSync(${JSON.stringify(capture)},JSON.stringify({argv:process.argv.slice(2),agent:process.env.MARSHAL_AGENT_EXECUTABLE}));
    const token='fixture-generic-service-token-001';let handler;
    const server=createServer((req,res)=>handler(req,res));
    server.listen(0,'127.0.0.1',()=>{
      const url='http://127.0.0.1:'+server.address().port;
      handler=createTaskApiHandler({token,expectedHost:new URL(url).host,application:async()=>contract.components.schemas.Readiness.examples[0]});
      fs.writeFileSync(${JSON.stringify(connectionFile)},JSON.stringify({url,token}),{mode:0o600});
      console.log(JSON.stringify({connectionFile:${JSON.stringify(connectionFile)},address:'http://127.0.0.1:34567'}));
    });
    process.on('SIGTERM',()=>{server.closeAllConnections();server.close();});
  `);
  fs.mkdirSync(path.join(installed, 'packages/task-generic-files'));
  const config = path.join(installed, legacy ? 'packages/task-generic-files/service-config.mjs' : 'packages/task-generic-files/qwen-review-service-config.mjs');
  fs.writeFileSync(config, 'export default {};');
  const ui = path.join(home, 'ui'); fs.mkdirSync(ui);
  const bin = path.join(home, 'bin'); fs.mkdirSync(bin);
  fs.symlinkSync(process.execPath, path.join(bin, 'qwen'));
  const originalPath = process.env.PATH;
  process.env.PATH = bin;
  t.after(() => {if (originalPath === undefined) delete process.env.PATH; else process.env.PATH = originalPath;});
  if (legacy || upgrade) {
    fs.mkdirSync(path.join(home,'.marshal-client'), {mode:0o700});
    const previousRoot=upgrade ? path.join(home,'old-install') : installed;
    const previousConfig=upgrade ? path.join(previousRoot,'packages/task-generic-files',path.basename(config)) : config;
    if(upgrade) {fs.mkdirSync(path.dirname(previousConfig),{recursive:true});fs.writeFileSync(previousConfig,'throw Error(\"old configuration must never load\");');}
    fs.writeFileSync(path.join(home,'.marshal-client/local.json'),JSON.stringify({version:1,installRoot:previousRoot,generic:true,config:previousConfig,...(!legacy?{genericProfile:2}:{})}),{mode:0o600});
  }
  let connected = false;
  await run(['serve', '--install-root', installed, '--ui', ui, '--port', '34567'], {
    home, output(value) {
      connected = true;
      assert.equal(value.uiUrl, 'http://127.0.0.1:34567/ui/');
      const settings = JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json')));
      assert.equal(settings.connectionFile, connectionFile);
      assert.equal(settings.config, config); assert.equal(settings.generic, true);
      assert.equal(settings.genericProfile, legacy ? undefined : 2);
      assert.equal(settings.dataDir, path.join(home, legacy ? '.marshal-node/generic-team' : '.marshal-node/generic-team-v2'));
      assert.notEqual(JSON.parse(fs.readFileSync(connectionFile)).url, settings.address);
      process.emit('SIGTERM');
    },
  });
  assert.equal(connected, true);
  const actual = JSON.parse(fs.readFileSync(capture));
  assert.equal(actual.agent, fs.realpathSync(process.execPath));
  assert.deepEqual(actual.argv, ['--config', config, '--data-dir', path.join(home, legacy ? '.marshal-node/generic-team' : '.marshal-node/generic-team-v2'), '--port', '34567', '--ui', ui]);
  assert.equal(fs.statSync(path.join(home, '.marshal-node')).mode & 0o777, 0o700);
});

test('port options reject malformed and out-of-range values without launch', async t => {
  const home = fixture(t);
  for (const value of ['-1', '65536', '01', 'nan']) await assert.rejects(run(['serve', '--port', value], {home}), /invalid_arguments/);
});

test('no-ui is mutually exclusive and removes only the recorded UI option', async t => {
  const home = fixture(t), ui = path.join(home, 'ui'), data = path.join(home, 'data');
  for (const args of [['--no-ui', '--ui', ui], ['--ui', ui, '--no-ui'], ['--no-ui', '--no-ui']]) {
    await assert.rejects(run(['init', ...args], {home}), /invalid_arguments/);
  }
  await run(['init', '--install-root', root, '--ui', ui, '--data-dir', data], {home, output() {}});
  await run(['init', '--no-ui'], {home, output() {}});
  const settings = JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json')));
  assert.equal(settings.ui, undefined); assert.equal(settings.dataDir, data);
});

test('explicit generic selection preserves old files but does not adopt old business data root', async t => {
  const home = fixture(t), config = path.join(home, 'business.mjs'), data = path.join(home, 'business-data');
  fs.writeFileSync(config, 'export default {};'); fs.mkdirSync(data);
  await run(['init', '--install-root', root, '--config', config, '--data-dir', data], {home, output() {}});
  await run(['init', '--generic'], {home, output() {}});
  const settings = JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json')));
  assert.equal(settings.generic, true); assert.equal(settings.config, undefined); assert.equal(settings.dataDir, undefined);
  assert.equal(fs.readFileSync(config, 'utf8'), 'export default {};'); assert.equal(fs.statSync(data).isDirectory(), true);
  for (const args of [['--generic', '--config', config], ['--config', config, '--generic']])
    await assert.rejects(run(['init', ...args], {home}), /invalid_arguments/);
});

test('upgrade init preserves old settings on launcher conflict and replaces only explicitly', async t => {
  const home = fixture(t), config = path.join(home, 'business.mjs'); fs.writeFileSync(config, 'export default {};');
  const old = fakeInstall(home, '');
  fs.mkdirSync(path.join(old, 'packages/task-local'));
  fs.writeFileSync(path.join(old, 'packages/task-local/main.mjs'), '');
  await run(['init', '--install-root', old, '--config', config], {home, output() {}});
  const settingsFile = path.join(home, '.marshal-client/local.json'), before = fs.readFileSync(settingsFile);
  const launcher = path.join(home, '.local/bin/marshal'), launcherBefore = fs.readFileSync(launcher);
  const output = [];
  await run(['init'], {home, output: x => output.push(x)});
  assert.equal(output[0].launcher.state, 'conflict');
  assert.deepEqual(fs.readFileSync(settingsFile), before); assert.deepEqual(fs.readFileSync(launcher), launcherBefore);
  await run(['init', '--replace-launcher'], {home, output: x => output.push(x)});
  assert.equal(output[1].launcher.replaced, true);
  const settings = JSON.parse(fs.readFileSync(settingsFile));
  assert.equal(settings.installRoot, root); assert.equal(settings.config, config);
  assert.notDeepEqual(fs.readFileSync(launcher), launcherBefore);
});

function fakeInstall(home, program) {
  const installed = path.join(home, 'installed');
  fs.mkdirSync(path.join(installed, 'packages/task-service'), {recursive: true, mode: 0o700});
  for (const pkg of ['task-client', 'task-api']) fs.cpSync(path.join(root, 'packages', pkg), path.join(installed, 'packages', pkg), {recursive: true});
  fs.writeFileSync(path.join(installed, 'packages/task-service/main.mjs'), program);
  return installed;
}
test('serve owns startup, checks real HTTP before persisting connection, and forwards shutdown', {timeout: 10000}, async t => {
  const home = fixture(t), config = path.join(home, 'config.mjs');
  fs.writeFileSync(config, JSON.stringify({connectionFile: path.join(home, 'live.json')}));
  const installed = fakeInstall(home, `
    import fs from 'node:fs'; import {createServer} from 'node:http';
    import {createTaskApiHandler} from '../task-api/http-handler.mjs';
    import {contract} from '../task-api/contract.mjs';
    const {connectionFile} = JSON.parse(fs.readFileSync(process.argv[3]));
    const token = 'fixture-managed-start-token-private-001'; let handler;
    const server = createServer((req,res) => handler(req,res));
    server.listen(0,'127.0.0.1', () => {
      const url = 'http://127.0.0.1:' + server.address().port;
      handler = createTaskApiHandler({token,expectedHost:new URL(url).host,application:async()=>contract.components.schemas.Readiness.examples[0]});
      fs.writeFileSync(connectionFile,JSON.stringify({url,token}),{mode:0o600});
      console.log(JSON.stringify({connectionFile}));
    });
    process.on('SIGTERM',()=>{server.closeAllConnections();server.close();});
  `);
  let connected = false;
  await run(['init', '--install-root', installed, '--config', config], {home, output() {}});
  await run(['serve'], {home, output(value) {
    assert.equal(value.state, 'connected'); connected = true;
    const settings = JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json')));
    assert.equal(settings.connectionFile, path.join(home, 'live.json'));
    process.emit('SIGTERM');
  }});
  assert.equal(connected, true);
  // Normal exit leaves a real connection record. Its refused listener permits
  // a new child; the service remains responsible for its own Store lock.
  connected = false;
  await run(['serve'], {home, output(value) {
    assert.equal(value.state, 'connected'); connected = true; process.emit('SIGTERM');
  }});
  assert.equal(connected, true);
});
test('startup timeout kills only own unresponsive service and preserves old connection record', {timeout: 10000}, async t => {
  const home = fixture(t), config = path.join(home, 'config.mjs');
  fs.writeFileSync(config, '{}');
  const installed = fakeInstall(home, `process.on('SIGTERM',()=>{});setInterval(()=>{},1000);`);
  const oldConnection = path.join(home, 'old.json');
  const closed = createServer(); closed.listen(0, '127.0.0.1'); await once(closed, 'listening');
  const closedURL = `http://127.0.0.1:${closed.address().port}`; await new Promise(resolve => closed.close(resolve));
  fs.writeFileSync(oldConnection, JSON.stringify({url: closedURL, token: 'closed-local-fixture-secret-token-001'}), {mode: 0o600});
  await run(['init', '--install-root', installed, '--config', config, '--connection-file', oldConnection], {home, output() {}});
  await assert.rejects(run(['serve'], {home, startupTimeoutMs: 500, stopTimeoutMs: 100, output() {assert.fail('must not report connected');}}), /service_start_failed/);
  assert.equal(JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json'))).connectionFile, oldConnection);
});

 test('new default refuses an arbitrary ACP executable; explicit configs remain available', async t => {
  const home=fixture(t);
  await assert.rejects(run(['serve','--install-root',root,'--agent-executable',process.execPath],{home,output(){}}), /qwen_configuration_required/);
 });
 test('explicit generic does not upgrade existing settings or reuse another business root', async t => {
  const home=fixture(t), dir=path.join(home,'.marshal-client'), config=path.join(root,'packages/task-generic-files/service-config.mjs');
  fs.mkdirSync(dir,{mode:0o700});
  fs.writeFileSync(path.join(dir,'local.json'),JSON.stringify({version:1,installRoot:root,generic:true,config,dataDir:path.join(home,'legacy-data')}),{mode:0o600});
  await run(['init','--generic'],{home,output(){}});
  const settings=JSON.parse(fs.readFileSync(path.join(dir,'local.json')));
  assert.equal(settings.config,config);assert.equal(settings.genericProfile,undefined);assert.equal(settings.dataDir,path.join(home,'legacy-data'));
 });
