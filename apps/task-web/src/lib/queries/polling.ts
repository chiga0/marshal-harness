// 视图激活轮询契约：激活详情 ≈2s、已加载列表 ≈5s、隐藏页 ≈30s 或停止；401 停轮询终止在查询层。
// 在浏览器文档可见性切换时把在途轮询立即换到隐藏节奏，恢复时回到详情节奏；兜底由 React Query 超时链控制。

import {useEffect, useState} from 'react';
import {intervalFor, type PollMode} from './client';

export function usePollMode(mode: PollMode): number | null {
  const [effectiveMode, setEffectiveMode] = useState<PollMode>(mode);
  useEffect(() => {
    setEffectiveMode(mode);
  }, [mode]);
  useEffect(() => {
    if (typeof document === 'undefined') return;
    const update = () => {
      if (document.hidden) {
        setEffectiveMode('hidden');
      } else {
        setEffectiveMode(mode);
      }
    };
    document.addEventListener('visibilitychange', update);
    update();
    return () => document.removeEventListener('visibilitychange', update);
  }, [mode]);
  return intervalFor(effectiveMode);
}
