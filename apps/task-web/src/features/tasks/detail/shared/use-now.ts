// 轻量「当前时间」hooks：用于到期即有 UI 反馈（禁用按钮/拒绝提交），
// 不能只依赖轮询重渲染（数据不变时组件不重渲染，到期会被错过——UI-10）。

import {useEffect, useState} from 'react';

/** 每 intervalMs 重新取一次 Date.now()；返回当前毫秒时间。 */
export function useNow(intervalMs = 10000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
