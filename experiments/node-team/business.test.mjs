import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { getEventListeners } from 'node:events';
import { plan, verifyFiles } from './business.mjs';

// Deterministic code fixtures, not real Provider output or team-run evidence.
const validate = `function validate(rows) {
  if (!Array.isArray(rows)) throw new Error('rows');
  const result = [];
  for (const row of rows) {
    if (!row || typeof row !== 'object' || Array.isArray(row) || typeof row.sku !== 'string' || !row.sku.trim() ||
        !Number.isSafeInteger(row.quantity) || row.quantity <= 0 || !Number.isSafeInteger(row.priceCents) || row.priceCents < 0 ||
        !Number.isSafeInteger(row.quantity * row.priceCents)) throw new Error('row');
    result.push({sku:row.sku,quantity:row.quantity,priceCents:row.priceCents});
  }
  return result;
}`;
const normalize = `${validate}\nexport function normalize(rows) { return validate(rows); }`;
const report = `${validate}
export function report(rows) {
  const groups = new Map();
  for (const row of validate(rows)) {
    const prior = groups.get(row.sku) || {quantity:0,totalCents:0};
    const quantity = prior.quantity + row.quantity;
    const totalCents = prior.totalCents + row.quantity * row.priceCents;
    if (!Number.isSafeInteger(quantity) || !Number.isSafeInteger(totalCents)) throw new Error('overflow');
    groups.set(row.sku, {sku:row.sku,quantity,totalCents});
  }
  return [...groups.values()].sort((a,b) => a.sku < b.sku ? -1 : a.sku > b.sku ? 1 : 0);
}`;
const files = (first = normalize, second = report) => [
  { name: 'normalize.mjs', content: first }, { name: 'report.mjs', content: second },
];

test('plan freezes two complementary authors and bounded pure-module contracts', () => {
  const result = plan('订单汇总');
  assert.equal(result.intent, '订单汇总');
  assert.equal(result.timeoutMs, 300000);
  assert.equal(result.nodes.length, 2);
  for (const node of result.nodes) {
    assert.equal(node.role, 'author');
    assert.ok(node.prompt.includes(node.file));
    assert.ok(node.prompt.includes('sparse array/hole'));
    const other = result.nodes.find((value) => value !== node);
    assert.ok(!node.prompt.includes(other.file));
  }
  for (const value of ['', ' ', 42, 'x'.repeat(16385), '\ud800']) assert.throws(() => plan(value));
});

test('fixed oracle verifies composition, negatives and exact SHA-256 content', async () => {
  const controller = new AbortController();
  const result = await verifyFiles(files(), { signal: controller.signal });
  assert.equal(result.passed, true, result.reason);
  assert.ok(result.checks >= 60);
  assert.equal(getEventListeners(controller.signal, 'abort').length, 0);
  for (const file of result.files) {
    assert.equal(file.sha256, createHash('sha256').update(file.content).digest('hex'));
    assert.equal(file.content, files().find((input) => input.name === file.name).content);
  }
});

test('candidate allowlist rejects traversal, duplicate, extra and oversized files before execution', async () => {
  for (const value of [null, [], files().slice(0, 1), [...files(), {name:'extra.mjs',content:'x'}],
    [{name:'../normalize.mjs',content:normalize},files()[1]], [files()[0],files()[0]],
    files('x'.repeat(65537)), files('\ud800'), files('\0'), [{...files()[0],sha256:'claimed'},files()[1]]]) {
    assert.deepEqual(await verifyFiles(value), { passed: false, checks: 0, files: [], reason: 'candidate_files_invalid' });
  }
});

test('plausible code and ordinary clean candidate execution do not replace oracle assertions', async () => {
  for (const candidate of [files('export function normalize() { return []; }'),
    files(normalize, report.replace("if (!Number.isSafeInteger(quantity) || !Number.isSafeInteger(totalCents)) throw new Error('overflow');", '')),
    files(normalize, report.replace('a.sku < b.sku ? -1 : a.sku > b.sku ? 1 : 0', 'a.sku.localeCompare(b.sku)')),
    files(`${validate}\nexport function normalize(rows) { const result=validate(rows); rows.reverse(); return result; }`),
    files('export async function normalize() { return []; }'), files('syntax error'),
    files('JSON.stringify = () => "[]"; export function normalize() { return []; }')]) {
    const result = await verifyFiles(candidate);
    assert.equal(result.passed, false);
    assert.deepEqual(result.files, []);
  }
});

test('builtin imports, network globals and caught dynamic imports cannot run candidates', async () => {
  for (const source of [`import 'node:fs';\n${normalize}`, `import 'node:child_process';\n${normalize}`,
    `import 'node:net';\n${normalize}`, `fetch('http://127.0.0.1/');\n${normalize}`,
    `try { await import('node:net'); } catch {}\n${normalize}`,
    `process.exit(0);\n${normalize}`]) {
    const result = await verifyFiles(files(source));
    assert.equal(result.passed, false);
    assert.deepEqual(result.files, []);
  }
});

test('function and getter loops are bounded inside the timed context', { timeout: 8000 }, async () => {
  for (const source of ['export function normalize() { while (true) {} }',
    'export function normalize() { return [{get sku(){while(true){}}}]; }']) {
    const started = Date.now();
    const result = await verifyFiles(files(source));
    assert.equal(result.passed, false);
    assert.ok(Date.now() - started < 3500);
  }
});

test('abort before/during verification waits for owned process close and removes listener', async () => {
  const pre = new AbortController();
  pre.abort();
  assert.equal((await verifyFiles(files(), { signal: pre.signal })).reason, 'verification_aborted');
  const during = new AbortController();
  const pending = verifyFiles(files('export function normalize() { while(true){} }'), { signal: during.signal });
  const timer = setTimeout(() => during.abort(), 50);
  const result = await pending;
  clearTimeout(timer);
  assert.equal(result.passed, false);
  assert.equal(result.reason, 'verification_aborted');
  assert.equal(getEventListeners(during.signal, 'abort').length, 0);
});
