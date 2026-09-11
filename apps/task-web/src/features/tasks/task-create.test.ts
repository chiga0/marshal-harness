import {describe, expect, it, vi} from 'vitest';
import {
  TASK_INPUT_MAX_COUNT,
  newSubmissionSession, parseContextJson, resolveCreateTaskApi, runSubmission,
  utf8Bytes, validateIntentText, validateSelectedFiles,
} from './task-create';
import type {ComposerFile, CreateTaskApi} from './task-create';

function composerFile(name: string, size: number, content?: Uint8Array): ComposerFile {
  const bytes = content ?? new Uint8Array(Math.min(size, 8));
  const file = new File([bytes], name, {type: 'text/plain'});
  if (file.size !== size) {
    Object.defineProperty(file, 'size', {value: size});
  }
  return {file, name, size, type: file.type};
}

describe('utf8Bytes', () => {
  it('区分单字节与多字节字符', () => {
    expect(utf8Bytes('abc')).toBe(3);
    expect(utf8Bytes('需求')).toBe(6);
  });
});

describe('validateIntentText', () => {
  it('必填：空串与纯空白都拒绝', () => {
    expect(validateIntentText('')).toMatch(/必填/);
    expect(validateIntentText('   ')).toMatch(/必填/);
  });
  it('NUL 与孤立代理项按合同拒绝', () => {
    expect(validateIntentText('需求' + String.fromCharCode(0) + '中断')).toMatch(/NUL/);
    expect(validateIntentText('需求\uD800中断')).toMatch(/孤立代理项/);
  });
  it('8192 字节按 UTF-8 计', () => {
    expect(validateIntentText('a'.repeat(8192))).toBeNull();
    expect(validateIntentText('a'.repeat(8193))).toMatch(/8192 字节/);
  });
  it('长中文合法通过', () => {
    expect(validateIntentText('汇总东、西两个地区的销售清单并独立复核。')).toBeNull();
  });
});

describe('parseContextJson', () => {
  it('空输入等价于不携带 context', () => {
    expect(parseContextJson('')).toEqual({ok: true, context: {}});
    expect(parseContextJson('   ')).toEqual({ok: true, context: {}});
  });
  it('非法 JSON 给出可读错误', () => {
    const result = parseContextJson('{bad');
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toMatch(/不是合法 JSON/);
  });
  it('数组与标量被拒绝（Context 是对象）', () => {
    for (const raw of ['[1]', '"x"', '42', 'null']) {
      const result = parseContextJson(raw);
      expect(result.ok).toBe(false);
    }
  });
  it('合同外字段被拒绝', () => {
    const result = parseContextJson('{"text": "ok", "workspace": "x"}');
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toMatch(/workspace/);
  });
  it('text 超 32768 字节被拒绝', () => {
    const result = parseContextJson('{"text": "' + 'a'.repeat(32769) + '"}');
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toMatch(/32768/);
  });
  it('合法 text/inputRefs 通过并保留', () => {
    const result = parseContextJson('{"text": "背景", "inputRefs": ["input-1"]}');
    expect(result).toEqual({ok: true, context: {text: '背景', inputRefs: ['input-1']}});
  });
  it('inputRefs 必须是字符串数组', () => {
    const result = parseContextJson('{"inputRefs": [1]}');
    expect(result.ok).toBe(false);
  });
});

describe('validateSelectedFiles', () => {
  it('最多 32 个引用', () => {
    const files = Array.from({length: TASK_INPUT_MAX_COUNT + 1}, (_, i) => composerFile('f' + i + '.txt', 1));
    expect(validateSelectedFiles(files)).toMatch(/最多 32 个引用（当前 33 个）/);
  });
  it('单 input 解码后超 256 KiB 被拒', () => {
    expect(validateSelectedFiles([composerFile('big.bin', 256 * 1024 + 1)])).toMatch(/256 KiB/);
    expect(validateSelectedFiles([composerFile('ok.bin', 256 * 1024)])).toBeNull();
  });
  it('附件名超 255 字节被拒', () => {
    expect(validateSelectedFiles([composerFile('很'.repeat(90) + '.txt', 1)])).toMatch(/255 字节/);
  });
});

describe('resolveCreateTaskApi', () => {
  it('冻结 transport 形状（无 createTask）判为未接入', () => {
    expect(resolveCreateTaskApi(null)).toBeNull();
    expect(resolveCreateTaskApi({listTasks: async () => ({tasks: [], nextCursor: null})})).toBeNull();
  });
  it('具备 createTask/createInput 时返回归一化 seam', async () => {
    const candidate = {
      createTask: vi.fn(async () => ({id: 'task-1'})),
      createInput: vi.fn(async () => ({id: 'input-1'})),
    };
    const api = resolveCreateTaskApi(candidate);
    expect(api).not.toBeNull();
    await api!.createTask({intent: 'x', idempotencyKey: 'k'});
    expect(candidate.createTask).toHaveBeenCalledWith({intent: 'x', idempotencyKey: 'k'});
  });
  it('兼容 inputCreate 命名', () => {
    const candidate = {createTask: async () => ({id: 't'}), inputCreate: async () => ({id: 'i'})};
    expect(resolveCreateTaskApi(candidate)).not.toBeNull();
  });
});

