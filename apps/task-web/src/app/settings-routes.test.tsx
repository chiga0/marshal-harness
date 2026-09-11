import {afterEach, describe, expect, it, vi} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, useLocation} from 'react-router-dom';
import {AppRoutes} from './app';
import {ConnectionProvider} from '../features/connection/connection';

vi.mock('../features/tasks/task-list-page', () => ({TaskListPage: () => <h1>任务列表页面</h1>}));
vi.mock('../features/tasks/task-new-page', () => ({TaskNewPage: () => <h1>新建任务页面</h1>}));
vi.mock('../features/tasks/detail/task-detail-layout', () => ({TaskDetailLayout: () => <h1>原任务详情</h1>}));

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

function renderRoutes(entry: string | {pathname: string; state: unknown}, compact = false) {
  vi.stubGlobal('matchMedia', vi.fn((query: string) => ({matches: compact && query === '(max-width: 1023px)', addEventListener() {}, removeEventListener() {}})));
  return render(<MemoryRouter initialEntries={[entry]}><ConnectionProvider><AppRoutes /><LocationProbe /></ConnectionProvider></MemoryRouter>);
}

afterEach(() => vi.unstubAllGlobals());

describe('独立设置外壳与工作台返回', () => {
  it.each(['/tasks/task-1/team?worker=worker-2', '/tasks/task-1/graph'])('从 %s 进入设置，跨分组后返回原任务子页，不回退到历史记录', async path => {
    renderRoutes(path);
    const user = userEvent.setup();
    await user.click(screen.getByRole('link', {name: '设置'}));
    expect(screen.queryByRole('navigation', {name: '主导航'})).toBeNull();
    expect(screen.queryByText('原任务详情')).toBeNull();
    expect(screen.getByRole('heading', {name: '通用'})).toBeInTheDocument();
    await user.click(screen.getByRole('link', {name: '连接与安全'}));
    await user.click(screen.getByRole('link', {name: '关于'}));
    expect(screen.getByRole('heading', {name: '配置管理尚未开放'})).toBeInTheDocument();
    await user.click(screen.getByRole('link', {name: '返回工作台'}));
    expect(screen.getByTestId('location')).toHaveTextContent(path);
    expect(screen.getByRole('heading', {name: '原任务详情'})).toBeInTheDocument();
    expect(screen.queryByTestId('settings-shell')).toBeNull();
  });

  it.each(['/settings', '/settings/unknown', '/settings/about', '/settings/connection'])('直接导航 %s 无返回历史时明确回任务列表', async path => {
    renderRoutes(path);
    const user = userEvent.setup();
    expect(screen.getByTestId('settings-shell')).toBeInTheDocument();
    expect(screen.getByRole('link', {name: '返回任务列表'})).toHaveAttribute('href', '/');
    if (path === '/settings' || path === '/settings/unknown') expect(screen.getByTestId('location')).toHaveTextContent('/settings/general');
    await user.click(screen.getByRole('link', {name: '返回任务列表'}));
    expect(screen.getByRole('heading', {name: '任务列表页面'})).toBeInTheDocument();
  });

  it('不接受来自路由 state 的外部返回目标', () => {
    renderRoutes({pathname: '/settings/about', state: {returnTo: '//evil.example/tasks/1'}});
    expect(screen.getByRole('link', {name: '返回任务列表'})).toHaveAttribute('href', '/');
  });

  it('375px 模拟：主抽屉键盘进入设置，分组与返回均可达且不遗留模态锁', async () => {
    vi.stubGlobal('innerWidth', 375);
    renderRoutes('/tasks/task-1/activity', true);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', {name: '打开导航'}));
    const drawer = screen.getByRole('dialog', {name: '主导航'});
    within(drawer).getByRole('link', {name: '设置'}).focus();
    await user.keyboard('{Enter}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.body.style.overflow).not.toBe('hidden');
    const nav = screen.getByRole('navigation', {name: '设置分组'});
    expect(within(nav).getAllByRole('link')).toHaveLength(3);
    expect(screen.getByRole('link', {name: '返回工作台'})).toHaveFocus();
    await user.tab();
    expect(within(nav).getByRole('link', {name: '通用'})).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('link', {name: '连接与安全'})).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('heading', {name: '连接与安全'})).toBeInTheDocument();
    expect(screen.getByRole('link', {name: '连接与安全'})).toHaveFocus();
    await user.tab();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('heading', {name: '关于'})).toBeInTheDocument();
    const back = screen.getByRole('link', {name: '返回工作台'});
    back.focus();
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('location')).toHaveTextContent('/tasks/task-1/activity');
    expect(screen.getByRole('button', {name: '打开导航'})).toBeInTheDocument();
  });
});
