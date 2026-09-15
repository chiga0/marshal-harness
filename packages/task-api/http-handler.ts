import {randomUUID, createHash} from 'node:crypto';
import {operations, validate, validAnswerResponse, validQuestionItems, validRepairResponse, validAuditResponse,
  validLeaderView, validLeaderReplyResponse, MAX_LEADER_VIEW_BYTES, TaskApiError, errorPayload} from './contract.mjs';
import {protect, mutationHeaders, readJson, sendJson, closeIncompleteRequest} from './http-boundary.mjs';

const MAX_RESPONSE = 8 * 1024 * 1024;
function route(method, url) {
  if (typeof url !== 'string' || url.length > 4096 || !url.startsWith('/') || /[\\#\x00-\x20]/.test(url)) throw new TaskApiError('not_found');
  const [pathname, search, extra] = url.split('?');
  if (extra !== undefined || pathname.includes('%') || pathname.includes('//')) throw new TaskApiError('not_found');
  let match;
  for (const entry of operations) {
    const names = [...entry.path.matchAll(/{([^}]+)}/g)].map(v => v[1]);
    const pattern = '^' + entry.path.replace(/{[^}]+}/g, '([A-Za-z0-9][A-Za-z0-9_-]{0,127})') + '$';
    const values = new RegExp(pattern).exec(pathname); if (!values) continue;
    if (entry.method !== method) { match = true; continue; }
    const request = {operation: entry.operation};
    names.forEach((name, i) => { request[name] = values[i + 1]; });
    if (search !== undefined && !entry.paged) throw new TaskApiError('invalid_request');
    if (entry.paged) {
      const params = new URLSearchParams(search ?? '');
      if ([...params.keys()].some(k => !['limit', 'cursor'].includes(k)) || params.getAll('limit').length > 1 || params.getAll('cursor').length > 1 ||
          params.has('limit') && !/^(?:[1-9][0-9]?|100)$/.test(params.get('limit')) ||
          params.has('cursor') && !validate(params.get('cursor'), 'Id')) throw new TaskApiError('invalid_request');
      request.page = {limit: Number(params.get('limit') ?? 50), cursor: params.get('cursor')};
    }
    return {entry, request};
  }
  throw new TaskApiError(match ? 'method_not_allowed' : 'not_found');
}
function boundResponse(entry, request, value) {
  if (!validate(value, entry.response)) throw new TaskApiError('invalid_application_response');
  const taskId = value.taskId ?? (['task.get'].includes(entry.operation) ? value.id : undefined);
  if (request.taskId && taskId !== request.taskId || request.workerId && entry.operation === 'worker.get' && value.id !== request.workerId ||
      entry.operation === 'worker.cancel' && value.workerId !== request.workerId ||
      request.operationId && value.id !== request.operationId || request.artifactId && value.id !== request.artifactId ||
      request.page && value.items.length > request.page.limit) throw new TaskApiError('invalid_application_response');
  if (entry.response === 'Operation' && entry.operation !== 'operation.get' && value.kind !== entry.operation) throw new TaskApiError('invalid_application_response');
  if (entry.operation === 'task.answer' && !validAnswerResponse(request, value) ||
      entry.operation === 'task.repair' && !validRepairResponse(request, value) ||
      entry.operation === 'task.audit' && !validAuditResponse(value, request.taskId) ||
      entry.operation === 'task.leader' && !validLeaderView(value, request.taskId) ||
      entry.operation === 'task.leader.reply' && !validLeaderReplyResponse(request, value) ||
      entry.operation === 'task.questions' && !validQuestionItems(value, request.taskId))
    throw new TaskApiError('invalid_application_response');
}
/**
 * application(request, {principal, requestId, signal}) is the only application
 * dependency. It owns authorization, original-key replay, CAS, budgets and
 * durable effects. A signal ends HTTP observation; it does NOT cancel a Task.
 * No socket, process, Agent, database or Task state is created in this module.
 */
export function createTaskApiHandler({application, token, expectedHost, requestTimeoutMs = 10000}) {
  if (typeof application !== 'function' || typeof token !== 'string' || token.length < 32 || /\s/.test(token) ||
      !/^127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(expectedHost ?? '') || Number(expectedHost.split(':')[1]) > 65535 ||
      !Number.isSafeInteger(requestTimeoutMs) || requestTimeoutMs < 10 || requestTimeoutMs > 30000) throw new TypeError('invalid-task-api-composition');
  return async (req, res) => {
    const requestId = randomUUID(); const controller = new AbortController();
    let timer, abort, readingBody = false;
    const deadline = new Promise((_, reject) => {
      abort = () => { controller.abort(); reject(new TaskApiError('request_timeout')); };
      timer = setTimeout(abort, requestTimeoutMs);
      req.once('aborted', abort);
    });
    try {
      // Authenticate before routing to avoid leaking route/object existence.
      const publicHealth = req.method === 'GET' && ['/health', '/ready'].includes(req.url);
      protect(req, token, expectedHost, !publicHealth);
      const {entry, request} = route(req.method, req.url);
      const invoke = async () => {
        if (entry.request) {
          request.key = mutationHeaders(req);
          readingBody = true;
          request.body = await readJson(req, entry.operation === 'input.create' ? 393216 : 262144, controller.signal);
          readingBody = false;
          if (!validate(request.body, entry.request)) throw new TaskApiError('invalid_request');
        }
        if (controller.signal.aborted) throw new TaskApiError('request_timeout');
        return application(request, {principal: 'local-operator', requestId, signal: controller.signal});
      };
      const value = await Promise.race([invoke(), deadline]);
      if (entry.operation === 'artifact.content') {
        if (!value || !validate(value.artifact, 'Artifact') || !(value.content instanceof Uint8Array) ||
            value.artifact.id !== request.artifactId || value.artifact.status !== 'ready' || value.content.byteLength > MAX_RESPONSE ||
            value.artifact.bytes !== value.content.byteLength) throw new TaskApiError('invalid_application_response');
        const bytes = Buffer.from(value.content), digest = createHash('sha256').update(bytes).digest();
        if (value.artifact.digest !== 'sha256:' + digest.toString('hex')) throw new TaskApiError('invalid_application_response');
        res.writeHead(200, {'Content-Type': 'application/octet-stream', 'Content-Length': bytes.length,
          'Content-Disposition': `attachment; filename="${request.artifactId}"`, 'Cache-Control': 'no-store',
          'X-Content-Type-Options': 'nosniff', 'X-Request-Id': requestId, 'Content-Digest': `sha-256=:${digest.toString('base64')}:`});
        res.end(bytes);
      } else {
        const raw = JSON.stringify(value);
        const limit = entry.operation === 'task.leader' ? MAX_LEADER_VIEW_BYTES : MAX_RESPONSE;
        if (typeof raw !== 'string' || Buffer.byteLength(raw) > limit || raw.includes(token)) throw new TaskApiError('invalid_application_response');
        // Serialize once into plain data before checking: getters/toJSON cannot
        // supply one checked value and a different value to the response writer.
        const safe = JSON.parse(raw); boundResponse(entry, request, safe);
        sendJson(res, entry.status, safe, requestId);
      }
    } catch (error) {
      const failure = errorPayload(error, requestId);
      if (readingBody && !req.readableEnded) closeIncompleteRequest(req, res);
      if (!res.destroyed && !res.headersSent) sendJson(res, failure.status, failure.body, requestId);
    } finally {
      clearTimeout(timer); req.off('aborted', abort);
    }
  };
}
