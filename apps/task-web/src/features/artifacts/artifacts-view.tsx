// 成果（P09）：独立验收读数 + 最终交付清单（真实 files/digest/bytes）+ 发布记录只读。
// 下载有 Bearer 流 + 本机复验 + 错误显示；下载不等于发布；候选/部分成果缺清单接口时如实说明，不伪造列表。

import {useState} from 'react';
import {Button} from '@/components/ui/button';
import {Badge} from '@/components/ui/badge';
import {Card} from '@/components/ui/card';
import type {DeliveryFile, PublicationRecord, TaskDetail, Transport} from '@/lib/transport/types';
import {AcceptancePanel} from '../tasks/detail/overview/acceptance-panel';
import {ErrorNotice} from '../tasks/detail/shared/error-notice';
import {formatBytes, formatDateTime, publicationLabel} from '../tasks/detail/shared/format';
import {StatusBadge, toneForAcceptance} from '../tasks/detail/shared/status-badge';
import {downloadArtifact, DownloadRejection} from './downloader';

export interface ArtifactsViewProps {
  detail: TaskDetail;
  publications: PublicationRecord[] | null;
  transport: Transport;
}

export function ArtifactsView({detail, publications, transport}: ArtifactsViewProps) {
  const delivery = detail.latestDelivery ?? null;
  return (
    <div className="space-y-4" data-testid="artifacts-view">
      <AcceptancePanel detail={detail} />

      <Card aria-label="最终交付" className="space-y-2" data-testid="final-delivery">
        <h2 className="text-base font-semibold leading-6">最终交付成果</h2>
        {delivery ? (
          <>
            <dl className="grid grid-cols-1 gap-y-1 text-sm leading-[22px]">
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">交付摘要</dt><dd className="break-all"><code className="text-xs">{delivery.digest}</code></dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">总大小</dt><dd>{formatBytes(delivery.bytes)}</dd></div>
              <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">本机落盘</dt><dd>{delivery.isLocalRecipient ? '已标记交付到本机接收目录' : '未标记本机落盘'}</dd></div>
            </dl>
            {delivery.files && delivery.files.length > 0 ? (
              <div className="overflow-x-auto rounded-md border border-border">
                <table className="w-full min-w-[560px] text-sm leading-[22px]">
                  <thead className="bg-surface-muted/60 text-left text-xs text-text-secondary">
                    <tr>
                      <th className="px-3 py-2 font-medium">文件</th>
                      <th className="px-3 py-2 font-medium">大小</th>
                      <th className="px-3 py-2 font-medium">摘要</th>
                      <th className="px-3 py-2 font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {delivery.files.map(file => (
                      <FileRow key={file.path} taskId={detail.id} file={file} transport={transport} />
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="flex flex-wrap items-center gap-2">
                <p className="text-sm text-text-secondary">未提供文件清单。</p>
                <WholeDeliveryDownload taskId={detail.id} digest={delivery.digest} bytes={delivery.bytes} transport={transport} />
              </div>
            )}
            {detail.status === 'failed' ? (
              <p className="text-sm text-danger">任务最终状态为失败：以上仅是服务端给出的交付清单，不表示整体成功。</p>
            ) : null}
          </>
        ) : (
          <div className="space-y-1">
            <p className="text-sm text-text-secondary" data-testid="delivery-empty">
              尚无交付成果{detail.status === 'failed' ? '（任务失败，无交付成果）' : ''}。
            </p>
            {detail.failureCode ? <p className="text-sm text-danger">失败代码：<code>{detail.failureCode}</code></p> : null}
          </div>
        )}
        <p className="text-xs text-text-secondary">
          候选产物与失败部分成果需要服务端制品清单接口；当前 transport 未提供该清单，本页只展示可核验的最终交付与验收读数，不伪造候选或部分成果列表。
        </p>
      </Card>

      <Card aria-label="发布记录" className="space-y-2" data-testid="publications-card">
        <h2 className="text-base font-semibold leading-6">发布记录（只读）</h2>
        {publications === null ? (
          <p className="text-sm text-text-secondary">发布记录未加载或加载失败。</p>
        ) : publications.length === 0 ? (
          <p className="text-sm text-text-secondary">无发布记录（未启用发布的任务正常只交付成果）。</p>
        ) : (
          <ul className="space-y-2">
            {publications.map(publication => (
              <li key={publication.id} className="rounded-md border border-border px-3 py-2" data-testid="publication-row">
                <div className="flex flex-wrap items-center gap-2">
                  <code className="text-xs text-text-secondary">{publication.id}</code>
                  <StatusBadge machine={publication.status} label={publicationLabel(publication.status)} tone={toneForAcceptance(publication.status)} showMachine={false} />
                  <span className="text-xs text-text-secondary">更新：{formatDateTime(publication.updatedAt)}</span>
                </div>
                <p className="mt-1 break-all text-xs text-text-secondary">快照摘要：<code>{publication.snapshotDigest}</code></p>
              </li>
            ))}
          </ul>
        )}
        <div className="flex items-start gap-2 rounded-md border border-border bg-surface-muted/30 p-2">
          <Badge variant="secondary">说明</Badge>
          <p className="text-xs leading-5 text-text-secondary" data-testid="download-not-publication">
            下载只是将文件保存到本机浏览器目录，不触发、不代表任何发布；发布由服务端按精确授权执行，状态以本区只读记录为准。
            后验失败（postverify-failed）或未知时不重复发布、不标整体成功。
          </p>
        </div>
      </Card>
    </div>
  );
}

