import {afterEach, describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter} from 'react-router-dom';
import {ShellLayout} from './layout';
import {ConnectionProvider} from '../features/connection/connection';

function stubMatchMedia(matches: boolean) {
  const stub = vi.fn().mockImplementation((query: string) => ({
    matches,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }));
  vi.stubGlobal('matchMedia', stub);
}

function renderShell() {
  return render(
    <MemoryRouter>
      <ConnectionProvider>
        <ShellLayout>
          <div>页面内容</div>
        </ShellLayout>
      </ConnectionProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ShellLayout 响应式导航', () => {
  it('宽屏：静态侧栏呈现一级入口', () => {
    stubMatchMedia(false);
    renderShell();
    expect(screen.getByRole('complementary', {name: '一级导航'})).toBeInTheDocument();
    expect(screen.getByRole('link', {name: '任务'})).toBeInTheDocument();
    expect(screen.getByRole('link', {name: '设置'})).toBeInTheDocument();
    expect(screen.queryByRole('button', {name: '打开导航'})).toBeNull();
  });

  it('窄屏：导航折叠为抽屉，打开后 Escape 关闭且焦点归还触发按钮', async () => {
    stubMatchMedia(true);
    const user = userEvent.setup();
    renderShell();

    expect(screen.queryByRole('dialog', {name: '主导航'})).toBeNull();
    const trigger = screen.getByRole('button', {name: '打开导航'});
    expect(trigger).toHaveAttribute('aria-expanded', 'false');

    await user.click(trigger);
    const drawer = screen.getByRole('dialog', {name: '主导航'});
    expect(drawer).toBeInTheDocument();
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    // 打开后焦点进入抽屉内的第一个导航项，键盘可继续操作
    const firstNav = screen.getAllByRole('link', {name: '任务'})[0];
    expect(document.activeElement).toBe(firstNav);

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', {name: '主导航'})).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('窄屏：抽屉内导航点击后自动收起；新建入口固定可达', async () => {
    stubMatchMedia(true);
    const user = userEvent.setup();
    renderShell();
    expect(screen.getByRole('link', {name: /新建/})).toHaveAttribute('href', '/tasks/new');

    await user.click(screen.getByRole('button', {name: '打开导航'}));
    await user.click(screen.getAllByRole('link', {name: '设置'})[0]!);
    expect(screen.queryByRole('dialog', {name: '主导航'})).toBeNull();
  });

  it('窄屏：点击遮罩关闭抽屉', async () => {
    stubMatchMedia(true);
    const user = userEvent.setup();
    renderShell();
    await user.click(screen.getByRole('button', {name: '打开导航'}));
    const drawer = screen.getByRole('dialog', {name: '主导航'});
    const backdrop = drawer.parentElement?.querySelector('.bg-black\\/40');
    expect(backdrop).not.toBeNull();
    await user.click(backdrop as Element);
    expect(screen.queryByRole('dialog', {name: '主导航'})).toBeNull();
  });
});
