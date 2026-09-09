import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {Store, UNPERMITTED_FORMAT} from '../task-store/store.mjs';
import {createStagingOnlyBusinessFactory, isStagingOnlyBusiness} from '../task-business/index.mjs';
import {createAuditDisclosure} from '../task-application/application.mjs';
import {startTaskService} from './composition.mjs';

const custody = {profile: 'node-execution-custody/v1'}, unpermitted = {profile: 'node-unpermitted-reservation/v1'};
const providers = new Map([['fixture', {id: 'fixture', start() {throw Error('no task in configuration tests');}}]]);
test('v5 original narrow factory identity, arbitrary callbacks and wrappers reject before touching root', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-v5-config-'))), root = path.join(parent, 'data');
  t.after(() => fs.rmSync(parent, {recursive: true, force: true}));
  const factory = createStagingOnlyBusinessFactory();
  assert.equal(isStagingOnlyBusiness(factory), true);
  for (const key of ['layoutFor', 'depot', 'clock', 'prepare', 'eligible'])
    assert.throws(() => createStagingOnlyBusinessFactory({[key]: () => {throw Error('must not run');}}), {code: 'business_unsupported_preparation'});
  const disclosure = createAuditDisclosure({id: 'bad', version: '1', redact() {throw Error('must not run');}});
  for (const extra of [{businessFactory: Object.assign(() => {}, {eligible: true, profile: 'file-staging-only/v1'})},
    {businessFactory: context => factory(context)}, {auditDisclosure: disclosure}, {custody: undefined}]) {
    await assert.rejects(startTaskService({root, mode: 'create', providers, custody, unpermitted, businessFactory: factory, ...extra}),
      {code: 'service_unsupported_preparation'});
    assert.equal(fs.existsSync(root), false);
  }
  // Exact object AND actual original prepare identity, not copied properties.
  const business = factory({executionParent: parent, depot: {get() {}, put() {}}, approvedLayout() {}, observeExecution() {}});
  try {
    assert.equal(isStagingOnlyBusiness(factory, business), true);
    assert.equal(isStagingOnlyBusiness(factory, {...business}), false);
    assert.equal(isStagingOnlyBusiness(factory, {...business, prepare: (...args) => business.prepare(...args)}), false);
    assert.throws(() => {business.prepare = () => {};}, TypeError);
  } finally {business.close();}
});
test('v5 actual service opens original format; unsupported disclosure rejects before generation claim', async t => {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'marshal-v5-open-'))), root = path.join(parent, 'data');
  const config = {root, providers, custody, unpermitted, businessFactory: createStagingOnlyBusinessFactory()};
  let service; t.after(async () => {await service?.shutdown(); fs.rmSync(parent, {recursive: true, force: true});});
  service = await startTaskService({...config, mode: 'create'}); assert.equal(JSON.parse(fs.readFileSync(path.join(root, 'profile.json'))).layout, 5);
  assert.equal((await service.shutdown()).shutdownClean, true);
  const generation = () => {const store = Store.openExisting(path.join(root, 'store'), {format: UNPERMITTED_FORMAT});
    try {return store.info().generation;} finally {store.close();}};
  const before = generation();
  await assert.rejects(startTaskService({...config, mode: 'open', auditDisclosure: createAuditDisclosure({id: 'x', version: '1', redact: value => value})}),
    {code: 'service_unsupported_preparation'}); assert.equal(generation(), before);
  await assert.rejects(startTaskService({...config, mode: 'open', unpermitted: undefined}), {code: 'service_root_unavailable'});
  assert.equal(generation(), before);
  service = await startTaskService({...config, mode: 'open'}); assert.equal((await service.shutdown()).shutdownClean, true);
  assert.equal(generation(), before + 1n);
});
