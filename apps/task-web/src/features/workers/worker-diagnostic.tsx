import type {ExecutionDiagnostic, WorkerRecord} from '@/lib/transport/types';
const stages: Record<ExecutionDiagnostic['stage'], string> = {preparing:'准备工作输入',starting:'启动 Agent',provider:'Agent 执行',permission:'工具授权检查',collecting:'收集交付成果',cleanup:'清理执行环境',protocol:'受管结果协议解析'};
const reasons: Record<string,string> = {
  invalid_json:'返回文本不是合法 JSON',
  invalid_leader_result:'受管执行结果不符合要求',
  invalid_leader_decision:'Leader 提案不符合合同',
  invalid_review_report:'独立评审报告不符合合同',
  task_files_missing_output: '缺少约定的输出文件',
  task_files_input_changed: '输入文件内容已改变',
  task_files_identity_changed: '文件身份或属性已改变',
  task_files_changed: '采集期间文件发生变化',
  task_files_unallowed_output: '发现未授权的输出文件',
  task_files_depot_integrity: '制品存储校验失败',
  task_files_limit: '文件数量或大小超过限制',
  task_files_unavailable: '文件采集条件不满足',
  business_cleanup_required: '缺少确认清理的证据',
  business_execution_mismatch: '执行身份不匹配',
  business_unapproved_layout: '输出布局绑定不匹配',
  business_report_limit: '最终报告格式或大小不符合限制',

  preparation_failed:'准备工作输入失败',provider_start_failed:'Agent 启动失败',provider_failed:'Agent 执行失败',collection_failed:'成果收集失败',cleanup_unconfirmed:'尚未确认执行环境已清理',deadline_exceeded:'执行超过截止期限',permission_denied:'工具请求未获授权',permission_shape_denied:'工具授权请求格式不符合要求',permission_kind_denied:'该类工具操作未获授权',permission_path_denied:'请求访问的路径未获授权',
};
export function diagnosticReason(diagnostic: ExecutionDiagnostic): string {return reasons[diagnostic.code] ?? '尚无可识别的具体原因';}
export function WorkerDiagnostic({worker}:{worker:WorkerRecord}) {
  const diagnostic=worker.observation?.diagnostic;
  if(!diagnostic && !['failed','unknown'].includes(worker.status))return null;
  return <section aria-label="最近记录的诊断" data-testid="worker-diagnostic" className="space-y-2 border-t border-border pt-4">
    <h3 className="text-sm font-semibold">最近记录的诊断</h3>
    {diagnostic ? <>
      <p className="text-sm font-medium">{diagnosticReason(diagnostic)}</p>
      <p className="text-xs text-text-secondary">观察阶段：{stages[diagnostic.stage] ?? '未知阶段'} · {diagnostic.source === 'controller' ? '执行控制器记录' : '工具授权检查记录'}</p>
      <p className="text-xs text-text-secondary">这是保留的历史诊断，不一定是最终失败原因；执行状态以上方成员状态为准。</p>
      <details className="workspace-disclosure"><summary>诊断技术详情</summary><code className="break-all text-xs">{diagnostic.stage} / {diagnostic.code} / {diagnostic.source}</code></details>
    </> : <p className="text-sm text-text-secondary">未报告具体原因，不能从失败或未知状态推断根因。</p>}
  </section>;
}
