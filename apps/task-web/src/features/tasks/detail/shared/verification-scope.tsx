import type {AcceptanceStatus} from '@/lib/transport/types';

// 保留 audit.acceptance 的原状态；不从自由文本计划或 Task 终态扩充检查范围。
export function configuredCheckLabel(status: AcceptanceStatus): string {
  return {pending:'配置检查待完成',passed:'配置检查通过',failed:'配置检查失败',unknown:'配置检查结果未知'}[status];
}
export function VerificationScope() {
  return <div className="space-y-1 text-xs leading-5 text-text-secondary" data-testid="verification-scope">
    <p className="font-medium">逐项业务验证覆盖：未确认</p>
    <p>此读数表示所配置检查的结果；具体检查项尚未在这里解析。业务操作与外部效果的实际验证范围以绑定证据为准。</p>
  </div>;
}
