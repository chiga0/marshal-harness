import {afterEach, describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ConnectPage, extractToken} from './connect-page';
import {ConnectionProvider} from './connection';

const VALID_TOKEN = 'a877d9010564d94ae94cf3f5d54cf50a6f87c7aa4bac7dfb7f67ac9861dbcd11';

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

  it('非法字符/非十六进制 token：本地即给出格式错误，不发请求', async () => {
    const user = userEvent.setup();
    const spy = vi.fn();
    vi.stubGlobal('fetch', spy);
    renderConnect();
    await user.click(screen.getByLabelText('Bearer token'));
    await user.paste('中文说明：token：' + VALID_TOKEN + '。');
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('token 格式不对');
    expect(spy).not.toHaveBeenCalled();
  });

  it('从剪贴板获取：从混有说明的文字中提取干净 token 并填入', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response(JSON.stringify({items: [], nextCursor: null}), {status: 200, headers: {'Content-Type': 'application/json'}}),
    ));
    Object.defineProperty(navigator, 'clipboard', {
      value: {readText: async () => '连接 token：`' + VALID_TOKEN + '` 请收好'},
      configurable: true,
    });
    renderConnect();
    await user.click(screen.getByRole('button', {name: '从剪贴板获取'}));
    await user.click(screen.getByRole('button', {name: '连接'}));
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('剪贴板没有 token 时如实说明', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      value: {readText: async () => '没有任何十六进制内容'},
      configurable: true,
    });
    renderConnect();
    await user.click(screen.getByRole('button', {name: '从剪贴板获取'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('剪贴板里没有 token');
  });

  it('401：区分展示凭据未通过验收', async () => {
    const user = userEvent.setup();
    stubFetchError(401, {code: 'unauthorized', requestId: 'req-c1'});
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), VALID_TOKEN);
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
    await user.type(screen.getByLabelText('Bearer token'), VALID_TOKEN);
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('服务已响应但未就绪（就绪探测返回 503）');
    expect(alert).toHaveTextContent('not_ready');
  });

  it('网络层失败：只说「未取得有效响应」，不声称请求没有到达服务、不猜测原因', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('fetch failed'); }));
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), VALID_TOKEN);
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('无法连接本机服务（未取得有效响应）');
    expect(alert).toHaveTextContent('不能证明请求没有到达服务');
    expect(alert).toHaveTextContent('本页不做猜测');
  });

  it('请求头构造失败（中文引号混入）：拆出与网络不可达不同的定位文案', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError("Failed to execute 'fetch' on 'Window': Invalid value"); }));
    renderConnect();
    await user.type(screen.getByLabelText('Bearer token'), VALID_TOKEN);
    await user.click(screen.getByRole('button', {name: '连接'}));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('请求构造失败：token 含非法字符');
    expect(alert).not.toHaveTextContent('未取得有效响应');
  });
});

describe('extractToken', () => {
  it('从任意说明文字提取 64 位十六进制 token', () => {
    expect(extractToken('请使用 token：`' + VALID_TOKEN + '`。')).toBe(VALID_TOKEN);
    expect(extractToken(VALID_TOKEN)).toBe(VALID_TOKEN);
    expect(extractToken('没有 token')).toBeNull();
    expect(extractToken('a'.repeat(63))).toBeNull(); // 不够长
  });
});
