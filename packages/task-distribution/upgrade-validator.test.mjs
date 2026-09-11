import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
import {SOURCE_FILES, verify} from './index.mjs';
import {LEGACY_SOURCE, LEGACY_HELPER_SHA, verifyLegacyPackage} from './upgrade-validator.fixture.mjs';
import {validatePair} from './upgrade-consumer.fixture.mjs';
const helper = fileURLToPath(new URL('./v102-validator.fixture.txt', import.meta.url));
const digest = b => 'sha256:' + createHash('sha256').update(b).digest('hex');
function setup(t) {
  const parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'upgrade-validator-')));
  t.after(() => fs.rmSync(parent, {recursive:true, force:true}));
  const legacy = [...fs.readFileSync(helper,'utf8').match(/SOURCE_FILES = Object.freeze\(\[([\s\S]*?)\]\)/)[1].matchAll(/'([^']+)'/g)].map(m=>m[1]);
  function install(name, sourceHead, paths) {
    const root=path.join(parent,name);fs.mkdirSync(root,{mode:0o700});
    const files=paths.map(p=>{fs.mkdirSync(path.dirname(path.join(root,p)),{recursive:true,mode:0o700});fs.writeFileSync(path.join(root,p),'',{mode:0o600});return {path:p,digest:digest(''),bytes:0};});
    const manifest=Buffer.from(JSON.stringify({format:'marshal-node-script-package/v1',sourceHead,node:'24.15.0',platforms:['darwin-arm64','linux-x64'],entrypoint:'packages/task-service/main.mjs',files},null,2)+'\n');
    fs.writeFileSync(path.join(root,'manifest.json'),manifest,{mode:0o600});return {root,sourceHead,manifestDigest:digest(manifest)};
  }
  // Schema/transport fixtures only: never execute these empty runtime files.
  return {parent,oldPackage:install('old',LEGACY_SOURCE,legacy),newPackage:install('new','a'.repeat(40),[...SOURCE_FILES,'apps/task-web/dist/index.html']),
    runDir:path.join(parent,'run'),assetKind:'fixed-assets',oldValidator:helper};
}
test('only pinned v1.0.2 helper verifies its old inventory; current verifier remains strict', t=>{
  const o=setup(t);assert.equal(digest(fs.readFileSync(helper)),'sha256:'+LEGACY_HELPER_SHA);
  assert.throws(()=>verify({root:o.oldPackage.root,manifestDigest:o.oldPackage.manifestDigest}),{code:'invalid_manifest'});
  const r=validatePair(o);assert.equal(r.oldReport.files,65);assert.equal(r.newReport.files,SOURCE_FILES.length+1);
  assert.equal(fs.existsSync(o.runDir),false);
  assert.throws(()=>validatePair({...o,oldValidator:null}),{code:'invalid_manifest'});
  assert.throws(()=>validatePair({...o,oldPackage:{...o.oldPackage,sourceHead:'b'.repeat(40)}}),/unsupported_legacy_source/);
});
test('helper unknown bytes, sizes, links and package embedding fail before code execution', t=>{
  const o=setup(t), custom=path.join(o.parent,'untrusted.mjs'), marker=path.join(o.parent,'executed');
  fs.writeFileSync(custom,(`import fs from 'node:fs';fs.writeFileSync(${JSON.stringify(marker)},'bad');`).padEnd(18240,' '));
  assert.throws(()=>validatePair({...o,oldValidator:custom}),/legacy_helper_digest_mismatch/);assert.equal(fs.existsSync(marker),false);
  fs.writeFileSync(custom,'short');assert.throws(()=>validatePair({...o,oldValidator:custom}),/invalid_legacy_helper_file/);
  const link=path.join(o.parent,'link');fs.symlinkSync(helper,link);assert.throws(()=>validatePair({...o,oldValidator:link}),/linked_legacy_helper/);
  assert.throws(()=>validatePair({...o,oldValidator:o.parent}),/invalid_legacy_helper_file/);
  const embedded=path.join(o.oldPackage.root,'helper.mjs');fs.copyFileSync(helper,embedded);assert.throws(()=>validatePair({...o,oldValidator:embedded}),/package_embedded_validator/);
  const newEmbedded=path.join(o.newPackage.root,'helper.mjs');fs.copyFileSync(helper,newEmbedded);assert.throws(()=>validatePair({...o,oldValidator:newEmbedded}),/package_embedded_validator/);
});
test('pinned legacy verifier rejects wrong manifest, extra/missing/symlink/drifted package files', t=>{
  const o=setup(t), roots=[o.oldPackage.root,o.newPackage.root], run=()=>verifyLegacyPackage(o.oldPackage,helper,roots);
  assert.throws(()=>verifyLegacyPackage({...o.oldPackage,manifestDigest:'sha256:'+'0'.repeat(64)},helper,roots),/legacy_package_verification_failed/);
  const extra=path.join(o.oldPackage.root,'extra');fs.writeFileSync(extra,'');assert.throws(run,/legacy_package_verification_failed/);fs.unlinkSync(extra);
  const target=path.join(o.oldPackage.root,'packages/task-service/main.mjs');fs.unlinkSync(target);assert.throws(run,/legacy_package_verification_failed/);
  fs.symlinkSync(helper,target);assert.throws(run,/legacy_package_verification_failed/);fs.unlinkSync(target);
  fs.writeFileSync(target,'changed',{mode:0o600});assert.throws(run,/legacy_package_verification_failed/);
  fs.writeFileSync(target,'',{mode:0o600});assert.equal(run().files,65);
});
test('legacy selection does not weaken current package or common profile checks', t=>{
  const o=setup(t);
  assert.throws(()=>validatePair({...o,newPackage:{...o.newPackage,manifestDigest:'sha256:'+'0'.repeat(64)}}),{code:'manifest_digest_mismatch'});
  const filename=path.join(o.newPackage.root,'packages/task-regional-window/policy.mjs');fs.writeFileSync(filename,'changed',{mode:0o600});
  const m=JSON.parse(fs.readFileSync(path.join(o.newPackage.root,'manifest.json'))), entry=m.files.find(f=>f.path==='packages/task-regional-window/policy.mjs');
  entry.bytes=7;entry.digest=digest('changed');const raw=JSON.stringify(m,null,2)+'\n';fs.writeFileSync(path.join(o.newPackage.root,'manifest.json'),raw,{mode:0o600});
  assert.throws(()=>validatePair({...o,newPackage:{...o.newPackage,manifestDigest:digest(raw)}}),/regional_config_identity_changed/);
  assert.equal(fs.existsSync(o.runDir),false);
});
