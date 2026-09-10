// 外壳：导航 + 主区域。主区域承载交流与业务结论，右侧团队概要由页面自己提供（shell 最小）。
import {useEffect, useState} from 'react';
import {NavLink, useLocation} from 'react-router-dom';
import type {ReactNode} from 'react';
import {cn} from '../lib/cn';
import {useConnection} from '../features/connection/connection';
import {Button} from '../components/ui/button';

const NAV_ITEMS = [
  {to: '/', label: '任务', key: 'tasks'},
  {to: '/settings', label: '设置', key: 'settings'},
] as const;

export function ShellLayout({children, rightPanel}: {children: ReactNode; rightPanel?: ReactNode}) {
  const {disconnect} = useConnection();
  const location = useLocation();
  const [narrow, setNarrow] = useState<boolean>(false);
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 1023px)');
    const onChange = () => setNarrow(mq.matches);
    onChange();
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  return (
    <div className="flex h-screen flex-col bg-app-bg text-text-primary">
      <div className="flex min-h-0 flex-1">
        {!narrow ? (
          <aside className="w-56 shrink-0 border-r border-border bg-surface" aria-label="一级导航">
            <div className="px-4 py-4">
              <div className="mb-4 text-base font-semibold leading-6">Marshal</div>
              <nav className="space-y-1">
                {NAV_ITEMS.map(item => (
                  <NavLink key={item.key} to={item.to} end={item.to === '/'}
                    className={({isActive}) => cn('block rounded-md px-3 py-2 text-sm leading-[22px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
                      isActive ? 'bg-accent text-accent-foreground' : 'text-text-secondary hover:bg-surface-muted hover:text-text-primary')}>
                    {item.label}
                  </NavLink>
                ))}
              </nav>
            </div>
            <div className="mt-auto border-t border-border px-4 py-3">
              <Button variant="ghost" size="sm" onClick={disconnect} className="w-full justify-start text-text-secondary">
                断开清理（token 不入存）
              </Button>
            </div>
          </aside>
        ) : null}
        <main className="flex min-w-0 flex-1 flex-col" aria-live="polite" aria-atomic="false">
          {children}
        </main>
        {rightPanel ? (
          <aside className="hidden w-80 shrink-0 border-l border-border bg-surface lg:block" aria-label="概要侧栏">
            {rightPanel}
          </aside>
        ) : null}
      </div>
    </div>
  );
}
