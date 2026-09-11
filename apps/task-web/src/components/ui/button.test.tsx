import {describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {Button} from './button';

describe('有界长标签按钮', () => {
  it('完整保留长选项名称并可键盘触发，默认尺寸允许多行增长', async () => {
    const user = userEvent.setup();
    const label = '这是需要完整展示而不是截断的业务选项。'.repeat(100);
    const onClick = vi.fn();
    render(<Button size="sm" onClick={onClick}>{label}</Button>);
    const button = screen.getByRole('button', {name: label});
    expect(button).toHaveClass('min-w-0', 'max-w-full', 'whitespace-normal', '[overflow-wrap:anywhere]', 'min-h-11');
    expect(button).not.toHaveClass('whitespace-nowrap', 'h-9');
    await user.tab();
    expect(button).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
