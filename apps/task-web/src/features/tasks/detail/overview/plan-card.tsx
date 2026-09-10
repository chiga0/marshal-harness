// 计划确认卡片：冻结展示用户所见 revision/digest；确认走 approveTask（revision+decisionDigest+幂等键）；
// 409/过期保留草稿并指引查看新内容，不自动替换摘要再提交；仅 allowedActions 含 approve 时提供确认入口。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {PlanRecord, TaskDetail, Transport} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction} from '../shared/logical-action';
import {formatDateTime, formatDuration} from '../shared/format';

export interface PlanCardProps {
  taskId: string;
  detail: TaskDetail;
  plan: PlanRecord;
  transport: Transport;
  /** 已查看内容过期后「查看新内容」：重新查询并回到最新计划。 */
  onViewLatest: () => void;
}

export function PlanCard({taskId, detail, plan, transport, onViewLatest}: PlanCardProps) {
  const [confirming, setConfirming] = useState<{revision: string; decisionDigest: string} | null>(null);
  const confirmed = plan.acceptedAt !== null || plan.rejectedAt !== null;
  const canApprove = detail.allowedActions.includes('approve') && !confirmed;
  return (
    <Card aria-label="当前计划" className="space-y-3" data-testid="plan-card">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold leading-6">当前计划</h2>
        <div className="flex flex-wrap items-center gap-2 text-xs text-text-secondary">
          <span>revision <code data-testid="plan-revision">{plan.revision}</code></span>
          {confirmed ? <span className="text-success">已确认于 {formatDateTime(plan.acceptedAt ?? plan.rejectedAt)}</span> : null}
        </div>
      </div>

      <dl className="grid grid-cols-1 gap-x-6 gap-y-1 text-sm leading-[22px] sm:grid-cols-2">
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">冻结时间</dt><dd>{formatDateTime(plan.frozenAt)}</dd></div>
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">过期时间</dt><dd>{plan.expiresAt ? formatDateTime(plan.expiresAt) : '无'}</dd></div>
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">预算</dt><dd>{plan.budgetMs !== null ? formatDuration(plan.budgetMs) : '暂无数据'}</dd></div>
        <div className="flex gap-2"><dt className="shrink-0 text-text-secondary">计划期限</dt><dd>{plan.deadlineAt ? formatDateTime(plan.deadlineAt) : '无'}</dd></div>
        <div className="flex gap-2 sm:col-span-2"><dt className="shrink-0 text-text-secondary">计划摘要</dt><dd className="break-all"><code className="text-xs" data-testid="plan-digest">{plan.planDigest}</code></dd></div>
        <div className="flex gap-2 sm:col-span-2"><dt className="shrink-0 text-text-secondary">确认摘要</dt><dd className="break-all"><code className="text-xs" data-testid="plan-decision-digest">{plan.decisionDigest}</code></dd></div>
      </dl>

      <div>
        <h3 className="mb-1 text-sm font-medium">执行节点（{plan.nodes.length}）</h3>
        <ul className="space-y-1 text-sm leading-[22px]">
          {plan.nodes.map(node => (
            <li key={node.nodeId} className="rounded border border-border bg-surface-muted/40 px-2 py-1">
              <code className="text-xs">{node.nodeId}</code>
              <span className="mx-2 text-text-secondary">{node.role}</span>
              <span>{node.description}</span>
            </li>
          ))}
        </ul>
        {plan.edges.length > 0 ? (
          <p className="mt-1 text-xs text-text-secondary">
            依赖：{plan.edges.map(edge => `${edge.from} → ${edge.to}`).join('；')}
          </p>
        ) : null}
      </div>

      {canApprove ? (
        <>
          <p className="text-xs text-text-secondary">
            只有显式确认后才会开始执行；确认将绑定上方 revision 与确认摘要。
          </p>
          <div>
            <Button
              variant="default"
              onClick={() => setConfirming({revision: plan.revision, decisionDigest: plan.decisionDigest})}
              data-testid="plan-approve-open"
            >
              确认执行该计划
            </Button>
          </div>
        </>
      ) : (
        <p className="text-xs text-text-secondary" data-testid="plan-approve-unavailable">
          {confirmed
            ? '该计划已确认，执行以服务端状态为准。'
            : '当前状态不提供计划确认（allowedActions 不含 approve）。'}
        </p>
      )}

      {confirming ? (
        <ApproveAttempt
          taskId={taskId}
          target={confirming}
          planExpiresAt={plan.expiresAt}
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
  target: {revision: string; decisionDigest: string};
  planExpiresAt: string | null;
  transport: Transport;
  onClose: () => void;
  onAccepted: () => void;
  onViewLatest: () => void;
}

export function ApproveAttempt({taskId, target, planExpiresAt, transport, onClose, onAccepted, onViewLatest}: ApproveAttemptProps) {
  const queryClient = useQueryClient();
  // 逻辑动作键绑定快照：同一计划内容重放复用同键；查看新内容后重新确认 => 新键。
  const action = useLogicalAction([taskId, 'approve', target.revision, target.decisionDigest]);
  const expired = planExpiresAt !== null && Date.parse(planExpiresAt) < Date.now();

  const doSubmit = (key: string) =>
    transport.approveTask(taskId, {revision: target.revision, decisionDigest: target.decisionDigest, idempotencyKey: key});

  const phase = action.phase;
  const isConflict = phase.kind === 'rejected' && phase.error instanceof ApiError && phase.error.status === 409;

  return (
    <>
      <ConfirmDialog
        open={phase.kind === 'idle'}
        title="确认执行该计划？"
        description={`确认将绑定 revision ${target.revision} 与确认摘要 ${target.decisionDigest}。确认后开始执行；提交中请勿重复操作。${expired ? '注意：该计划的过期时间已过，服务端可能拒绝确认。' : ''}`}
        confirmText="确认执行"
        onConfirm={() => void action.submit(doSubmit)}
        onCancel={onClose}
      />
      {phase.kind !== 'idle' ? (
        <div className="mt-3 space-y-2" data-testid="plan-approve-result">
          {phase.kind === 'submitting' ? (
            <p className="text-sm text-text-secondary" role="status">正在提交确认…（期间不可重复提交）</p>
          ) : null}
          {phase.kind === 'accepted' ? (
            <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status">
              <p className="text-sm font-medium text-success">确认已受理（受理不等于已开始执行）。</p>
              <p className="mt-1 text-sm text-text-secondary">稍后由任务状态反映结果；如上次操作中断，请先核对任务与回执后手动再操作。</p>
              <div className="mt-2"><Button size="sm" variant="outline" onClick={() => { onAccepted(); onClose(); }}>关闭并刷新任务</Button></div>
            </div>
          ) : null}
          {phase.kind === 'rejected' ? (
            <div className="space-y-2">
              <ErrorNotice
                error={phase.error}
                title="确认计划失败"
                onRefresh={onViewLatest}
              />
              {isConflict ? (
                <div className="rounded-md border border-warning/40 bg-warning/5 p-3" data-testid="plan-approve-stale">
                  <p className="text-sm text-text-primary">
                    计划在你查看期间已更新。你已查看的 revision {target.revision}（摘要 {target.decisionDigest}）草稿保留在此，
                    不会自动替换为新摘要再提交。
                  </p>
                  <div className="mt-2 flex gap-2">
                    <Button size="sm" variant="secondary" onClick={onViewLatest} data-testid="plan-approve-view-latest">
                      查看最新计划内容
                    </Button>
                    <Button size="sm" variant="ghost" onClick={onClose}>保留草稿并关闭</Button>
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
          {phase.kind === 'unknown' ? (
            <ErrorNotice
              error={phase.error}
              title="确认结果未知"
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
