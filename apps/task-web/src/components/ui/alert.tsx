// 通知原子：区分即时打断（role=alert）与状态播报（role=status）；技术明细由 children 承载。
import {cva, type VariantProps} from 'class-variance-authority';
import type {HTMLAttributes, ReactNode} from 'react';
import {AlertCircle, AlertTriangle, CheckCircle2, Info} from 'lucide-react';
import {cn} from '../../lib/cn';

export const alertVariants = cva(
  'flex w-full items-start gap-3 rounded-md border px-4 py-3 text-sm leading-[22px]',
  {
    variants: {
      variant: {
        info: 'border-border bg-surface-muted text-text-primary [&>svg]:text-accent',
        success: 'border-success/40 bg-success/10 text-text-primary [&>svg]:text-success',
        warning: 'border-warning/40 bg-warning/10 text-text-primary [&>svg]:text-warning',
        danger: 'border-danger/40 bg-danger/10 text-text-primary [&>svg]:text-danger',
      },
    },
    defaultVariants: {variant: 'info'},
  },
);

const ICONS = {info: Info, success: CheckCircle2, warning: AlertTriangle, danger: AlertCircle} as const;

export interface AlertProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'>, VariantProps<typeof alertVariants> {
  title?: ReactNode;
  /** 即时错误用 alert（默认）；进度/结果播报用 status。 */
  role?: 'alert' | 'status';
  icon?: boolean;
}

export function Alert({className, variant = 'info', role = 'alert', title, icon = true, children, ...props}: AlertProps) {
  const Icon = ICONS[variant ?? 'info'];
  return (
    <div role={role} className={cn(alertVariants({variant}), className)} {...props}>
      {icon ? <Icon aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0" /> : null}
      <div className="min-w-0 flex-1">
        {title ? <div className="font-medium text-text-primary">{title}</div> : null}
        {children ? <div className={cn(title ? 'mt-1 text-text-secondary' : 'text-text-primary')}>{children}</div> : null}
      </div>
    </div>
  );
}
