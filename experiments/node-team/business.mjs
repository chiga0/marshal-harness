import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdtemp, chmod, writeFile, readFile, realpath, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const FILE_NAMES = ['normalize.mjs', 'report.mjs'];
const MAX_FILE = 64 * 1024;
const MAX_VERIFY_OUTPUT = 8192;
const VERIFY_DEADLINE_MS = 3000;

export function plan(intent) {
  if (typeof intent !== 'string' || !intent.trim() || !intent.isWellFormed() || Buffer.byteLength(intent) > 16 * 1024) {
    throw new Error('intent_invalid');
  }
  const common = `本轮是固定订单处理实验。用户意图仅作背景，不得改变下述接口或验收：${JSON.stringify(intent)}。
只实现指定的一个文件；不要读取文件、运行命令、调用工具或网络。不生成测试、不自报验证通过。
代码为无依赖纯 JavaScript ES module，不使用 import/dynamic import、Node API、全局副作用、异步函数或外部状态。
函数同步返回新数组，不修改输入。SKU 原样保留（不 trim、不改大小写）；空白字符串无效。数值必须是 number，禁止隐式字符串转换。
数组必须稠密：sparse array/hole（如 Array(1) 或 [,]）必须 throw；不得用 map/forEach 静默跳过缺项，需显式检查每个索引或逐项拒绝 undefined。
所有 quantity、priceCents、quantity*priceCents 及汇总值必须为安全整数，溢出必须同步 throw。
回复只包含一个 JSON 对象，字段严格为 name 和 content（content 是完整源代码字符串），不要 Markdown fence、解释或其他文件。`;
  return {
    version: 'node-orders-v1', intent, timeoutMs: 300000,
    nodes: [
      {
        id: 'normalize', role: 'author', file: 'normalize.mjs',
        prompt: `${common}
你的唯一产出文件 name 为 "normalize.mjs"，导出 export function normalize(rows)。
rows 是订单数组；每项必须是非 null 对象，sku 是非空/非全空白字符串，quantity 是正 safe integer，priceCents 是非负 safe integer。
保留输入顺序，输出每项仅 {sku,quantity,priceCents}，丢弃额外字段；不合并、不按 SKU 排序。非法输入（含非数组、空项、缺字段、NaN/Infinity、乘法溢出）必须 throw。
例：[{sku:"B",quantity:2,priceCents:150,note:"x"},{sku:"A",quantity:1,priceCents:0}] → [{sku:"B",quantity:2,priceCents:150},{sku:"A",quantity:1,priceCents:0}]；[] → []。
反例：quantity=0、quantity="2"、priceCents=-1、sku=" "、quantity=2且priceCents=Number.MAX_SAFE_INTEGER 都必须 throw。
后继接口 report(normalized) 会消费你的输出，按 SKU 汇总；你不实现该后继函数。`,
      },
      {
        id: 'report', role: 'author', file: 'report.mjs',
        prompt: `${common}
你的唯一产出文件 name 为 "report.mjs"，导出 export function report(normalized)。
输入是规范化订单数组，每项 {sku,quantity,priceCents}；仍须拒绝非数组、非法项/字段、NaN/Infinity 和任何溢出。
按完全相同 sku 汇总，返回每项仅 {sku,quantity,totalCents}，quantity 为数量总和，totalCents 为 quantity*priceCents 总和。
按 JavaScript 字符串 < / > 的字典序排序（UTF-16 code unit order），不能使用地区 localeCompare。
安全处理 "__proto__"、"constructor" 等 SKU。数量和金额任一累计超过 Number.MAX_SAFE_INTEGER 必须 throw。
例：[{sku:"B",quantity:2,priceCents:150},{sku:"A",quantity:1,priceCents:0},{sku:"B",quantity:1,priceCents:200}] → [{sku:"A",quantity:1,totalCents:0},{sku:"B",quantity:3,totalCents:500}]；[] → []。
反例：同 SKU 数量 Number.MAX_SAFE_INTEGER 与 1（价格均0）必须 throw；同 SKU 金额 Number.MAX_SAFE_INTEGER 与1必须 throw。
上游接口 normalize(rows) 会提供上述规范化数据；你不实现该上游函数。`,
      },
    ],
  };
}

