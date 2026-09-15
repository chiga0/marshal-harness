import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {launchProtocol} from './index.mjs';
import {PiRpcClient} from '../agent-pi-rpc/client.mjs';

const fixture = fileURLToPath(new URL('../agent-provider-pi/agent.fixture.mjs', import.meta.url));
function options(t) {
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'protocol-runtime-test-')));
  t.after(() => fs.rmSync(cwd, {recursive: true, force: true})); return {cwd, executable: process.execPath, args: [fixture], deadline: Date.now() + 10000};
}
test('different protocol shares the exact guard identity, bidirectional stream and cleanup', {timeout: 15000}, async t => {
  const execution = await launchProtocol({...options(t), createClient: connection => new PiRpcClient(connection)});
  t.after(() => execution.stop()); assert.equal((await execution.client.getState()).sessionId, 'pi-session');
  assert.equal((await execution.client.prompt('fixture')).stopReason, 'stop');
  const fact = await execution.stop(); assert.equal(fact.started, execution.started); assert.equal(fact.cleaned, true);
  assert.equal(await execution.completion, fact); assert.equal(execution.client.closed, true);
});
test('missing/throwing/async/invalid client factory never leaves a guard or launches a replacement', {timeout: 15000}, async t => {
  await assert.rejects(launchProtocol(options(t)), {code: 'runtime_invalid_client_factory'});
  for (const createClient of [() => { throw Error('PRIVATE_FACTORY'); }, async () => ({}), () => ({})]) {
    await assert.rejects(launchProtocol({...options(t), createClient}), error => {
      assert.equal(error.code, 'runtime_client_factory_failed'); assert.equal(error.completion.cleaned, true);
      assert.equal(error.completion.started, null); assert.doesNotMatch(JSON.stringify(error), /PRIVATE/); return true;
    });
  }
});
test('a client close bug cannot interrupt the existing owner stop or its cleanup promise', {timeout: 15000}, async t => {
  const execution = await launchProtocol({...options(t), createClient: () => ({close() { throw Error('PRIVATE_CLOSE_FAILURE'); }})});
  t.after(() => execution.stop()); const cleanup = await execution.stop(); assert.equal(cleanup.cleaned, true);
  assert.equal(await execution.completion, cleanup); assert.doesNotMatch(JSON.stringify(cleanup), /PRIVATE/);
});
