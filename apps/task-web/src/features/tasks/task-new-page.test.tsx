import {beforeEach, describe, expect, it, vi} from 'vitest';
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError} from '../../lib/transport/types';
import {TaskNewComposer} from './task-new-page';
import {inFlightWriteCount, LogicalActionScope} from './detail/shared/logical-action';
import type {CreateTaskApi} from './task-create';

vi.mock('../../lib/transport/types', async importOriginal => {
  const original = await importOriginal<typeof import('../../lib/transport/types')>();
  const state = {
    seq: 0,
    reset() {
      this.seq = 0;
    },
  };
  (globalThis as {__idemKeyState?: typeof state}).__idemKeyState = state;
  return {
    ...original,
    newIdempotencyKey: () => {
      state.seq += 1;
      return 'key-' + state.seq;
    },
  };
});

function createTestQueryClient(): QueryClient {
  return new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
}

function renderComposer(api: CreateTaskApi | null) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <MemoryRouter>
        <TaskNewComposer api={api} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

type MockedApi = CreateTaskApi & {
  createTask: ReturnType<typeof vi.fn<CreateTaskApi['createTask']>>;
  createInput: ReturnType<typeof vi.fn<CreateTaskApi['createInput']>>;
};

function makeApi(overrides: Partial<CreateTaskApi> = {}): MockedApi {
  return {
    createInput: vi.fn(async (body: {name: string}) => ({id: 'input-' + body.name})),
    createTask: vi.fn(async () => ({id: 'task-created-1'})),
    ...overrides,
  } as MockedApi;
}

beforeEach(() => {
  (globalThis as {__idemKeyState?: {reset: () => void} | undefined}).__idemKeyState?.reset();
});

