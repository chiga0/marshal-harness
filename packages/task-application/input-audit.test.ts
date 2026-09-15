import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {Store, digest} from '../task-store/store.mjs';
import {ArtifactDepot} from '../task-artifacts/depot.mjs';
import {TaskApplication, createAuditDisclosure} from './application.mjs';
import {validAuditResponse} from '../task-api/contract.mjs';

const context = {principal: 'local-operator'};
function fixture(t, {disclosure = null, depotEnabled = true} = {}) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-input-observation-')));
  const state = path.join(parent, 'state'), objects = path.join(parent, 'objects');
  let now = 1800000000000, store = Store.create(state, {clock: () => now});
  let owner = store.claimOwner(0, 'fixture', now + 3600000), depot = depotEnabled ? ArtifactDepot.create(objects) : null;
  const config = () => ({store, owner, depot, clock: () => now, auditDisclosure: disclosure,
    execution: {maxWorkers: 2, providerIds: ['fixture'], defaultProvider: 'fixture'}});
  let app = new TaskApplication(config());
  t.after(() => {store.close(); depot?.close(); fs.rmSync(parent, {recursive: true, force: true});});
  const f = {parent, state, objects, get app() {return app;}, get depot() {return depot;}, get store() {return store;},
    call: req => app.dispatch(req, context), read: fn => store.read(owner, fn), advance(ms) {now += ms;},
    async reserve(inputRefs = []) {
      const task = await f.call({operation: 'task.create', key: 'create', body: {intent: '公开合成业务', context: {inputRefs},
        limits: {maxAttempts: 4, maxWorkers: 2, timeoutMs: 30000}}});
      const command = f.read(tx => tx.taskCommands(task.id))[0];
      return {task, ticket: app.execution.nextWork(command.id, command.revision)};
    },
    audit: taskId => f.call({operation: 'task.audit', taskId}),
    reopen() {
      store.close(); depot?.close(); store = Store.openExisting(state, {clock: () => now});
      owner = store.claimOwner(owner.generation, 'next', now + 3600000); depot = depotEnabled ? ArtifactDepot.openExisting(objects) : null;
      app = new TaskApplication(config());
    }};
  return f;
}
const disclosure = redact => createAuditDisclosure({id: 'public-synthetic', version: '1', redact});

test('default retains only observed metadata; exact replay changes neither Task revision nor budget, and old records are unavailable', async t => {
  const f = fixture(t), {task, ticket} = await f.reserve(), secret = 'synthetic-private-body-only-in-prepare-4e80';
  const old = await f.audit(task.id); assert.equal(old.prompts[0].observation.stage, 'unavailable'); assert.equal(validAuditResponse(old, task.id), true);
  const before = await f.call({operation: 'task.get', taskId: task.id});
  assert.equal(f.app.execution.observeInput(ticket, 'prepared', secret), true);
  const preparedHead = f.read(tx => tx.head(task.id));
  f.app.execution.observeInput(ticket, 'prepared', secret); assert.deepEqual(f.read(tx => tx.head(task.id)), preparedHead);
  const prepared = await f.audit(task.id); assert.equal(validAuditResponse(prepared, task.id), true);
  assert.equal(prepared.prompts[0].observation.stage, 'prepared'); assert.equal(prepared.prompts[0].observation.promptDigest, digest(Buffer.from(secret)));
  assert.equal(prepared.prompts[0].text, ''); assert.equal(prepared.prompts[0].observation.snapshot, null);
  f.advance(1); f.app.execution.observeInput(ticket, 'handed-off');
  const handedHead = f.read(tx => tx.head(task.id)); f.app.execution.observeInput(ticket, 'handed-off'); assert.deepEqual(f.read(tx => tx.head(task.id)), handedHead);
  const audit = await f.audit(task.id); assert.equal(audit.prompts[0].observation.stage, 'handed-off'); assert.equal(audit.attempts, 1);
  assert.deepEqual(await f.call({operation: 'task.get', taskId: task.id}), before);
  assert.equal(f.read(tx => tx.projections('artifact')).length, 0);
  for (const file of fs.readdirSync(f.state, {recursive: true})) {
    const filename = path.join(f.state, file); if (fs.statSync(filename).isFile()) assert.equal(fs.readFileSync(filename).includes(Buffer.from(secret)), false);
  }
  f.reopen(); assert.deepEqual(await f.audit(task.id), audit);
  assert.throws(() => f.app.execution.observeInput(ticket, 'handed-off'), {code: 'recovery_required'});
});

