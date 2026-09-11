import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {randomUUID} from 'node:crypto';
import {pathToFileURL} from 'node:url';
import {startTaskService} from './composition.mjs';
import {parseLaunchArguments, prepareLaunch} from './launch.mjs';
import {safeManagedDiagnostic, safeRejectedOutputDiagnostic} from '../task-application/leader-ports.mjs';
import {sqliteRuntimeCapabilities} from '../task-store/store.mjs';
import {TaskApiError, errorPayload} from '../task-api/contract.mjs';
import {headerCount} from '../task-api/http-boundary.mjs';
import {cliShutdown} from './cli-shutdown.mjs';
import {safeServiceDiagnostic, terminalServiceDiagnostic} from './service-diagnostic.mjs';

// 可选本机浏览器 UI（ADR0098）。默认不启用；启用后本入口在 127.0.0.1 上多监听
// 一个唯一公开端口，同时承担 /ui/ 冻结静态清单与精确 Origin 边界，其余请求
// 经无会话、无状态中继原样进入组合根内部仅 loopback 的 API 监听。授权不变：
// token 权威、Bearer、回执/CAS 与原错误语义保持原样；连接文件的 {url,token}
// 继续指向原 API 监听，旧 Node 客户端与 API-only 规则不受影响。中继不转发
// Origin/Cookie，也不附加任何 CORS 头；HTTP 提供的路径永远不能进入磁盘。
const UI_PREFIX = '/ui/';
const UI_CSP = "default-src 'self'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; " +
  "img-src 'self' data:; object-src 'none'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'";
const UI_MEDIA = Object.freeze({
  html: 'text/html; charset=utf-8', js: 'text/javascript; charset=utf-8', css: 'text/css; charset=utf-8',
  json: 'application/json', map: 'application/json', svg: 'image/svg+xml', png: 'image/png', jpg: 'image/jpeg',
  jpeg: 'image/jpeg', ico: 'image/x-icon', webmanifest: 'application/manifest+json', txt: 'text/plain; charset=utf-8',
  woff: 'font/woff', woff2: 'font/woff2',
});
const UI_MAX_FILES = 512, UI_MAX_DEPTH = 16, UI_MAX_FILE_BYTES = 8 * 1024 * 1024, UI_MAX_TOTAL_BYTES = 32 * 1024 * 1024;
const NOFOLLOW = fs.constants.O_NOFOLLOW;

class UiBoundaryError extends Error {
  constructor(code) { super(code); this.name = 'UiBoundaryError'; this.code = code; }
}

// Mirrors launch.mjs path hygiene; the build directory stays trusted release
// input, never an HTTP-provided path. Same-UID content trust is accepted, but
// symlinks, non-regular entries and foreign ownership are refused before start.
function uiDirectory(buildDir) {
  const ok = typeof buildDir === 'string' && buildDir.isWellFormed() && !/[\x00-\x1f\x7f]/.test(buildDir) &&
    Buffer.byteLength(buildDir) <= 4096 && path.isAbsolute(buildDir) && path.normalize(buildDir) === buildDir &&
    buildDir !== path.parse(buildDir).root;
  if (!ok) throw new UiBoundaryError('invalid_launch_configuration');
  const stat = fs.lstatSync(buildDir);
  if (!stat.isDirectory() || stat.uid !== process.getuid() || fs.realpathSync(buildDir) !== buildDir) throw new UiBoundaryError('ui_assets_unavailable');
}

/** Frozen in-memory snapshot of the exact built UI assets: each byte is read
 * once at admission and never re-read from disk while answering requests. */
