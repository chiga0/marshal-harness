// Token 只存放在内存（service/transport 模块层单例）——URL、Web Storage、IndexedDB、日志、构建产物均不存放。
// 断开/刷新清除；连接只指向当前 origin，不接收任意远程 baseURL。
import {ApiError, parseGraph} from './types';
import type {
  Transport, TasksResponse, TaskRecord, WorkersResponse, PlanRecord,
  LeaderRecord, TaskAuditRecord, Events, QuestionsResponse, ArtifactRecord, ControlBody,
} from './types';

export interface TransportConfig {
  token: string;
  baseURL?: string;
  fetchLike?: typeof fetch;
}

const BASE_DEFAULT: string = '';

// 当前内存单一保存处；不 export 读取函数给任意页面以外的消费者，页面通过 AuthContext 持引用。
let currentToken: string | null = null;

export function installToken(token: string | null): void {
  currentToken = token && token.trim() !== '' ? token : null;
}

export function hasToken(): boolean {
  return currentToken !== null;
}

export function clearToken(): void {
  currentToken = null;
}

export interface ErrorLikeBody {
  code?: string;
  message?: string;
  requestId?: string;
}

async function toError(response: Response, fallbackCode: string): Promise<ApiError> {
  let body: ErrorLikeBody | null = null;
  try {
    const text = await response.text();
    if (text) {
      body = JSON.parse(text) as ErrorLikeBody;
    }
  } catch {
    body = null;
  }
  return new ApiError(
    response.status,
    body?.code ?? fallbackCode,
    body?.message ?? response.statusText ?? fallbackCode,
    body?.requestId ?? null,
  );
}

interface JsonOptions<Body = unknown> extends Omit<RequestInit, 'body'> {
  body?: Body;
  idempotencyKey?: string;
}

/** 读请求的默认有界时限：悬挂的轮询/详情读必须自行终结，不能无限挂起。 */
export const READ_DEADLINE_MS = 15000;
/** 写请求的通知时限：到期只代表「未收到回执」（结果未知），不代表服务端已取消。 */
export const WRITE_DEADLINE_MS = 60000;

// 调用方取消 signal 与 deadline 合并；宿主缺 AbortSignal.timeout/any 时如实降级为调用方原 signal。
function combineSignal(caller: AbortSignal | undefined, deadlineMs: number): AbortSignal | undefined {
  if (typeof AbortSignal === 'undefined' || typeof AbortSignal.timeout !== 'function') return caller;
  const deadline = AbortSignal.timeout(deadlineMs);
  if (caller === undefined) return deadline;
  if (typeof AbortSignal.any !== 'function') return caller;
  return AbortSignal.any([caller, deadline]);
}

async function requestJson<T>(config: Required<TransportConfig>, path: string, init?: JsonOptions<unknown>): Promise<T> {
  const token = currentToken;
  if (!token) throw new ApiError(401, 'token_missing', '未连接服务', null);
  const {body: jsonBody, idempotencyKey, ...rest} = init ?? {};
  const options: RequestInit = {};
  if (rest.method !== undefined) options.method = rest.method;
  options.headers = {
    'Accept': 'application/json',
    'Authorization': 'Bearer ' + token,
    ...(jsonBody !== undefined ? {'Content-Type': 'application/json'} : {}),
    ...(idempotencyKey ? {'Idempotency-Key': idempotencyKey} : {}),
    ...(rest.headers ?? {}),
  };
  const method = rest.method ?? 'GET';
  const callerSignal = rest.signal ?? undefined;
  const signal = combineSignal(callerSignal, method === 'GET' ? READ_DEADLINE_MS : WRITE_DEADLINE_MS);
  if (signal !== undefined) options.signal = signal;
  if (jsonBody !== undefined) options.body = JSON.stringify(jsonBody);
  const response = await config.fetchLike(config.baseURL + path, options);
  if (!response.ok) throw await toError(response, 'api_error');
  return (await response.json()) as T;
}

function withKey<Body extends {idempotencyKey: string}>(body: Body): {init: JsonOptions<unknown>} {
  const {idempotencyKey, ...rest} = body;
  return {init: {method: 'POST', idempotencyKey, body: rest}};
}

/** exactOptionalPropertyTypes：仅在确有 signal 时携带，避免显式 undefined 属性。 */
function readInit(signal: AbortSignal | undefined): JsonOptions<unknown> | undefined {
  return signal === undefined ? undefined : {signal};
}

