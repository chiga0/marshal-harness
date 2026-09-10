import {cva, type VariantProps} from 'class-variance-authority';
import {forwardRef} from 'react';
import type {ButtonHTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export const buttonVariants = cva(
  'inline-flex select-none items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 active:scale-[0.99] [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        default: 'bg-accent text-accent-foreground hover:bg-accent/90 focus-visible:ring-accent',
        secondary: 'bg-surface-muted text-text-primary hover:bg-surface-muted/80 focus-visible:ring-accent',
        outline: 'border border-border bg-transparent text-text-primary hover:bg-surface-muted/60 focus-visible:ring-accent',
        destructive: 'bg-danger text-danger-foreground hover:bg-danger/90 focus-visible:ring-danger',
        success: 'bg-success text-success-foreground hover:bg-success/90 focus-visible:ring-success',
        warning: 'bg-warning text-warning-foreground hover:bg-warning/90 focus-visible:ring-warning',
        ghost: 'hover:bg-surface-muted text-text-primary focus-visible:ring-accent',
        link: 'text-accent underline-offset-4 hover:underline focus-visible:ring-accent',
      },
      size: {
        default: 'h-10 px-4 py-2',
        sm: 'h-9 px-3 text-[13px] leading-5',
        lg: 'h-11 px-5 text-base',
        icon: 'h-10 w-10',
      },
    },
    defaultVariants: {variant: 'default', size: 'default'},
  },
);

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement>, VariantProps<typeof buttonVariants> {
  loading?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(({className, variant, size, loading, children, disabled, type, ...props}, ref) => (
  <button type={type ?? 'button'} className={cn(buttonVariants({variant, size}), className)} disabled={disabled ?? loading} ref={ref} {...props}>
    {loading ? (
      <span className="inline-block h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" aria-hidden />
    ) : null}
    {children}
  </button>
));
Button.displayName = 'Button';
