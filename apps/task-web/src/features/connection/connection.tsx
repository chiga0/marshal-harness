// 连接上下文：token 仅在内存，断开清除全部缓存；不在 URL/Web Storage/构建产物保存。
// 每个视图拿到的 transport 由 createTransport 每次新建轻实例（fetch 原样），token 由模块单例读；连接失败统一区分 401/不可达/就绪失败。
import {createContext, useCallback, useContext, useMemo, useState} from 'react';
import type {ReactNode} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import type {QueryClient} from '@tanstack/react-query';
import {ApiError} from '../../lib/transport/types';
import type {Transport} from '../../lib/transport/types';
import {clearToken, createTransport, installToken} from '../../lib/transport/client';

export type ConnectState = 'idle' | 'connecting' | 'ready' | 'unauthorized' | 'unreachable' | 'api-down';

export interface ConnectionValue {
  connected: boolean;
  state: ConnectState;
  errorMessage: string | null;
  connect: (token: string) => Promise<void>;
  disconnect: () => void;
  transport: Transport | null;
}

const ConnectionContext = createContext<ConnectionValue | null>(null);

/** QueryClientProvider 外的测试宿主（如连接页单测）不持有 QueryClient：容忍缺失，生产树必在。 */
function useOptionalQueryClient(): QueryClient | null {
  try {
    return useQueryClient();
  } catch {
    return null;
  }
}

export function ConnectionProvider({children}: {children: ReactNode}) {
  const [state, setState] = useState<ConnectState>('idle');
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [stage, setStage] = useState(0);
  const queryClient = useOptionalQueryClient();

  const connect = useCallback(async (token: string) => {
    setState('connecting');
    setErrorMessage(null);
    try {
      installToken(token.trim());
      const transport = createTransport({token: token.trim()});
      await transport.listTasks({limit: 1});
      setState('ready');
      setStage(value => value + 1);
    } catch (error) {
      installToken(null);
      if (error instanceof ApiError) {
        if (error.isUnauthorized) setState('unauthorized');
        else setState('api-down');
      } else {
        setState('unreachable');
      }
      setErrorMessage(error instanceof Error ? error.message : '无法连接');
      throw error;
    }
  }, []);

  const disconnect = useCallback(() => {
    clearToken();
    queryClient?.clear(); // E27：断开清理缓存，不容许串服务数据经 stale 缓存先渲染
    setState('idle');
    setErrorMessage(null);
    setStage(value => value + 1);
  }, [queryClient]);

  const value = useMemo<ConnectionValue>(() => {
    const connected = state === 'ready';
    return {
      connected,
      state,
      errorMessage,
      connect,
      disconnect,
      transport: connected ? createTransport({token: '__internal__'}) : null,
    };
  }, [state, errorMessage, connect, disconnect, stage]);
  return <ConnectionContext.Provider value={value}>{children}</ConnectionContext.Provider>;
}

export function useConnection(): ConnectionValue {
  const value = useContext(ConnectionContext);
  if (!value) throw new Error('useConnection 必须在 ConnectionProvider 内使用');
  return value;
}
