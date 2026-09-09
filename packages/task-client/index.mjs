import {createHash} from 'node:crypto';
import {operations, validate, validAnswerResponse, validQuestionItems, validRepairResponse, validAuditResponse, TaskApiError} from '../task-api/contract.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';

const MAX_BYTES = 8 * 1024 * 1024;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const fail = code => new TaskClientError(code);
function containsToken(value, token) {
  if (typeof value === 'string') return value.includes(token);
  return value !== null && typeof value === 'object' && Object.entries(value).some(([key, child]) => key.includes(token) || containsToken(child, token));
}
export class TaskClientError extends Error {
  constructor(code, {status = null, requestId = null, allowedActions = []} = {}) {
    super(code); this.name = 'TaskClientError'; this.code = code;
    this.status = status; this.requestId = requestId; this.allowedActions = allowedActions;
  }
}
function timeout(value) {
  if (!Number.isSafeInteger(value) || value < 10 || value > 30000) throw fail('client_invalid_timeout');
  return value;
}
function prepare(operation, options) {
  const entry = operations.find(item => item.operation === operation);
  if (!entry || !object(options) || Object.keys(options).some(key => !['path', 'query', 'body', 'idempotencyKey', 'signal', 'timeoutMs'].includes(key))) throw fail('client_invalid_request');
  const paths = options.path ?? {}, query = options.query ?? {};
  if (!object(paths) || !object(query)) throw fail('client_invalid_request');
  const names = [...entry.path.matchAll(/{([^}]+)}/g)].map(match => match[1]);
  if (Object.keys(paths).length !== names.length || names.some(name => !validate(paths[name], 'Id'))) throw fail('client_invalid_request');
  let route = entry.path.replace(/{([^}]+)}/g, (_match, name) => paths[name]);
  if (Object.keys(query).length) {
    if (!entry.paged || Object.keys(query).some(key => !['limit', 'cursor'].includes(key)) ||
      query.limit !== undefined && (!Number.isSafeInteger(query.limit) || query.limit < 1 || query.limit > 100) ||
      query.cursor !== undefined && !validate(query.cursor, 'Id')) throw fail('client_invalid_request');
    const params = new URLSearchParams();
    if (query.limit !== undefined) params.set('limit', query.limit);
    if (query.cursor !== undefined) params.set('cursor', query.cursor);
    if (params.size) route += '?' + params;
  }
  let body;
  if (entry.request) {
    if (!validate(options.idempotencyKey, 'Id')) throw fail('client_invalid_idempotency_key');
    try { body = JSON.stringify(options.body); } catch { throw fail('client_invalid_request'); }
    if (typeof body !== 'string' || Buffer.byteLength(body) > (operation === 'input.create' ? 393216 : 262144)) throw fail('client_invalid_request');
    let plain; try { plain = parseJson(Buffer.from(body)); } catch { throw fail('client_invalid_request'); }
    if (!validate(plain, entry.request)) throw fail('client_invalid_request');
  } else if (options.body !== undefined || options.idempotencyKey !== undefined) throw fail('client_invalid_request');
  return {entry, route, body, paths: structuredClone(paths), query: structuredClone(query), key: options.idempotencyKey};
}
function boundIdentity(entry, options, value) {
  if (!validate(value, entry.response)) throw fail('client_invalid_response');
  const taskId = value.taskId ?? (entry.operation === 'task.get' ? value.id : undefined);
  if (options.paths.taskId && taskId !== options.paths.taskId ||
    options.paths.workerId && entry.operation === 'worker.get' && value.id !== options.paths.workerId ||
    entry.operation === 'worker.cancel' && value.workerId !== options.paths.workerId ||
    options.paths.operationId && value.id !== options.paths.operationId || options.paths.artifactId && value.id !== options.paths.artifactId ||
    entry.paged && value.items.length > (options.query.limit ?? 50) ||
    entry.response === 'Operation' && entry.operation !== 'operation.get' && value.kind !== entry.operation) throw fail('client_invalid_response');
  if (entry.operation === 'task.answer' && !validAnswerResponse({...options.paths, body: JSON.parse(options.body)}, value) ||
      entry.operation === 'task.repair' && !validRepairResponse({...options.paths, body: JSON.parse(options.body)}, value) ||
      entry.operation === 'task.audit' && !validAuditResponse(value, options.paths.taskId) ||
      entry.operation === 'task.questions' && !validQuestionItems(value, options.paths.taskId)) throw fail('client_invalid_response');
}
async function readBounded(response, signal) {
  const length = response.headers.get('content-length');
  if (length !== null && (!/^(0|[1-9][0-9]*)$/.test(length) || Number(length) > MAX_BYTES)) throw fail('client_response_limit');
  if (!response.body?.getReader) throw fail('client_invalid_response');
  const reader = response.body.getReader(), chunks = []; let count = 0;
  const cancel = () => { void reader.cancel().catch(() => {}); };
  signal.addEventListener('abort', cancel, {once: true});
  try {
    for (;;) {
      if (signal.aborted) throw fail('client_aborted');
      const {done, value} = await reader.read(); if (done) break;
      if (!(value instanceof Uint8Array)) throw fail('client_invalid_response');
      count += value.length; if (count > MAX_BYTES) throw fail('client_response_limit');
      chunks.push(Buffer.from(value));
    }
    if (length !== null && Number(length) !== count) throw fail('client_invalid_response');
    return Buffer.concat(chunks, count);
  } finally { signal.removeEventListener('abort', cancel); cancel(); reader.releaseLock(); }
}