test('explicit disclosure stores only returned bytes, bounded Unicode preview and original input refs; cold download is exact', async t => {
  const privatePart = 'fixture-confidential-suffix', publicPart = '公开合成🙂'.repeat(6000);
  let calls = 0;
  const f = fixture(t, {disclosure: disclosure((prompt, identity) => {calls++; assert.ok(identity.workerId); return prompt.replace(privatePart, '[已移除]');})});
  const input = await f.call({operation: 'input.create', key: 'input', body: {name: 'public.txt', mediaType: 'text/plain', contentBase64: Buffer.from('public').toString('base64')}});
  const {task, ticket} = await f.reserve([input.id]), raw = publicPart + privatePart, expected = raw.replace(privatePart, '[已移除]');
  f.app.execution.observeInput(ticket, 'prepared', raw); f.app.execution.observeInput(ticket, 'prepared', raw); assert.equal(calls, 1);
  const audit = await f.audit(task.id), prompt = audit.prompts[0]; assert.equal(validAuditResponse(audit, task.id), true);
  assert.deepEqual(prompt.contextRefs, [input.id]); assert.equal(prompt.observation.coverage, 'policy-redacted'); assert.equal(prompt.source, 'prepared-redacted');
  assert.ok(Buffer.byteLength(prompt.text) <= 2048 && prompt.text.isWellFormed()); assert.equal(prompt.observation.previewTruncated, true);
  assert.equal(prompt.observation.promptDigest, digest(Buffer.from(raw))); assert.equal(prompt.observation.snapshot.digest, digest(Buffer.from(expected)));
  const downloaded = await f.call({operation: 'artifact.content', artifactId: prompt.observation.snapshot.id}); assert.equal(downloaded.content.toString(), expected);
  assert.equal((await f.call({operation: 'task.get', taskId: task.id})).artifactIds.length, 0, 'input observation is not a Decision artifact');
  for (const file of fs.readdirSync(f.objects)) if (fs.statSync(path.join(f.objects, file)).isFile()) assert.equal(fs.readFileSync(path.join(f.objects, file)).includes(Buffer.from(privatePart)), false);
  f.reopen(); assert.deepEqual(await f.audit(task.id), audit);
  assert.equal((await f.call({operation: 'artifact.content', artifactId: prompt.observation.snapshot.id})).content.toString(), expected);
});

test('policy decline/throw/Promise/oversize and missing Depot produce unavailable content, not a new execution fence', async t => {
  const policies = [() => null, () => {throw Error('private-failure');}, () => Promise.reject(Error('private-failure')),
    () => 'x'.repeat(262145), () => '\0', prompt => prompt];
  for (let index = 0; index < policies.length; index++) {
    const f = fixture(t, {disclosure: disclosure(policies[index]), depotEnabled: index !== policies.length - 1}), {task, ticket} = await f.reserve();
    assert.equal(f.app.execution.observeInput(ticket, 'prepared', 'original-private-input'), true);
    const audit = await f.audit(task.id); assert.equal(audit.prompts[0].observation.coverage, 'unavailable'); assert.equal(audit.prompts[0].text, '');
    assert.equal(audit.prompts[0].observation.snapshot, null); assert.equal(f.app.execution.mayStart(ticket), true); assert.equal(audit.attempts, 1);
    assert.equal(validAuditResponse(audit, task.id), true);
  }
});

test('wrong ticket/body, partial SQL failure and missing committed snapshot never fabricate a handoff or repair bytes', async t => {
  const f = fixture(t, {disclosure: disclosure(prompt => prompt)}), {task, ticket} = await f.reserve();
  assert.equal(f.app.execution.observeInput(ticket, 'handed-off'), false);
  assert.throws(() => f.app.execution.observeInput({...ticket, inputDigest: 'sha256:' + '0'.repeat(64)}, 'prepared', 'public'), {code: 'recovery_required'});
  const before = f.read(tx => tx.head(task.id)), write = t.mock.method(f.store, 'write', () => {throw Error('private-sql-failure');});
  assert.throws(() => f.app.execution.observeInput(ticket, 'prepared', 'public'), {code: 'application_unavailable'}); write.mock.restore();
  assert.deepEqual(f.read(tx => tx.head(task.id)), before); assert.equal((await f.audit(task.id)).prompts[0].observation.stage, 'unavailable');
  f.app.execution.observeInput(ticket, 'prepared', 'public');
  assert.throws(() => f.app.execution.observeInput(ticket, 'prepared', 'different'), {code: 'invalid_request'});
  const snapshot = (await f.audit(task.id)).prompts[0].observation.snapshot;
  const filename = fs.readdirSync(f.objects).find(name => name.includes(snapshot.digest.slice(7))); assert.ok(filename);
  fs.unlinkSync(path.join(f.objects, filename));
  await assert.rejects(f.call({operation: 'artifact.content', artifactId: snapshot.id}), {code: 'application_unavailable'});
  assert.equal(f.app.execution.observeInput(ticket, 'prepared', 'public'), true, 'historical observation replay does not re-put content');
  assert.equal(fs.existsSync(path.join(f.objects, filename)), false);
  assert.equal((await f.audit(task.id)).prompts[0].observation.stage, 'prepared');
});

test('API audit binding rejects wrong task/worker, fabricated observation stage and foreign snapshot', async t => {
  const f = fixture(t, {disclosure: disclosure(prompt => prompt)}), {task, ticket} = await f.reserve();
  f.app.execution.observeInput(ticket, 'prepared', 'public'); const original = await f.audit(task.id);
  assert.equal(validAuditResponse(original, task.id), true);
  for (const mutate of [value => value.workers[0].taskId = 'other', value => value.prompts[0].workerId = 'other',
    value => value.prompts.push(structuredClone(value.prompts[0])), value => value.prompts[0].observation.stage = 'handed-off',
    value => value.prompts[0].observation.snapshot.taskId = 'other', value => value.prompts[0].observation.snapshot.name = 'other.input.txt',
    value => value.prompts[0].source = 'submitted-redacted', value => value.prompts[0].observation.policy = null]) {
    const changed = structuredClone(original); mutate(changed); assert.equal(validAuditResponse(changed, task.id), false);
  }
});
