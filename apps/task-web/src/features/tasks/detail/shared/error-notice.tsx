// ErrorNotice：已知错误码的等价中文说明 + requestId 显眼展示 + 可执行恢复动作；技术详情默认折叠。
// 永不展示或记录 token（本组件根本没有 token 来源）。

import type {ReactNode} from 'react';
import {Button} from '@/components/ui/button';
import {cn} from '@/lib/cn';
import {guidanceFor, RECOVERY_LABELS, toRenderedError} from './error-codes';

export interface ErrorNoticeProps {
  error: unknown;
  /** 场景标题，如「确认计划失败」「下载失败」。 */
  title?: string;
  /** 结果未知时语义说明（如「请求可能已被受理」），来自上层业务文案。 */
  outcomeNote?: string;
  /** 提供时显示「刷新」按钮。 */
  onRefresh?: (() => void) | null;
  /** 提供时显示「原键重放」按钮（复用同一 Idempotency-Key，不重新执行）。 */
  onReplay?: (() => void) | null;
  replayBusy?: boolean;
  /** 提供时显示「断开并重新连接」。 */
  onReconnect?: (() => void) | null;
  onDismiss?: (() => void) | null;
  extra?: ReactNode;
}

export function ErrorNotice({error, title, outcomeNote, onRefresh, onReplay, replayBusy, onReconnect, onDismiss, extra}: ErrorNoticeProps) {
  const rendered = toRenderedError(error);
  const guide = guidanceFor(rendered.code);
  const suggested = guide.action;
  const refreshLabel = suggested === 'refresh' ? RECOVERY_LABELS.refresh : '刷新查看';
  return (
    <div role="alert" className="rounded-md border border-danger/40 bg-danger/5 p-3" data-testid="error-notice" data-error-code={rendered.code}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-danger">{title ? `${title}：${guide.title}` : guide.title}</span>
        <code className="rounded bg-surface-muted px-1.5 py-0.5 text-xs text-text-secondary" data-testid="error-code">
          {rendered.code}{rendered.status !== null ? ` · HTTP ${rendered.status}` : ''}
        </code>
      </div>
      <p className="mt-1 text-sm leading-[22px] text-text-primary">{guide.guidance}</p>
      {outcomeNote ? <p className="mt-1 text-sm leading-[22px] text-text-secondary">{outcomeNote}</p> : null}
      {rendered.message && rendered.message !== rendered.code ? (
        <p className="mt-1 text-sm leading-[22px] text-text-secondary">服务端说明：{rendered.message}</p>
      ) : null}
      {rendered.requestId ? (
        <p className="mt-1 text-sm leading-[22px]">
          <span className="text-text-secondary">requestId：</span>
          <code className="break-all rounded bg-surface-muted px-1.5 py-0.5 text-xs" data-testid="error-request-id">{rendered.requestId}</code>
        </p>
      ) : null}
      <div className="mt-2 flex flex-wrap gap-2">
        {onReplay ? (
          <Button size="sm" variant="secondary" onClick={onReplay} loading={replayBusy ?? false}>
            {RECOVERY_LABELS['retry-same-key']}
          </Button>
        ) : null}
        {onRefresh ? (
          <Button size="sm" variant="outline" onClick={onRefresh}>
            {refreshLabel}
          </Button>
        ) : null}
        {onReconnect ? (
          <Button size="sm" variant="outline" onClick={onReconnect}>
            {RECOVERY_LABELS.reconnect}
          </Button>
        ) : null}
        {onDismiss ? (
          <Button size="sm" variant="ghost" onClick={onDismiss}>知道了</Button>
        ) : null}
        {extra}
      </div>
      <details className={cn('mt-2 text-xs text-text-secondary')}>
        <summary className="cursor-pointer select-none">技术详情（默认折叠）</summary>
        <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
          <dt>错误码</dt>
          <dd><code className="break-all">{rendered.code}</code></dd>
          <dt>HTTP 状态</dt>
          <dd>{rendered.status !== null ? rendered.status : '无（本地或网络失败）'}</dd>
          <dt>requestId</dt>
          <dd>{rendered.requestId ? <code className="break-all">{rendered.requestId}</code> : '服务端未返回'}</dd>
          <dt>恢复建议</dt>
          <dd>{suggested === 'none' ? '无自动恢复路径，按上方说明人工处理' : RECOVERY_LABELS[suggested]}</dd>
        </dl>
      </details>
    </div>
  );
}
