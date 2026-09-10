// 多行文本原子：默认允许纵向缩放，错误态同 Input 一致走 aria-invalid。
import {cva, type VariantProps} from 'class-variance-authority';
import {forwardRef} from 'react';
import type {TextareaHTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export const textareaVariants = cva(
  'flex w-full rounded-md border border-border bg-surface px-3 py-2 text-sm leading-[22px] text-text-primary placeholder:text-text-secondary outline-none transition-colors focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger/30',
  {
    variants: {
      resize: {
        vertical: 'resize-y',
        none: 'resize-none',
      },
    },
    defaultVariants: {resize: 'vertical'},
  },
);

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement>, VariantProps<typeof textareaVariants> {}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(({className, resize, rows, ...props}, ref) => (
  <textarea rows={rows ?? 4} className={cn(textareaVariants({resize}), className)} ref={ref} {...props} />
));
Textarea.displayName = 'Textarea';
