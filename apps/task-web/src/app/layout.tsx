// 外壳：≥1024px 静态侧栏；<1024px（含 <768px 窄屏）导航折叠为抽屉——默认关闭、Escape 关闭、
// 关闭后焦点归还触发按钮、切换路由自动收起。主区域可滚动，全部操作键盘可达。
import {useEffect, useRef, useState} from 'react';
import type {ReactNode, Ref} from 'react';
import {Link, NavLink, useLocation} from 'react-router-dom';
import {Menu, Plus, X} from 'lucide-react';
import {cn} from '../lib/cn';
import {useConnection} from '../features/connection/connection';
import {Button} from '../components/ui/button';

const NAV_ITEMS = [
  {to: '/', label: '任务', key: 'tasks'},
  {to: '/settings', label: '设置', key: 'settings'},
] as const;

function NavMenu({onNavigate, firstItemRef}: {onNavigate?: () => void; firstItemRef?: Ref<HTMLAnchorElement>}) {
  return (
    <nav className="space-y-1" aria-label="主导航">
      {NAV_ITEMS.map((item, index) => (
        <NavLink
          key={item.key}
          to={item.to}
          end={item.to === '/'}
          ref={index === 0 ? firstItemRef : undefined}
          onClick={onNavigate}
          className={({isActive}) => cn(
            'block rounded-md px-3 py-2 text-sm leading-[22px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
            isActive ? 'bg-accent text-accent-foreground' : 'text-text-secondary hover:bg-surface-muted hover:text-text-primary',
          )}
        >
          {item.label}
        </NavLink>
      ))}
    </nav>
  );
}

function DisconnectButton({className}: {className?: string}) {
  const {disconnect} = useConnection();
  return (
    <Button variant="ghost" size="sm" onClick={disconnect} className={cn('justify-start text-text-secondary', className)}>
      断开清理（token 不入存）
    </Button>
  );
}

export function ShellLayout({children, rightPanel}: {children: ReactNode; rightPanel?: ReactNode}) {
  const location = useLocation();
  const [compact, setCompact] = useState<boolean>(() =>
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia('(max-width: 1023px)').matches
      : false,
  );
  const [drawerOpen, setDrawerOpen] = useState(false);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const firstNavRef = useRef<HTMLAnchorElement>(null);

  useEffect(() => {
    const mq = window.matchMedia('(max-width: 1023px)');
    const onChange = () => {
      setCompact(mq.matches);
      if (!mq.matches) setDrawerOpen(false);
    };
    onChange();
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setDrawerOpen(false);
    };
    document.addEventListener('keydown', onKey);
    firstNavRef.current?.focus();
    const body = document.body;
    const previousOverflow = body.style.overflow;
    body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      body.style.overflow = previousOverflow;
      menuTriggerRef.current?.focus();
    };
  }, [drawerOpen]);

  const closeDrawer = () => setDrawerOpen(false);

  return (
    <div className="flex h-screen flex-col bg-app-bg text-text-primary">
      {compact ? (
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-surface px-3">
          <Button
            ref={menuTriggerRef}
            variant="ghost"
            size="icon"
            className="h-9 w-9"
            aria-label="打开导航"
            aria-expanded={drawerOpen}
            aria-controls="shell-nav-drawer"
            onClick={() => setDrawerOpen(true)}
          >
            <Menu aria-hidden />
          </Button>
          <span className="text-base font-semibold leading-6">Marshal</span>
          <span className="flex-1" />
          <Link
            to="/tasks/new"
            className="inline-flex h-9 select-none items-center justify-center gap-1.5 rounded-md border border-border bg-transparent px-3 text-[13px] font-medium leading-5 text-text-primary transition-colors hover:bg-surface-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            <Plus aria-hidden className="h-4 w-4" />
            新建
          </Link>
        </header>
      ) : null}

      <div className="flex min-h-0 flex-1">
        {!compact ? (
          <aside className="flex w-56 shrink-0 flex-col border-r border-border bg-surface" aria-label="一级导航">
            <div className="px-4 py-4">
              <div className="mb-4 text-base font-semibold leading-6">Marshal</div>
              <NavMenu />
            </div>
            <div className="mt-auto border-t border-border px-4 py-3">
              <DisconnectButton className="w-full" />
            </div>
          </aside>
        ) : null}

        <main className="flex min-w-0 flex-1 flex-col overflow-y-auto">
          {children}
        </main>

        {rightPanel ? (
          <aside className="hidden w-80 shrink-0 overflow-y-auto border-l border-border bg-surface lg:block" aria-label="概要侧栏">
            {rightPanel}
          </aside>
        ) : null}
      </div>

      {compact && drawerOpen ? (
        <div className="fixed inset-0 z-50" role="presentation">
          <div className="absolute inset-0 bg-black/40" aria-hidden="true" onClick={closeDrawer} />
          <aside
            id="shell-nav-drawer"
            role="dialog"
            aria-modal="true"
            aria-label="主导航"
            className="absolute inset-y-0 left-0 flex w-64 max-w-[85vw] flex-col border-r border-border bg-surface"
          >
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <span className="text-base font-semibold leading-6">Marshal</span>
              <Button variant="ghost" size="icon" className="h-9 w-9" onClick={closeDrawer} aria-label="关闭导航">
                <X aria-hidden />
              </Button>
            </div>
            <div className="px-4 py-4">
              <NavMenu onNavigate={closeDrawer} firstItemRef={firstNavRef} />
            </div>
            <div className="mt-auto border-t border-border px-4 py-3">
              <DisconnectButton className="w-full" />
            </div>
          </aside>
        </div>
      ) : null}
    </div>
  );
}