function FileRow({taskId, file, transport}: {taskId: string; file: DeliveryFile; transport: Transport}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState<string | null>(null);

  const onDownload = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const result = await downloadArtifact(transport, {ref: file.digest, fileName: file.path, expectedBytes: file.bytes, expectedDigest: file.digest});
      setSaved(result.savedName);
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <tr className="border-t border-border" data-testid="delivery-file-row">
        <td className="break-all px-3 py-2"><code className="text-xs">{file.path}</code></td>
        <td className="px-3 py-2">{formatBytes(file.bytes)}</td>
        <td className="break-all px-3 py-2"><code className="text-xs">{file.digest}</code></td>
        <td className="px-3 py-2">
          <Button size="sm" variant="outline" onClick={() => void onDownload()} loading={busy} data-testid="download-button">
            下载
          </Button>
        </td>
      </tr>
      {error !== null || saved !== null ? (
        <tr className="border-t border-border">
          <td colSpan={4} className="px-3 py-2">
            {saved !== null ? (
              <p className="text-sm text-success" role="status" data-testid="download-success">
                已保存 {saved}（SHA-256 复验通过）。下载不等于发布。
              </p>
            ) : null}
            {error instanceof DownloadRejection ? (
              <p className="text-sm text-danger" role="alert" data-testid="download-rejected">{error.message}</p>
            ) : null}
            {error !== null && !(error instanceof DownloadRejection) ? (
              <ErrorNotice error={error} title="下载失败" onRefresh={() => void onDownload()} />
            ) : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function WholeDeliveryDownload({taskId, digest, bytes, transport}: {taskId: string; digest: string; bytes: number; transport: Transport}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const onDownload = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const result = await downloadArtifact(transport, {ref: digest, fileName: `${taskId}-delivery`, expectedBytes: bytes, expectedDigest: digest});
      setSaved(result.savedName);
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  };
  return (
    <span className="inline-flex items-center gap-2">
      <Button size="sm" variant="outline" onClick={() => void onDownload()} loading={busy} data-testid="download-whole">
        下载整份交付
      </Button>
      {saved !== null ? <span className="text-sm text-success" role="status">已保存 {saved}（复验通过）。下载不等于发布。</span> : null}
      {error instanceof DownloadRejection ? <span className="text-sm text-danger" role="alert">{error.message}</span> : null}
      {error !== null && !(error instanceof DownloadRejection) ? (
        <span className="block w-full"><ErrorNotice error={error} title="下载失败" onRefresh={() => void onDownload()} /></span>
      ) : null}
    </span>
  );
}
