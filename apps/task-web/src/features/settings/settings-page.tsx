// 骨架占位：设置（主题等）。主题选择将在 W1 交付设置为独立完整实现后保留。
import {useTheme} from '../../lib/theme/useTheme';
import {Button} from '../../components/ui/button';

export function SettingsPage() {
  const {preference, resolved, set} = useTheme();
  return (
    <section aria-label="设置" className="p-6">
      <h1 className="text-[22px] font-semibold leading-[30px]">设置</h1>
      <div className="mt-4 rounded-md border border-border bg-surface p-4">
        <div className="text-sm font-medium">主题（当前 {preference} → {resolved}）</div>
        <div className="mt-3 flex gap-2">
          {['light', 'dark', 'system'].map(value => (
            <Button key={value} variant={preference === value ? 'default' : 'outline'} size="sm" onClick={() => set(value as 'light' | 'dark' | 'system')}>
              {value === 'light' ? '浅色' : value === 'dark' ? '深色' : '跟随系统'}
            </Button>
          ))}
        </div>
        <p className="mt-3 text-xs leading-[18px] text-text-secondary">只保存这个偏好在会话存储，不保存 token。</p>
      </div>
    </section>
  );
}
