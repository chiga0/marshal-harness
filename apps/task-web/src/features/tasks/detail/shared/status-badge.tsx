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
  if (status === 'completed' || status === 'confirmed') return 'success';
  if (status === 'failed' || status === 'cancelled') return 'danger';
  if (status.startsWith('awaiting')) return 'warning';
  if (status === 'unknown') return 'outline';
  return 'default';
}

export function toneForWorker(status: string): Tone {
  if (status === 'completed') return 'success';
  if (status === 'failed' || status === 'cancelled') return 'danger';
  if (status === 'running' || status === 'stopping') return 'default';
  return 'secondary';
}

export function toneForAcceptance(status: string): Tone {
  if (status === 'passed' || status === 'postverify-passed') return 'success';
  if (status === 'failed' || status === 'postverify-failed') return 'danger';
  if (status === 'pending') return 'warning';
  return 'secondary';
}
