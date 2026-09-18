import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createExecutableAcpProvider} from './index.ts';
import {launchAcp} from '../agent-runtime/index.ts';

function setup(t, mode = 'normal') {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'executable-acp-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true}));
  const bin = path.join(cwd, 'releases', '0.23.2-dataworks.0', 'bin');
  fs.mkdirSync(bin, {recursive: true});
  const executable = path.join(bin, 'qwen');
  // A deterministic directly-executable peer, not a model or npm installation.
  fs.writeFileSync(executable, '#!' + process.execPath + '\n' +
    "if (JSON.stringify(process.argv.slice(2)) !== '[\"--acp\"]') process.exit(91);\n" +
    'process.argv[2] = ' + JSON.stringify(mode) + ';\n' +
    'await import(' + JSON.stringify(new URL('./agent.fixture.ts', import.meta.url).href) + ');\n', {mode: 0o700});
  return {cwd, executable};
}
const input = cwd => ({cwd, deadline: Date.now() + 10000, prompt: 'Perform the approved fixture work'});

test('direct release/bin/qwen uses fixed ACP argv, explicit env and real handshake', {timeout: 15000}, async t => {
  const {cwd, executable} = setup(t);
  const env = {LANG: 'C'}, launches = [];
  const provider = createExecutableAcpProvider({id: 'qwen', executable, env});
  env.EXTRA_AFTER_FACTORY = 'not copied';
  const handle = provider.start({...input(cwd), executionContext: {
    launch(options, callbacks) {
      launches.push(options);
      return launchAcp({...options, onUpdate: callbacks.onUpdate, onPermission: callbacks.onPermission});
    },
    extraScope() {},
  }});
  t.after(() => handle.stop());
  const result = await handle.completion;
  assert.equal(launches.length, 1);
  assert.equal(launches[0].executable, executable);
  assert.deepEqual(launches[0].args, ['--acp']);
  assert.deepEqual(launches[0].env, {LANG: 'C'});
  assert.equal(result.status, 'completed');
  assert.equal(result.sessionId, 'session-fixture');
  assert.equal(result.outputText, 'public output');
  assert.equal(result.cleanup.cleaned, true);
  assert.equal(provider.profile, 'ordinary-user');
  assert.equal(result.usage.source, 'unavailable');
});

test('direct CLI can be cancelled while initialization has no reply', {timeout: 15000}, async t => {
  const {cwd, executable} = setup(t, 'hang-init');
  let ready;
  const initializing = new Promise(resolve => { ready = resolve; });
  const handle = createExecutableAcpProvider({id: 'qwen', executable}).start({
    ...input(cwd), onProgress: event => { if (event.phase === 'initializing') ready(); },
  });
  t.after(() => handle.stop());
  await initializing;
  const result = await handle.stop();
  assert.equal(result.status, 'cancelled');
  assert.equal(result.sessionId, null);
  assert.equal(result.cleanup.cleaned, true);
});

test('missing or non-executable paths fail through runtime without fallback', {timeout: 15000}, async t => {
  const {cwd, executable} = setup(t);
  fs.chmodSync(executable, 0o600);
  for (const target of [executable, path.join(cwd, 'missing')]) {
    const handle = createExecutableAcpProvider({id: 'qwen', executable: target}).start(input(cwd));
    t.after(() => handle.stop());
    const result = await handle.completion;
    assert.notEqual(result.status, 'completed');
    assert.equal(result.sessionId, null);
    assert.equal(result.cleanup.cleaned, true);
  }
});

test('relative paths, custom argv and malformed options are rejected before launch', () => {
  for (const options of [null, [], {id: 'qwen', executable: 'qwen'},
    {id: 'qwen', executable: '/qwen', args: ['--other']},
    {id: 'qwen', executable: '/qwen', env: {INVALID: 1}}])
    assert.throws(() => createExecutableAcpProvider(options), {code: 'provider_invalid_configuration'});
});
