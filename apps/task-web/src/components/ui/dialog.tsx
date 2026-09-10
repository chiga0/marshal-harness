// 轻量模态 Dialog（不引 Radix 依赖）：Escape 关闭、覆盖层点击默认不关闭（确认动作由内容决定）。
// 焦点/Escape/层叠统一由 useModalLayer 提供（UI-09）；提示性提示 vs 破坏性确认分开：ConfirmDialog 派生自本组件。
import type {ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {cn} from '../../lib/cn';
import {Button} from './button';
import {useModalLayer} from './modal-layer';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children?: ReactNode;
  footer?: ReactNode;
  className?: string;
}

export function Dialog({open, onClose, title, description, children, footer, className}: DialogProps) {
  const dialogRef = useModalLayer<HTMLDivElement>({open, onEscape: onClose, initialSelector: '[data-dialog-initial]', lockBodyScroll: true});

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 px-4" role="presentation">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="dialog-title"
        aria-describedby={description ? 'dialog-description' : undefined}
        tabIndex={-1}
        className={cn(
          'w-full max-w-lg rounded-lg border border-border bg-surface p-5 shadow-xl focus:outline-none',
          className,
        )}
      >
        <h2 id="dialog-title" className="text-base font-semibold leading-6 text-text-primary">{title}</h2>
        {description ? <p id="dialog-description" className="mt-1 text-sm leading-[22px] text-text-secondary">{description}</p> : null}
        <div className="mt-4">{children}</div>
        {footer ? <div className="mt-5 flex justify-end gap-2">{footer}</div> : null}
      </div>
    </div>,
    document.body,
  );
}

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  description?: string;
  confirmText: string;
  cancelText?: string;
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  loading?: boolean;
}

export function ConfirmDialog({open, title, description, confirmText, cancelText = '取消', destructive, onConfirm, onCancel, loading}: ConfirmDialogProps) {
  return (
    <Dialog
      open={open}
      onClose={onCancel}
      title={title}
      {...(description !== undefined ? {description} : {})}
      footer={
        <>
          <Button variant="outline" onClick={onCancel}>{cancelText}</Button>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            onClick={onConfirm}
            loading={loading ?? false}
            data-dialog-initial
          >
            {confirmText}
          </Button>
        </>
      }
    />
  );
}
