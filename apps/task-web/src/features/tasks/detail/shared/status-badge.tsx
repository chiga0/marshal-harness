// 状态徽标：中文标签 + 原机器状态名（中文是标签，不改变机器状态）。

import {Badge, type BadgeProps} from '@/components/ui/badge';
import {cn} from '@/lib/cn';

type Tone = 'default' | 'secondary' | 'success' | 'warning' | 'danger' | 'outline';

export interface StatusBadgeProps {
  machine: string;
  label: string;
  tone?: Tone;
  className?: string;
  showMachine?: boolean;
}

export function StatusBadge({machine, label, tone = 'secondary', className, showMachine = true}: StatusBadgeProps) {
  return (
    <span className={cn('inline-flex flex-wrap items-center gap-1.5', className)}>
      <Badge variant={tone as BadgeProps['variant']}>{label}</Badge>
      {showMachine ? (
        <code className="text-xs text-text-secondary" data-testid="machine-state">{machine}</code>
      ) : null}
    </span>
  );
}

export function toneForTask(status: string): Tone {
  if (status === 'completed') return 'success';
  if (status === 'failed' || status === 'cancelled') return 'danger';
  if (status.startsWith('awaiting') || status === 'intervention') return 'warning';
  if (status === 'unknown') return 'outline';
  if (status === 'paused' || status === 'cancelling') return 'secondary';
  return 'default';
}

export function toneForWorker(status: string): Tone {
  if (status === 'completed') return 'success';
  if (status === 'failed' || status === 'cancelled') return 'danger';
  if (status === 'awaiting-answer') return 'warning';
  if (status === 'running' || status === 'stopping') return 'default';
  return 'secondary';
}

/** 验收/集中评审 verdict（accept/rework/reject）。 */
export function toneForAcceptance(status: string): Tone {
  if (status === 'accept') return 'success';
  if (status === 'reject') return 'danger';
  if (status === 'rework') return 'warning';
  if (status === 'unknown') return 'outline';
  return 'secondary';
}

/** 独立验收 Acceptance.status（pending/passed/failed/unknown）。 */
export function toneForAcceptanceStatus(status: string): Tone {
  if (status === 'passed') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'unknown') return 'outline';
  if (status === 'pending') return 'default';
  return 'secondary';
}

/** Leader 动作状态（publication/postverify）。 */
export function toneForLeaderAction(status: string): Tone {
  if (status === 'succeeded') return 'success';
  if (status === 'failed') return 'danger';
  if (status === 'running') return 'default';
  if (status === 'unknown') return 'outline';
  if (status === 'pending' || status === 'cancelled') return 'secondary';
  return 'secondary';
}
