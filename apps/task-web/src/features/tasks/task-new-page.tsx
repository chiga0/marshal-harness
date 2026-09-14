// 新建任务（P03）：客户端边界校验与 OpenAPI createTask/input_create 一致（intent 8192B、context.text 32768B、
// 单 input 解码后 256 KiB、最多 32 个 inputRefs）；提交中防重复；失败保留草稿与错误码/requestId；
// 结果未知时在本次连接内保留草稿和原请求，SPA 切换不丢同键重放；刷新或断开不承诺保留。
import {useMemo, useState} from 'react';
import type {ChangeEvent, FormEvent} from 'react';
import {Link} from 'react-router-dom';
import {Paperclip, X} from 'lucide-react';
import {ApiError} from '../../lib/transport/types';
import {useConnection} from '../connection/connection';
import {Alert} from '../../components/ui/alert';
import {Button} from '../../components/ui/button';
import {Card} from '../../components/ui/card';
import {Input} from '../../components/ui/input';
import {Label} from '../../components/ui/label';
import {Textarea} from '../../components/ui/textarea';
import {
  TASK_CONTEXT_TEXT_MAX_BYTES, TASK_INPUT_MAX_COUNT, TASK_INPUT_MAX_BYTES, TASK_INTENT_MAX_BYTES,
  newSubmissionSession, parseContextJson, resolveCreateTaskApi, runSubmission,
  utf8Bytes, validateIntentText, validateSelectedFiles, validateInputReferenceCount,
} from './task-create';
import type {ComposerFile, CreateTaskApi, CreateTaskDraft, SubmissionSession} from './task-create';
import {isAmbiguousFailure, useLogicalAction, useLogicalActionMemory} from './detail/shared/logical-action';
import {formatBytes} from './format';

interface SubmissionError {
  kind: 'api' | 'unknown';
  title: string;
  detail: string;
}

function describeSubmissionError(error: unknown): SubmissionError {
  if (error instanceof ApiError) {
    if (error.code === 'idempotency_conflict') {
      return {
        kind: 'api',
        title: '服务端记录了相同幂等键但内容不同的请求（idempotency_conflict）',
        detail: '说明同一请求曾被受理过。请到任务列表核对原任务及其回执；本页不会用相同键再发，也不会自动新建替代任务。'
          + (error.requestId ? ' requestId：' + error.requestId : ''),
      };
    }
    // UI-01：5xx（除 501）/网关类应答不证明服务端未创建——属结果未知，保留会话、body、上传状态与幂等键。
    if (isAmbiguousFailure(error)) {
      const parts = ['服务端返回 ' + error.code + '（HTTP ' + error.status + '）'];
      if (error.message && error.message !== error.code) parts.push(error.message);
      if (error.requestId) parts.push('requestId：' + error.requestId);
      return {
        kind: 'unknown',
        title: '提交结果未知：服务端错误应答不证明创建未被受理',
        detail: parts.join('；') + '。请求可能已被受理并创建了任务，请不要凭猜测再建。页面存活期间可用下方「同一请求重试」'
          + '按相同幂等键与相同内容重放（不自动发送）：服务端按幂等键去重，重放后仍只存在一个任务；'
          + '已上传完成的附件按会话记录跳过，不会重复上传。也可以先到任务列表核对回执再决定。',
      };
    }
    const parts = ['错误码 ' + error.code + '（HTTP ' + error.status + '）'];
    if (error.message && error.message !== error.code) parts.push(error.message);
    if (error.requestId) parts.push('requestId：' + error.requestId);
    if (error.isUnauthorized) parts.push('凭据可能已失效，请在核对后重新连接。');
    return {kind: 'api', title: '服务端拒绝了本次创建', detail: parts.join('；') + '。草稿已保留，修改后提交将使用新的幂等键。'};
  }
  if (error instanceof Error) {
    return {
      kind: 'unknown',
      title: '提交结果未知：请求已发出但没有收到回执',
      detail: '服务端可能已经受理。重连或刷新后请先在任务列表核对，不要凭猜测再建；页面存活期间可用下方「同一请求重试」按相同幂等键与相同内容重放（不自动发送）。原始信息：' + error.message,
    };
  }
  return {kind: 'unknown', title: '提交结果未知', detail: '未收到服务端回执；请先核对任务列表。'};
}

