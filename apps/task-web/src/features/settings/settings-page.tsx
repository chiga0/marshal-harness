// 设置：主题偏好（sessionStorage，仅非敏感偏好）、连接状态与管理、不持久化使用说明。
import {useTheme} from '../../lib/theme/useTheme';
import type {ThemePreference} from '../../lib/theme/useTheme';
import {useConnection} from '../connection/connection';
import type {ConnectState} from '../connection/connection';
import {Button} from '../../components/ui/button';
import {Card, CardHeader, CardTitle} from '../../components/ui/card';

const THEME_OPTIONS: {value: ThemePreference; label: string}[] = [
  {value: 'light', label: '浅色'},
  {value: 'dark', label: '深色'},
  {value: 'system', label: '跟随系统'},
];

const CONNECT_STATE_LABEL: Record<ConnectState, string> = {
  idle: '未连接',
  connecting: '正在连接…',
  ready: '已连接（token 仅存于内存）',
  unauthorized: '凭据未通过验收（401）',
  unreachable: '无法连接本机服务',
  'api-down': '服务已响应但未就绪',
};

const USAGE_NOTES = [
  'token 只保存在本页面内存：不写入 URL、LocalStorage/IndexedDB、日志或构建产物；断开或刷新即被清除，需要重新输入。',
  '主题偏好只写入 sessionStorage（仅当前会话，不含 token），其他偏好不落盘。',
  '如上次操作中断，请先回到任务列表核对任务与回执；本界面不能识别未决请求，也不会自动重放任何提交。',
  '任务列表的搜索与筛选只作用于已加载的任务项，不代表服务端全局结果。',
  '所有数据来自当前 origin 的本机服务；界面不接受远程服务地址。',
];

export function SettingsPage() {
  const {preference, resolved, set} = useTheme();
  const {state, disconnect} = useConnection();

  return (
    <section aria-label="设置" className="flex min-w-0 max-w-3xl flex-col gap-4 p-6">
      <h1 className="text-[22px] font-semibold leading-[30px]">设置</h1>

      <Card>
        <CardHeader>
          <CardTitle>主题</CardTitle>
        </CardHeader>
        <div role="radiogroup" aria-label="主题偏好" className="flex flex-col gap-1 sm:flex-row sm:gap-4">
          {THEME_OPTIONS.map(option => (
            <label key={option.value} className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm leading-[22px] hover:bg-surface-muted">
              <input
                type="radio"
                name="theme-preference"
                value={option.value}
                checked={preference === option.value}
                onChange={() => set(option.value)}
                className="h-4 w-4 accent-accent"
              />
              {option.label}
            </label>
          ))}
        </div>
        <p className="mt-2 text-xs leading-[18px] text-text-secondary">
          当前生效：{resolved === 'dark' ? '深色' : '浅色'}。偏好只保存在会话存储（sessionStorage），不保存 token。
        </p>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>连接</CardTitle>
        </CardHeader>
        <p className="text-sm leading-[22px] text-text-primary">状态：{CONNECT_STATE_LABEL[state]}</p>
        <p className="mt-1 text-xs leading-[18px] text-text-secondary">连接只指向当前 origin 的本机服务，Bearer token 手动输入、不持久化。</p>
        {state === 'ready' ? (
          <div className="mt-3">
            <Button variant="outline" size="sm" onClick={disconnect}>断开并清理内存 token</Button>
          </div>
        ) : null}
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>使用说明（不持久化约定）</CardTitle>
        </CardHeader>
        <ul className="list-disc space-y-1 pl-5 text-sm leading-[22px] text-text-secondary">
          {USAGE_NOTES.map(note => <li key={note}>{note}</li>)}
        </ul>
      </Card>
    </section>
  );
}
