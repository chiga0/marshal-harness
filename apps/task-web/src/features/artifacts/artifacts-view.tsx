// 成果（P09）：Task.artifactIds → 服务端产物元数据清单（候选/最终/证据/输入分组，逐项可用性如实，E15 事实层）；
// 独立验收来自 audit.acceptance，集中评审来自 leader.review；发布/后验来自 leader.publication/postverify；
// 下载经 getArtifactContent 拉流 + 本机 SHA-256 复验（E17），任何不一致拒绝保存；下载不等于发布（E16）。

import {useState} from 'react';
import {Badge} from '@/components/ui/badge';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ApiError} from '@/lib/transport/types';
import type {ArtifactKind, ArtifactRecord, LeaderActionStatus, LeaderRecord, LeaderReview, TaskAuditRecord, TaskRecord, Transport} from '@/lib/transport/types';
import {ErrorNotice} from '../tasks/detail/shared/error-notice';
import {acceptanceStatusLabel, formatBytes} from '../tasks/detail/shared/format';
import {StatusBadge, toneForAcceptanceStatus} from '../tasks/detail/shared/status-badge';
import {downloadArtifact, DownloadRejection} from './downloader';
import type {ArtifactEntry} from './use-task-artifacts';
import type {useObservedInputs} from './use-observed-inputs';

export interface ArtifactsViewProps {
  task: TaskRecord;
  leader: LeaderRecord | null;
  audit: TaskAuditRecord | null;
  /** null=清单未加载或整体加载失败；数组逐项 ok/failed（failed 项如实显示不可用，不静默丢弃）。 */
  artifacts: ArtifactEntry[] | null;
  transport: Transport;
  observedInputs?: ReturnType<typeof useObservedInputs>;
}

const ARTIFACT_KIND_LABELS: Record<ArtifactKind, string> = {
  candidate: '候选成果',
  delivery: '最终成果',
  evidence: '验收/后验证据',
  input: '输入',
};

const ARTIFACT_STATUS_LABELS: Record<ArtifactRecord['status'], string> = {
  ready: '就绪',
  partial: '部分',
  unavailable: '不可用',
};

const ACTION_STATUS_LABELS: Record<LeaderActionStatus, string> = {
  pending: '待处理',
  running: '执行中',
  succeeded: '已成功',
  failed: '已失败',
  unknown: '未知',
  cancelled: '已取消',
};

const REVIEW_VERDICT_LABELS: Record<LeaderReview['verdict'], string> = {
  accept: '评审通过',
  rework: '评审返工',
  reject: '评审拒绝',
};

function artifactStatusTone(status: ArtifactRecord['status']): 'success' | 'warning' | 'danger' {
  if (status === 'ready') return 'success';
  if (status === 'partial') return 'warning';
  return 'danger';
}

function actionStatusTone(status: LeaderActionStatus): 'default' | 'success' | 'warning' | 'danger' | 'secondary' {
  if (status === 'succeeded') return 'success';
  if (status === 'failed' || status === 'cancelled') return 'danger';
  if (status === 'running') return 'default';
  if (status === 'pending') return 'warning';
  return 'secondary';
}

