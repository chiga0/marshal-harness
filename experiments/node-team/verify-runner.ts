// Fixed oracle, run from stdin by business.mjs under Node permissions. Candidate
// modules are trusted experiment code, not an adversarial security boundary.
import { readFileSync, lstatSync } from 'node:fs';
import { join, isAbsolute } from 'node:path';
import vm from 'node:vm';
import { isDeepStrictEqual } from 'node:util';

let checks = 0;
let deniedImport = false;
const directory = process.argv[2];
const timeout = 250;
const context = vm.createContext(Object.create(null), {
  codeGeneration: { strings: false, wasm: false }, microtaskMode: 'afterEvaluate',
});
function execute(source) { return vm.runInContext(source, context, { timeout }); }
function check(condition) {
  if (!condition || deniedImport) throw new Error('oracle_check_failed');
  checks++;
}
function positive(fn, input, expected) {
  // Input, invocation, getters and result serialization all execute inside the
  // timed context. No author object/function is invoked by the outer oracle.
  const actual = JSON.parse(execute(`(() => {
    const input = ${input}; const before = __oracleStringify(input);
    const value = ${fn}(input);
    return __oracleStringify({value, unchanged: before === __oracleStringify(input)});
  })()`));
  check(actual.unchanged === true && isDeepStrictEqual(actual.value, expected));
}
function negative(fn, input) {
  check(execute(`(() => { try { ${fn}(${input}); return false; } catch { return true; } })()`) === true);
}

try {
  if (!isAbsolute(directory) || !process.permission || process.permission.has('fs.write') ||
      process.permission.has('child') || process.permission.has('addons')) throw new Error('permission_missing');
  execute('const __oracleStringify = JSON.stringify; delete globalThis.console;');
  const modules = {};
  for (const name of ['normalize.mjs', 'report.mjs']) {
    const path = join(directory, name);
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 64 * 1024) throw new Error('candidate_invalid');
    const module = new vm.SourceTextModule(readFileSync(path, 'utf8'), {
      context, identifier: name,
      importModuleDynamically() {
        deniedImport = true;
        throw execute('new Error("candidate_import_denied")');
      },
    });
    await module.link(() => { throw new Error('candidate_import_denied'); });
    await module.evaluate({ timeout });
    modules[name] = module;
  }
  for (const [name, exported] of [['normalize.mjs', 'normalize'], ['report.mjs', 'report']]) {
    const module = modules[name];
    check(typeof module.namespace[exported] === 'function');
    Object.defineProperty(context, `__${exported}`, { value: module.namespace[exported], writable: false, configurable: false });
  }
  // VM has no host process/require/fetch and no linker to Node, files or network.
  // Node 24 permissions alone do not restrict networking; do not claim they do.
  check(execute('typeof process === "undefined" && typeof require === "undefined" && typeof fetch === "undefined"') === true);

  positive('__normalize', '[]', []);
  positive('__report', '[]', []);
  const orders = '[{sku:"B",quantity:2,priceCents:150,note:"drop"},{sku:"A",quantity:1,priceCents:0},{sku:"B",quantity:1,priceCents:200}]';
  positive('__normalize', orders, [
    { sku: 'B', quantity: 2, priceCents: 150 }, { sku: 'A', quantity: 1, priceCents: 0 }, { sku: 'B', quantity: 1, priceCents: 200 },
  ]);
  positive('__report', orders, [{ sku: 'A', quantity: 1, totalCents: 0 }, { sku: 'B', quantity: 3, totalCents: 500 }]);
  positive('(rows => __report(__normalize(rows)))', orders, [{ sku: 'A', quantity: 1, totalCents: 0 }, { sku: 'B', quantity: 3, totalCents: 500 }]);
  positive('__normalize', '[{sku:" x ",quantity:1,priceCents:0}]', [{ sku: ' x ', quantity: 1, priceCents: 0 }]);
  positive('__report', '[{sku:"__proto__",quantity:1,priceCents:2},{sku:"constructor",quantity:2,priceCents:3},{sku:"__proto__",quantity:3,priceCents:4}]', [
    { sku: '__proto__', quantity: 4, totalCents: 14 }, { sku: 'constructor', quantity: 2, totalCents: 6 },
  ]);
  positive('__report', '[{sku:"ä",quantity:1,priceCents:1},{sku:"a",quantity:1,priceCents:1},{sku:"Z",quantity:1,priceCents:1}]', [
    { sku: 'Z', quantity: 1, totalCents: 1 }, { sku: 'a', quantity: 1, totalCents: 1 }, { sku: 'ä', quantity: 1, totalCents: 1 },
  ]);
  for (const fn of ['__normalize', '__report']) {
    positive(fn, '[{sku:"max",quantity:1,priceCents:Number.MAX_SAFE_INTEGER}]', [
      fn === '__normalize' ? { sku: 'max', quantity: 1, priceCents: Number.MAX_SAFE_INTEGER } : { sku: 'max', quantity: 1, totalCents: Number.MAX_SAFE_INTEGER },
    ]);
    for (const input of ['null', '{}', '"rows"', '[null]', '[undefined]', '[,]', '[[]]', '[{}]',
      '[{sku:"",quantity:1,priceCents:1}]', '[{sku:" ",quantity:1,priceCents:1}]', '[{sku:1,quantity:1,priceCents:1}]']) negative(fn, input);
    for (const quantity of ['0', '-1', '1.5', '"2"', 'NaN', 'Infinity', 'Number.MAX_SAFE_INTEGER+1', 'undefined']) {
      negative(fn, `[{sku:"x",quantity:${quantity},priceCents:1}]`);
    }
    for (const price of ['-1', '0.5', '"2"', 'NaN', 'Infinity', 'Number.MAX_SAFE_INTEGER+1', 'undefined']) {
      negative(fn, `[{sku:"x",quantity:1,priceCents:${price}}]`);
    }
    negative(fn, '[{sku:"x",quantity:2,priceCents:Number.MAX_SAFE_INTEGER}]');
  }
  negative('__report', '[{sku:"x",quantity:Number.MAX_SAFE_INTEGER,priceCents:0},{sku:"x",quantity:1,priceCents:0}]');
  negative('__report', '[{sku:"x",quantity:1,priceCents:Number.MAX_SAFE_INTEGER},{sku:"x",quantity:1,priceCents:1}]');
  process.stdout.write(JSON.stringify({ passed: true, checks }) + '\n');
} catch {
  process.stdout.write(JSON.stringify({ passed: false, checks, reason: 'oracle_failed' }) + '\n');
  process.exitCode = 1;
}