function uiSnapshot(buildDir) {
  uiDirectory(buildDir);
  const files = new Map(); let total = 0;
  const walk = (directory, relative, depth) => {
    if (depth > UI_MAX_DEPTH) throw new UiBoundaryError('ui_assets_unavailable');
    for (const name of fs.readdirSync(directory).sort()) {
      const child = path.join(directory, name), relativePath = relative ? relative + '/' + name : name;
      const stat = fs.lstatSync(child);
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name)) throw new UiBoundaryError('ui_assets_unavailable');
      if (stat.isDirectory()) { walk(child, relativePath, depth + 1); continue; }
      if (!stat.isFile() || stat.nlink !== 1 || stat.uid !== process.getuid() || files.size >= UI_MAX_FILES ||
          stat.size > UI_MAX_FILE_BYTES || (total += stat.size) > UI_MAX_TOTAL_BYTES) throw new UiBoundaryError('ui_assets_unavailable');
      const type = UI_MEDIA[name.includes('.') ? name.split('.').pop().toLowerCase() : ''];
      if (!type) throw new UiBoundaryError('ui_assets_unavailable');
      const fd = fs.openSync(child, fs.constants.O_RDONLY | NOFOLLOW);
      try {
        const before = fs.fstatSync(fd);
        if (!before.isFile() || before.nlink !== 1 || before.size !== stat.size || before.mtimeMs !== stat.mtimeMs || before.ctimeMs !== stat.ctimeMs)
          throw new UiBoundaryError('ui_assets_unavailable');
        const bytes = fs.readFileSync(fd);
        const after = fs.fstatSync(fd);
        if (bytes.length !== before.size || after.size !== before.size || after.mtimeMs !== before.mtimeMs || after.ctimeMs !== before.ctimeMs)
          throw new UiBoundaryError('ui_assets_unavailable');
        // Content-hashed release assets are immutable; every other file is never cached.
        const immutable = /^assets\/[A-Za-z0-9._-]*-[A-Za-z0-9_-]{8}\.[a-z0-9]+$/.test(relativePath);
        files.set(relativePath, Object.freeze({type, bytes, immutable}));
      } finally { fs.closeSync(fd); }
    }
  };
  walk(buildDir, '', 1);
  if (!files.has('index.html')) throw new UiBoundaryError('ui_assets_unavailable');
  return Object.freeze({files});
}

function uiFailure(response, code, requestId = randomUUID()) {
  const {status, body} = errorPayload(new TaskApiError(code), requestId);
  const raw = JSON.stringify(body);
  response.writeHead(status, {'Content-Type': 'application/json', 'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff', 'Content-Security-Policy': UI_CSP, 'Referrer-Policy': 'no-referrer',
    'X-Request-Id': requestId, 'Content-Length': Buffer.byteLength(raw)});
  response.end(raw);
}

