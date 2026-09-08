import { FORMAT, Fault, fail, id, sameSecret } from './store.mjs';

// A thin transport adapter. The injected Application operation owns body
// semantics, revision CAS, idempotent replay and every Task lifecycle change.
// No owner, Provider, filesystem root or process launcher is constructed here.
export function createTaskHandler({ application, token, expectedHost }) {
  if (typeof application !== 'function' || typeof token !== 'string' || !token || typeof expectedHost !== 'string' || !expectedHost) throw new TypeError('invalid-task-handler-composition');
  return async (req, res) => {
    try {
      if (req.headers.host !== expectedHost || req.headers.origin || headerCount(req, 'host') !== 1) fail('untrusted-request', 403);
      if (req.method === 'GET' && req.url === '/health') { response(res, 200, { status: 'ok', profile: FORMAT }); return; }
      if (headerCount(req, 'authorization') !== 1 || !sameSecret(req.headers.authorization, 'Bearer ' + token)) fail('unauthorized', 401);
      const input = route(req.method, req.url);
      if (req.method === 'POST') {
        if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(req.headers['content-type'] ?? '') || req.headers['content-encoding']) fail('invalid-content-type', 415);
        if (headerCount(req, 'idempotency-key') !== 1 || !id(req.headers['idempotency-key'])) fail('invalid-idempotency-key', 400);
        input.key = req.headers['idempotency-key']; input.body = await readBody(req, 16384);
      }
      const value = await application(input);
      response(res, input.operation === 'create' ? 201 : ['approve', 'cancel'].includes(input.operation) ? 202 : 200, value);
    } catch (error) {
      response(res, error instanceof Fault ? error.status : 503, { error: error instanceof Fault ? error.code : 'internal-unavailable' });
    }
  };
}

function headerCount(request, name) {
  return request.rawHeaders.filter((value, index) => index % 2 === 0 && value.toLowerCase() === name).length;
}
function route(method, url) {
  if (url.includes('?') || url.includes('%') || url.includes('//')) fail('not-found', 404);
  if (url === '/v1/tasks') {
    if (method === 'GET') return { operation: 'list' };
    if (method === 'POST') return { operation: 'create' };
  }
  const match = /^\/v1\/tasks\/([a-zA-Z0-9_-]+)(?:\/(approve|cancel|workers|audit|delivery))?$/.exec(url);
  if (!match || !id(match[1])) fail('not-found', 404);
  const operation = match[2] ?? 'get';
  if ((['approve', 'cancel'].includes(operation) ? 'POST' : 'GET') !== method) fail('method-not-allowed', 405);
  return { operation, taskId: match[1] };
}
async function readBody(request, limit) {
  let size = 0; const chunks = [];
  for await (const chunk of request) { size += chunk.length; if (size > limit) fail('request-too-large', 413); chunks.push(chunk); }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { fail('invalid-json', 400); }
}
function response(res, status, body) {
  const raw = JSON.stringify(body); res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'Content-Length': Buffer.byteLength(raw) }); res.end(raw);
}