/** Explicit caller-owned keys/revisions; each request performs exactly one HTTP call. */
export class TaskClient {
  #base; #host; #token; #fetch; #timeout;
  constructor({baseURL, token, fetch = globalThis.fetch, timeoutMs = 10000} = {}) {
    if (typeof baseURL !== 'string' || !/^http:\/\/127\.0\.0\.1:([1-9][0-9]{0,4})\/?$/.test(baseURL) ||
      Number(/^http:\/\/127\.0\.0\.1:(\d+)/.exec(baseURL)[1]) > 65535 ||
      typeof token !== 'string' || token.length < 32 || /\s/.test(token) || typeof fetch !== 'function') throw fail('client_invalid_configuration');
    this.#base = baseURL.replace(/\/$/, ''); this.#host = this.#base.slice('http://'.length);
    this.#token = token; this.#fetch = fetch; this.#timeout = timeout(timeoutMs);
  }
  async #once(prepared, signal) {
    const {entry, route, body, key} = prepared, url = this.#base + route;
    const headers = {Accept: entry.operation === 'artifact.content' ? 'application/octet-stream' : 'application/json',
      Host: this.#host, 'Accept-Encoding': 'identity'};
    if (entry.authenticated) headers.Authorization = 'Bearer ' + this.#token;
    if (body !== undefined) { headers['Content-Type'] = 'application/json'; headers['Idempotency-Key'] = key; }
    const response = await this.#fetch(url, {method: entry.method, headers, body, signal, redirect: 'manual', credentials: 'omit', cache: 'no-store'});
    try {
    if (!response || response.redirected || response.status >= 300 && response.status < 400 || response.url && response.url !== new URL(url).href) throw fail('client_redirect_rejected');
    const encoding = response.headers.get('content-encoding');
    if (encoding && encoding !== 'identity') throw fail('client_invalid_response');
    const bytes = await readBounded(response, signal);
    if (response.status === entry.status && entry.operation === 'artifact.content') {
      if (response.headers.get('content-type') !== 'application/octet-stream') throw fail('client_invalid_response');
      return {bytes, contentDigest: response.headers.get('content-digest')};
    }
    if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(response.headers.get('content-type') ?? '') || bytes.includes(Buffer.from(this.#token))) throw fail('client_invalid_response');
    let value; try { value = parseJson(bytes); } catch { throw fail('client_invalid_response'); }
    // JSON escapes can conceal the credential on the wire. Check decoded
    // strings before copying requestId or returning any success projection.
    if (containsToken(value, this.#token)) throw fail('client_invalid_response');
    if (response.status !== entry.status) {
      if (!validate(value, 'Error')) throw fail('client_invalid_response');
      let expected; try { expected = new TaskApiError(value.code); } catch { throw fail('client_invalid_response'); }
      if (expected.status !== response.status) throw fail('client_invalid_response');
      // Never copy the peer's free-form message, body or exception into errors.
      throw new TaskClientError(value.code, {status: response.status, requestId: value.requestId, allowedActions: [...value.allowedActions]});
    }
    boundIdentity(entry, prepared, value); return value;
    } finally {
      if (response?.body && !response.body.locked) void response.body.cancel().catch(() => {});
    }
  }
  async request(operation, options = {}) {
    let prepared, wait;
    try { prepared = prepare(operation, options); wait = timeout(options.timeoutMs ?? this.#timeout); }
    catch (failure) { if (failure instanceof TaskClientError) throw failure; throw fail('client_invalid_request'); }
    if (options.signal !== undefined && !(options.signal instanceof AbortSignal)) throw fail('client_invalid_request');
    const controller = new AbortController(); let timer, callerAbort, expired = false;
    const deadline = new Promise((_, reject) => {
      callerAbort = () => { controller.abort(); reject(fail('client_aborted')); };
      options.signal?.addEventListener('abort', callerAbort, {once: true});
      timer = setTimeout(() => { expired = true; controller.abort(); reject(fail('client_timeout')); }, wait);
      if (options.signal?.aborted) callerAbort();
    });
    try {
      const execute = async () => {
        if (controller.signal.aborted) throw fail('client_aborted');
        if (operation !== 'artifact.content') return this.#once(prepared, controller.signal);
        const metadata = await this.#once(prepare('artifact.get', {path: prepared.paths}), controller.signal);
        if (metadata.status !== 'ready') throw fail('client_artifact_not_ready');
        const {bytes, contentDigest} = await this.#once(prepared, controller.signal);
        const digest = createHash('sha256').update(bytes).digest();
        if (bytes.length !== metadata.bytes || metadata.digest !== 'sha256:' + digest.toString('hex') ||
          contentDigest !== 'sha-256=:' + digest.toString('base64') + ':') throw fail('client_artifact_integrity');
        return {artifact: metadata, content: bytes};
      };
      return await Promise.race([execute(), deadline]);
    } catch (failure) {
      if (expired) throw fail('client_timeout');
      if (options.signal?.aborted) throw fail('client_aborted');
      if (failure instanceof TaskClientError) throw failure;
      throw fail('client_transport_error');
    } finally { clearTimeout(timer); options.signal?.removeEventListener('abort', callerAbort); controller.abort(); }
  }
  createTask(body, idempotencyKey, options = {}) { return this.request('task.create', {...options, body, idempotencyKey}); }
  getTask(taskId, options = {}) { return this.request('task.get', {...options, path: {taskId}}); }
  cancelWorker(workerId, body, idempotencyKey, options = {}) {return this.request('worker.cancel', {...options, path: {workerId}, body, idempotencyKey});}
  approveTask(taskId, body, idempotencyKey, options = {}) { return this.request('task.approve', {...options, path: {taskId}, body, idempotencyKey}); }
  repairTask(taskId, body, idempotencyKey, options = {}) { return this.request('task.repair', {...options, path: {taskId}, body, idempotencyKey}); }
  getAudit(taskId, options = {}) { return this.request('task.audit', {...options, path: {taskId}}); }
  async downloadInputSnapshot(taskId, prompt, options = {}) {
    const snapshot = prompt?.observation?.snapshot;
    if (!validate(taskId, 'Id') || !validate(prompt, 'Prompt') || !snapshot || snapshot.taskId !== taskId ||
      snapshot.name !== prompt.workerId + '.input.txt' || snapshot.kind !== 'evidence' || snapshot.mediaType !== 'text/plain' ||
      prompt.observation.coverage !== 'policy-redacted') throw fail('client_invalid_request');
    const downloaded = await this.downloadArtifact(snapshot.id, options);
    if (['id', 'taskId', 'name', 'kind', 'status', 'mediaType', 'digest', 'bytes', 'createdAt'].some(key => downloaded.artifact[key] !== snapshot[key]) ||
      !downloaded.content.subarray(0, Buffer.byteLength(prompt.text)).equals(Buffer.from(prompt.text))) throw fail('client_artifact_integrity');
    return downloaded;
  }
  downloadArtifact(artifactId, options = {}) { return this.request('artifact.content', {...options, path: {artifactId}}); }
}
