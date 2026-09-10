// 原生 select 原子：保持系统键盘与读屏语义，只统一边框/焦点/箭头外观。
import {cva, type VariantProps} from 'class-variance-authority';
import {forwardRef} from 'react';
import type {SelectHTMLAttributes} from 'react';
import {ChevronDown} from 'lucide-react';
import {cn} from '../../lib/cn';

export const selectVariants = cva(
  'w-full appearance-none rounded-md border border-border bg-surface px-3 pr-9 text-sm leading-[22px] text-text-primary outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50',
  {
    variants: {
      size: {
        default: 'h-10',
        sm: 'h-9 text-[13px] leading-5',
      },
    },
    defaultVariants: {size: 'default'},
  },
);

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'size'>, VariantProps<typeof selectVariants> {}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(({className, size, children, ...props}, ref) => (
  <span className="relative inline-block w-full">
    <select ref={ref} className={cn(selectVariants({size}), className)} {...props}>
      {children}
    </select>
    <ChevronDown aria-hidden="true" className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-secondary" />
  </span>
));
Select.displayName = 'Select';
