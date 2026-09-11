import {act, render, screen} from '@testing-library/react';
import {afterAll, afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {SettingsPage} from './settings-page';
import {ConnectionProvider, useConnection} from '../connection/connection';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {clearToken} from '../../lib/transport/client';
import {setThemePreference} from '../../lib/theme/useTheme';

// matchMedia 替身放在模块级：组件挂载/卸载与主题复位的 effect 都可能调用它，生命周期不能挂在单个测试的 beforeEach 上。
vi.stubGlobal('matchMedia', vi.fn().mockImplementation((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => {},
  removeEventListener: () => {},
  addListener: () => {},
  removeListener: () => {},
  dispatchEvent: () => false,
})));

function renderSettings(path = '/settings/general') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ConnectionProvider>
        <Routes><Route path="settings/*" element={<SettingsPage />} /></Routes>
      </ConnectionProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  window.sessionStorage.clear();
  setThemePreference('light');
  document.documentElement.classList.remove('dark');
});

afterEach(() => {
  act(() => setThemePreference('system'));
  window.sessionStorage.clear();
  document.documentElement.classList.remove('dark');
});

afterAll(() => {
  vi.unstubAllGlobals();
});

describe('SettingsPage', () => {
  it('连接与安全保留实际接入/断开行为，断开后清理查询缓存', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({items: [], nextCursor: null}), {status: 200, headers: {'Content-Type': 'application/json'}}));
    const client = new QueryClient();
    function ConnectProbe() {
      const {connect} = useConnection();
      return <button onClick={() => void connect('a'.repeat(64))}>测试接入</button>;
    }
    try {
      render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/settings/connection']}><ConnectionProvider>
        <ConnectProbe /><Routes><Route path="settings/*" element={<SettingsPage />} /></Routes>
      </ConnectionProvider></MemoryRouter></QueryClientProvider>);
      const user = userEvent.setup();
      await user.click(screen.getByRole('button', {name: '测试接入'}));
      expect(await screen.findByText(/状态：已连接/)).toBeInTheDocument();
      client.setQueryData(['tasks', 'list'], {items: [{id: 'old-task'}]});
      await user.click(screen.getByRole('button', {name: '断开并清理内存 token'}));
      expect(screen.getByText(/状态：未连接/)).toBeInTheDocument();
      expect(client.getQueryCache().getAll()).toHaveLength(0);
      expect(screen.queryByRole('button', {name: '断开并清理内存 token'})).toBeNull();
    } finally {
      fetchMock.mockRestore();
      clearToken();
      client.clear();
    }
  });

  it('主题切换：选中深色后根节点应用 dark 且写入会话存储', async () => {
    renderSettings();
    const user = userEvent.setup();
    expect(screen.getByRole('radiogroup', {name: '主题偏好'})).toBeInTheDocument();
    expect(screen.getByRole('radio', {name: '浅色'})).toBeChecked();
    expect(document.documentElement.classList.contains('dark')).toBe(false);

    await user.click(screen.getByRole('radio', {name: '深色'}));
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    expect(window.sessionStorage.getItem('ui1-theme-preference')).toBe('dark');
    expect(screen.getByText(/当前生效：深色/)).toBeInTheDocument();

    await user.click(screen.getByRole('radio', {name: '跟随系统'}));
    expect(document.documentElement.classList.contains('dark')).toBe(false);
  });

  it('连接状态与不持久化说明可见；未连接时不显示断开按钮', () => {
    renderSettings('/settings/connection');
    expect(screen.getByText(/状态：未连接/)).toBeInTheDocument();
    expect(screen.queryByRole('button', {name: '断开并清理内存 token'})).toBeNull();
    expect(screen.getByText(/token 只保存在本页面内存/)).toBeInTheDocument();
    expect(screen.getByText(/不会自动重放任何提交/)).toBeInTheDocument();
    expect(screen.getByText(/不代表服务端全局结果/)).toBeInTheDocument();
  });
});