function describeLoadError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}${error.requestId ? `（requestId：${error.requestId}）` : ''}`;
  }
  return error instanceof Error ? error.message : String(error);
}

export function ArtifactsView({task, leader, audit, artifacts, observedInputs, transport}: ArtifactsViewProps) {
  const acceptance = audit?.acceptance ?? null;
  const entries = artifacts ?? [];
  const okArtifacts = entries
    .filter((entry): entry is Extract<ArtifactEntry, {status: 'ok'}> => entry.status === 'ok')
    .map(entry => entry.artifact);
  const failedEntries = entries.filter((entry): entry is Extract<ArtifactEntry, {status: 'failed'}> => entry.status === 'failed');
  const delivery = okArtifacts.find(artifact => artifact.kind === 'delivery') ?? null;

  return (
    <div className="space-y-4" data-testid="artifacts-view">
      <Card aria-label="最终交付" className="space-y-2" data-testid="final-delivery">
        <h2 className="text-base font-semibold leading-6">最终交付成果</h2>
        {delivery ? (
          <>
            <ArtifactFacts artifact={delivery} />
            {delivery.status === 'partial' ? (
              <p className="text-sm text-warning" data-testid="delivery-partial">
                该交付成果被服务端标记为部分（partial），不代表完整交付。
              </p>
            ) : null}
            {delivery.status === 'unavailable' ? (
              <p className="text-sm text-danger" data-testid="delivery-content-unavailable">
                服务端标记该成果内容不可用；本页不伪造其内容，也不提供下载。
              </p>
            ) : (
              <ArtifactDownload artifact={delivery} transport={transport} />
            )}
            {task.status === 'failed' ? (
              <p className="text-sm text-danger">任务最终状态为失败：以上仅是服务端记录的交付成果，不表示整体成功。</p>
            ) : null}
          </>
        ) : (
          <div className="space-y-1">
            <p className="text-sm text-text-secondary" data-testid="delivery-empty">
              尚无交付成果{task.status === 'failed' ? '（任务失败，无交付成果）' : ''}。
            </p>
            {task.code ? <p className="text-sm text-danger">失败代码：<code>{task.code}</code></p> : null}
          </div>
        )}
        <p className="text-xs text-text-secondary">
          单个成果内容的服务端合同上限为 8388608 字节（8 MiB）；下载只是保存到本机浏览器目录，下载不等于发布。
        </p>
      </Card>

      <Card aria-label="产物清单" className="space-y-3" data-testid="artifact-inventory">
        <h2 className="text-base font-semibold leading-6">产物清单（按类别）</h2>
        {artifacts === null ? (
          <p className="text-sm text-text-secondary" data-testid="artifact-list-unavailable">
            成果清单未加载或整体加载失败（产物仍存在于服务端任务记录）；请刷新重试，不以本地猜测冒充清单。
          </p>
        ) : null}
        {artifacts !== null && entries.length === 0 ? (
          <p className="text-sm text-text-secondary" data-testid="artifact-list-empty">暂无产物（任务尚无 artifactIds 记录）。</p>
        ) : null}
        {failedEntries.length > 0 ? (
          <div className="space-y-1" data-testid="artifact-load-failures">
            <p className="text-sm text-warning">{failedEntries.length} 条产物元数据未成功加载，如实占位而非静默丢弃：</p>
            <ul className="space-y-1">
              {failedEntries.map(entry => (
                <li key={entry.id} className="text-xs text-text-secondary" data-testid={`artifact-unavailable-${entry.id}`}>
                  <code>{entry.id}</code>：不可用（{describeLoadError(entry.error)}）{entry.sources?.length ? `；来源：${entry.sources.join('、')}` : ''}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        {(['candidate', 'evidence'] as const).map(kind => {
          const rows = okArtifacts.filter(artifact => artifact.kind === kind);
          return (
            <section key={kind} aria-label={ARTIFACT_KIND_LABELS[kind]} className="space-y-2">
              <div className="flex items-center gap-2">
                <h3 className="text-sm font-medium">{ARTIFACT_KIND_LABELS[kind]}</h3>
                <Badge variant="secondary">{rows.length} 条</Badge>
              </div>
              {rows.length === 0 ? (
                <p className="text-xs text-text-secondary" data-testid={`artifact-group-empty-${kind}`}>暂无{ARTIFACT_KIND_LABELS[kind]}。</p>
              ) : (
                <div className="overflow-x-auto rounded-md border border-border">
                  <table className="w-full min-w-[720px] text-sm leading-[22px]">
                    <thead className="bg-surface-muted/60 text-left text-xs text-text-secondary">
                      <tr>
                        <th className="px-3 py-2 font-medium">文件名</th>
                        <th className="px-3 py-2 font-medium">mediaType</th>
                        <th className="px-3 py-2 font-medium">大小</th>
                        <th className="px-3 py-2 font-medium">摘要</th>
                        <th className="px-3 py-2 font-medium">状态</th>
                        <th className="px-3 py-2 font-medium">操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {rows.map(artifact => (
                        <ArtifactRow key={artifact.id} artifact={artifact} transport={transport}
                          sources={entries.find(entry => entry.status === 'ok' && entry.artifact.id === artifact.id)?.sources ?? []} />
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
          );
        })}
      </Card>

      <Card aria-label="执行审计观测输入" className="space-y-3" data-testid="observed-inputs">
        <h2 className="text-base font-semibold leading-6">执行审计观测到的输入</h2>
        <p className="text-xs text-text-secondary">仅列出当前 Task 执行审计明确关联的输入；不是完整原始输入清单，也不证明 Agent 或模型已消费。输入按合同归属本地操作者（taskId=null），不冒充 Task 产物。</p>
        {observedInputs?.referenceError ? <ErrorNotice error={observedInputs.referenceError} title="输入关联校验失败" /> : null}
        {!observedInputs || observedInputs.total === null || observedInputs.total === 0 ? (
          <p className="text-sm text-text-secondary" data-testid="input-association-unavailable">输入关联未提供或尚无观测；不能据此认定没有输入。</p>
        ) : <>
          <p className="text-xs text-text-secondary">当前审计共关联 {observedInputs.total} 个不同输入；本页请求前 {observedInputs.loaded} 个{observedInputs.loaded < observedInputs.total ? '（尚未加载全部关联）' : ''}。</p>
          {observedInputs.entries === null ? <p role="status">正在读取输入元数据…</p> : null}
          {observedInputs.entries?.filter(entry => entry.status === 'failed').map(entry => entry.status === 'failed' ? (
            <p key={entry.id} role="alert" className="break-all text-sm text-danger">输入 {entry.id} 不可用：{describeLoadError(entry.error)}</p>
          ) : null)}
          <div className="overflow-x-auto rounded-md border border-border">
            <table className="w-full min-w-[720px] text-sm leading-[22px]">
              <thead><tr>{['文件名 / 来源', 'mediaType', '大小', '摘要', '状态', '操作'].map(label => <th key={label} className="px-3 py-2 text-left font-medium">{label}</th>)}</tr></thead>
              <tbody>{observedInputs.entries?.map(entry => entry.status === 'ok' ? <ArtifactRow key={entry.artifact.id} artifact={entry.artifact} transport={transport} sources={entry.sources ?? []} readyOnly /> : null)}</tbody>
            </table>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => void observedInputs.query.refetch()} disabled={observedInputs.query.isFetching}>刷新输入元数据</Button>
            {observedInputs.loaded < observedInputs.total ? <Button variant="outline" size="sm" onClick={observedInputs.loadMore}>加载更多观测输入</Button> : null}
          </div>
        </>}
      </Card>

      <Card aria-label="验收读数" className="space-y-2" data-testid="verification-readout">
        <h2 className="text-base font-semibold leading-6">独立验收/集中评审读数</h2>
        {leader === null ? (
          <p className="text-sm text-text-secondary" data-testid="verification-unavailable">
            Leader 投影不可用，集中评审读数暂不可用。
          </p>
        ) : leader.review === null ? (
          <p className="text-sm text-text-secondary" data-testid="verification-empty">
            暂无集中评审读数（评审未完成或该服务未提供）。
          </p>
        ) : (
          <div className="space-y-1" data-testid="review-verdict">
            <StatusBadge
              machine={leader.review.verdict}
              label={REVIEW_VERDICT_LABELS[leader.review.verdict]}
              tone={leader.review.verdict === 'accept' ? 'success' : leader.review.verdict === 'reject' ? 'danger' : 'warning'}
            />
            <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审摘要</dt><dd className="break-all"><code className="text-xs">{leader.review.digest}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">评审 Worker</dt><dd className="break-all"><code className="text-xs">{leader.review.workerId}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">证据</dt><dd>{leader.review.evidenceIds.length > 0 ? `${leader.review.evidenceIds.length} 条（${leader.review.evidenceIds.join('，')}）` : '暂无数据'}</dd></div>
            </dl>
          </div>
        )}
        <div className="space-y-1 border-t border-border pt-2" data-testid="acceptance-readout">
          <h3 className="text-sm font-medium">独立验收（task.audit.acceptance）</h3>
          {acceptance === null ? (
            <p className="text-sm text-text-secondary" data-testid="acceptance-unloaded">验收读数未加载或 audit 投影不可用；不能以评审结果代替验收。</p>
          ) : (
            <>
              <StatusBadge machine={acceptance.status} label={acceptanceStatusLabel(acceptance.status)} tone={toneForAcceptanceStatus(acceptance.status)} />
              <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
                <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">验收摘要</dt><dd className="break-all">{acceptance.digest !== null ? <code className="text-xs">{acceptance.digest}</code> : '暂无摘要'}</dd></div>
                <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">验收证据</dt><dd className="break-all">{acceptance.evidenceIds.length > 0 ? `${acceptance.evidenceIds.length} 条（${acceptance.evidenceIds.join('，')}）` : '暂无数据'}</dd></div>
              </dl>
            </>
          )}
        </div>
        <p className="text-xs text-text-secondary">评审通过不等于验收通过；只有独立验收 acceptance=passed 才是验收通过。执行结束不代表验收通过。</p>
      </Card>

      <Card aria-label="发布与后验" className="space-y-2" data-testid="publications-card">
        <h2 className="text-base font-semibold leading-6">发布与后验（只读）</h2>
        {leader === null ? (
          <p className="text-sm text-text-secondary" data-testid="publication-unavailable">
            Leader 投影不可用：发布与后验状态暂不可用；不重复发布、不标整体成功。
          </p>
        ) : (
          <>
            {leader.publication === null && leader.postverify === null ? (
              <p className="text-sm text-text-secondary" data-testid="publication-empty">无发布记录（未启用发布的任务正常只交付成果）。</p>
            ) : null}
            {leader.publication !== null ? (
              <div className="space-y-1" data-testid="publication-line">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">发布</span>
                  <StatusBadge
                    machine={leader.publication.status}
                    label={ACTION_STATUS_LABELS[leader.publication.status]}
                    tone={actionStatusTone(leader.publication.status)}
                  />
                  <span className="text-xs text-text-secondary">actionId：<code>{leader.publication.actionId}</code></span>
                </div>
                <p className="text-xs text-text-secondary">
                  回执成果：{leader.publication.receiptArtifactId ? <code className="break-all">{leader.publication.receiptArtifactId}</code> : '暂无数据'}
                </p>
              </div>
            ) : null}
            {leader.postverify !== null ? (
              <div className="space-y-1" data-testid="postverify-line">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">发布后验</span>
                  <StatusBadge
                    machine={leader.postverify.status}
                    label={ACTION_STATUS_LABELS[leader.postverify.status]}
                    tone={actionStatusTone(leader.postverify.status)}
                  />
                  <span className="text-xs text-text-secondary">actionId：<code>{leader.postverify.actionId}</code></span>
                </div>
                <p className="text-xs text-text-secondary">
                  后验证据成果：{leader.postverify.evidenceArtifactId ? <code className="break-all">{leader.postverify.evidenceArtifactId}</code> : '暂无数据'}
                </p>
              </div>
            ) : null}
          </>
        )}
        <div className="flex items-start gap-2 rounded-md border border-border bg-surface-muted/30 p-2">
          <Badge variant="secondary">说明</Badge>
          <p className="text-xs leading-5 text-text-secondary" data-testid="download-not-publication">
            下载只是将文件保存到本机浏览器目录，不触发、不代表任何发布；发布由服务端按精确授权执行，状态以本区只读状态为准。
            发布后验失败或未知时不重复发布、不标整体成功。
          </p>
        </div>
      </Card>
    </div>
  );
}

function ArtifactFacts({artifact}: {artifact: ArtifactRecord}) {
  return (
    <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
      <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">文件名</dt><dd className="break-all"><code className="text-xs">{artifact.name}</code></dd></div>
      <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">mediaType</dt><dd><code className="text-xs">{artifact.mediaType}</code></dd></div>
      <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">大小</dt><dd>{formatBytes(artifact.bytes)}</dd></div>
      <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">摘要</dt><dd className="break-all"><code className="text-xs">{artifact.digest}</code></dd></div>
      <div className="flex items-center gap-2">
        <dt className="shrink-0 text-text-secondary">状态</dt>
        <dd>
          <StatusBadge machine={artifact.status} label={ARTIFACT_STATUS_LABELS[artifact.status]} tone={artifactStatusTone(artifact.status)} />
          {artifact.kind === 'delivery' ? <Badge variant="secondary" className="ml-1">{ARTIFACT_KIND_LABELS[artifact.kind]}</Badge> : null}
        </dd>
      </div>
    </dl>
  );
}

function ArtifactRow({artifact, transport, sources = [], readyOnly = false}: {artifact: ArtifactRecord; transport: Transport; sources?: string[]; readyOnly?: boolean}) {
  return (
    <tr className="border-t border-border" data-testid="artifact-row" data-artifact-id={artifact.id}>
      <td className="break-all px-3 py-2"><code className="text-xs">{artifact.name}</code>{sources.map(source => <p key={source} className="text-xs text-text-secondary">来源：{source}</p>)}</td>
      <td className="px-3 py-2"><code className="text-xs">{artifact.mediaType}</code></td>
      <td className="px-3 py-2">{formatBytes(artifact.bytes)}</td>
      <td className="max-w-[220px] break-all px-3 py-2"><code className="text-xs">{artifact.digest}</code></td>
      <td className="px-3 py-2">
        <StatusBadge machine={artifact.status} label={ARTIFACT_STATUS_LABELS[artifact.status]} tone={artifactStatusTone(artifact.status)} showMachine={false} />
      </td>
      <td className="px-3 py-2">
        {artifact.status === 'unavailable' || readyOnly && artifact.status !== 'ready' ? (
          <span className="text-xs text-text-secondary">{artifact.status === 'unavailable' ? '内容不可用，不提供下载' : '内容未就绪，不提供下载'}</span>
        ) : (
          <ArtifactDownload artifact={artifact} transport={transport} />
        )}
      </td>
    </tr>
  );
}

function ArtifactDownload({artifact, transport}: {artifact: ArtifactRecord; transport: Transport}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState<string | null>(null);

  const onDownload = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const result = await downloadArtifact(transport, {
        artifactId: artifact.id,
        fileName: artifact.name,
        expectedBytes: artifact.bytes,
        expectedDigest: artifact.digest,
      });
      setSaved(result.savedName);
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  };

  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" onClick={() => void onDownload()} loading={busy} data-testid="download-button">
        下载
      </Button>
      {saved !== null ? (
        <span className="text-sm text-success" role="status" data-testid="download-success">
          已保存 {saved}（SHA-256 复验通过）。下载不等于发布。
        </span>
      ) : null}
      {error instanceof DownloadRejection ? (
        <span className="text-sm text-danger" role="alert" data-testid="download-rejected">{error.message}</span>
      ) : null}
      {error !== null && !(error instanceof DownloadRejection) ? (
        <span className="block w-full"><ErrorNotice error={error} title="下载失败" onRefresh={() => void onDownload()} /></span>
      ) : null}
    </span>
  );
}