function serveUi(request, response, snapshot, requestId) {
  const url = request.url;
  if (typeof url !== 'string' || url.length > 4096 || !url.startsWith(UI_PREFIX) || /[\\#\x00-\x20%]/.test(url) ||
      url.includes('?') || url.includes('//')) throw new TaskApiError('not_found');
  if (!['GET', 'HEAD'].includes(request.method)) throw new TaskApiError('method_not_allowed');
  const entry = snapshot.files.get(url === UI_PREFIX ? 'index.html' : url.slice(UI_PREFIX.length));
  if (!entry) throw new TaskApiError('not_found');
  response.writeHead(200, {'Content-Type': entry.type, 'Content-Length': entry.bytes.length,
    'Cache-Control': entry.immutable ? 'public, max-age=31536000, immutable' : 'no-store',
    'X-Content-Type-Options': 'nosniff', 'Content-Security-Policy': UI_CSP, 'Referrer-Policy': 'no-referrer',
    'Cross-Origin-Resource-Policy': 'same-origin', 'X-Request-Id': requestId});
  response.end(request.method === 'HEAD' ? undefined : entry.bytes);
}

const BLOCKED_RESPONSE_HEADERS = new Set(['connection', 'keep-alive', 'transfer-encoding', 'upgrade', 'trailer',
  'te', 'expect', 'proxy-authenticate', 'proxy-authorization', 'set-cookie']);
const FORWARD_REQUEST_HEADERS = ['accept', 'content-type', 'content-length', 'authorization', 'idempotency-key'];

// Stateless per-request relay to the original loopback API listener. Origin and
// cookies never cross, the Host stays the inner expectation; only the ADR0098
// exact-origin edge check admits browser traffic onto this one public socket.
function relay(request, response, inner, timeoutMs, requestId) {
  const headers = {'accept-encoding': 'identity'};
  for (const name of FORWARD_REQUEST_HEADERS) if (request.headers[name] !== undefined) headers[name] = request.headers[name];
  let settled = false;
  const fail = code => {
    if (settled) return; settled = true; clearTimeout(timer); outbound.destroy();
    if (response.headersSent || response.destroyed) response.destroy(); else uiFailure(response, code, requestId);
  };
  const timer = setTimeout(() => fail('application_unavailable'), timeoutMs + 1500);
  const outbound = http.request({host: inner.host, port: inner.port, method: request.method, path: request.url, headers, agent: false});
  outbound.once('error', () => fail('application_unavailable'));
  outbound.once('response', proxy => {
    const headers = {};
    for (const [name, value] of Object.entries(proxy.headers)) {
      if (Array.isArray(value) || BLOCKED_RESPONSE_HEADERS.has(name) || name.startsWith('access-control-')) continue;
      headers[name] = value;
    }
    headers['content-security-policy'] = UI_CSP;
    headers['referrer-policy'] = 'no-referrer';
    headers['x-content-type-options'] = 'nosniff';
    response.writeHead(proxy.statusCode, headers);
    response.once('error', () => proxy.destroy());
    response.once('close', () => { if (!response.writableEnded) proxy.destroy(); });
    proxy.once('error', () => { clearTimeout(timer); response.destroy(); });
    proxy.once('end', () => { clearTimeout(timer); settled = true; });
    proxy.pipe(response);
  });
  request.once('aborted', () => fail('application_unavailable'));
  request.pipe(outbound);
}

/** Public 127.0.0.1 edge for opt-in UI mode; composition's listener stays API-only. */
async function startUiEdge({snapshot, innerAddress, port, requestTimeoutMs}) {
  const inner = new URL(innerAddress);
  let edgeHost = '', edgeOrigin = '';
  const server = http.createServer((request, response) => {
    const requestId = randomUUID();
    try {
      const origin = request.headers.origin;
      if (request.headers.host !== edgeHost || headerCount(request, 'host') !== 1 ||
          origin !== undefined && (origin !== edgeOrigin || headerCount(request, 'origin') !== 1)) {
        uiFailure(response, 'untrusted_request', requestId);
        return;
      }
      if (request.url?.startsWith(UI_PREFIX)) serveUi(request, response, snapshot, requestId);
      else relay(request, response, {host: inner.hostname, port: Number(inner.port)}, requestTimeoutMs, requestId);
    } catch (error) {
      if (!response.headersSent && !response.destroyed) {
        uiFailure(response, error instanceof TaskApiError ? error.code : 'application_unavailable', requestId);
      } else response.destroy();
    }
  });
  server.requestTimeout = requestTimeoutMs + 1000; server.headersTimeout = requestTimeoutMs;
  server.keepAliveTimeout = 1000; server.maxRequestsPerSocket = 100;
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => { server.off('error', reject); resolve(); });
  });
  edgeHost = `127.0.0.1:${server.address().port}`;
  edgeOrigin = `http://${edgeHost}`;
  const address = edgeOrigin;
  const close = () => {
    const drain = new Promise(resolve => server.close(() => resolve()));
    server.closeIdleConnections();
    const timer = setTimeout(() => server.closeAllConnections(), requestTimeoutMs + 100);
    return drain.finally(() => clearTimeout(timer));
  };
  return Object.freeze({address, close});
}

/** Extract the opt-in `--ui <buildDir>` pair first; the frozen launch parser
 * runs against the remaining argv with unchanged legacy semantics. */
