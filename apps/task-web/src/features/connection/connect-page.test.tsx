import {describe, expect, it} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ConnectPage} from './connect-page';
import {ConnectionProvider} from './connection';

function renderConnect() {
  return render(
    <ConnectionProvider>
      <ConnectPage />
    </ConnectionProvider>,
  );
}

describe('ConnectPage', () => {
  it('承诺 token 仅内存、未连接时可见连接指引', () => {
    renderConnect();
    expect(screen.getByRole('heading', {name: '连接 Marshal 服务'})).toBeInTheDocument();
    expect(screen.getByLabelText('Bearer token')).toBeInTheDocument();
    expect(screen.getByRole('button', {name: '连接'})).toBeEnabled();
  });

  it('空 token 点击连接展示错误', async () => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(screen.getByRole('button', {name: '连接'}));
    expect(screen.getByRole('alert')).toHaveTextContent('请输入本机既有 Bearer token');
  });
});
