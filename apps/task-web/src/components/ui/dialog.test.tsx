import {useState} from 'react';
import {describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {Button} from './button';
import {ConfirmDialog, Dialog} from './dialog';

describe('确认框可访问操作', () => {
  it('长正文完整保留；键盘可取消并归还仍挂载的触发按钮', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const description = '长授权描述与目标标识。'.repeat(200);
    function Example() {
      const [open, setOpen] = useState(false);
      return <>
        <Button onClick={() => setOpen(true)}>查看确认</Button>
        <ConfirmDialog open={open} onCancel={() => setOpen(false)} onConfirm={onConfirm}
          title="确认操作" description={description} confirmText="确认提交" />
      </>;
    }
    render(<Example />);
    await user.click(screen.getByRole('button', {name: '查看确认'}));
    const dialog = screen.getByRole('dialog', {name: '确认操作'});
    expect(dialog).toHaveAccessibleDescription(description);
    // CSS 防回归；jsdom 不测量布局，实际滚动和缩放仍由浏览器验收。
    expect(dialog).toHaveClass('max-h-[calc(100dvh-2rem)]', 'overflow-y-auto', '[overflow-wrap:anywhere]');
    expect(screen.getByRole('button', {name: '确认提交'})).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('button', {name: '取消'})).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByRole('button', {name: '查看确认'})).toHaveFocus();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('多个模态层各自绑定正确标题和描述', () => {
    render(<>
      <Dialog open onClose={() => {}} title="底层" description="底层说明" />
      <Dialog open onClose={() => {}} title="上层" description="上层说明" />
    </>);
    expect(screen.getByRole('dialog', {name: '底层'})).toHaveAccessibleDescription('底层说明');
    expect(screen.getByRole('dialog', {name: '上层'})).toHaveAccessibleDescription('上层说明');
  });
});
