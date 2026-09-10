// TanStack Query 客户端的服务端缓存装配：断线、401、隐藏页、轮询启停、请求去重与单在途（TanStack 的默认 dedupe）。
// 语义规则：mutation 零自动重试；401 停轮询并清除 token 使全部立即失败；同 key 至多一个在途请求；卸载 abort。
import {MutationCache, QueryCache, QueryClient, type QueryClientConfig} from '@tanstack/react-query';
import {ApiError} from '../transport/types';
import {clearToken} from '../transport/client';

export const LIST_FRESH_MS = 5000;
export const DETAIL_FRESH_MS = 2000;
export const HIDDEN_PAUSE_MS = 30000;

export type PollMode = 'detail' | 'list' | 'hidden' | 'none';

export interface PollingSpec {
  intervalMs: number;
  stopWhenHidden?: boolean;
}

export function intervalFor(mode: PollMode): number | null {
  if (mode === 'detail') return DETAIL_FRESH_MS;
  if (mode === 'list') return LIST_FRESH_MS;
  if (mode === 'hidden') return HIDDEN_PAUSE_MS;
  return null;
}

type UnauthorizedListener = () => void;

const unauthorizedListeners = new Set<UnauthorizedListener>();

/** 连接层订阅「凭据已被服务端拒绝」；返回解除函数。监听器异常不阻断其余通知。 */
export function onUnauthorized(listener: UnauthorizedListener): () => void {
  unauthorizedListeners.add(listener);
  return () => {
    unauthorizedListeners.delete(listener);
  };
}

export function signalUnauthorized(): void {
  clearToken();
  // UI-07：401 不只清 token——连接状态、缓存与收口的 UI 由监听者同步（停轮询、禁写、要求重连）。
  for (const listener of [...unauthorizedListeners]) {
    try {
      listener();
    } catch {
      // 单个监听器失败不影响其余通知；不作进一步处理（连接层保证自身幂等）。
    }
  }
}

export function createQueryClientWorker(config: Partial<QueryClientConfig> = {}): QueryClient {
  return new QueryClient({
    ...config,
    queryCache: config.queryCache ?? new QueryCache({
      onError: error => {
        if (error instanceof ApiError && error.isUnauthorized) signalUnauthorized();
      },
    }),
    mutationCache: config.mutationCache ?? new MutationCache({
      onError: error => {
        if (error instanceof ApiError && error.isUnauthorized) signalUnauthorized();
      },
    }),
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => {
          if (error instanceof ApiError && error.isUnauthorized) return false;
          return failureCount < 3;
        },
        retryDelay: attempt => Math.min(1000 * 2 ** attempt, 30000),
        refetchOnWindowFocus: false,
        refetchOnReconnect: false,
        structuralSharing: true,
      },
      mutations: {retry: 0, retryDelay: 0},
    },
  });
}
