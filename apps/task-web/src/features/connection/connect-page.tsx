// 连接面板：仅本机既有 token 手动输入、内存保存；状态显式区分 不可达 / 401 / 就绪响应异常；
// 不猜测启动失败原因，给出检查本机原始启动输出的指引；token 失败立即清除内存。
import {useState} from 'react';
import type {FormEvent} from 'react';
import {ApiError} from '../../lib/transport/types';
import {Alert} from '../../components/ui/alert';
import {Button} from '../../components/ui/button';
import {Input} from '../../components/ui/input';
import {Label} from '../../components/ui/label';
import {useConnection} from './connection';

interface ConnectFailure {
  title: string;
  detail: string;
}

function describeConnectFailure(error: unknown): ConnectFailure {
  if (error instanceof ApiError && error.isUnauthorized) {
    return {
      title: '凭据未通过验收（401）',
      detail: '服务端拒绝了该 Bearer token。请从本机既有连接信息重新取 token 再试；本页不会记录或持久化输入内容。'
        + (error.requestId ? ' requestId：' + error.requestId : ''),
    };
  }
  if (error instanceof ApiError) {
    return {
      title: '服务已响应但未就绪（就绪探测返回 ' + error.status + '）',
      detail: '错误码 ' + error.code + '。请核对服务端的原始启动输出与就绪状态后重试。'
        + (error.requestId ? ' requestId：' + error.requestId : ''),
    };
  }
  return {
    title: '无法连接本机服务（网络层不可达）',
    detail: '请求没有到达服务。请确认服务已在本机启动并监听其绑定端口；具体原因以服务端原始启动输出为准，本页不做猜测。',
  };
}

const LOCAL_GUIDE = [
  '确认服务已在本机启动，并检查其原始启动输出（端口、就绪/未就绪原因）。',
  'token 从本机既有连接信息复制；界面不代为生成或找回。',
  '连接只指向当前绑定的 origin，不提供远程地址。',
];

export function ConnectPage() {
  const {connect, state, errorMessage} = useConnection();
  const [token, setToken] = useState('');
  const [failure, setFailure] = useState<ConnectFailure | null>(null);

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setFailure(null);
    if (!token.trim()) {
      setFailure({title: '缺少 token', detail: '请输入本机既有 Bearer token。'});
      return;
    }
    try {
      await connect(token);
    } catch (error) {
      setFailure(describeConnectFailure(error));
    }
  };

  return (
    <main className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center px-6 py-12" aria-label="连接代理">
      <div className="rounded-lg border border-border bg-surface p-6">
        <h1 className="text-xl font-semibold leading-7">连接 Marshal 服务</h1>
        <p className="mt-2 text-sm leading-[22px] text-text-secondary">
          仅使用当前绑定端口的 origin；token 只保存在本页面内存，不写入 URL/存储。如上次操作中断，请先核对任务与回执，不会自动重复请求。
        </p>

        <form onSubmit={onSubmit} className="mt-5 space-y-4" noValidate>
          <div>
            <Label htmlFor="connect-token">Bearer token</Label>
            <Input
              id="connect-token"
              value={token}
              onChange={event => setToken(event.target.value)}
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder="从本机既有连接信息复制"
              aria-invalid={failure !== null}
              aria-describedby={failure ? 'connect-error' : undefined}
            />
          </div>
          {failure ? (
            <Alert id="connect-error" variant="danger" title={failure.title}>
              {failure.detail}
            </Alert>
          ) : null}
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs leading-[18px] text-text-secondary" role="status">
              {state === 'connecting' ? '正在连接并就绪探测…' : '连接成功后会自动进入任务列表。'}
            </p>
            <Button type="submit" loading={state === 'connecting'} disabled={state === 'connecting'}>
              连接
            </Button>
          </div>
        </form>

        {errorMessage && !failure ? <p className="mt-3 text-xs leading-[18px] text-text-secondary">{errorMessage}</p> : null}

        <div className="mt-5 border-t border-border pt-4">
          <h2 className="text-sm font-medium leading-5">连不上时的本机指引</h2>
          <ul className="mt-2 list-disc space-y-1 pl-5 text-xs leading-[18px] text-text-secondary">
            {LOCAL_GUIDE.map(item => <li key={item}>{item}</li>)}
          </ul>
        </div>
      </div>
    </main>
  );
}
