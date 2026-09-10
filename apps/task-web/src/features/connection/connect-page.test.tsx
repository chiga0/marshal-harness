import {afterEach, describe, expect, it, vi} from 'vitest';
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

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetchError(status: number, body: unknown) {
  vi.stubGlobal('fetch', vi.fn(async () =>
    new Response(JSON.stringify(body), {status, headers: {'Content-Type': 'application/json'}}),
  ));
}

describe('ConnectPage', () => {
  it('承诺 token 仅内存、未连接时可见连接指引', () => {
    renderConnect();
    expect(screen.getByRole('heading', {name: '连接 Marshal 服务'})).toBeInTheDocument();
    expect(screen.getByLabelText('Bearer token')).toBeInTheDocument();
    expect(screen.getByRole('button', {name: '连接'})).toBeEnabled();
    expect(screen.getByText(/连不上时的本机指引/)).toBeInTheDocument();
  });

  it('空 token 点击连接展示错误', async () => {
    const user = userEvent.setup();
    renderConnect();
    await user.click(screen.getByRole('button', {name: '连接'}));
    expect(screen.getByRole('alert')).toHaveTextContent('请输入本机既有 Bearer token');
  });

  it('401：区分展示凭据未通过验收', async () => {
    const user = userEvent.setup();
    stubFetchError(401, {code: 'unauthorized', requestId: 'req-c1'});
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), 'bad-token');
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('凭据未通过验收（401）');
    expect(alert).toHaveTextContent('req-c1');
    expect(screen.getByRole('button', {name: '连接'})).toBeEnabled();
  });

  it('非 401 的错误响应：区分展示服务未就绪', async () => {
    const user = userEvent.setup();
    stubFetchError(503, {code: 'not_ready', requestId: 'req-c2'});
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), 'token-x');
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('服务已响应但未就绪（就绪探测返回 503）');
    expect(alert).toHaveTextContent('not_ready');
  });

  it('网络层失败：区分展示本机不可达且不猜测原因', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('fetch failed'); }));
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), 'token-x');
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('无法连接本机服务（网络层不可达）');
    expect(alert).toHaveTextContent('本页不做猜测');
  });
});
