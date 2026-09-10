import type {HTMLAttributes} from 'react';
import {cn} from '../../lib/cn';

export function Card({className, ...props}: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('rounded-lg border border-border bg-surface p-4 shadow-none', className)} {...props} />;
}

export function CardHeader({className, ...props}: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('mb-3 flex items-center justify-between gap-3', className)} {...props} />;
}

export function CardTitle({className, ...props}: HTMLAttributes<HTMLHeadingElement>) {
  return <h3 className={cn('text-base font-semibold leading-6', className)} {...props} />;
}
