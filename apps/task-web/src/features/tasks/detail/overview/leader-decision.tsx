import {useQuery} from '@tanstack/react-query';
import type {LeaderRecord, Transport} from '@/lib/transport/types';
import {checkArtifact, matchesContract} from '../../../artifacts/traceability';
import {
  blobBytes,
  fetchVerifiedArtifact,
  sha256Hex,
} from '../../../artifacts/downloader';
import {taskKeys} from '../query-keys';

const actionLabels: Record<string, string> = {
  plan: '拟定执行计划',
  dispatch: '安排执行',
  review: '组织独立评审',
  repair: '根据反馈修正',
  deliver: '交付成果',
  conclude: '结束任务',
  ask: '请求业务答复',
  request: '请求确认',
  publish: '执行获准发布',
  postverify: '核验发布结果',
};
function canonical(value: unknown, depth = 0): string {
  if (depth > 64) throw new Error('decision_depth');
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value))
    return '[' + value.map((v) => canonical(v, depth + 1)).join(',') + ']';
  return (
    '{' +
    Object.keys(value)
      .sort()
      .map(
        (k) =>
          JSON.stringify(k) +
          ':' +
          canonical((value as Record<string, unknown>)[k], depth + 1),
      )
      .join(',') +
    '}'
  );
}
export async function readLeaderDecision(
  leader: LeaderRecord,
  transport: Transport,
) {
  if (!matchesContract(leader, 'LeaderView') || !leader.lastDecision)
    throw new Error('decision_reference');
  const ref = leader.lastDecision;
  const artifact = checkArtifact(
    await transport.getArtifact(ref.evidenceId),
    ref.evidenceId,
    leader.taskId,
  );
  if (
    artifact.kind !== 'evidence' ||
    artifact.bytes > 262144 ||
    artifact.status !== 'ready'
  )
    throw new Error('decision_artifact');
  const {blob} = await fetchVerifiedArtifact(transport, {
    artifactId: artifact.id,
    fileName: artifact.name,
    expectedBytes: artifact.bytes,
    expectedDigest: artifact.digest,
  });
  const value: unknown = JSON.parse(
    new TextDecoder('utf-8', {fatal: true}).decode(await blobBytes(blob)),
  );
  const report = (value as {report?: Record<string, unknown>})?.report;
  if (
    !report ||
    report.profile !== 'task-managed-leader/v1' ||
    report.callId !== ref.callId ||
    typeof report.summary !== 'string' ||
    !Array.isArray(report.actions) ||
    report.actions.length > 4 ||
    report.actions.some(
      (action) =>
        !action ||
        typeof action !== 'object' ||
        typeof action.type !== 'string',
    )
  )
    throw new Error('decision_binding');
  if (
    'sha256:' + (await sha256Hex(new Blob([canonical(report)]))) !==
    ref.digest
  )
    throw new Error('decision_digest');
  return {
    summary: report.summary,
    actions: report.actions as Record<string, unknown>[],
  };
}
export function LeaderDecision({
  leader,
  transport,
}: {
  leader: LeaderRecord | null;
  transport: Transport;
}) {
  const ref = leader?.lastDecision;
  const query = useQuery({
    queryKey: [...taskKeys.all(leader?.taskId ?? ''), 'decision', ref?.digest],
    enabled: !!ref,
    retry: false,
    queryFn: () => readLeaderDecision(leader!, transport),
  });
  return (
    <section
      className="space-y-3"
      aria-label="团队当前策略"
      data-testid="leader-decision"
    >
      <h2 className="text-base font-semibold">团队当前策略</h2>
      {query.data ? (
        <>
          <p className="whitespace-pre-wrap break-words text-sm leading-6">
            {query.data.summary}
          </p>
          <div className="flex flex-wrap gap-2">
            {query.data.actions.map((action, i) => (
              <span
                key={i}
                className="rounded-full bg-surface-muted px-3 py-1 text-xs"
              >
                {actionLabels[String(action.type)] ?? '已记录团队行动'}
              </span>
            ))}
          </div>
          <p className="text-xs text-text-secondary">
            来自已记录的 Leader 决定；行动建议不等于执行或验收已完成。
          </p>
        </>
      ) : (
        <p className="text-sm text-text-secondary">
          {query.isError
            ? '最近决定正文暂不可读取，可在活动与审计中核对。'
            : query.isFetching
              ? '正在读取团队决定…'
              : '尚未记录可读决定，团队的后续安排会显示在这里。'}
        </p>
      )}
    </section>
  );
}