function invalidFiles(files) {
  return !Array.isArray(files) || files.length !== 2 ||
    files.some((file) => !file || Object.keys(file).sort().join(',') !== 'content,name' ||
      !FILE_NAMES.includes(file.name) || typeof file.content !== 'string' || !file.content.trim() ||
      !file.content.isWellFormed() || file.content.includes('\0') || Buffer.byteLength(file.content) > MAX_FILE) ||
    new Set(files.map((file) => file.name)).size !== 2;
}
function failed(reason, checks = 0) { return { passed: false, checks, files: [], reason }; }

function runVerifier(directory, runner, signal) {
  return new Promise((resolve) => {
    // Node 24 permissions restrict fs/child/addons, NOT network. The fixed
    // runner additionally exposes only pure JS VM modules with imports denied.
    // Neither mechanism is an adversarial-code sandbox (ADR 0087).
    const child = spawn(process.execPath, [
      '--permission', `--allow-fs-read=${directory}`, '--no-addons', '--disable-proto=throw',
      '--experimental-vm-modules', '--max-old-space-size=64', '--input-type=module', '-', directory,
    ], { cwd: directory, env: { LANG: 'C', TZ: 'UTC' }, stdio: ['pipe', 'pipe', 'pipe'], shell: false });
    let stdout = '', outBytes = 0, errBytes = 0, reason;
    const stop = (why) => { reason ||= why; child.kill('SIGKILL'); };
    const timer = setTimeout(() => stop('verification_timeout'), VERIFY_DEADLINE_MS);
    const onAbort = () => stop('verification_aborted');
    signal?.addEventListener('abort', onAbort, { once: true });
    if (signal?.aborted) onAbort();
    child.on('error', () => { reason ||= 'verification_unavailable'; });
    child.stdin.on('error', () => { reason ||= 'verification_unavailable'; });
    child.stdout.on('data', (data) => {
      outBytes += data.length;
      if (outBytes > MAX_VERIFY_OUTPUT) stop('verification_output_limit');
      else stdout += data.toString('utf8');
    });
    child.stderr.on('data', (data) => {
      errBytes += data.length;
      if (errBytes > MAX_VERIFY_OUTPUT) stop('verification_output_limit');
    });
    child.on('close', (code, exitSignal) => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      if (reason) return resolve(failed(reason));
      let result;
      try { result = JSON.parse(stdout); } catch { return resolve(failed('verification_protocol')); }
      if (!result || !Number.isSafeInteger(result.checks) || result.checks < 0 ||
          typeof result.passed !== 'boolean' || Object.keys(result).some((key) => !['passed', 'checks', 'reason'].includes(key))) {
        return resolve(failed('verification_protocol'));
      }
      if (code !== 0 || exitSignal || !result.passed || result.checks === 0) {
        return resolve(failed('verification_failed', result.checks));
      }
      resolve({ passed: true, checks: result.checks });
    });
    child.stdin.end(runner);
  });
}

// Parent owns the immutable source snapshot and fixed oracle; authors only
// supply two code candidates. A zero exit without successful checks is failure.
export async function verifyFiles(files, { signal } = {}) {
  if (signal !== undefined && !(signal instanceof AbortSignal)) return failed('verification_signal_invalid');
  if (signal?.aborted) return failed('verification_aborted');
  if (invalidFiles(files)) return failed('candidate_files_invalid');
  const snapshot = FILE_NAMES.map((name) => {
    const { content } = files.find((file) => file.name === name);
    return { name, content, sha256: createHash('sha256').update(content, 'utf8').digest('hex') };
  });
  let directory;
  try {
    const runner = await readFile(new URL('./verify-runner.mjs', import.meta.url), 'utf8');
    if (Buffer.byteLength(runner) > 64 * 1024) return failed('verification_unavailable');
    directory = await mkdtemp(join(await realpath(tmpdir()), 'marshal-node-verify-'));
    await chmod(directory, 0o700);
    for (const file of snapshot) await writeFile(join(directory, file.name), file.content, { flag: 'wx', mode: 0o600 });
    if (signal?.aborted) return failed('verification_aborted');
    const result = await runVerifier(directory, runner, signal);
    return result.passed ? { ...result, files: snapshot } : result;
  } catch { return failed('verification_unavailable'); }
  finally {
    if (directory) {
      try { await rm(directory, { recursive: true, force: true }); }
      catch { return failed('verification_cleanup_failed'); }
    }
  }
}