interface FieldErrors {
  intent?: string | undefined;
  context?: string | undefined;
  files?: string | undefined;
}

export interface TaskNewComposerProps {
  /** 创建能力；null 表示当前传输未接入（W3 浏览器接入前），提交保持禁用且不假称成功。 */
  api: CreateTaskApi | null;
}

export function TaskNewComposer({api}: TaskNewComposerProps) {
  const [intent, setIntent] = useLogicalActionMemory(['task.create', 'intent'], () => '');
  const [contextText, setContextText] = useLogicalActionMemory(['task.create', 'contextText'], () => '');
  const [contextMode, setContextMode] = useLogicalActionMemory<'text' | 'json'>(['task.create', 'contextMode'], () => 'text');
  const [contextRaw, setContextRaw] = useLogicalActionMemory(['task.create', 'context'], () => '');
  const [files, setFiles] = useLogicalActionMemory<ComposerFile[]>(['task.create', 'files'], () => []);
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [createdTaskId, setCreatedTaskId, isSessionActive] = useLogicalActionMemory<string | null>(['task.create', 'receipt'], () => null);
  const [uploadProgress, setUploadProgress] = useLogicalActionMemory<{index: number; total: number} | null>(['task.create', 'upload'], () => null);
  const action = useLogicalAction(['task.create'], ['task.create']);
  const submitError = action.phase.kind === 'unknown' || action.phase.kind === 'rejected'
    ? describeSubmissionError(action.phase.error) : null;

  const intentBytes = useMemo(() => utf8Bytes(intent), [intent]);

  const execute = (draft: CreateTaskDraft, session: SubmissionSession) => async () => {
      if (!api) throw new Error('当前传输未接入创建能力');
      // 上传是一串异步请求。旧连接消失后，不只禁止写回 UI，还须禁止后续上传/创建。
      // 检查放在每次实际 API 调用前，覆盖读取附件与前一上传等待期间的连接切换。
      const assertSession = () => {
        if (!isSessionActive()) throw new Error('原连接已断开；未继续发送后续请求，请核对已发送请求的结果');
      };
      const scopedApi: CreateTaskApi = {
        createInput: body => { assertSession(); return api.createInput(body); },
        createTask: body => { assertSession(); return api.createTask(body); },
      };
      try {
        const result = await runSubmission(scopedApi, draft, session, progress => setUploadProgress(progress.uploading));
        setCreatedTaskId(result.taskId);
        return result;
      } finally {
        setUploadProgress(null);
      }
  };

  const busy = action.phase.kind === 'submitting';
  const unresolved = busy || action.phase.kind === 'unknown';

  const addFiles = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = event.target.files;
    if (!picked) return;
    const next: ComposerFile[] = [...files];
    for (const file of Array.from(picked)) {
      if (next.some(existing => existing.file === file)) continue;
      next.push({file, name: file.name, size: file.size, type: file.type});
    }
    setFiles(next);
    setFieldErrors(previous => ({...previous, files: undefined}));
    event.target.value = '';
  };

  const removeFile = (index: number) => {
    setFiles(previous => previous.filter((_, i) => i !== index));
  };

  const resetForAnother = () => {
    setIntent('');
    setContextRaw('');
    setContextText('');
    setContextMode('text');
    setFiles([]);
    setFieldErrors({});
    setCreatedTaskId(null);
    action.reset();
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (!api || unresolved) return;
    setCreatedTaskId(null);

    const errors: FieldErrors = {};
    const intentError = validateIntentText(intent);
    if (intentError) errors.intent = intentError;
    const parsed = parseContextJson(contextMode === 'json' ? contextRaw : JSON.stringify(contextText ? {text: contextText} : {}));
    if (!parsed.ok) errors.context = parsed.error;
    const filesError = validateSelectedFiles(files);
    if (filesError) errors.files = filesError;
    if (parsed.ok) {
      const countError = validateInputReferenceCount(parsed.context.inputRefs?.length ?? 0, files.length);
      if (countError) errors.context = countError;
    }
    setFieldErrors(errors);
    if (errors.intent || errors.context || errors.files) return;

    const draft: CreateTaskDraft = {
      intent,
      context: parsed.ok ? parsed.context : {},
      files,
    };
    const session = newSubmissionSession(files.length);
    if (action.phase.kind === 'rejected') action.reset();
    void action.submit(execute(draft, session));
  };

  const replaySameRequest = () => {
    if (!api || busy || action.phase.kind !== 'unknown') return;
    // registry 仅重放第一次提交时冻结的闭包（含上传进度与原始幂等键）。
    void action.replay(async () => { throw new Error('缺少原请求，不允许重新构造'); });
  };

  const canReplay = action.phase.kind === 'unknown';

  if (createdTaskId !== null) {
    return (
      <section aria-label="新建任务" className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-6 p-4 sm:p-8 lg:py-12">
        <h1 className="text-[22px] font-semibold leading-[30px]">新建任务</h1>
        <Alert variant="success" role="status" title="任务已创建">
          <p>前往任务工作台，查看团队计划和执行进展。</p><details className="mt-2 text-xs"><summary className="cursor-pointer">创建回执</summary><span className="break-all">任务 ID：{createdTaskId}</span></details>
          <div className="mt-3 flex flex-wrap gap-2">
            <Link
              to={'/tasks/' + encodeURIComponent(createdTaskId)}
              className="inline-flex h-9 select-none items-center justify-center gap-2 rounded-md bg-accent px-3 text-[13px] font-medium leading-5 text-accent-foreground transition-colors hover:bg-accent/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2"
            >
              查看任务详情
            </Link>
            <Button variant="outline" size="sm" onClick={resetForAnother}>再新建一个任务</Button>
            <Link to="/" className="inline-flex h-9 items-center px-2 text-[13px] leading-5 text-accent underline-offset-4 hover:underline">
              返回任务列表
            </Link>
          </div>
        </Alert>
      </section>
    );
  }

  return (
    <section aria-label="新建任务" className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-6 p-4 sm:p-8 lg:py-12">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-[22px] font-semibold leading-[30px]">新建任务</h1>
        <Link to="/" className="text-sm leading-[22px] text-accent underline-offset-4 hover:underline">返回任务列表</Link>
      </div>

      {api === null ? (
        <Alert variant="warning" title="当前浏览器通道尚未接入创建操作">
          冻结的浏览器传输层（W3 浏览器接入范围）还没有暴露 createTask/createInput。草稿可以先填写，
          但提交保持禁用；接入完成前本页不会假装提交成功。
        </Alert>
      ) : null}

      {submitError ? (
        <Alert variant={submitError.kind === 'unknown' ? 'warning' : 'danger'} title={submitError.title}>
          {submitError.detail}
          {submitError.kind === 'unknown' && canReplay ? (
            <div className="mt-3">
              <Button variant="outline" size="sm" onClick={replaySameRequest} disabled={busy}>
                同一请求重试（沿用相同幂等键与相同内容）
              </Button>
            </div>
          ) : null}
        </Alert>
      ) : null}

      <form onSubmit={onSubmit} noValidate>
        <fieldset disabled={unresolved} className="min-w-0 space-y-5 disabled:opacity-90">
          <Card>
            <Label
              htmlFor="task-intent"
              required
              hint={'服务端合同上限 ' + TASK_INTENT_MAX_BYTES + ' 字节 UTF-8（当前 ' + intentBytes + ' 字节）。'}
            >
              需求内容
            </Label>
            <Textarea
              id="task-intent"
              rows={6}
              value={intent}
              onChange={event => {
                setIntent(event.target.value);
                setFieldErrors(previous => ({...previous, intent: undefined}));
              }}
              placeholder="描述需要团队交付的业务需求，例如：按窗口汇总两个地区的销售清单并独立复核。"
              aria-invalid={fieldErrors.intent !== undefined}
              aria-describedby={fieldErrors.intent !== undefined ? 'task-intent-error' : undefined}
            />
            {fieldErrors.intent !== undefined ? (
              <p id="task-intent-error" role="alert" className="mt-1 text-sm leading-[22px] text-danger">{fieldErrors.intent}</p>
            ) : null}
          </Card>

          <Card>
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2"><span className="text-sm font-medium">补充背景</span><div className="flex gap-1" role="group" aria-label="上下文输入方式"><Button size="sm" variant={contextMode === 'text' ? 'secondary' : 'ghost'} aria-pressed={contextMode === 'text'} onClick={() => setContextMode('text')}>文本</Button><Button size="sm" variant={contextMode === 'json' ? 'secondary' : 'ghost'} aria-pressed={contextMode === 'json'} onClick={() => setContextMode('json')}>高级 JSON</Button></div></div>
            {contextMode === 'text' ? <><Label htmlFor="task-context-text" hint={`可选，最多 ${TASK_CONTEXT_TEXT_MAX_BYTES} 字节。`}>附加上下文</Label><Textarea id="task-context-text" rows={4} value={contextText} onChange={event => {setContextText(event.target.value); setFieldErrors(previous => ({...previous,context:undefined}));}} placeholder="补充数据口径、参考资料、限制条件或验收要求…" aria-invalid={fieldErrors.context !== undefined} aria-describedby={fieldErrors.context ? 'task-context-error' : undefined} /></> : <><Label htmlFor="task-context" hint='仅支持 text 与 inputRefs。切换方式保留两份草稿，仅提交当前方式。'>附加上下文（JSON）</Label><Textarea id="task-context" rows={4} value={contextRaw} onChange={event => {setContextRaw(event.target.value); setFieldErrors(previous => ({...previous,context:undefined}));}} placeholder='{"text": "补充说明"}' spellCheck={false} className="font-mono" aria-invalid={fieldErrors.context !== undefined} aria-describedby={fieldErrors.context ? 'task-context-error' : undefined} /></>}
            {fieldErrors.context !== undefined ? <p id="task-context-error" role="alert" className="mt-1 text-sm text-danger">{fieldErrors.context}</p> : null}
          </Card>

          <Card>
            <Label
              htmlFor="task-files"
              hint={'可选。每个附件解码后 ≤ ' + formatBytes(TASK_INPUT_MAX_BYTES) + '，最多 ' + TASK_INPUT_MAX_COUNT + ' 个附件与已有输入引用。'}
            >
              附件输入
            </Label>
            <Input
              id="task-files"
              type="file"
              multiple
              onChange={addFiles}
              aria-invalid={fieldErrors.files !== undefined}
              aria-describedby={fieldErrors.files !== undefined ? 'task-files-error' : undefined}
              className="h-auto cursor-pointer py-2 file:mr-3 file:rounded-md file:border-0 file:bg-surface-muted file:px-3 file:py-1.5 file:text-sm file:text-text-primary"
            />
            {files.length > 0 ? (
              <ul aria-label="已选附件" className="mt-3 space-y-1">
                {files.map((f, index) => (
                  <li key={index} className="flex items-center justify-between gap-2 rounded-md border border-border bg-surface-muted px-3 py-1.5">
                    <span className="flex min-w-0 items-center gap-2 text-sm leading-[22px]">
                      <Paperclip aria-hidden className="h-3.5 w-3.5 shrink-0 text-text-secondary" />
                      <span className="break-all">{f.name}</span>
                      <span className="shrink-0 text-xs text-text-secondary">{formatBytes(f.size)}</span>
                    </span>
                    <Button variant="ghost" size="sm" className="h-7 px-2" onClick={() => removeFile(index)} aria-label={'移除附件 ' + f.name}>
                      <X aria-hidden className="h-4 w-4" />
                    </Button>
                  </li>
                ))}
              </ul>
            ) : null}
            {fieldErrors.files !== undefined ? (
              <p id="task-files-error" role="alert" className="mt-1 text-sm leading-[22px] text-danger">{fieldErrors.files}</p>
            ) : null}
          </Card>

          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" loading={busy} disabled={!api || unresolved}>
              {busy
                ? uploadProgress
                  ? '上传附件 ' + uploadProgress.index + '/' + uploadProgress.total + '…'
                  : '正在创建任务…'
                : '创建任务'}
            </Button>
            <p className="text-xs leading-[18px] text-text-secondary">
              团队会先整理执行计划；需要确认时会在任务中提醒你。
            </p>
          </div>
        </fieldset>
      </form>
    </section>
  );
}

export function TaskNewPage() {
  const {transport} = useConnection();
  const api = useMemo(() => resolveCreateTaskApi(transport), [transport]);
  return <TaskNewComposer api={api} />;
}
