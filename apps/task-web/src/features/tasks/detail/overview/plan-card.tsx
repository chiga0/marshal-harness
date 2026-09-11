// 计划批准卡片：展示用户所见的计划 revision/digest 快照；批准走 approvePlan
//（expectedRevision=任务 revision + planRevision=计划 revision + planDigest=计划摘要 + 幂等键）。
// 409/冲突保留已查看快照并指引查看新内容，不自动替换摘要再提交；仅 allowedActions 含 approve 时提供批准入口。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {PlanRecord, Revision, TaskRecord, Transport} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {ErrorNotice} from '../shared/error-notice';
import {OperationReceipt} from '../shared/operation-receipt';
import {useLogicalAction} from '../shared/logical-action';
import {formatDuration, workerRoleLabel} from '../shared/format';

export interface PlanCardProps {
  task: TaskRecord;
  plan: PlanRecord;
  transport: Transport;
  /** 已查看内容过期后「查看新内容」：重新查询并回到最新计划。 */
  onViewLatest: () => void;
}

interface ApproveTarget {
  expectedRevision: Revision;
  planRevision: Revision;
  planDigest: string;
}

// 仅识别整条、合法的 JSON 对象/数组以改善排版，不解释其中字段或生成新的业务合同。
function isStructuredCriterion(value: string): boolean {
  const text = value.trim();
  if (!text.startsWith('{') && !text.startsWith('[')) return false;
  try { const parsed: unknown = JSON.parse(text); return parsed !== null && typeof parsed === 'object'; }
  catch { return false; }
}

