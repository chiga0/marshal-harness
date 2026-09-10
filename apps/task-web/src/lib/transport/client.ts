// Token 只存放在内存（service/transport 模块层单例）——URL、Web Storage、IndexedDB、日志、构建产物均不存放。
// 断开/刷新清除；连接只指向当前 origin，不接收任意远程 baseURL。
import {ApiError} from './types';
import type {
  Transport, TasksResponse, TaskDetail, WorkerRecord, PlanRecord, Revision, Sha256,
  LeaderRecord, PublicationRecord, WorkerId, TaskId,
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
  if (rest.signal !== undefined) options.signal = rest.signal;
  if (jsonBody !== undefined) options.body = JSON.stringify(jsonBody);
  const response = await config.fetchLike(config.baseURL + path, options);
  if (!response.ok) throw await toError(response, 'api_error');
  return (await response.json()) as T;
}

export function createTransport(config: TransportConfig): Transport {
  const cfg: Required<TransportConfig> = {
    token: config.token,
    baseURL: config.baseURL ?? BASE_DEFAULT,
    fetchLike: config.fetchLike ?? fetch,
  };
  const json = <T>(path: string, init?: JsonOptions<unknown>) => requestJson<T>(cfg, path, init);
  return {
    listTasks: options => {
      const params = new URLSearchParams();
      if (options.limit !== undefined) params.set('limit', String(options.limit));
      if (options.cursor) params.set('cursor', options.cursor);
      return json<TasksResponse>(`/v1/tasks${params.size ? '?' + params.toString() : ''}`);
    },
    getTask: taskId => json<TaskDetail>(`/v1/tasks/${encodeURIComponent(taskId)}`),
    getWorkers: taskId => json<{workers: WorkerRecord[]}>(`/v1/tasks/${encodeURIComponent(taskId)}/workers`),
    getPlan: taskId => json<PlanRecord>(`/v1/tasks/${encodeURIComponent(taskId)}/plan`),
    freezePlan: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/plan:freeze`, {method: 'POST', body}),
    approveTask: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}:approve`, {method: 'POST', body}),
    answerTask: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}:answer`, {method: 'POST', body}),
    cancelTask: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}:cancel`, {method: 'POST', body}),
    pauseTask: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}:pause`, {method: 'POST', body}),
    resumeTask: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}:resume`, {method: 'POST', body}),
    cancelWorker: (taskId, workerId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/workers/${encodeURIComponent(workerId)}:cancel`, {method: 'POST', body}),
    getLeader: taskId => json<LeaderRecord>(`/v1/tasks/${encodeURIComponent(taskId)}/leader`),
    leaderReply: (taskId, body) => json(`/v1/tasks/${encodeURIComponent(taskId)}/leader:reply`, {method: 'POST', body}),
    getPublications: taskId => json<{publications: PublicationRecord[]}>(`/v1/tasks/${encodeURIComponent(taskId)}/publications`),
    getArtifactBearer: async ref => {
      const token = currentToken;
      if (!token) throw new ApiError(401, 'token_missing', '未连接服务', null);
      const response = await cfg.fetchLike(cfg.baseURL + ref, {headers: {Authorization: 'Bearer ' + token}});
      if (!response.ok) throw await toError(response, 'api_error');
      return response.blob();
    },
  };
}
