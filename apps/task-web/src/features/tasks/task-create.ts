// 创建任务的纯逻辑层：客户端校验与 OpenAPI createTask/input_create 合同一致，并定义创建能力 seam。
// 背景：冻结的浏览器 Transport（W3 范围）尚未暴露 createTask/createInput，本文件按 OpenAPI 形状定义
// CreateTaskApi，页面经 resolveCreateTaskApi 探测；未接入时显式禁用提交，不假称成功（见 prd P03/E04/E05）。
// 幂等：每个逻辑提交动作用 newIdempotencyKey 生成一次 key 并随会话持有到完结；显式重放复用同一 key/同一 body。
import {newIdempotencyKey} from '../../lib/transport/types';

/** 与 packages/task-api/openapi.json 对齐的客户端上限（CreateTask/Context/CreateInput）。 */
export const TASK_INTENT_MAX_BYTES = 8192;
export const TASK_CONTEXT_TEXT_MAX_BYTES = 32768;
export const TASK_INPUT_MAX_BYTES = 256 * 1024;
export const TASK_INPUT_MAX_COUNT = 32;
export const TASK_INPUT_NAME_MAX_BYTES = 255;

export interface CreateTaskContext {
  text?: string;
  inputRefs?: string[];
}

/** mutation body 携带必需 idempotencyKey（客户端装进 Idempotency-Key 头，见 transport 契约补丁）。 */
export interface CreateInputBody {
  name: string;
  mediaType: string;
  contentBase64: string;
  idempotencyKey: string;
}

export interface CreateTaskBody {
  intent: string;
  context?: CreateTaskContext;
  idempotencyKey: string;
}

export interface CreatedTaskRef {
  id: string;
  revision?: string | number;
  status?: string;
}

export interface CreatedInputRef {
  id: string;
}

/** W1/W3 衔接 seam：W3 在冻结 Transport 上按同名形状补齐后即自动接入。 */
export interface CreateTaskApi {
  createInput(body: CreateInputBody): Promise<CreatedInputRef>;
  createTask(body: CreateTaskBody): Promise<CreatedTaskRef>;
}

export function resolveCreateTaskApi(candidate: unknown): CreateTaskApi | null {
  if (candidate === null || typeof candidate !== 'object') return null;
  const probe = candidate as Record<string, unknown>;
  if (typeof probe['createTask'] !== 'function') return null;
  const inputMethod = typeof probe['createInput'] === 'function' ? probe['createInput'] : probe['inputCreate'];
  if (typeof inputMethod !== 'function') return null;
  const target = candidate as {createTask: CreateTaskApi['createTask']};
  const input = inputMethod as CreateTaskApi['createInput'];
  return {
    createInput: body => input.call(candidate, body),
    createTask: body => target.createTask.call(candidate, body),
  };
}

export function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length;
}

/** 孤立代理项（well-formed Unicode 要求的反面）：低代理项不跟在高代理项后、高代理项未被低代理项闭合。 */
const LONE_SURROGATE = new RegExp(
  '(?:^|[^\\uD800-\\uDBFF])[\\uDC00-\\uDFFF]|[\\uD800-\\uDBFF](?:$|[^\\uDC00-\\uDFFF])',
);
const NUL_CHAR = '\u0000';

export function validateIntentText(intent: string): string | null {
  if (intent.length === 0 || intent.trim() === '') return '需求内容为必填项，请描述要交付的业务需求。';
  if (intent.includes(NUL_CHAR)) return '需求内容包含 NUL 字符，服务端合同不允许。';
  if (LONE_SURROGATE.test(intent)) return '需求内容包含孤立代理项字符，合同要求 well-formed Unicode。';
  const bytes = utf8Bytes(intent);
  if (bytes > TASK_INTENT_MAX_BYTES) {
    return '需求内容超出服务端合同上限 ' + TASK_INTENT_MAX_BYTES + ' 字节（当前 ' + bytes + ' 字节），请精简。';
  }
  return null;
}

export interface ContextParseOk {
  ok: true;
  /** 规范化后的 context；无有效内容时为空对象（调用方决定是否携带）。 */
  context: CreateTaskContext;
}

export interface ContextParseFail {
  ok: false;
  error: string;
}

export function parseContextJson(raw: string): ContextParseOk | ContextParseFail {
  const trimmed = raw.trim();
  if (trimmed === '') return {ok: true, context: {}};
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (error) {
    return {ok: false, error: '上下文不是合法 JSON：' + (error instanceof Error ? error.message : '解析失败')};
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return {ok: false, error: '上下文合同是 JSON 对象（Context），不允许数组或标量。'};
  }
  const source = parsed as Record<string, unknown>;
  const allowed = new Set(['text', 'inputRefs']);
  const extra = Object.keys(source).filter(key => !allowed.has(key));
  if (extra.length > 0) {
    return {ok: false, error: '上下文含合同外字段：' + extra.join('、') + '（Context 仅支持 text/inputRefs）。'};
  }
  const context: CreateTaskContext = {};
  if ('text' in source && source['text'] !== undefined) {
    if (typeof source['text'] !== 'string') return {ok: false, error: 'context.text 必须是字符串。'};
    const text = source['text'];
    if (text.includes(NUL_CHAR)) return {ok: false, error: 'context.text 包含 NUL 字符，服务端合同不允许。'};
    const bytes = utf8Bytes(text);
    if (bytes > TASK_CONTEXT_TEXT_MAX_BYTES) {
      return {ok: false, error: 'context.text 超出服务端合同上限 ' + TASK_CONTEXT_TEXT_MAX_BYTES + ' 字节（当前 ' + bytes + ' 字节）。'};
    }
    context.text = text;
  }
  if ('inputRefs' in source && source['inputRefs'] !== undefined) {
    if (!Array.isArray(source['inputRefs']) || !source['inputRefs'].every(item => typeof item === 'string')) {
      return {ok: false, error: 'context.inputRefs 必须是字符串数组（POST /v1/inputs 返回的 id）。'};
    }
    context.inputRefs = [...(source['inputRefs'] as string[])];
  }
  return {ok: true, context};
}

