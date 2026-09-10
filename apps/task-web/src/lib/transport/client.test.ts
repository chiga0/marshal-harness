import {afterEach, describe, expect, it, vi} from 'vitest';
import {ApiError} from './types';
import {clearToken, createTransport, installToken} from './client';

function mockResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {status, headers: {'Content-Type': 'application/json'}});
}

describe('transport', () => {
  afterEach(() => clearToken());

  it('拒绝无 token 的请求', async () => {
    const transport = createTransport({token: ''});
    await expect(transport.listTasks({limit: 1})).rejects.toMatchObject({status: 401, code: 'token_missing'});
  });

  it('按固定 origin 加 Bearer 调用并解析列表', async () => {
    installToken('t-123');
    const spy = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe('/v1/tasks?limit=2');
      return mockResponse(200, {items: [], nextCursor: null});
    });
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    const response = await transport.listTasks({limit: 2});
    expect(response.items).toEqual([]);
    expect(spy).toHaveBeenCalledTimes(1);
    const init = (spy.mock.calls[0] as unknown as [unknown, RequestInit | undefined] | undefined)?.[1];
    expect((init?.headers as Record<string, string> | undefined)?.Authorization).toBe('Bearer t-123');
    expect((init?.headers as Record<string, string> | undefined)?.Accept).toBe('application/json');
  });

  it('解析失败响应的请求级 ApiError 且区分 401', async () => {
    installToken('t-123');
    const fetchLike = vi.fn(async () => mockResponse(401, {code: 'unauthorized', requestId: 'req-1'})) as unknown as typeof fetch;
    const transport = createTransport({token: 't-123', fetchLike});
    const error = await transport.listTasks({limit: 1}).then(() => null).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).isUnauthorized).toBe(true);
    expect((error as ApiError).code).toBe('unauthorized');
    expect((error as ApiError).requestId).toBe('req-1');
  });

  it('mutation 把 idempotencyKey 提升到头部且不出现在 JSON body 中', async () => {
    installToken('t-123');
    const spy = vi.fn(async () => mockResponse(202, {accepted: true}));
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    await transport.cancelTask('task-x', {expectedRevision: 4, idempotencyKey: 'ikey-abc'});
    const init = (spy.mock.calls[0] as unknown as [unknown, RequestInit | undefined] | undefined)?.[1];
    expect((init?.headers as Record<string, string> | undefined)?.['Idempotency-Key']).toBe('ikey-abc');
    expect(JSON.parse(String(init?.body))).toEqual({expectedRevision: 4});
  });

  it('leaderReply 只向 requests/:id/reply 提交合同字段', async () => {
    installToken('t-123');
    const spy = vi.fn(async () => mockResponse(202, {accepted: true}));
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    await transport.leaderReply('task-x', 'req-1', {expectedRevision: 2, requestDigest: 'sha256:aa', answer: 'north', idempotencyKey: 'ikey-b'});
    const call = spy.mock.calls[0] as unknown as [unknown, RequestInit | undefined] | undefined;
    expect(String(call?.[0])).toBe('/v1/tasks/task-x/leader/requests/req-1/reply');
    const body = JSON.parse(String(call?.[1]?.body));
    expect(body).toMatchObject({expectedRevision: 2, requestDigest: 'sha256:aa', answer: 'north'});
    expect(body).not.toHaveProperty('idempotencyKey');
  });

  it('answerTask 运行时分支：branch 不下送、questionDigest 保留', async () => {
    installToken('t-123');
    const spy = vi.fn(async () => mockResponse(202, {accepted: true}));
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    await transport.answerTask('task-x', 'question-1', {
      branch: 'runtime', expectedRevision: 3, questionRevision: 1, questionDigest: 'sha256:bb', answer: '2026-09-01', idempotencyKey: 'ikey-c',
    });
    const call = spy.mock.calls[0] as unknown as [unknown, RequestInit | undefined] | undefined;
    expect(String(call?.[0])).toBe('/v1/tasks/task-x/questions/question-1/answers');
    const body = JSON.parse(String(call?.[1]?.body));
    expect(body).toEqual({expectedRevision: 3, questionRevision: 1, questionDigest: 'sha256:bb', answer: '2026-09-01'});
  });

  it('UI-03：默认 fetch 经闭包调用，receiver 为 undefined（模拟严格浏览器）', async () => {
    installToken('t-123');
    let receiver: unknown = 'not-called';
    const original = globalThis.fetch;
    // 严格浏览器语义：window.fetch 以非 Window 接收者调用即抛 TypeError
    globalThis.fetch = function (this: unknown, ...args: Parameters<typeof fetch>) {
      receiver = this;
      if (this !== undefined && this !== globalThis) {
        return Promise.reject(new TypeError('Illegal invocation'));
      }
      return Promise.resolve(mockResponse(200, {items: [], nextCursor: null}));
    } as typeof fetch;
    try {
      const transport = createTransport({token: 't-123'});
      const response = await transport.listTasks({limit: 1});
      expect(response.items).toEqual([]);
      expect(receiver).toBeUndefined();
    } finally {
      globalThis.fetch = original;
    }
  });

  it('UI-06：读请求默认携带 deadline 信号，且与调用方取消 signal 合并', async () => {
    installToken('t-123');
    const controller = new AbortController();
    const spy = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBeInstanceOf(AbortSignal);
      controller.abort();
      // 合并后：调用方中止必须传导到实际发出的 signal
      if (typeof AbortSignal.any === 'function') expect(init?.signal?.aborted).toBe(true);
      return mockResponse(200, {id: 'task-x'});
    });
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    await transport.getTask('task-x', {signal: controller.signal});
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it('UI-06：写请求也携带时限信号（到期中止只代表未收到回执）', async () => {
    installToken('t-123');
    const spy = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBeInstanceOf(AbortSignal);
      return mockResponse(202, {accepted: true});
    });
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    await transport.pauseTask('task-x', {expectedRevision: 1, idempotencyKey: 'ikey-w'});
    expect(spy).toHaveBeenCalledTimes(1);
  });
});
