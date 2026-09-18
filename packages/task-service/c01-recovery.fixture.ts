// C01 test fixture only. This is a real filesystem exercise for the bounded
// import model; it is not a production recovery primitive or an ADR0107 API.
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';

const NOFOLLOW = fs.constants.O_NOFOLLOW;
const PROFILE = 'marshal-c01-import/v1';
const SHA = /^sha256:[0-9a-f]{64}$/;
const STEPS = Object.freeze(['record-pending', 'build-temporary', 'replace-index', 'mark-complete']);

export class C01RecoveryError extends Error {
  constructor(code) { super(code); this.name = 'C01RecoveryError'; this.code = code; }
}
export class C01Crash extends Error {
  constructor(step) { super('crash-after-' + step); this.name = 'C01Crash'; this.step = step; }
}

const fail = code => { throw new C01RecoveryError(code); };
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const jsonBytes = value => Buffer.from(JSON.stringify(value) + '\n');
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const exactKeys = (value, names) => object(value) && Object.keys(value).length === names.length && names.every(name => Object.hasOwn(value, name));
const sameBytes = (a, b) => Buffer.isBuffer(a) && Buffer.isBuffer(b) && a.equals(b);
const sameValue = (a, b) => JSON.stringify(a) === JSON.stringify(b);

function validRows(rows) {
  if (!Array.isArray(rows) || rows.length < 1 || rows.length > 64) return false;
  const ids = new Set();
  return rows.every(row => exactKeys(row, ['id', 'body', 'tags']) &&
    typeof row.id === 'string' && row.id.length > 0 && row.id.length <= 256 && !ids.has(row.id) && ids.add(row.id) &&
    typeof row.body === 'string' && row.body.length <= 8192 && Array.isArray(row.tags) && row.tags.length <= 16 &&
    row.tags.every(tag => typeof tag === 'string' && tag.length <= 256));
}
function rootCheck(root) {
  if (typeof root !== 'string' || !path.isAbsolute(root) || path.normalize(root) !== root) fail('invalid_root');
  const stat = fs.lstatSync(root);
  if (!stat.isDirectory() || stat.uid !== process.getuid() || (stat.mode & 0o777) !== 0o700 || fs.realpathSync(root) !== root) fail('invalid_root');
}
function fileBytes(root, name, {required = false} = {}) {
  if (name !== '.index.pending' && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name)) fail('invalid_path');
  const target = path.join(root, name);
  let fd;
  try { fd = fs.openSync(target, fs.constants.O_RDONLY | NOFOLLOW); }
  catch (error) { if (error.code === 'ENOENT' && !required) return null; fail('file_unavailable'); }
  try {
    const before = fs.fstatSync(fd);
    if (!before.isFile() || before.uid !== process.getuid() || before.nlink !== 1 || !([0o600, 0o400].includes(before.mode & 0o777)) || before.size > 1024 * 1024) fail('file_unavailable');
    const bytes = Buffer.alloc(before.size); let offset = 0;
    while (offset < bytes.length) { const count = fs.readSync(fd, bytes, offset, bytes.length - offset, offset); if (count <= 0) fail('file_changed'); offset += count; }
    const after = fs.fstatSync(fd);
    if (after.dev !== before.dev || after.ino !== before.ino || after.size !== before.size || after.mtimeMs !== before.mtimeMs || after.ctimeMs !== before.ctimeMs) fail('file_changed');
    return bytes;
  } finally { fs.closeSync(fd); }
}
function syncRoot(root) { const fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | NOFOLLOW); try { fs.fsyncSync(fd); } finally { fs.closeSync(fd); } }
function writeNew(root, name, bytes) {
  const target = path.join(root, name);
  let fd;
  try { fd = fs.openSync(target, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | NOFOLLOW, 0o600); }
  catch (error) { if (error.code === 'EEXIST') fail('record_exists'); fail('file_unavailable'); }
  try {
    fs.writeFileSync(fd, bytes); fs.fsyncSync(fd);
    const stat = fs.fstatSync(fd);
    if (!stat.isFile() || stat.uid !== process.getuid() || stat.nlink !== 1 || (stat.mode & 0o777) !== 0o600 || stat.size !== bytes.length) fail('file_changed');
  } finally { fs.closeSync(fd); }
  syncRoot(root);
  const stored = fileBytes(root, name, {required: true});
  if (!sameBytes(stored, bytes)) fail('file_changed');
}
function parse(bytes, code) {
  try { const value = JSON.parse(bytes.toString('utf8')); if (!object(value)) fail(code); return value; }
  catch (error) { if (error instanceof C01RecoveryError) throw error; fail(code); }
}
function expectedFiles(rows) {
  const normalized = structuredClone(rows);
  const manifest = {profile: PROFILE, rows: normalized};
  const index = {profile: PROFILE, rows: normalized};
  const manifestBytes = jsonBytes(manifest), indexBytes = jsonBytes(index);
  const marker = {profile: PROFILE, manifestDigest: hash(manifestBytes), indexDigest: hash(indexBytes)};
  return {manifest, index, marker, manifestBytes, indexBytes, markerBytes: jsonBytes(marker)};
}
function consistent(root, expected) {
  const manifestBytes = fileBytes(root, 'manifest.json');
  const indexBytes = fileBytes(root, 'index.json');
  const markerBytes = fileBytes(root, 'complete.marker');
  if (!manifestBytes || !indexBytes || !markerBytes) return false;
  let manifest, index, marker;
  try { manifest = parse(manifestBytes, 'manifest_invalid'); index = parse(indexBytes, 'index_invalid'); marker = parse(markerBytes, 'marker_invalid'); } catch { return false; }
  return exactKeys(manifest, ['profile', 'rows']) && manifest.profile === PROFILE && sameValue(manifest.rows, expected.manifest.rows) &&
    exactKeys(index, ['profile', 'rows']) && index.profile === PROFILE && sameValue(index.rows, expected.index.rows) &&
    exactKeys(marker, ['profile', 'manifestDigest', 'indexDigest']) && marker.profile === PROFILE &&
    marker.manifestDigest === hash(manifestBytes) && marker.indexDigest === hash(indexBytes) &&
    sameBytes(markerBytes, expected.markerBytes) && !fileBytes(root, '.index.pending');
}
function stepCrash(crashAfter, step) { if (crashAfter === step) throw new C01Crash(step); }