function takeUiOption(argv) {
  if (!argv.includes('--ui')) return {argv, buildDir: null};
  const rest = []; let buildDir = null;
  for (let at = 0; at < argv.length; at++) {
    if (argv[at] !== '--ui') { rest.push(argv[at]); continue; }
    if (buildDir !== null || at + 1 >= argv.length) throw new UiBoundaryError('invalid_launch_configuration');
    buildDir = argv[at + 1]; at++;
    if (typeof buildDir !== 'string' || buildDir.length === 0 || argv.includes('--ui', at + 1)) throw new UiBoundaryError('invalid_launch_configuration');
  }
  return {argv: rest, buildDir};
}

// The module is trusted deployment code, never an HTTP-provided plugin/argv.
async function main(argv) {
  const uiOption = takeUiOption(argv);
  const args = parseLaunchArguments(uiOption.argv);
  if (args.help) {
    process.stdout.write('固定 Node 服务：--config /absolute/trusted/config.mjs [--data-dir /absolute/private/data] [--mode auto|create|open] [--port 0] [--ui /absolute/trusted/ui-dist]\n' +
      '默认数据目录 HOME/.marshal-node/task-service；不存在才创建，已有根只按原格式打开。--root 保留为 --data-dir 的互斥旧别名。\n' +
      '--ui 为可选本机同源浏览器 UI（默认不启用）：静态目录只读托管于 /ui/，浏览器与 API 共用本启动输出的 127.0.0.1 端口；未启用时不托管 UI。\n');
    return;
  }
  const configuration = (await import(pathToFileURL(args.config).href)).default;
  if (!configuration || typeof configuration !== 'object' || Array.isArray(configuration)) throw new Error('configuration');
  sqliteRuntimeCapabilities();
  // Admit the frozen UI assets before any data root exists; failures leave no state.
  const snapshotUi = uiOption.buildDir ? uiSnapshot(uiOption.buildDir) : null;
  const target = prepareLaunch(args); let service, edge, managedDiagnostics = 0, rejectedDiagnostics = 0, serviceDiagnostics = 0;
  const closing = cliShutdown(() => ({service, edge}), (result, code) => {
    if (result) process.stdout.write(JSON.stringify(result) + '\n');
    process.exitCode = code;
  });
  const stop = () => {void closing.stop();};
  process.on('SIGTERM', stop); process.on('SIGINT', stop);
  try {
    target.check();
    service = await startTaskService({...configuration, root: target.root, mode: target.mode, port: snapshotUi ? 0 : args.port,
      onDiagnostic: report => {
        const diagnostic = safeManagedDiagnostic(report);
        if (diagnostic && managedDiagnostics < 32) {managedDiagnostics++; process.stderr.write(JSON.stringify(diagnostic) + '\n');}
        else if (!diagnostic) {
          const rejected = safeRejectedOutputDiagnostic(report);
          if (rejected && rejectedDiagnostics < 8) {rejectedDiagnostics++; process.stderr.write(JSON.stringify(rejected) + '\n');}
        }
        const safe = safeServiceDiagnostic(report);
        if (safe && serviceDiagnostics++ < 32) process.stderr.write(JSON.stringify(safe) + '\n');
        if (terminalServiceDiagnostic(report)) {process.exitCode = 1; void closing.stop(report.code);}
        return configuration.onDiagnostic?.(report);
      }});
    target.check(); // Composition now owns the same named private root graph.
    if (snapshotUi && !closing.requested) {
      edge = await startUiEdge({snapshot: snapshotUi, innerAddress: service.address, port: args.port,
        requestTimeoutMs: configuration.requestTimeoutMs ?? 10000});
    }
  } catch (error) {
    void closing.stop('service_start_unavailable');
    throw error;
  } finally {
    try {target.close();} finally {closing.started(); if (closing.requested) await closing.stop();}
  }
  if (closing.requested) return;
  const address = edge ? edge.address : service.address;
  process.stdout.write(JSON.stringify({profile: service.snapshot().profile, address, connectionFile: service.connectionFile}) + '\n');
}

main(process.argv.slice(2)).catch(() => {
  process.stderr.write('{"code":"service_start_unavailable"}\n');
  process.exitCode = 1;
});
