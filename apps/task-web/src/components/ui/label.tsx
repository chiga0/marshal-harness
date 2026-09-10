// 表单标签原子：与控件 id/htmlFor 关联；required 标记仅作视觉提示，语义仍在控件本身。
import {forwardRef} from 'react';
import type {LabelHTMLAttributes, ReactNode} from 'react';
import {cn} from '../../lib/cn';

export interface LabelProps extends LabelHTMLAttributes<HTMLLabelElement> {
  required?: boolean;
  hint?: ReactNode;
}

export const Label = forwardRef<HTMLLabelElement, LabelProps>(({className, required, hint, children, ...props}, ref) => (
  <span className="block">
    <label ref={ref} className={cn('mb-1 inline-block text-sm font-medium leading-5 text-text-primary', className)} {...props}>
      {children}
      {required ? <span className="ml-0.5 text-danger" aria-hidden="true">*</span> : null}
    </label>
    {hint ? <span className="mb-1 block text-xs leading-[18px] text-text-secondary">{hint}</span> : null}
  </span>
));
Label.displayName = 'Label';
