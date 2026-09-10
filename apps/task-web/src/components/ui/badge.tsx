import {cva, type VariantProps} from 'class-variance-authority';
import type {HTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-semibold leading-[18px]',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-accent/10 text-accent',
        secondary: 'border-border bg-surface-muted text-text-secondary',
        success: 'border-transparent bg-success/12 text-success',
        warning: 'border-transparent bg-warning/12 text-warning',
        danger: 'border-transparent bg-danger/12 text-danger',
        outline: 'border-border text-text-primary',
      },
    },
    defaultVariants: {variant: 'default'},
  },
);

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

export function Badge({className, variant, ...props}: BadgeProps) {
  return <span className={cn(badgeVariants({variant}), className)} {...props} />;
}
