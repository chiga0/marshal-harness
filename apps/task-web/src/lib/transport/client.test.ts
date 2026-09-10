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
      return mockResponse(200, {tasks: [], nextCursor: null});
    });
    const transport = createTransport({token: 't-123', fetchLike: spy as unknown as typeof fetch});
    const response = await transport.listTasks({limit: 2});
    expect(response.tasks).toEqual([]);
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
});
