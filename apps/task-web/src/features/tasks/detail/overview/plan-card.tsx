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

export function PlanCard({task, plan, transport, onViewLatest}: PlanCardProps) {
  const [confirming, setConfirming] = useState<ApproveTarget | null>(null);
  const canApprove = task.allowedActions.includes('approve');
  return (
    <Card aria-label="当前计划" className="space-y-3" data-testid="plan-card">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">当前计划</h2>
        <span className="text-xs text-text-secondary">计划 revision <code data-testid="plan-revision">{plan.revision}</code></span>
      </div>

      <p className="text-sm leading-[22px]">{plan.summary}</p>

      <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">时长预算</dt><dd>{formatDuration(plan.budget.timeoutMs)}</dd></div>
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">尝试/并行上限</dt><dd>{plan.budget.maxAttempts} 次 / {plan.budget.maxWorkers} 个</dd></div>
        <div className="flex gap-2 sm:col-span-2"><dt className="shrink-0 text-text-secondary">计划摘要</dt><dd className="break-all"><code className="text-xs" data-testid="plan-digest">{plan.digest}</code></dd></div>
      </dl>

      <div>
        <h3 className="mb-1 text-sm font-medium">执行节点（{plan.nodes.length}）</h3>
        <ul className="space-y-1 text-sm leading-[22px]">
          {plan.nodes.map(node => (
            <li key={node.id} className="rounded border border-border bg-surface-muted/40 px-2 py-1">
              <code className="text-xs">{node.id}</code>
              <span className="mx-2 text-text-secondary">{workerRoleLabel(node.role)}（{node.role}）</span>
              <span>{node.goal}</span>
              {node.scope.length > 0 ? <span className="ml-2 text-xs text-text-secondary">范围：{node.scope.join('、')}</span> : null}
            </li>
          ))}
        </ul>
        {plan.edges.length > 0 ? (
          <p className="mt-1 text-xs text-text-secondary">
            依赖：{plan.edges.map(edge => `${edge.from} → ${edge.to}`).join('；')}
          </p>
        ) : null}
      </div>

      {plan.deliverables.length > 0 ? (
        <p className="text-sm leading-[22px]"><span className="text-text-secondary">预期交付：</span>{plan.deliverables.join('、')}</p>
      ) : null}
      {plan.acceptance.length > 0 ? (
        <p className="text-sm leading-[22px]"><span className="text-text-secondary">验收口径：</span>{plan.acceptance.join('、')}</p>
      ) : null}
      {plan.assumptions.length > 0 ? (
        <p className="text-sm leading-[22px]"><span className="text-text-secondary">前提假设：</span>{plan.assumptions.join('、')}</p>
      ) : null}
      {plan.interaction ? (
        <p className="text-xs text-text-secondary">
          问答策略：<code>{plan.interaction.profile}</code>（最多 {plan.interaction.maxQuestions} 问）
        </p>
      ) : null}
      {plan.repair ? (
        <p className="text-xs text-text-secondary">修复策略：<code>{plan.repair.profile}</code></p>
      ) : null}

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
  const action = useLogicalAction([taskId, 'approve', target.expectedRevision, target.planRevision, target.planDigest]);

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
