// 模态层原语（UI-09）：Dialog/抽屉/移动导航统一走这一个焦点与层叠模型——
// - LIFO 层叠仲裁：一次 Escape 只作用于最上层，嵌套确认框不会连坐关闭底层抽屉；
// - Tab/Shift+Tab 圈禁仅最上层生效，焦点不进入背景层；
// - 初始聚焦与焦点归还按 open 变化恰好执行一次：渲染期回调 identity 变化（轮询重渲染）不重置焦点。

import {useEffect, useRef} from 'react';

const FOCUSABLE = 'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

const stack: number[] = [];
let nextLayerId = 1;

/** 测试/诊断：当前层叠深度。 */
export function modalLayerDepth(): number {
  return stack.length;
}

export interface ModalLayerOptions {
  /** 层是否打开；false→true 时入栈并聚焦，true→false 时出栈并归还焦点。 */
  open: boolean;
  /** Escape 回调：仅当本层为最上层时触发。 */
  onEscape: () => void;
  /** 初始聚焦元素的层内选择器；缺省聚焦容器自身（容器需 tabIndex={-1}）。 */
  initialSelector?: string;
  /** 打开期间锁定 body 滚动。 */
  lockBodyScroll?: boolean;
}

export function useModalLayer<T extends HTMLElement>({open, onEscape, initialSelector, lockBodyScroll = false}: ModalLayerOptions) {
  const ref = useRef<T>(null);
  const onEscapeRef = useRef(onEscape);
  onEscapeRef.current = onEscape;

  useEffect(() => {
    if (!open) return;
    const id = nextLayerId;
    nextLayerId += 1;
    stack.push(id);
    const previousFocus = document.activeElement;
    const previousOverflow = lockBodyScroll ? document.body.style.overflow : null;
    if (lockBodyScroll) document.body.style.overflow = 'hidden';

    const onKey = (event: KeyboardEvent) => {
      // 一次 Escape/一次 Tab 移动只作用于最上层；下层监听同事件但按栈序弃权
      if (stack[stack.length - 1] !== id) return;
      if (event.key === 'Escape') {
        onEscapeRef.current();
        return;
      }
      if (event.key !== 'Tab') return;
      const root = ref.current;
      if (!root) return;
      // 不做 offsetParent 可见性过滤（jsdom 恒为 null 且模态内容按设计整层可见）；禁用项由选择器排除
      const items = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) {
        event.preventDefault();
        return;
      }
      const first = items[0]!;
      const last = items[items.length - 1]!;
      const active = document.activeElement;
      if (event.shiftKey && (active === first || active === root || !root.contains(active))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (active === last || active === root || !root.contains(active))) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    const initial = initialSelector ? ref.current?.querySelector<HTMLElement>(initialSelector) : null;
    (initial ?? ref.current)?.focus?.();

    return () => {
      document.removeEventListener('keydown', onKey);
      const index = stack.indexOf(id);
      if (index >= 0) stack.splice(index, 1);
      if (lockBodyScroll) document.body.style.overflow = previousOverflow ?? '';
      (previousFocus as HTMLElement | null)?.focus?.();
    };
  }, [open, initialSelector, lockBodyScroll]);

  return ref;
}