describe('TaskNewComposer 客户端校验', () => {
  it.each([0, 1])('合计33引用（含%i附件）提交前拒绝且可修改至合法32项', async fileCount => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    const intent = screen.getByLabelText(/需求内容/);
    const context = screen.getByLabelText(/附加上下文/);
    await user.type(intent, '保留的需求');
    const refs = Array.from({length: 33 - fileCount}, (_, index) => 'input-' + index);
    const raw = JSON.stringify({text: '保留的上下文', inputRefs: refs});
    fireEvent.change(context, {target: {value: raw}});
    if (fileCount) await user.upload(screen.getByLabelText('附件输入'), new File(['a'], 'a.txt'));
    const keysBeforeSubmit = (globalThis as {__idemKeyState?: {seq: number}}).__idemKeyState?.seq;
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/inputRefs 合计 33 个/)).toBeInTheDocument();
    expect(api.createInput).not.toHaveBeenCalled();
    expect(api.createTask).not.toHaveBeenCalled();
    expect(intent).toBeEnabled();
    expect(intent).toHaveValue('保留的需求');
    expect(context).toBeEnabled();
    expect(context).toHaveValue(raw);
    expect(screen.queryByText(/提交结果未知/)).not.toBeInTheDocument();
    expect((globalThis as {__idemKeyState?: {seq: number}}).__idemKeyState?.seq).toBe(keysBeforeSubmit);
    fireEvent.change(context, {target: {value: JSON.stringify({text: '保留的上下文', inputRefs: refs.slice(1)})}});
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await screen.findByText('任务已创建，服务端返回受理回执');
    expect(api.createInput).toHaveBeenCalledTimes(fileCount);
    expect(api.createTask).toHaveBeenCalledTimes(1);
    expect(api.createTask.mock.calls[0]![0].context?.inputRefs).toHaveLength(32);
  });

  it.each([1, 2])('独立审查：%i个附件时连接销毁后迟到上传成功不得启动后续写', async fileCount => {
    const user = userEvent.setup();
    let finishInput!: (value: {id: string}) => void;
    const oldApi = makeApi({createInput: vi.fn(() => new Promise<{id: string}>(resolve => { finishInput = resolve; }))});
    const newApi = makeApi();
    const shell = (api: CreateTaskApi) => <MemoryRouter><LogicalActionScope session={api}><TaskNewComposer api={api} /></LogicalActionScope></MemoryRouter>;
    const view = render(shell(oldApi));
    await user.type(screen.getByLabelText(/需求内容/), '旧连接草稿');
    await user.upload(screen.getByLabelText('附件输入'), Array.from({length: fileCount}, (_, index) => new File(['old'], `old-${index}.txt`)));
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await waitFor(() => expect(oldApi.createInput).toHaveBeenCalledTimes(1));
    view.rerender(shell(newApi));
    await act(async () => { finishInput({id: 'old-input'}); });
    await waitFor(() => expect(inFlightWriteCount()).toBe(0));
    expect(oldApi.createInput).toHaveBeenCalledTimes(1);
    expect(oldApi.createTask).not.toHaveBeenCalled();
    expect(newApi.createTask).not.toHaveBeenCalled();
  });

  it('独立审查：附件读取期间连接切换，读取完成后不得借新会话发送旧附件', async () => {
    const user = userEvent.setup();
    let reader!: FileReader;
    const read = vi.spyOn(FileReader.prototype, 'readAsArrayBuffer').mockImplementation(function(this: FileReader) { reader = this; });
    const oldApi = makeApi();
    const newApi = makeApi();
    const shell = (api: CreateTaskApi) => <MemoryRouter><LogicalActionScope session={api}><TaskNewComposer api={api} /></LogicalActionScope></MemoryRouter>;
    try {
      const view = render(shell(oldApi));
      await user.type(screen.getByLabelText(/需求内容/), '旧连接草稿');
      await user.upload(screen.getByLabelText('附件输入'), new File(['old'], 'old.txt'));
      await user.click(screen.getByRole('button', {name: '创建任务'}));
      await waitFor(() => expect(read).toHaveBeenCalledTimes(1));
      view.rerender(shell(newApi));
      await act(async () => {
        Object.defineProperty(reader, 'result', {value: new ArrayBuffer(3)});
        reader.onload?.(new ProgressEvent('load') as ProgressEvent<FileReader>);
      });
      expect(oldApi.createInput).not.toHaveBeenCalled();
      expect(oldApi.createTask).not.toHaveBeenCalled();
      expect(newApi.createInput).not.toHaveBeenCalled();
      expect(newApi.createTask).not.toHaveBeenCalled();
    } finally { read.mockRestore(); }
  });

  it('需求为空时不调用服务端', async () => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText('需求内容为必填项，请描述要交付的业务需求。')).toBeInTheDocument();
    expect(api.createTask).not.toHaveBeenCalled();
    expect(api.createInput).not.toHaveBeenCalled();
  });

  it('需求超 8192 字节给出合同上限提示且不发请求', async () => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    const tooLong = 'a'.repeat(8200);
    // 直接触发 change 避免超长 type 耗时
    const field = screen.getByLabelText(/需求内容/);
    await user.click(field);
    await user.paste(tooLong);
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/超出服务端合同上限 8192 字节（当前 8200 字节）/)).toBeInTheDocument();
    expect(api.createTask).not.toHaveBeenCalled();
  });

  it('上下文 JSON 非法或不是对象时不发请求', async () => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    // user.type 会把 { / [ 解释为控制符，JSON 文本用 paste 输入
    await user.click(screen.getByLabelText(/附加上下文/));
    await user.paste('{not json');
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/上下文不是合法 JSON/)).toBeInTheDocument();
    expect(api.createTask).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText(/附加上下文/));
    await user.click(screen.getByLabelText(/附加上下文/));
    await user.paste('["x"]');
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/上下文合同是 JSON 对象/)).toBeInTheDocument();
    expect(api.createTask).not.toHaveBeenCalled();
  });

  it('附件超 256 KiB 给出合同提示且不发请求', async () => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    const big = new File([new Uint8Array(256 * 1024 + 1)], 'big.bin', {type: 'application/octet-stream'});
    await user.upload(screen.getByLabelText('附件输入'), big);
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/超过单 input 262144 字节（256 KiB）上限/)).toBeInTheDocument();
    expect(api.createInput).not.toHaveBeenCalled();
    expect(api.createTask).not.toHaveBeenCalled();
  });

  it('附件超过 32 个给出合同提示且不发请求', async () => {
    const user = userEvent.setup();
    const api = makeApi();
    renderComposer(api);
    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    const files = Array.from({length: 33}, (_, index) => new File([new Uint8Array(1)], 'f' + index + '.txt', {type: 'text/plain'}));
    await user.upload(screen.getByLabelText('附件输入'), files);
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(await screen.findByText(/附件最多 32 个引用（当前 33 个）/)).toBeInTheDocument();
    expect(api.createTask).not.toHaveBeenCalled();
  });
});

