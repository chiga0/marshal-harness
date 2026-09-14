import {useQuery} from '@tanstack/react-query';
import type {
  TaskAuditRecord,
  Transport,
  WorkerRecord,
} from '@/lib/transport/types';
import {observedPrompt} from '../artifacts/traceability';
import {blobBytes, fetchVerifiedArtifact} from '../artifacts/downloader';
import {taskKeys} from '../tasks/detail/query-keys';

export function WorkerPrompt({
  worker,
  audit,
  transport,
}: {
  worker: WorkerRecord;
  audit: TaskAuditRecord | null;
  transport: Transport;
}) {
  let prompt = null,
    invalid = false;
  try {
    prompt = observedPrompt(audit, worker.taskId, worker.id);
  } catch {
    invalid = true;
  }
  const snapshot = prompt?.observation?.snapshot;
  const query = useQuery({
    queryKey: [
      ...taskKeys.all(worker.taskId),
      'prompt',
      worker.id,
      snapshot?.digest,
    ],
    enabled: false,
    retry: false,
    queryFn: async () => {
      if (!snapshot) throw new Error('missing_snapshot');
      const {blob} = await fetchVerifiedArtifact(transport, {
        artifactId: snapshot.id,
        fileName: snapshot.name,
        expectedBytes: snapshot.bytes,
        expectedDigest: snapshot.digest,
      });
      return new TextDecoder('utf-8', {fatal: true}).decode(
        await blobBytes(blob),
      );
    },
  });
  return (
    <section
      className="space-y-3 border-t border-border pt-5"
      aria-label="成员收到的任务输入"
      data-testid="worker-prompt"
    >
      <h3 className="text-sm font-semibold">成员收到的任务输入</h3>
      {invalid ? (
        <p className="text-sm text-text-secondary">
          输入审计不可验证，暂不展示提示词。
        </p>
      ) : !prompt || prompt.source === 'unavailable' ? (
        <p className="text-sm text-text-secondary">
          此执行未留存可展示的提示词正文。任务目标不等于实际下发提示词。
        </p>
      ) : (
        <>
          <p className="text-xs text-text-secondary">
            {prompt.source === 'handed-off-redacted'
              ? '已交给 Provider'
              : prompt.source === 'prepared-redacted'
                ? '已准备，尚未确认交接'
                : '已提交的输入'}{' '}
            · 按留存策略脱敏；不包含 Agent 私有系统提示。
          </p>
          <details className="workspace-disclosure">
            <summary data-testid="worker-prompt-expand">查看实际提示词</summary>
            <pre
              data-testid="worker-prompt-text"
              className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-surface-muted p-4 text-xs leading-6"
              tabIndex={0}
            >
              {snapshot && query.data ? query.data : prompt.text}
            </pre>
            {snapshot && prompt.observation?.previewTruncated && !query.data ? (
              <button
                className="min-h-11 text-sm text-accent"
                disabled={query.isFetching}
                onClick={() => void query.refetch()}
              >
                {query.isFetching
                  ? '读取完整输入…'
                  : '展开完整留存输入（当前为节选）'}
              </button>
            ) : null}
          </details>
          {query.isError ? (
            <p role="alert" className="text-sm text-danger">
              完整输入读取或摘要校验失败，保留节选。
            </p>
          ) : null}
        </>
      )}
    </section>
  );
}