export function PlanCard({task, plan, transport, onViewLatest}: PlanCardProps) {
  const [confirming, setConfirming] = useState<ApproveTarget | null>(null);
  const canApprove = task.allowedActions.includes('approve');
  const readableAcceptance = plan.acceptance.filter(value => !isStructuredCriterion(value));
  const structuredAcceptance = plan.acceptance.filter(isStructuredCriterion);
  return (
    <Card aria-label="当前计划" className="space-y-3" data-testid="plan-card">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">当前计划</h2>
        <span className="text-xs text-text-secondary">计划 revision <code data-testid="plan-revision">{plan.revision}</code></span>
      </div>

      <p className="whitespace-pre-wrap break-words text-sm leading-[22px]">{plan.summary}</p>

      <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">时长预算</dt><dd>{formatDuration(plan.budget.timeoutMs)}</dd></div>
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">尝试/并行上限</dt><dd>{plan.budget.maxAttempts} 次 / {plan.budget.maxWorkers} 个</dd></div>
      </dl>

      <div>
        <h3 className="mb-1 text-sm font-medium">角色分工（{plan.nodes.length} 个节点）</h3>
        <ul className="space-y-2 text-sm leading-[22px]">
          {plan.nodes.map(node => (
            <li key={node.id} className="break-words border-l-2 border-border pl-3">
              <p className="font-medium"><span>{workerRoleLabel(node.role)}</span><code className="ml-2 break-all text-xs text-text-secondary">{node.id}</code></p>
              <p className="whitespace-pre-wrap">{node.goal}</p>
              {node.scope.length > 0 ? <p className="mt-1 text-xs text-text-secondary">范围：{node.scope.join('、')}</p> : null}
            </li>
          ))}
        </ul>
        {plan.edges.length > 0 ? (
          <p className="mt-2 break-words text-xs text-text-secondary">
            依赖：{plan.edges.map(edge => `${edge.from} → ${edge.to}`).join('；')}
          </p>
        ) : null}
      </div>

      {plan.deliverables.length > 0 ? (
        <section aria-label="预期交付" className="space-y-1 text-sm leading-[22px]"><h3 className="font-medium">预期交付</h3><ul className="list-disc space-y-1 pl-5">{plan.deliverables.map((value, index) => <li key={index} className="whitespace-pre-wrap break-words">{value}</li>)}</ul></section>
      ) : null}
      {plan.acceptance.length > 0 ? (
        <section aria-label="验收口径" className="space-y-1 text-sm leading-[22px]">
          <h3 className="font-medium">验收口径</h3>
          {readableAcceptance.length ? <ul className="list-disc space-y-1 pl-5">{readableAcceptance.map((value, index) => <li key={index} className="whitespace-pre-wrap break-words">{value}</li>)}</ul> : <p className="text-text-secondary">服务端未提供人可读验收口径，请展开技术与审计详情核对结构化原文。</p>}
          {structuredAcceptance.length > 0 ? <p className="text-xs text-text-secondary">另有 {structuredAcceptance.length} 条结构化验收原文，完整保留在下方技术与审计详情中；批准前请一并展开核对。界面不解释这些 JSON 的业务含义，也不据此认定验收通过。</p> : null}
        </section>
      ) : null}
      {plan.assumptions.length > 0 ? (
        <section aria-label="前提假设" className="space-y-1 text-sm leading-[22px]"><h3 className="font-medium">前提假设</h3><ul className="list-disc space-y-1 pl-5">{plan.assumptions.map((value, index) => <li key={index} className="whitespace-pre-wrap break-words">{value}</li>)}</ul></section>
      ) : null}
      {plan.interaction ? <p className="text-xs text-text-secondary">问答数量上限：{plan.interaction.maxQuestions} 问</p> : null}

      <details className="rounded border border-border p-3 text-xs" data-testid="plan-technical-details">
        <summary className="min-h-11 cursor-pointer py-3 font-medium text-text-secondary focus-visible:outline focus-visible:outline-2">计划技术与审计详情{structuredAcceptance.length ? `（含 ${structuredAcceptance.length} 条结构化验收原文）` : ''}</summary>
        <div className="space-y-3 break-words">
          <p>计划摘要：<code className="break-all" data-testid="plan-digest">{plan.digest}</code></p>
          {plan.interaction ? <p>问答策略：<code>{plan.interaction.profile}</code></p> : null}
          {plan.repair ? <p>修复策略：<code>{plan.repair.profile}</code></p> : null}
          {structuredAcceptance.map((value, index) => <div key={index}><h4 className="mb-1 font-medium">结构化验收原文 {index + 1}</h4><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-surface-muted p-2" tabIndex={0}>{value}</pre></div>)}
        </div>
      </details>

      {canApprove ? (
        <>
          <p className="text-xs text-text-secondary">
            只有显式批准后才会进入执行；批准将绑定当前任务 revision、计划 revision 与计划摘要。
          </p>
          <div>
            <Button
              variant="default"
              onClick={() => setConfirming({expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest})}
              data-testid="plan-approve-open"
            >
              批准执行该计划
            </Button>
          </div>
        </>
      ) : (
        <p className="text-xs text-text-secondary" data-testid="plan-approve-unavailable">
          当前状态不提供计划批准（allowedActions 不含 approve）。
        </p>
      )}

      {confirming ? (
        <ApproveAttempt
          taskId={task.id}
          target={confirming}
          transport={transport}
          onClose={() => setConfirming(null)}
          onAccepted={() => onViewLatest()}
          onViewLatest={() => {
            setConfirming(null);
            onViewLatest();
          }}
        />
      ) : null}
    </Card>
  );
}

interface ApproveAttemptProps {
  taskId: string;
  /** 打开确认时冻结的快照；提交绑定该快照，轮询更新不会替换它。 */
  target: ApproveTarget;
  transport: Transport;
  onClose: () => void;
  onAccepted: () => void;
  onViewLatest: () => void;
}

export function ApproveAttempt({taskId, target, transport, onClose, onAccepted, onViewLatest}: ApproveAttemptProps) {
  const queryClient = useQueryClient();
  // 逻辑动作键绑定快照：同一计划内容重放复用同键；查看新内容后重新批准 => 新键。
  const action = useLogicalAction([taskId, 'approve', target.expectedRevision, target.planRevision, target.planDigest], [taskId, 'approve']);

  const doSubmit = (key: string) =>
    transport.approvePlan(taskId, {
      expectedRevision: target.expectedRevision,
      planRevision: target.planRevision,
      planDigest: target.planDigest,
      idempotencyKey: key,
    });

  const phase = action.phase;
  const isConflict = phase.kind === 'rejected' && phase.error instanceof ApiError && phase.error.status === 409;

  return (
    <>
      <ConfirmDialog
        open={phase.kind === 'idle'}
        title="批准执行该计划？"
        description={`批准将绑定任务 revision ${target.expectedRevision}、计划 revision ${target.planRevision} 与计划摘要 ${target.planDigest}。受理不等于已开始执行；执行进展以服务端任务状态为准。提交中请勿重复操作。`}
        confirmText="批准执行"
        onConfirm={() => void action.submit(doSubmit)}
        onCancel={onClose}
      />
      {phase.kind !== 'idle' ? (
        <div className="mt-3 space-y-2" data-testid="plan-approve-result">
          {phase.kind === 'submitting' ? (
            <p className="text-sm text-text-secondary" role="status">正在提交批准…（期间不可重复提交）</p>
          ) : null}
          {phase.kind === 'accepted' ? (
            <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status">
              <p className="text-sm font-medium text-success">批准已受理（受理不等于已开始执行）。</p>
              <p className="mt-1 text-sm text-text-secondary">稍后由任务状态反映结果；如上次操作中断，请先核对任务与回执后手动再操作。</p>
              <OperationReceipt result={phase.result} taskId={taskId} kind="task.approve" transport={transport} />
              <div className="mt-2"><Button size="sm" variant="outline" onClick={() => { onAccepted(); onClose(); }}>关闭并刷新任务</Button></div>
            </div>
          ) : null}
          {phase.kind === 'rejected' ? (
            <div className="space-y-2">
              <ErrorNotice
                error={phase.error}
                title="批准计划失败"
                onRefresh={onViewLatest}
              />
              {isConflict ? (
                <div className="rounded-md border border-warning/40 bg-warning/5 p-3" data-testid="plan-approve-stale">
                  <p className="text-sm text-text-primary">
                    计划或任务在你查看期间已更新。你已查看的任务 revision {target.expectedRevision} / 计划 revision {target.planRevision}
                    （摘要 {target.planDigest}）快照保留在此，不会自动替换为新摘要再提交。
                  </p>
                  <div className="mt-2 flex gap-2">
                    <Button size="sm" variant="secondary" onClick={onViewLatest} data-testid="plan-approve-view-latest">
                      查看最新计划内容
                    </Button>
                    <Button size="sm" variant="ghost" onClick={onClose}>保留快照并关闭</Button>
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
          {phase.kind === 'unknown' ? (
            <ErrorNotice
              error={phase.error}
              title="批准结果未知"
              outcomeNote="请求可能已被服务端受理。可显式原键重放一次（不重新执行），或先刷新核对任务与回执。"
              onReplay={() => void action.replay(doSubmit)}
              onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); }}
            />
          ) : null}
        </div>
      ) : null}
    </>
  );
}