describe('TaskNewComposer 提交流程', () => {
  it('成功：上传附件合并 inputRefs，提交中禁用按钮防重复，显示受理回执', async () => {
    const user = userEvent.setup();
    let releaseCreate: (value: {id: string}) => void = () => {};
    const createTask = vi.fn<CreateTaskApi['createTask']>(() => new Promise<{id: string}>(resolve => { releaseCreate = resolve; }));
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    await user.upload(screen.getByLabelText('附件输入'), new File([new TextEncoder().encode('hello')], 'a.txt', {type: 'text/plain'}));

    const submit = screen.getByRole('button', {name: '创建任务'});
    await user.click(submit);

    // 附件先上传，随后创建进入在途态
    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(1));
    expect(api.createInput).toHaveBeenCalledTimes(1);
    expect(api.createInput.mock.calls[0]?.[0]).toEqual({
      name: 'a.txt',
      mediaType: 'text/plain',
      contentBase64: 'aGVsbG8=',
      idempotencyKey: expect.any(String),
    });
    expect(createTask.mock.calls[0]?.[0]).toEqual({
      intent: '生成对账报告',
      context: {inputRefs: ['input-a.txt']},
      idempotencyKey: expect.any(String),
    });

    // 在途期间按钮禁用，重复点击不产生第二次调用
    expect(submit).toBeDisabled();
    await user.click(submit);
    expect(createTask).toHaveBeenCalledTimes(1);

    releaseCreate({id: 'task-9'});
    const receipt = await screen.findByRole('status');
    expect(receipt).toHaveTextContent('任务已创建，服务端返回受理回执');
    expect(receipt).toHaveTextContent('任务 ID：task-9');
    expect(screen.getByRole('link', {name: '查看任务详情'})).toHaveAttribute('href', '/tasks/task-9');
  });

  it('服务端拒绝：保留草稿与错误码/requestId，再次提交使用新的幂等键', async () => {
    const user = userEvent.setup();
    const createTask = vi
      .fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new ApiError(422, 'invalid_intent', '需求不合法', 'req-77'))
      .mockResolvedValueOnce({id: 'task-10'});
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    await user.click(screen.getByRole('button', {name: '创建任务'}));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('服务端拒绝了本次创建');
    expect(alert).toHaveTextContent('invalid_intent');
    expect(alert).toHaveTextContent('HTTP 422');
    expect(alert).toHaveTextContent('req-77');
    // 草稿保留
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('生成对账报告');
    // ApiError 已有服务端答复，不提供同键重放
    expect(screen.queryByRole('button', {name: /同一请求重试/})).toBeNull();

    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(2));
    const firstKey = (createTask.mock.calls[0]?.[0] as {idempotencyKey: string}).idempotencyKey;
    const secondKey = (createTask.mock.calls[1]?.[0] as {idempotencyKey: string}).idempotencyKey;
    expect(firstKey).not.toEqual(secondKey);
    expect(await screen.findByRole('status')).toHaveTextContent('任务 ID：task-10');
  });

  it('网络层失败：显示结果未知，显式同键重试复用相同幂等键与内容', async () => {
    const user = userEvent.setup();
    const createTask = vi
      .fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new TypeError('network down'))
      .mockResolvedValueOnce({id: 'task-11'});
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    await user.click(screen.getByRole('button', {name: '创建任务'}));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('提交结果未知：请求已发出但没有收到回执');
    expect(alert).toHaveTextContent('network down');
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('生成对账报告');

    await user.click(screen.getByRole('button', {name: /同一请求重试/}));
    expect(await screen.findByRole('status')).toHaveTextContent('任务 ID：task-11');
    expect(createTask).toHaveBeenCalledTimes(2);
    expect(createTask.mock.calls[0]?.[0]).toEqual(createTask.mock.calls[1]?.[0]);
  });

  it('UI-01：提交后服务端返回 504 视为结果未知（不当拒绝丢弃会话），同键重放后只存在一个任务', async () => {
    const user = userEvent.setup();
    // 服务端实际已创建，但回执是 504——客户端不得据此丢弃提交会话
    const createTask = vi
      .fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new ApiError(504, 'upstream_timeout', '网关超时', 'req-504'))
      .mockResolvedValueOnce({id: 'task-dedup-1'});
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '汇总两地区销售清单');
    await user.click(screen.getByRole('button', {name: '创建任务'}));

    const alert = await screen.findByRole('alert');
    // 结论必须是「结果未知」而非「服务端拒绝」；保留幂等键、body 与上传状态
    expect(alert).toHaveTextContent('提交结果未知');
    expect(alert).toHaveTextContent('不证明创建未被受理');
    expect(alert).toHaveTextContent('HTTP 504');
    expect(alert).toHaveTextContent('upstream_timeout');
    expect(alert).toHaveTextContent('req-504');
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('汇总两地区销售清单');

    // 显式同键重放：相同幂等键 + 相同 body；服务端按键去重，仍只存在一个任务
    await user.click(screen.getByRole('button', {name: /同一请求重试/}));
    expect(await screen.findByRole('status')).toHaveTextContent('任务 ID：task-dedup-1');
    expect(createTask).toHaveBeenCalledTimes(2);
    expect(createTask.mock.calls[0]?.[0]).toEqual(createTask.mock.calls[1]?.[0]);
    expect(api.createInput).not.toHaveBeenCalled();
  });

  it('UI-01：501 unsupported_operation 是明确拒绝，不提供同键重放', async () => {
    const user = userEvent.setup();
    const createTask = vi
      .fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new ApiError(501, 'unsupported_operation', '不支持', 'req-501'));
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    await user.click(screen.getByRole('button', {name: '创建任务'}));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('服务端拒绝了本次创建');
    expect(alert).toHaveTextContent('HTTP 501');
    expect(screen.queryByRole('button', {name: /同一请求重试/})).toBeNull();
  });

  it('结果未知时锁住原草稿，不允许改正文换键造成重复任务', async () => {
    const user = userEvent.setup();
    const createTask = vi
      .fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new TypeError('lost response'))
      .mockResolvedValueOnce({id: 'task-12'});
    const api = makeApi({createTask});
    renderComposer(api);

    await user.type(screen.getByLabelText(/需求内容/), '生成对账报告');
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await screen.findByRole('alert');
    expect(screen.getByRole('button', {name: /同一请求重试/})).toBeInTheDocument();

    await user.type(screen.getByLabelText(/需求内容/), '并补充复核');
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('生成对账报告');
    expect(screen.getByLabelText(/需求内容/)).toBeDisabled();

    await user.click(screen.getByRole('button', {name: '创建任务'}));
    expect(createTask).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', {name: /同一请求重试/}));
    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(2));
    const keys = createTask.mock.calls.map(call => call[0].idempotencyKey);
    expect(keys[0]).toEqual(keys[1]);
    expect(createTask.mock.calls[1]?.[0].intent).toBe('生成对账报告');
  });

  it('SPA 离开后返回保留未知创建与附件会话，原键重放且不重复上传', async () => {
    const user = userEvent.setup();
    const api = makeApi({createTask: vi.fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new TypeError('lost response')).mockResolvedValueOnce({id: 'task-recovered'})});
    const shell = (visible: boolean) => <MemoryRouter><LogicalActionScope session={api}>
      {visible ? <TaskNewComposer api={api} /> : <p>其他页面</p>}
    </LogicalActionScope></MemoryRouter>;
    const view = render(shell(true));
    await user.type(screen.getByLabelText(/需求内容/), '恢复创建请求');
    await user.upload(screen.getByLabelText('附件输入'), new File(['hello'], 'a.txt'));
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await screen.findByRole('alert');
    view.rerender(shell(false));
    expect(screen.getByRole('status', {name: '本次连接未决操作'})).toHaveTextContent('结果未知');
    view.rerender(shell(true));
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('恢复创建请求');
    await user.click(screen.getByRole('button', {name: /同一请求重试/}));
    expect(await screen.findByText(/任务 ID：task-recovered/)).toBeInTheDocument();
    expect(api.createInput).toHaveBeenCalledTimes(1);
    expect(api.createTask).toHaveBeenCalledTimes(2);
    expect(api.createTask.mock.calls[0]?.[0]).toEqual(api.createTask.mock.calls[1]?.[0]);
  });

  it('提交中切页后回执仍保留；切换连接清草稿且旧响应不写新连接', async () => {
    const user = userEvent.setup();
    let finish: (value: {id: string}) => void = () => {};
    const api = makeApi({createTask: vi.fn(() => new Promise<{id: string}>(resolve => { finish = resolve; }))});
    const other = makeApi();
    const shell = (connection: CreateTaskApi, visible: boolean) => <MemoryRouter><LogicalActionScope session={connection}>
      {visible ? <TaskNewComposer api={connection} /> : <p>其他页面</p>}
    </LogicalActionScope></MemoryRouter>;
    const view = render(shell(api, true));
    await user.type(screen.getByLabelText(/需求内容/), '进行中的请求');
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await waitFor(() => expect(api.createTask).toHaveBeenCalledTimes(1));
    view.rerender(shell(api, false));
    finish({id: 'task-in-flight'});
    await waitFor(() => expect(screen.queryByRole('status', {name: '本次连接未决操作'})).toBeNull());
    view.rerender(shell(api, true));
    expect(screen.getByText(/任务 ID：task-in-flight/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', {name: '再新建一个任务'}));
    await user.type(screen.getByLabelText(/需求内容/), '旧连接第二个请求');
    await user.click(screen.getByRole('button', {name: '创建任务'}));
    await waitFor(() => expect(api.createTask).toHaveBeenCalledTimes(2));
    view.rerender(shell(other, true));
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('');
    finish({id: 'task-old-session'});
    await waitFor(() => expect(screen.getByRole('button', {name: '创建任务'})).toBeEnabled());
    expect(screen.queryByText(/task-old-session/)).toBeNull();
    expect(other.createTask).not.toHaveBeenCalled();
  });

  it('当前通道未接入创建能力时明确说明并禁用提交', async () => {
    const user = userEvent.setup();
    renderComposer(null);
    expect(screen.getByText('当前浏览器通道尚未接入创建操作')).toBeInTheDocument();
    const submit = screen.getByRole('button', {name: '创建任务'});
    expect(submit).toBeDisabled();
    // 草稿仍可填写
    await user.type(screen.getByLabelText(/需求内容/), '先起草的需求');
    expect(screen.getByLabelText(/需求内容/)).toHaveValue('先起草的需求');
    expect(submit).toBeDisabled();
  });
});