export interface ComposerFile {
  file: File;
  name: string;
  size: number;
  /** 原始媒体型；值不合法时上传前回退 application/octet-stream。 */
  type: string;
}

const MEDIA_TYPE_PATTERN = /^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/;

export function mediaTypeFor(f: ComposerFile): string {
  const raw = f.type.trim().toLowerCase();
  return MEDIA_TYPE_PATTERN.test(raw) && raw.length <= 128 ? raw : 'application/octet-stream';
}

/** 返回第一条违规说明；全部通过返回 null。文案与服务端合同数字一致。 */
export function validateSelectedFiles(files: ComposerFile[]): string | null {
  if (files.length > TASK_INPUT_MAX_COUNT) {
    return '附件最多 ' + TASK_INPUT_MAX_COUNT + ' 个引用（当前 ' + files.length + ' 个），请减少后再提交。';
  }
  for (const f of files) {
    if (f.size > TASK_INPUT_MAX_BYTES) {
      return '附件「' + f.name + '」解码后 ' + f.size + ' 字节，超过单 input ' + TASK_INPUT_MAX_BYTES + ' 字节（256 KiB）上限。';
    }
    if (f.name.trim() === '' || f.name.includes(NUL_CHAR)) {
      return '附件名不能为空且不允许 NUL 字符。';
    }
    if (utf8Bytes(f.name) > TASK_INPUT_NAME_MAX_BYTES) {
      return '附件名「' + f.name + '」超过 255 字节上限，请重命名后再选。';
    }
  }
  return null;
}

export function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error('读取附件失败：' + file.name));
    reader.onload = () => {
      const bytes = new Uint8Array(reader.result as ArrayBuffer);
      let binary = '';
      const CHUNK = 0x8000;
      for (let offset = 0; offset < bytes.length; offset += CHUNK) {
        binary += String.fromCharCode(...bytes.subarray(offset, offset + CHUNK));
      }
      resolve(btoa(binary));
    };
    reader.readAsArrayBuffer(file);
  });
}

/** 一个逻辑提交动作的幂等 key 集：任务一个、每个附件一个；显式重放整组复用。 */
export interface SubmissionKeys {
  taskKey: string;
  inputKeys: string[];
}

export interface SubmissionSession {
  keys: SubmissionKeys;
  /** 已确认的 inputRef（上传成功后写入）；index 与所选附件对齐，null 表示结果未知/未完成。 */
  inputRefs: (string | null)[];
}

export function newSubmissionSession(fileCount: number): SubmissionSession {
  return {
    keys: {
      taskKey: newIdempotencyKey(),
      inputKeys: Array.from({length: fileCount}, () => newIdempotencyKey()),
    },
    inputRefs: Array.from({length: fileCount}, () => null),
  };
}

export interface CreateTaskDraft {
  intent: string;
  /** 规范化后的 context（可含用户手写 inputRefs）；允许为空对象。 */
  context: CreateTaskContext;
  files: ComposerFile[];
}

export interface SubmissionProgress {
  /** 正在上传的附件序号（1 起）与总数；没有附件或上传完成后为 null。 */
  uploading: {index: number; total: number} | null;
}

/**
 * 完整执行一次逻辑提交：逐个上传附件（已知成功的跳过）、合并 inputRefs、创建任务。
 * 不做任何自动重试；结果未知（网络层失败）由调用方保留 session 供显式同键重放。
 */
export async function runSubmission(
  api: CreateTaskApi,
  draft: CreateTaskDraft,
  session: SubmissionSession,
  onProgress?: (progress: SubmissionProgress) => void,
): Promise<{taskId: string}> {
  const refs: string[] = draft.context.inputRefs ? [...draft.context.inputRefs] : [];
  const total = draft.files.length;
  for (let index = 0; index < total; index += 1) {
    const known = session.inputRefs[index];
    if (known !== null && known !== undefined) {
      refs.push(known);
    } else {
      onProgress?.({uploading: {index: index + 1, total}});
      const f = draft.files[index]!;
      const contentBase64 = await fileToBase64(f.file);
      const created = await api.createInput({
        name: f.name,
        mediaType: mediaTypeFor(f),
        contentBase64,
        idempotencyKey: session.keys.inputKeys[index] ?? session.keys.taskKey,
      });
      session.inputRefs[index] = created.id;
      refs.push(created.id);
    }
  }
  onProgress?.({uploading: null});
  const context: CreateTaskContext = {...draft.context};
  if (refs.length > 0) {
    if (refs.length > TASK_INPUT_MAX_COUNT) {
      throw new Error('inputRefs 合计 ' + refs.length + ' 个，超过服务端合同上限 ' + TASK_INPUT_MAX_COUNT + ' 个。');
    }
    context.inputRefs = refs;
  }
  const body: CreateTaskBody = {intent: draft.intent, idempotencyKey: session.keys.taskKey};
  if (context.text !== undefined || context.inputRefs !== undefined) body.context = context;
  const created = await api.createTask(body);
  return {taskId: created.id};
}
