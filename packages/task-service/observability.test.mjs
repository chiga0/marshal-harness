import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {startTaskService} from './composition.mjs';
import {Store} from '../task-store/store.mjs';

const enabled = {profile: 'task-observation/v1', retainPrompts: true};
test('explicit observation identity freezes policy source and rejects changed root semantics before owner claim', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-observation-config-'))), root = path.join(parent, 'data');
  t.after(() => fs.rmSync(parent, {recursive:true, force:true}));
  const config = {root, providers: new Map([['fixture', {id:'fixture', start(){throw Error('must not start');}}]]),
    prepare: async () => ({prompt:'public'}), collect: async () => ({})};
  const first = await startTaskService({...config, mode:'create', observability: enabled}); await first.shutdown();
  const marker = JSON.parse(fs.readFileSync(path.join(root, 'profile.json')));
  assert.deepEqual({profile:marker.observability.profile, retainPrompts:marker.observability.retainPrompts}, enabled);
  assert.match(marker.observability.policy.sourceDigest, /^sha256:[a-f0-9]{64}$/);
  let store = Store.openExisting(path.join(root,'store')), generation = store.info().generation; store.close();
  for (const observability of [undefined, {...enabled, retainPrompts:false}, {...enabled, callback(){}}]) {
    await assert.rejects(startTaskService({...config, mode:'open', observability}));
    store = Store.openExisting(path.join(root,'store')); assert.equal(store.info().generation, generation); store.close();
  }
  const reopened = await startTaskService({...config, mode:'open', observability: enabled}); await reopened.shutdown();
});
