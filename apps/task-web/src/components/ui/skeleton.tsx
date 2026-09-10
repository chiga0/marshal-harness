// 骨架原子：仅用于加载占位；prefers-reduced-motion 下动画已被 tokens.css 全局停用。
import type {HTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export function Skeleton({className, ...props}: HTMLAttributes<HTMLDivElement>) {
  return <div aria-hidden="true" className={cn('animate-pulse rounded-md bg-surface-muted', className)} {...props} />;
}
