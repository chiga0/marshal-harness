// 主题: 浅/深/跟随系统，仅 sessionStorage（非敏感偏好）记录";模式"本身，token 永不持久化.
import {useEffect, useState, useSyncExternalStore} from 'react';

export type ThemePreference = 'light' | 'dark' | 'system';

let current: ThemePreference = 'system';
const listeners = new Set<() => void>();

if (typeof window !== 'undefined') {
  const saved = window.sessionStorage.getItem('ui1-theme-preference');
  if (saved === 'light' || saved === 'dark' || saved === 'system') current = saved;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function snapshot(): ThemePreference {
  return current;
}

export function setThemePreference(value: ThemePreference): void {
  current = value;
  if (typeof window !== 'undefined') window.sessionStorage.setItem('ui1-theme-preference', value);
  for (const listener of listeners) listener();
}

function resolveTheme(value: ThemePreference): 'light' | 'dark' {
  if (value === 'system' && typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  return value === 'dark' ? 'dark' : 'light';
}

export function useTheme(): {preference: ThemePreference; resolved: 'light' | 'dark'; set: (value: ThemePreference) => void} {
  const preference = useSyncExternalStore(subscribe, snapshot, snapshot);
  const [resolved, setResolved] = useState<'light' | 'dark'>(() => resolveTheme(preference));
  useEffect(() => {
    const value = resolveTheme(preference);
    setResolved(value);
    document.documentElement.classList.toggle('dark', value === 'dark');
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = () => {
      if (preference === 'system') {
        const next = resolveTheme('system');
        setResolved(next);
        document.documentElement.classList.toggle('dark', next === 'dark');
      }
    };
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, [preference]);
  return {preference, resolved, set: setThemePreference};
}
