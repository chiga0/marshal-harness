// 文本输入原子：cva + token；用 aria-invalid 表达错误，不扩展第三方组件库。
import {cva, type VariantProps} from 'class-variance-authority';
import {forwardRef} from 'react';
import type {InputHTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export const inputVariants = cva(
  'flex w-full rounded-md border border-border bg-surface px-3 text-sm leading-[22px] text-text-primary placeholder:text-text-secondary outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger/30',
  {
    variants: {
      size: {
        default: 'h-10 py-2',
        sm: 'h-9 text-[13px] leading-5',
      },
    },
    defaultVariants: {size: 'default'},
  },
);

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'size'>, VariantProps<typeof inputVariants> {}

export const Input = forwardRef<HTMLInputElement, InputProps>(({className, size, type, ...props}, ref) => (
  <input type={type ?? 'text'} className={cn(inputVariants({size}), className)} ref={ref} {...props} />
));
Input.displayName = 'Input';