describe('runSubmission', () => {
  it.each([0, 1])('合计超限在附件读取及任何API调用前拒绝（%i附件）', async fileCount => {
    const api: CreateTaskApi = {createInput: vi.fn(), createTask: vi.fn()};
    const draft = {
      intent: 'x',
      context: {inputRefs: Array.from({length: 33 - fileCount}, (_, index) => 'input-' + index)},
      files: fileCount ? [composerFile('a.txt', 1)] : [],
    };
    const progress = vi.fn();
    const session = newSubmissionSession(fileCount);
    await expect(runSubmission(api, draft, session, progress)).rejects.toThrow('inputRefs 合计 33 个');
    expect(api.createInput).not.toHaveBeenCalled();
    expect(api.createTask).not.toHaveBeenCalled();
    expect(progress).not.toHaveBeenCalled();
    expect(session.inputRefs.every(ref => ref === null)).toBe(true);
  });

  it('逐个上传附件、合并 inputRefs、用会话 key 创建任务', async () => {
    const createInput = vi.fn<CreateTaskApi['createInput']>(async body => ({id: 'ref-' + body.name}));
    const createTask = vi.fn<CreateTaskApi['createTask']>(async () => ({id: 'task-1'}));
    const api: CreateTaskApi = {createInput, createTask};
    const session = newSubmissionSession(2);
    const progress: ({index: number; total: number} | null)[] = [];
    const result = await runSubmission(
      api,
      {
        intent: '生成报告',
        context: {text: '背景', inputRefs: ['existing-ref']},
        files: [
          {file: new File([new TextEncoder().encode('hello')], 'a.txt', {type: 'text/plain'}), name: 'a.txt', size: 5, type: 'text/plain'},
          {file: new File([new TextEncoder().encode('world!')], 'b.txt', {type: ''}), name: 'b.txt', size: 6, type: ''},
        ],
      },
      session,
      p => progress.push(p.uploading),
    );
    expect(result.taskId).toBe('task-1');
    expect(createInput.mock.calls.map(call => call[0].name)).toEqual(['a.txt', 'b.txt']);
    const createBody = createTask.mock.calls[0]?.[0];
    expect(createBody?.context?.inputRefs).toEqual(['existing-ref', 'ref-a.txt', 'ref-b.txt']);
    expect(createBody?.idempotencyKey).toBe(session.keys.taskKey);
    // 无媒体型的附件回退 application/octet-stream
    expect(createInput.mock.calls[1]?.[0].mediaType).toBe('application/octet-stream');
    expect(progress).toEqual([{index: 1, total: 2}, {index: 2, total: 2}, null]);
  });

  it('重放同一会话：已上传附件不重复上传，幂等键原样复用', async () => {
    const createInput = vi.fn<CreateTaskApi['createInput']>(async body => ({id: 'ref-' + body.name}));
    const createTask = vi.fn<CreateTaskApi['createTask']>()
      .mockRejectedValueOnce(new TypeError('lost response'))
      .mockResolvedValueOnce({id: 'task-2'});
    const api: CreateTaskApi = {createInput, createTask};
    const session = newSubmissionSession(1);
    const draft = {
      intent: '生成报告',
      context: {},
      files: [{file: new File([new TextEncoder().encode('hi')], 'a.txt', {type: 'text/plain'}), name: 'a.txt', size: 2, type: 'text/plain'}],
    };
    await expect(runSubmission(api, draft, session)).rejects.toThrow('lost response');
    const inputCallsAfterFirst = createInput.mock.calls.length;
    const result = await runSubmission(api, draft, session);
    expect(result.taskId).toBe('task-2');
    // 已上传的附件不会被再次 POST；幂等键整组复用
    expect(createInput.mock.calls.length).toBe(inputCallsAfterFirst);
    const keys = createTask.mock.calls.map(call => call[0].idempotencyKey);
    expect(keys[0]).toBe(keys[1]);
  });

  it('无 context 内容时不携带 context 字段', async () => {
    const createTask = vi.fn<CreateTaskApi['createTask']>(async () => ({id: 'task-3'}));
    const api: CreateTaskApi = {createInput: vi.fn(), createTask};
    await runSubmission(api, {intent: 'x', context: {}, files: []}, newSubmissionSession(0));
    const body = createTask.mock.calls[0]?.[0] as unknown as Record<string, unknown>;
    expect('context' in body).toBe(false);
  });
});