export function createTransport(config: TransportConfig): Transport {
  const cfg: Required<TransportConfig> = {
    token: config.token,
    baseURL: config.baseURL ?? BASE_DEFAULT,
    // 必须以闭包包装再调用：原生 window.fetch 被存进对象后作为方法调用时 this 不再是 Window，
    // Safari/Chrome 会抛 TypeError（UI-03 真机「网络层不可达」的候选根因）。
    fetchLike: config.fetchLike ?? ((input, init) => fetch(input, init)),
  };
  const json = <T>(path: string, init?: JsonOptions<unknown>) => requestJson<T>(cfg, path, init);
  const control = (path: string, body: ControlBody) => json(path, withKey(body).init);
  return {
    createTask: body => json('/v1/tasks', withKey(body).init),
    createInput: body => json<ArtifactRecord>('/v1/inputs', withKey(body).init),
    listTasks: options => {
      const params = new URLSearchParams();
      if (options.limit !== undefined) params.set('limit', String(options.limit));
      if (options.cursor) params.set('cursor', options.cursor);
      return json<TasksResponse>(`/v1/tasks${params.size ? '?' + params.toString() : ''}`, readInit(options.signal));
    },
    getTask: (taskId, options = {}) => json<TaskRecord>(`/v1/tasks/${encodeURIComponent(taskId)}`, readInit(options.signal)),
    getWorkers: (taskId, options = {}) => {
      const params = new URLSearchParams();
      if (options.limit !== undefined) params.set('limit', String(options.limit));
      if (options.cursor) params.set('cursor', options.cursor);
      return json<WorkersResponse>(`/v1/tasks/${encodeURIComponent(taskId)}/workers${params.size ? '?' + params.toString() : ''}`, readInit(options.signal));
    },
    getPlan: (taskId, options = {}) => json<PlanRecord>(`/v1/tasks/${encodeURIComponent(taskId)}/plan`, readInit(options.signal)),
    getGraph: async (taskId, options = {}) => parseGraph(await json<unknown>(`/v1/tasks/${encodeURIComponent(taskId)}/graph`, readInit(options.signal)), taskId),
    approvePlan: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/plan/approve`, withKey(body).init),
    getQuestions: (taskId, options = {}) => {
      const params = new URLSearchParams();
      if (options.limit !== undefined) params.set('limit', String(options.limit));
      if (options.cursor) params.set('cursor', options.cursor);
      return json<QuestionsResponse>(`/v1/tasks/${encodeURIComponent(taskId)}/questions${params.size ? '?' + params.toString() : ''}`, readInit(options.signal));
    },
    answerTask: (taskId, questionId, body) => {
      const {branch: _branch, ...rest} = body;
      return json(`/v1/tasks/${encodeURIComponent(taskId)}/questions/${encodeURIComponent(questionId)}/answers`, withKey(rest).init);
    },
    cancelTask: (taskId, body) => control(`/v1/tasks/${encodeURIComponent(taskId)}/cancel`, body),
    pauseTask: (taskId, body) => control(`/v1/tasks/${encodeURIComponent(taskId)}/pause`, body),
    resumeTask: (taskId, body) => control(`/v1/tasks/${encodeURIComponent(taskId)}/resume`, body),
    cancelWorker: (workerId, body) => control(`/v1/workers/${encodeURIComponent(workerId)}/cancel`, body),
    getLeader: (taskId, options = {}) => json<LeaderRecord>(`/v1/tasks/${encodeURIComponent(taskId)}/leader`, readInit(options.signal)),
    getAudit: (taskId, options = {}) => json<TaskAuditRecord>(`/v1/tasks/${encodeURIComponent(taskId)}/audit`, readInit(options.signal)),
    leaderReply: (taskId, requestId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/leader/requests/${encodeURIComponent(requestId)}/reply`, withKey(body).init),
    repair: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/repair`, withKey(body).init),
    getEvents: (taskId, options = {}) => {
      const params = new URLSearchParams();
      if (options.limit !== undefined) params.set('limit', String(options.limit));
      if (options.cursor) params.set('cursor', options.cursor);
      return json<Events>(`/v1/tasks/${encodeURIComponent(taskId)}/events${params.size ? '?' + params.toString() : ''}`, readInit(options.signal));
    },
    getArtifact: (artifactId, options = {}) => json<ArtifactRecord>(`/v1/artifacts/${encodeURIComponent(artifactId)}`, readInit(options.signal)),
    getArtifactContent: async (artifactId, options = {}) => {
      const token = currentToken;
      if (!token) throw new ApiError(401, 'token_missing', '未连接服务', null);
      // 大文件下载不设自动 deadline；调用方 signal（卸载/重建）照常贯通。
      const response = await cfg.fetchLike(
        cfg.baseURL + `/v1/artifacts/${encodeURIComponent(artifactId)}/content`,
        {
          headers: {Authorization: 'Bearer ' + token},
          ...(options.signal !== undefined ? {signal: options.signal} : {}),
        },
      );
      if (!response.ok) throw await toError(response, 'api_error');
      return response.blob();
    },
  };
}
