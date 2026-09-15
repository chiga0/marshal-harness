import {timingSafeEqual} from 'node:crypto';
import {TaskApiError, validate} from './contract.mjs';

// Ported from the independently tested Node experiment's pure HTTP boundary;
// deliberately no experimental Store/Supervisor or fixed business imports.
export function headerCount(request, name) {
  return request.rawHeaders.filter((value, index) => index % 2 === 0 && value.toLowerCase() === name).length;
}
export function protect(request, token, expectedHost, authenticated) {
  if (request.headers.host !== expectedHost || request.headers.origin || headerCount(request, 'host') !== 1) throw new TaskApiError('untrusted_request');
  if (!authenticated) return;
  const authorization = request.headers.authorization;
  const expected = Buffer.from('Bearer ' + token);
  if (headerCount(request, 'authorization') !== 1 || typeof authorization !== 'string' ||
      Buffer.byteLength(authorization) !== expected.length || !timingSafeEqual(Buffer.from(authorization), expected)) throw new TaskApiError('unauthorized');
}
export function mutationHeaders(request) {
  if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(request.headers['content-type'] ?? '') ||
      headerCount(request, 'content-type') !== 1 || request.headers['content-encoding']) throw new TaskApiError('invalid_content_type');
  if (headerCount(request, 'idempotency-key') !== 1 || !validate(request.headers['idempotency-key'], 'Id')) throw new TaskApiError('invalid_idempotency_key');
  return request.headers['idempotency-key'];
}

// JSON.parse alone discards duplicate properties. Reject ambiguity before a
// command is presented to the application's receipt/CAS transaction.
export function parseJson(bytes) {
  let source;
  try { source = new TextDecoder('utf-8', {fatal: true}).decode(bytes); } catch { throw new TaskApiError('invalid_json'); }
  let at = 0;
  const bad = () => { throw new TaskApiError('invalid_json'); };
  const space = () => { while (/[\x20\t\r\n]/.test(source[at] ?? '') && at < source.length) at++; };
  function string() {
    if (source[at] !== '"') bad();
    const start = at++;
    for (;;) {
      if (at >= source.length) bad();
      if (source[at] === '\\') { at += 2; continue; }
      if (source[at++] === '"') break;
    }
    try { return JSON.parse(source.slice(start, at)); } catch { bad(); }
  }
  function value(depth) {
    if (depth > 32) bad();
    space();
    if (source[at] === '"') return string();
    if (source[at] === '{') {
      at++; space(); const result = Object.create(null), seen = new Set();
      if (source[at] === '}') { at++; return result; }
      for (;;) {
        space(); const name = string(); if (seen.has(name)) bad(); seen.add(name);
        space(); if (source[at++] !== ':') bad(); result[name] = value(depth + 1); space();
        const next = source[at++]; if (next === '}') return result; if (next !== ',') bad();
      }
    }
    if (source[at] === '[') {
      at++; space(); const result = []; if (source[at] === ']') { at++; return result; }
      for (;;) { result.push(value(depth + 1)); space(); const next = source[at++]; if (next === ']') return result; if (next !== ',') bad(); }
    }
    const match = /^(?:true|false|null|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/.exec(source.slice(at));
    if (!match) bad(); at += match[0].length;
    const parsed = JSON.parse(match[0]); if (typeof parsed === 'number' && !Number.isFinite(parsed)) bad(); return parsed;
  }
  const result = value(0); space(); if (at !== source.length) bad(); return result;
}
export async function readJson(request, limit, signal) {
  const chunks = await new Promise((resolve, reject) => {
    let bytes = 0; const chunks = [];
    const cleanup = () => {
      request.off('data', data); request.off('end', end); request.off('error', error);
      request.off('close', close); signal.removeEventListener('abort', abort);
    };
    const fail = failure => { cleanup(); request.pause(); reject(failure); };
    const data = chunk => {
      bytes += chunk.length;
      if (bytes > limit) { fail(new TaskApiError('request_too_large')); return; }
      chunks.push(chunk);
    };
    const end = () => { cleanup(); resolve(chunks); };
    const error = () => fail(new TaskApiError('invalid_request'));
    const close = () => fail(new TaskApiError('request_timeout'));
    const abort = () => fail(new TaskApiError('request_timeout'));
    request.on('data', data); request.once('end', end); request.once('error', error); request.once('close', close);
    signal.addEventListener('abort', abort, {once: true});
    if (signal.aborted) abort();
  });
  return parseJson(Buffer.concat(chunks));
}

// Do not destroy IncomingMessage before the error response is flushed: doing
// so also destroys its socket and hides the HTTP outcome from the caller.
// Pause the transport and forbid reuse while the unfinished body is discarded.
export function closeIncompleteRequest(request, response) {
  request.pause(); request.socket?.pause(); response.shouldKeepAlive = false;
  response.setHeader('Connection', 'close');
  const cleanup = () => {
    response.off('finish', cleanup); response.off('close', cleanup);
    request.destroy();
  };
  response.once('finish', cleanup); response.once('close', cleanup);
  if (response.destroyed) cleanup();
}
export function sendJson(response, status, body, requestId) {
  const raw = JSON.stringify(body);
  response.writeHead(status, {'Content-Type': 'application/json', 'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff', 'X-Request-Id': requestId, 'Content-Length': Buffer.byteLength(raw)});
  response.end(raw);
}