/**
 * Execute one bounded, single-writer import. The no-replace link operation
 * refuses a pre-existing different index; it never overwrites user state.
 */
export function runImport({root, source, crashAfter = -1, skipMode = 'complete-and-consistent'} = {}) {
  rootCheck(root);
  if (!validRows(source)) fail('invalid_source');
  if (!Number.isSafeInteger(crashAfter) || crashAfter < -1 || crashAfter > STEPS.length) fail('invalid_crash_boundary');
  if (!['record-exists', 'complete-and-consistent'].includes(skipMode)) fail('invalid_skip_mode');
  const expected = expectedFiles(source);
  if (skipMode === 'complete-and-consistent' && consistent(root, expected)) return {status: 'skipped', reason: 'complete-and-consistent'};
  if (skipMode === 'record-exists' && fileBytes(root, 'manifest.json')) {
    if (consistent(root, expected)) return {status: 'skipped', reason: 'record-exists'};
    fail('record_exists_incomplete');
  }
  stepCrash(crashAfter, 0);

  const manifest = fileBytes(root, 'manifest.json');
  if (manifest) {
    if (!sameBytes(manifest, expected.manifestBytes)) fail('record_exists_inconsistent');
  } else writeNew(root, 'manifest.json', expected.manifestBytes);
  stepCrash(crashAfter, 1);

  const pending = fileBytes(root, '.index.pending');
  if (pending && !sameBytes(pending, expected.indexBytes)) fail('temporary_index_conflict');
  if (!pending && !fileBytes(root, 'index.json')) writeNew(root, '.index.pending', expected.indexBytes);
  stepCrash(crashAfter, 2);

  const currentIndex = fileBytes(root, 'index.json');
  if (currentIndex && !sameBytes(currentIndex, expected.indexBytes)) fail('record_exists_inconsistent');
  if (!currentIndex) {
    const staged = fileBytes(root, '.index.pending', {required: true});
    if (!sameBytes(staged, expected.indexBytes)) fail('temporary_index_conflict');
    try { fs.linkSync(path.join(root, '.index.pending'), path.join(root, 'index.json')); }
    catch (error) { if (error.code !== 'EEXIST') fail('replace_index_failed'); }
    // Remove the staging name before strict nlink validation; both names
    // intentionally refer to the same inode during the atomic link window.
    try { fs.unlinkSync(path.join(root, '.index.pending')); syncRoot(root); } catch (error) { if (error.code !== 'ENOENT') fail('replace_index_failed'); }
    const linked = fileBytes(root, 'index.json', {required: true});
    if (!sameBytes(linked, expected.indexBytes)) fail('record_exists_inconsistent');
  }
  try { fs.unlinkSync(path.join(root, '.index.pending')); syncRoot(root); } catch (error) { if (error.code !== 'ENOENT') fail('replace_index_failed'); }
  stepCrash(crashAfter, 3);

  const marker = fileBytes(root, 'complete.marker');
  if (marker && !sameBytes(marker, expected.markerBytes)) fail('completion_marker_conflict');
  if (!marker) writeNew(root, 'complete.marker', expected.markerBytes);
  stepCrash(crashAfter, 4);
  if (!consistent(root, expected)) fail('recovery_postcondition_failed');
  return {status: 'completed', steps: [...STEPS]};
}

/** Independent verifier: derives the expected bytes from the immutable source,
 * then reads the candidate/recovered directory. It does not consume reports. */
export function verifyRecoveryState({root, source} = {}) {
  rootCheck(root);
  if (!validRows(source)) return {status: 'not-verified', code: 'invalid_source'};
  const expected = expectedFiles(source);
  const required = ['manifest.json', 'index.json', 'complete.marker'];
  const observed = Object.fromEntries(required.map(name => [name, fileBytes(root, name)]));
  if (required.some(name => !observed[name])) return {status: 'fail', code: 'missing_recovery_state'};
  if (!consistent(root, expected)) return {status: 'fail', code: 'recovery_state_mismatch'};
  return {status: 'pass', code: 'recovery_state_exact', inputDigest: hash(jsonBytes(source)), candidateDigest: hash(Buffer.concat(required.map(name => observed[name])))};
}

export {PROFILE, STEPS, expectedFiles};
