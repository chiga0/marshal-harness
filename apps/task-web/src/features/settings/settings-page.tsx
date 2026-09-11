// 设置：主题偏好（sessionStorage，仅非敏感偏好）、连接状态与管理、不持久化使用说明。
import {useTheme} from '../../lib/theme/useTheme';
import type {ThemePreference} from '../../lib/theme/useTheme';
import {useConnection} from '../connection/connection';
import type {ConnectState} from '../connection/connection';
import {Button} from '../../components/ui/button';
import {Card, CardHeader, CardTitle} from '../../components/ui/card';
import {Link, NavLink, Navigate, Route, Routes, useLocation} from 'react-router-dom';
import {ArrowLeft, Info, Palette, ShieldCheck, Settings} from 'lucide-react';
import {cn} from '../../lib/cn';
import {settingsReturnTo} from './settings-navigation';
import {useEffect, useRef} from 'react';

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
  '当前连接内的未决操作会提示核对，重放须由你明确触发；刷新或断开后不会恢复这些内存记录。请先回到任务列表核对任务与回执，不会自动重放任何提交。',
  '任务列表的搜索与筛选只作用于已加载的任务项，不代表服务端全局结果。',
  '所有数据来自当前 origin 的本机服务；界面不接受远程服务地址。',
];

export function SettingsPage() {
  const location = useLocation();
  const backRef = useRef<HTMLAnchorElement>(null);
  // 替换外壳后原抽屉触发点已卸载，明确把键盘起点交给设置返回入口。
  useEffect(() => { backRef.current?.focus({preventScroll: true}); }, []);
  const returnTo = settingsReturnTo(location.state);
  const state = {returnTo};
  const groups = [
    {path: 'general', label: '通用', icon: Palette},
    {path: 'connection', label: '连接与安全', icon: ShieldCheck},
    {path: 'about', label: '关于', icon: Info},
  ];
  return (
    <div className="flex h-screen min-w-0 flex-col bg-app-bg text-text-primary lg:flex-row" data-testid="settings-shell">
      <aside className="shrink-0 border-b border-border bg-surface p-4 lg:w-56 lg:border-b-0 lg:border-r lg:p-4" aria-label="设置导航">
        <Link ref={backRef} to={returnTo} replace className="mb-4 flex min-h-11 items-center gap-2 rounded-md px-3 py-2 text-sm text-text-secondary hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent">
          <ArrowLeft aria-hidden className="h-4 w-4" />{returnTo === '/' ? '返回任务列表' : '返回工作台'}
        </Link>
        <div className="mb-4 flex items-center gap-2 px-3 text-base font-semibold"><Settings aria-hidden className="h-5 w-5 text-accent" />设置</div>
        <nav aria-label="设置分组" className="flex flex-wrap gap-1 lg:flex-col">
          {groups.map(({path, label, icon: Icon}) => <NavLink key={path} to={`/settings/${path}`} state={state}
            className={({isActive}) => cn('flex min-h-11 items-center gap-2 rounded-md px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent', isActive ? 'bg-surface-muted font-medium text-text-primary' : 'text-text-secondary hover:bg-surface-muted')}>
            <Icon aria-hidden className="h-4 w-4 shrink-0" />{label}
          </NavLink>)}
        </nav>
      </aside>
      <main className="min-h-0 min-w-0 flex-1 overflow-y-auto p-4 sm:p-8 lg:p-12">
        <div className="mx-auto w-full max-w-3xl">
          <Routes>
            <Route path="general" element={<GeneralSettings />} />
            <Route path="connection" element={<ConnectionSettings />} />
            <Route path="about" element={<AboutSettings />} />
            <Route path="*" element={<Navigate to="/settings/general" state={state} replace />} />
          </Routes>
        </div>
      </main>
    </div>
  );
}

function SectionHeading({title, description}: {title: string; description: string}) {
  return <header className="mb-8 space-y-2"><h1 className="text-[22px] font-semibold leading-[30px]">{title}</h1><p className="text-sm leading-[22px] text-text-secondary">{description}</p></header>;
}

function GeneralSettings() {
  const {preference, resolved, set} = useTheme();
  return (
    <section aria-label="通用设置">
      <SectionHeading title="通用" description="调整工作台的显示偏好，仅对当前浏览器会话生效。" />
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
    </section>
  );
}

function ConnectionSettings() {
  const {state, disconnect} = useConnection();
  return (<section aria-label="连接与安全设置" className="space-y-4">
      <SectionHeading title="连接与安全" description="管理当前本机服务连接，了解凭据与中断恢复边界。" />
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

function AboutSettings() {
  return <section aria-label="关于 Marshal">
    <SectionHeading title="关于" description="Marshal · Task-first Agent Team" />
    <div className="space-y-4 border-t border-border pt-6 text-sm leading-[22px]">
      <p>围绕任务组织需求、计划确认、团队执行与成果核验。界面只展示当前本机服务提供的事实，不把受理当作执行或交付成功。</p>
      <div className="rounded-lg bg-surface-muted p-4">
        <h2 className="mb-2 text-base font-medium">配置管理尚未开放</h2>
        <p className="text-text-secondary">此设置中心目前仅提供主题和连接管理。Agent、Sandbox 与服务运行配置仍由本机服务侧管理，这里不提供配置表单，也不推断当前配置是否可用。</p>
      </div>
    </div>
  </section>;
}
