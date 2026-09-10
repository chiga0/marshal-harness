// Worker 明细抽屉（side sheet，URL 可定位）：全部可观察字段如实展示；用量/审计不可用显示「不可用/暂无数据」；
// 内含单 Worker 取消 flow（permission-free：无额外权限模型，仅按所属任务 revision CAS + 二次确认）。

import {useEffect, useRef} from 'react';
import {createPortal} from 'react-dom';
import {Badge} from '@/components/ui/badge';
import {Button} from '@/components/ui/button';
import type {Revision, Transport, WorkerRecord} from '@/lib/transport/types';
import {formatDateTime, formatDuration, workerPhaseLabel, workerRoleLabel, workerStatusLabel} from '../tasks/detail/shared/format';
import {StatusBadge, toneForWorker} from '../tasks/detail/shared/status-badge';
import {CancelWorkerFlow} from './cancel-worker-flow';

export interface WorkerDrawerProps {
  /** 所属 Task 的当前 revision（单 Worker 取消的 CAS expectedRevision）。 */
  taskRevision: Revision;
  worker: WorkerRecord;
  transport: Transport;
  onClose: () => void;
  onChanged: () => void;
}

export function WorkerDrawer({taskRevision, worker, transport, onClose, onChanged}: WorkerDrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const previousFocus = useRef<Element | null>(null);

  useEffect(() => {
    previousFocus.current = document.activeElement;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    const first = panelRef.current?.querySelector<HTMLElement>('[data-drawer-initial]') ?? panelRef.current;
    first?.focus?.();
    return () => {
      document.removeEventListener('keydown', onKey);
      (previousFocus.current as HTMLElement | null)?.focus?.();
    };
  }, [onClose]);

  const terminal = worker.status === 'completed' || worker.status === 'failed' || worker.status === 'cancelled';

  return createPortal(
    <div className="fixed inset-0 z-40 flex justify-end bg-black/40" role="presentation">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={`Worker 明细 ${worker.nodeId}`}
        tabIndex={-1}
        className="flex h-full w-full max-w-md flex-col gap-4 overflow-y-auto border-l border-border bg-surface p-4 focus:outline-none"
        data-testid="worker-drawer"
      >
        <div className="flex items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold leading-6">Worker 明细</h2>
            <code className="break-all text-xs text-text-secondary">{worker.id}</code>
          </div>
          <Button variant="ghost" size="sm" onClick={onClose} data-drawer-initial>关闭</Button>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge machine={worker.status} label={workerStatusLabel(worker.status)} tone={toneForWorker(worker.status)} />
          <Badge variant="secondary">阶段：{workerPhaseLabel(worker.phase)}</Badge>
          <Badge variant="secondary">角色：{workerRoleLabel(worker.role)}</Badge>
        </div>

        <section aria-label="身份" className="space-y-1 text-sm leading-[22px]">
          <h3 className="text-sm font-medium">身份</h3>
          <dl className="grid grid-cols-1 gap-y-1">
            <Row label="节点" value={<code className="text-xs">{worker.nodeId}</code>} />
            <Row label="Provider" value={<code className="text-xs">{worker.providerId}</code>} />
            <Row label="尝试" value={String(worker.attempt)} />
            <Row label="所属任务" value={<code className="break-all text-xs">{worker.taskId}</code>} />
          </dl>
        </section>

        <section aria-label="时间" className="space-y-1 text-sm leading-[22px]">
          <h3 className="text-sm font-medium">时间</h3>
          <dl className="grid grid-cols-1 gap-y-1">
            <Row label="开始" value={formatDateTime(worker.startedAt)} />
            <Row label="结束" value={worker.finishedAt ? formatDateTime(worker.finishedAt) : '未结束'} />
            <Row label="最近观察" value={formatDateTime(worker.lastObservedAt)} />
          </dl>
          <p className="text-xs text-text-secondary">最近观察时间不代表模型仍在持续工作。</p>
        </section>

        <section aria-label="进展" className="space-y-1 text-sm leading-[22px]">
          <h3 className="text-sm font-medium">进展</h3>
          {worker.progress ? (
            <dl className="grid grid-cols-1 gap-y-1">
              <Row label="摘要" value={worker.progress.summary} />
              <Row label="来源" value={<code className="text-xs">{worker.progress.source}</code>} />
              <Row label="最近工具" value={worker.progress.tool ?? '暂无数据'} />
            </dl>
          ) : (
            <p className="text-text-secondary">暂无数据</p>
          )}
        </section>

        <section aria-label="执行审计" className="space-y-1 text-sm leading-[22px]">
          <h3 className="text-sm font-medium">执行审计</h3>
          {worker.audit ? (
            <dl className="grid grid-cols-1 gap-y-1">
              <Row label="耗时" value={`${formatDuration(worker.audit.elapsedMs)}（来源：${worker.audit.elapsedSource}）`} />
              <Row label="等待" value={`${formatDuration(worker.audit.waitingMs)}（来源：${worker.audit.waitingSource}）`} />
              {worker.audit.repairId ? <Row label="局部修复" value={<code className="text-xs">{worker.audit.repairId}</code>} /> : null}
            </dl>
          ) : (
            <p className="text-text-secondary">暂无数据</p>
          )}
        </section>

        <section aria-label="用量" className="space-y-1 text-sm leading-[22px]">
          <h3 className="text-sm font-medium">用量</h3>
          {worker.usage.source === 'unavailable' ? (
            <p className="text-text-secondary" data-testid="usage-unavailable">不可用（服务端未提供该 Worker 的用量读数）。</p>
          ) : (
            <dl className="grid grid-cols-1 gap-y-1">
              <Row label="Token" value={worker.usage.tokens !== null ? String(worker.usage.tokens) : '暂无数据'} />
              <Row label="费用" value={worker.usage.cost !== null ? `${worker.usage.cost}${worker.usage.currency !== null ? ` ${worker.usage.currency}` : ''}` : '暂无数据'} />
              <Row label="来源" value={<code className="text-xs">{worker.usage.source}</code>} />
              <Row label="覆盖" value={String(worker.usage.coverage)} />
            </dl>
          )}
        </section>

        <section aria-label="操作" className="mt-auto space-y-2 border-t border-border pt-3">
          {terminal ? (
            <p className="text-sm text-text-secondary">该 Worker 已到达终态（{worker.status}），不能再取消。</p>
          ) : (
            <CancelWorkerFlow taskId={worker.taskId} taskRevision={taskRevision} worker={worker} transport={transport} onChanged={onChanged} />
          )}
        </section>
      </div>
    </div>,
    document.body,
  );
}

function Row({label, value}: {label: string; value: React.ReactNode}) {
  return (
    <div className="flex gap-2">
      <dt className="w-20 shrink-0 text-text-secondary">{label}</dt>
      <dd className="min-w-0 break-words">{value}</dd>
    </div>
  );
}
