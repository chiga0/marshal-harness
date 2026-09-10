// UI-09：抽屉与嵌套确认框的焦点/层叠回归——
// 一次 Escape 只关闭最上层；Tab 圈禁在抽屉内；轮询重渲染（回调 identity 变化）不重置焦点。
import {describe, expect, it, vi} from 'vitest';
import {fireEvent, render, screen} from '@testing-library/react';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {WorkerDrawer} from './worker-drawer';
import {makeFakeTransport, makeWorker} from '../tasks/detail/testing/fixtures';

function wrap(node: ReactNode, client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}})) {
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

describe('Worker 抽屉焦点与层叠（UI-09）', () => {
  it('嵌套确认框打开时：一次 Escape 只关闭确认框，再一次 Escape 才关闭抽屉', () => {
    const onClose = vi.fn();
    const {transport} = makeFakeTransport();
    wrap(<WorkerDrawer taskRevision={7} worker={makeWorker()} transport={transport} onClose={onClose} onChanged={() => {}} />);

    fireEvent.click(screen.getByTestId('cancel-worker-open'));
    expect(screen.getByRole('dialog', {name: /取消 Worker/})).toBeInTheDocument();

    fireEvent.keyDown(document, {key: 'Escape'});
    // 只关闭最上层（确认框）；抽屉保持打开且未触发其 onClose
    expect(screen.queryByRole('dialog', {name: /取消 Worker/})).toBeNull();
    expect(screen.getByTestId('worker-drawer')).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.keyDown(document, {key: 'Escape'});
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('Tab 圈禁：末项再 Tab 回到抽屉内首项，焦点不进入背景层', () => {
    const {transport} = makeFakeTransport();
    wrap(<WorkerDrawer taskRevision={7} worker={makeWorker()} transport={transport} onClose={() => {}} onChanged={() => {}} />);
    const drawer = screen.getByTestId('worker-drawer');
    const items = drawer.querySelectorAll('a[href], button:not([disabled]), textarea, input, select');
    const first = items[0] as HTMLElement;
    const last = items[items.length - 1] as HTMLElement;

    last.focus();
    fireEvent.keyDown(document, {key: 'Tab'});
    expect(document.activeElement).toBe(first);

    fireEvent.keyDown(document, {key: 'Tab', shiftKey: true});
    expect(document.activeElement).toBe(last);
  });

  it('上层回调 identity 变化（轮询重渲染）不重置抽屉内焦点', () => {
    const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
    const {transport} = makeFakeTransport();
    const node = (onClose: () => void) => (
      <QueryClientProvider client={client}>
        <WorkerDrawer taskRevision={7} worker={makeWorker()} transport={transport} onClose={onClose} onChanged={() => {}} />
      </QueryClientProvider>
    );
    const {rerender} = render(node(() => {}));
    const cancelButton = screen.getByTestId('cancel-worker-open');
    cancelButton.focus();
    expect(document.activeElement).toBe(cancelButton);

    // 等价于父组件轮询后重渲染：onClose/onChanged 是全新闭包
    rerender(node(() => {}));
    expect(document.activeElement).toBe(cancelButton);
  });
});
