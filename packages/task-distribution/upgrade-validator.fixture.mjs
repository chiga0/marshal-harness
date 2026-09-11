// Test consumer only. v1.0.2 helper pin is the reviewed install-node.sh
// HELPER_SHA for this exact source, not a digest supplied by the package.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
export const LEGACY_SOURCE = 'f9a93cd678cac40bcd04ff9d0c1672612f184701';
export const LEGACY_HELPER_SHA = '60ed1edfb01bd4dee8bc3304e142dc6080c88611362599c260065a51e97d534d';
export function verifyLegacyPackage(input, helper, packageRoots) {
  assert.equal(input.sourceHead, LEGACY_SOURCE, 'unsupported_legacy_source');
  assert.ok(typeof helper === 'string' && path.isAbsolute(helper) && path.resolve(helper) === helper, 'invalid_legacy_helper_path');
  assert.equal(fs.realpathSync(helper), helper, 'linked_legacy_helper');
  for (const root of packageRoots) assert.ok(helper !== root && !helper.startsWith(root + path.sep), 'package_embedded_validator');
  const before = fs.lstatSync(helper);
  assert.ok(before.isFile() && before.nlink === 1 && before.size === 18240, 'invalid_legacy_helper_file');
  const fd = fs.openSync(helper, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  let bytes;
  try {
    const held = fs.fstatSync(fd);
    assert.ok(held.isFile() && held.nlink === 1 && held.size === 18240 && held.dev === before.dev && held.ino === before.ino, 'legacy_helper_drift');
    bytes = Buffer.alloc(18241); const count = fs.readSync(fd, bytes, 0, bytes.length, 0); bytes = bytes.subarray(0, count);
    assert.equal(count, 18240, 'legacy_helper_size');
    const after = fs.fstatSync(fd);
    assert.ok(after.size === held.size && after.mtimeMs === held.mtimeMs && after.ctimeMs === held.ctimeMs, 'legacy_helper_drift');
  } finally {fs.closeSync(fd);}
  assert.equal(createHash('sha256').update(bytes).digest('hex'), LEGACY_HELPER_SHA, 'legacy_helper_digest_mismatch');
  // Import the verified byte snapshot, never re-open its path or resolve local
  // package imports. This fixed helper imports Node builtins only. No NODE_OPTIONS.
  const runner = "import fs from 'node:fs'; const p=JSON.parse(fs.readFileSync(0,'utf8')); const m=await import('data:text/javascript;base64,'+p.code); console.log(JSON.stringify(m.verify({root:p.root,manifestDigest:p.manifestDigest})));";
  let report;
  try {
    const raw = execFileSync(process.execPath, ['--input-type=module', '-e', runner], {
      input: JSON.stringify({code: bytes.toString('base64'), root: input.root, manifestDigest: input.manifestDigest}),
      env: {}, timeout: 10000, maxBuffer: 16384, stdio: ['pipe', 'pipe', 'pipe'], encoding: 'utf8'});
    report = JSON.parse(raw);
  } catch {throw new Error('legacy_package_verification_failed');}
  assert.equal(report.sourceHead, input.sourceHead, 'source_pin_mismatch');
  assert.equal(report.manifestDigest, input.manifestDigest, 'manifest_pin_mismatch');
  return report;
}
