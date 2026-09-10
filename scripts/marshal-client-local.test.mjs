import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {run, connectLocal} from '../packages/task-local/main.mjs';
import {contract} from '../packages/task-api/contract.mjs';
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
test('missing business config is explicit; unsafe settings rejected; own install root needs no search', async t => {
  const home = fixture(t);
  await run(['init', '--install-root', root], {home, output() {}});
  await assert.rejects(run(['serve'], {home}), /configuration_required/);
  const file = path.join(home, '.marshal-client/local.json');
  fs.chmodSync(file, 0o644);
  await assert.rejects(run(['status'], {home}), /unsafe_settings/);
  const otherHome = fixture(t);
  const output = [];
  await run(['init'], {home: otherHome, output: x => output.push(x)});
  assert.equal(output[0].installRoot, root);
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
  await run(['serve', '--install-root', installed, '--config', config], {home, output(value) {
    assert.equal(value.state, 'connected'); connected = true;
    const settings = JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json')));
    assert.equal(settings.connectionFile, path.join(home, 'live.json'));
    process.emit('SIGTERM');
  }});
  assert.equal(connected, true);
});
test('startup timeout kills only own unresponsive service and preserves old connection record', {timeout: 10000}, async t => {
  const home = fixture(t), config = path.join(home, 'config.mjs');
  fs.writeFileSync(config, '{}');
  const installed = fakeInstall(home, `process.on('SIGTERM',()=>{});setInterval(()=>{},1000);`);
  const oldConnection = path.join(home, 'old.json');
  await run(['init', '--install-root', installed, '--config', config, '--connection-file', oldConnection], {home, output() {}});
  await assert.rejects(run(['serve'], {home, startupTimeoutMs: 500, stopTimeoutMs: 100, output() {assert.fail('must not report connected');}}), /service_start_failed/);
  assert.equal(JSON.parse(fs.readFileSync(path.join(home, '.marshal-client/local.json'))).connectionFile, oldConnection);
});
