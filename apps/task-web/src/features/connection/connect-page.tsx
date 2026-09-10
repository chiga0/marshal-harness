// 连接面板：仅本机既有 token 手动输入、区分 401/服务不可达/未就绪；tok写入失败立即清除内存。
import {useState} from 'react';
import type {FormEvent} from 'react';
import {ApiError} from '../../lib/transport/types';
import {Button} from '../../components/ui/button';
import {useConnection} from './connection';

export function ConnectPage() {
  const {connect, state, errorMessage} = useConnection();
  const [token, setToken] = useState('');
  const [localError, setLocalError] = useState<string | null>(null);

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setLocalError(null);
    if (!token.trim()) {
      setLocalError('请输入本机既有 Bearer token。');
      return;
    }
    try {
      await connect(token);
    } catch (error) {
      if (error instanceof ApiError && error.isUnauthorized) {
        setLocalError('凭据未通过验收（401）。请从本机既有连接信息重新取 token 再试。');
      } else if (error instanceof ApiError) {
        setLocalError('服务返回未就绪响应（' + error.status + '：' + error.code + '）。请核对服务端启动输出。');
      } else {
        setLocalError('无法连接服务。请确认服务已在 127.0.0.1 监听，并检查其原始启动输出；不要仅靠本页猜测失败原因。');
      }
    }
  };

  return (
    <main className="mx-auto flex min-h-screen max-w-md flex-col justify-center px-6 py-12" aria-label="连接代理">
      <div className="rounded-lg border border-border bg-surface p-6">
        <h1 className="text-xl font-semibold leading-7">连接 Marshal 服务</h1>
        <p className="mt-2 text-sm leading-[22px] text-text-secondary">
          仅使用当前绑定端口的 origin；token 只保存在本页面内存，不写入 URL/存储。如上次操作中断，请先核对任务与回执，不会自动重复请求。
        </p>
        <form onSubmit={onSubmit} className="mt-5 space-y-4" noValidate>
          <div>
            <label htmlFor="connect-token" className="mb-1 block text-sm font-medium text-text-primary">Bearer token</label>
            <input
              id="connect-token"
              value={token}
              onChange={event => setToken(event.target.value)}
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder="从本机既有连接信息复制"
              className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm leading-[22px] outline-none focus-visible:ring-2 focus-visible:ring-accent"
              aria-invalid={localError !== null}
              aria-describedby={localError ? 'connect-error' : undefined}
            />
          </div>
          {localError ? <p id="connect-error" role="alert" className="text-sm leading-[22px] text-danger">{localError}</p> : null}
          <div className="flex justify-end">
            <Button type="submit" loading={state === 'connecting'} disabled={state === 'connecting'}>
              连接
            </Button>
          </div>
        </form>
        {errorMessage ? <p className="mt-3 text-xs leading-[18px] text-text-secondary">{errorMessage}</p> : null}
      </div>
    </main>
  );
}
