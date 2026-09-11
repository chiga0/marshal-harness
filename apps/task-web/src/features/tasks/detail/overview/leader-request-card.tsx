// Leader pendingRequest 卡片：kind=business 以自由文本或所选选项值答复（answer），
// kind=publication 完整展示 Core 授权正文并允许/拒绝（decision）；两者都走 leader.reply，
// body 绑定 expectedRevision=任务 revision + requestDigest=请求摘要 + 幂等键（闭集两分支，无 outcome/requestId/revision 字段）。
// 与 task.answer 严格分开；2xx 仅证明该 Leader 请求被接纳，不代表已继续/已发布，也不显示 Worker 投递/ACK。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {Badge} from '@/components/ui/badge';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {LeaderAuthorization, LeaderReplyBody, LeaderRequestDTO, Revision, Transport} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {isPast, isPastAt} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction, type ActionPhase} from '../shared/logical-action';
import {useLeaderReplyReceipt} from '../shared/leader-reply-receipt';
import {useNow} from '../shared/use-now';
import {formatBytes, formatDateTime} from '../shared/format';

export interface LeaderRequestCardProps {
  taskId: string;
  /** 所属 Task revision（CAS expectedRevision）。合同必返，永远可用。 */
  expectedRevision: Revision;
  request: LeaderRequestDTO;
  transport: Transport;
  onChanged: () => void;
}

export function LeaderRequestCard({taskId, expectedRevision, request, transport, onChanged}: LeaderRequestCardProps) {
  const [replyAccepted, setReplyAccepted] = useLeaderReplyReceipt(taskId, request);
  const actionable = request.status === 'pending';
  // UI-10：到期必须即时反馈（不能只靠轮询重渲染），到期立即禁用回答/授权
  const now = useNow(5000);
  const expired = isPastAt(request.deadlineAt, now);
  return (
    <Card aria-label="Leader 待处理请求" className="space-y-2" data-testid="leader-request-card" data-request-kind={request.kind}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={request.kind === 'publication' ? 'default' : 'warning'}>
          {request.kind === 'publication' ? 'Leader 发布授权请求' : 'Leader 业务请求'}
        </Badge>
        <code className="text-xs text-text-secondary">{request.id}</code>
        <span className="text-xs text-text-secondary">{replyAccepted && actionable ? '答复已受理，等待 Leader 更新。服务端投影：' : '状态：'}<code>{request.status}</code></span>
        <span className={`text-xs ${expired ? 'text-danger' : 'text-text-secondary'}`}>
          期限：{formatDateTime(request.deadlineAt)}{expired && request.status === 'pending' ? '（已过，服务端可能拒绝答复）' : ''}
        </span>
        {request.nodeIds.length > 0 ? (
          <span className="text-xs text-text-secondary">节点：{request.nodeIds.join('、')}</span>
        ) : null}
      </div>

      <p className="whitespace-pre-wrap text-sm leading-[22px]">{request.prompt}</p>

      {request.kind === 'publication' ? (
        <PublicationAuthorization authorization={request.authorization} />
      ) : null}

      <p className="break-all text-xs text-text-secondary">
        请求摘要：<code>{request.requestDigest}</code>
      </p>

      {!actionable ? (
        <p className="text-sm text-text-secondary" data-testid="leader-request-closed">
          该请求已答复或已关闭{request.replyDigest ? <>，答复摘要：<code className="break-all text-xs">{request.replyDigest}</code></> : null}。
        </p>
      ) : replyAccepted ? <AcceptedReplyNotice onRefresh={onChanged} /> : request.kind === 'publication' ? (
        <PublicationReplyActions key={request.id} taskId={taskId} expectedRevision={expectedRevision} request={request} expired={expired} transport={transport} onChanged={onChanged} onAccepted={() => setReplyAccepted(true)} />
      ) : (
        <BusinessReplyActions key={request.id} taskId={taskId} expectedRevision={expectedRevision} request={request} expired={expired} transport={transport} onChanged={onChanged} onAccepted={() => setReplyAccepted(true)} />
      )}
    </Card>
  );
}

function PublicationAuthorization({authorization}: {authorization: LeaderAuthorization | null}) {
  if (!authorization) {
    return (
      <p className="rounded border border-danger/40 bg-danger/5 p-2 text-sm text-danger" data-testid="publication-authorization-missing">
        服务端未提供发布授权正文。按合同不得批准：拒绝或等待服务修正。
      </p>
    );
  }
  const fields: Array<[string, unknown]> = [
    ['目标 targetId', authorization.targetId],
    ['文件名 name', authorization.name],
    ['操作 operation', authorization.operation],
    ['大小 bytes', formatBytes(authorization.bytes)],
    ['过期时间 expiresAt', formatDateTime(authorization.expiresAt)],
    ['artifactId', authorization.artifactId],
    ['成果摘要 artifactDigest', authorization.artifactDigest],
    ['计划摘要 planDigest', authorization.planDigest],
    ['验收摘要 acceptanceDigest', authorization.acceptanceDigest],
    ['评审摘要 reviewDigest', authorization.reviewDigest],
    ['目标策略摘要 targetPolicyDigest', authorization.targetPolicyDigest],
    ['taskId', authorization.taskId],
  ];
  return (
    <div className="rounded border border-border bg-surface-muted/30 p-2" data-testid="publication-authorization">
      <h4 className="text-xs font-medium text-text-secondary">发布授权正文（仅展示不等于批准；不允许替换目标）</h4>
      <dl className="mt-1 grid grid-cols-1 gap-y-1 text-xs leading-5">
        {fields.map(([label, value]) => (
          <div key={label} className="flex gap-2">
            <dt className="shrink-0 text-text-secondary">{label}</dt>
            <dd className="break-all"><code>{value === null || value === undefined ? '未提供' : String(value)}</code></dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

interface ReplyActionsProps {
  taskId: string;
  expectedRevision: Revision;
  request: LeaderRequestDTO;
  /** UI-10：到期立即禁用回答/授权；确认框提交时仍再校验一次。 */
  expired: boolean;
  transport: Transport;
  onChanged: () => void;
  onAccepted: () => void;
}

interface ConfirmationVersion {revision: Revision; id: string; digest: string}
function sameConfirmation(version: ConfirmationVersion | null, request: LeaderRequestDTO, revision: Revision): boolean {
  return version !== null && version.revision === revision && version.id === request.id && version.digest === request.requestDigest;
}

/** 业务请求：options 非空时选项即答复按钮；否则自由文本 + 答复按钮。answer 为选项 value 或文本原文。
 *  答复值保存在稳定 state（选择/文本不随确认清空），保证提交中原键重放仍用同一逻辑动作键。 */
function BusinessReplyActions({taskId, expectedRevision, request, expired, transport, onChanged, onAccepted}: ReplyActionsProps) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string | null>(null);
  const [freeText, setFreeText] = useState('');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [confirmation, setConfirmation] = useState<ConfirmationVersion | null>(null);
  const confirmationCurrent = sameConfirmation(confirmation, request, expectedRevision);
  const openConfirmation = () => {
    setConfirmation({revision: expectedRevision, id: request.id, digest: request.requestDigest});
    setDialogOpen(true);
  };
  const trimmed = freeText.trim();
  const hasOptions = request.options.length > 0;
  const answer = hasOptions ? selected : (trimmed === '' ? null : trimmed);
  const answerLabel = hasOptions
    ? (request.options.find(option => option.value === selected)?.label ?? selected ?? '')
    : '自由文本答复';
  const action = useLogicalAction([taskId, 'leader.reply', request.id, expectedRevision, request.requestDigest, answer ?? ''], [taskId, 'leader.reply', request.id]);
  const refresh = () => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); };

  const doSubmit = (key: string): Promise<unknown> => {
    if (answer === null) return Promise.reject(new Error('缺少答复内容'));
    const body: LeaderReplyBody = {
      expectedRevision,
      requestDigest: request.requestDigest,
      answer,
      idempotencyKey: key,
    };
    return transport.leaderReply(taskId, request.id, body).then(result => { onAccepted(); return result; });
  };

  return (
    <div className="space-y-2">
      {action.phase.kind === 'idle' ? (
        <>
          {hasOptions ? (
            <div className="flex flex-wrap gap-2" role="group" aria-label="答复选项">
              {request.options.map(option => (
                <Button
                  key={option.value}
                  size="sm"
                  variant={selected === option.value ? 'default' : 'outline'}
                  disabled={expired}
                  onClick={() => {
                    setSelected(option.value);
                    openConfirmation();
                  }}
                  data-testid={`leader-answer-option-${option.value}`}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          ) : (
            <div className="space-y-2">
              <div>
                <label className="block text-xs text-text-secondary" htmlFor={`leader-answer-${request.id}`}>答复内容</label>
                <textarea
                  id={`leader-answer-${request.id}`}
                  className="mt-1 w-full rounded-md border border-border bg-surface px-2 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                  rows={2}
                  value={freeText}
                  onChange={event => setFreeText(event.target.value)}
                />
              </div>
              <div>
                <Button
                  size="sm"
                  disabled={answer === null || expired}
                  onClick={openConfirmation}
                  data-testid="leader-answer-open"
                >
                  答复
                </Button>
                {answer === null && !expired ? <span className="ml-2 text-xs text-text-secondary">请先填写答复内容</span> : null}
              </div>
            </div>
          )}
          {expired ? (
            <p className="text-xs text-danger" data-testid="leader-request-expired-block">该请求已过答复期限，回答已禁用；请刷新查看最新待处理请求。</p>
          ) : (
            <p className="text-xs text-text-secondary">
              答复走 leader.reply（绑定任务 revision 与请求摘要），不走 task.answer；不回改 revision/批准状态。
            </p>
          )}
        </>
      ) : null}

      <LeaderReplyOutcome phase={action.phase} depsStale={action.depsStale} onReplay={() => void action.replay(doSubmit)} onRefresh={refresh} onRestart={action.reset} expired={expired} />
      {dialogOpen && !confirmationCurrent ? <p role="alert" className="text-sm text-warning">请求版本已变化，原确认已关闭。请核对最新正文并重新打开确认；尚未提交答复。</p> : null}

      <ConfirmDialog
        open={dialogOpen && confirmationCurrent && answer !== null && !expired && action.phase.kind === 'idle'}
        title="确认提交该 Leader 答复？"
        description={
          answer !== null
            ? `将以「${answerLabel}」答复请求 ${request.id}（任务 revision ${expectedRevision}）。提交绑定请求摘要 ${request.requestDigest}；受理不代表已执行或已继续，后续以 Leader 与任务投影为准。`
            : ''
        }
        confirmText="确认答复"
        onConfirm={() => {
          // UI-10：确认框打开期间到期的，一律不发请求（后端拒绝仍是兜底）
          if (confirmationCurrent && !expired && answer !== null && !isPast(request.deadlineAt)) void action.submit(doSubmit);
          setDialogOpen(false);
        }}
        onCancel={() => setDialogOpen(false)}
      />
    </div>
  );
}

/** 发布授权请求：允许/拒绝各有二次确认；缺授权正文时允许被禁用。decision 保持稳定，重放复用同一逻辑动作键。 */
function PublicationReplyActions({taskId, expectedRevision, request, expired, transport, onChanged, onAccepted}: ReplyActionsProps) {
  const queryClient = useQueryClient();
  const [decision, setDecision] = useState<'allow' | 'deny' | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [confirmation, setConfirmation] = useState<ConfirmationVersion | null>(null);
  const confirmationCurrent = sameConfirmation(confirmation, request, expectedRevision);
  const action = useLogicalAction([taskId, 'leader.reply', request.id, expectedRevision, request.requestDigest, decision ?? ''], [taskId, 'leader.reply', request.id]);
  const refresh = () => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); };
  const hasAuthorization = request.authorization !== null;

  const doSubmit = (key: string): Promise<unknown> => {
    if (decision === null) return Promise.reject(new Error('缺少决定'));
    const body: LeaderReplyBody = {
      expectedRevision,
      requestDigest: request.requestDigest,
      decision,
      idempotencyKey: key,
    };
    return transport.leaderReply(taskId, request.id, body).then(result => { onAccepted(); return result; });
  };

  const openDialog = (value: 'allow' | 'deny') => {
    setDecision(value);
    setConfirmation({revision: expectedRevision, id: request.id, digest: request.requestDigest});
    setDialogOpen(true);
  };

  return (
    <div className="space-y-2">
      {action.phase.kind === 'idle' ? (
        <>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={!hasAuthorization || expired} onClick={() => openDialog('allow')} data-testid="leader-reply-allow">
              允许发布
            </Button>
            <Button size="sm" variant="destructive" disabled={expired} onClick={() => openDialog('deny')} data-testid="leader-reply-deny">
              拒绝发布
            </Button>
          </div>
          {expired ? (
            <p className="text-xs text-danger" data-testid="leader-request-expired-block">该请求已过答复期限，允许/拒绝已禁用；请刷新查看最新待处理请求。</p>
          ) : !hasAuthorization ? (
            <p className="text-xs text-danger">授权正文缺失，「允许发布」已禁用；可以拒绝或刷新等待服务修正。</p>
          ) : null}
          {!expired ? (
            <p className="text-xs text-text-secondary">
              决定走 leader.reply（绑定任务 revision 与请求摘要）；允许只授权这一份授权正文，不会替换目标。
            </p>
          ) : null}
        </>
      ) : null}

      <LeaderReplyOutcome phase={action.phase} depsStale={action.depsStale} onReplay={() => void action.replay(doSubmit)} onRefresh={refresh} onRestart={action.reset} expired={expired} />
      {dialogOpen && !confirmationCurrent ? <p role="alert" className="text-sm text-warning">授权请求版本已变化，原确认已关闭。请核对最新授权正文并重新打开确认；尚未提交授权。</p> : null}

      <ConfirmDialog
        open={dialogOpen && confirmationCurrent && decision !== null && !expired && action.phase.kind === 'idle'}
        title={decision === 'allow' ? '确认允许该发布？' : '确认拒绝该发布？'}
        description={
          decision === 'allow'
            ? `将允许按上方授权正文发布 artifact ${request.authorization?.artifactId ?? ''} 到目标 ${request.authorization?.targetId ?? ''}。受理不代表已发布完成；发布与后验进展以 Leader 投影为准。`
            : decision === 'deny'
              ? `将拒绝该发布请求（${request.id}）。拒绝是最终决定，服务端不会执行该发布。`
              : ''
        }
        destructive={decision === 'deny'}
        confirmText={decision === 'allow' ? '确认允许' : '确认拒绝'}
        onConfirm={() => {
          // UI-10：确认框打开期间到期的，一律不发请求（后端拒绝仍是兜底）
          if (confirmationCurrent && !expired && decision !== null && !isPast(request.deadlineAt)) void action.submit(doSubmit);
          setDialogOpen(false);
        }}
        onCancel={() => setDialogOpen(false)}
      />
    </div>
  );
}

function LeaderReplyOutcome({phase, depsStale, onReplay, onRefresh, onRestart, expired}: {phase: ActionPhase; depsStale: boolean; onReplay: () => void; onRefresh: () => void; onRestart: () => void; expired: boolean}) {
  const staleNote = depsStale && (phase.kind === 'unknown' || phase.kind === 'rejected') ? (
    <p className="text-xs leading-[18px] text-text-secondary" data-testid="leader-reply-deps-stale">
      检测到任务已推进到新版本（轮询 revision 已变化）。本次提交的键与状态保持不变；
      {phase.kind === 'unknown' ? '结果仍未知，请先刷新核对 Leader 投影；重放仍使用原键与原提交内容。' : '请先核对当前 Leader 请求正文、摘要及发布授权，再显式重新开始。'}
    </p>
  ) : null;
  if (phase.kind === 'submitting') return <p className="text-sm text-text-secondary" role="status">正在提交 Leader 答复…</p>;
  if (phase.kind === 'accepted') {
    return <AcceptedReplyNotice onRefresh={onRefresh} />;
  }
  if (phase.kind === 'rejected') {
    return (
      <>
        {staleNote}
        <ErrorNotice error={phase.error} title="Leader 答复失败" onRefresh={onRefresh} />
        {phase.error instanceof ApiError && phase.error.status === 409 ? (
          <div className="space-y-2" data-testid="leader-reply-conflict-recovery">
            <p className="text-xs text-text-secondary">
              请刷新查看新版请求。核对上方最新正文后可保留草稿重新开始；下次确认使用新幂等键和当前 revision、请求摘要，不会自动提交。
            </p>
            <Button size="sm" variant="outline" disabled={!depsStale || expired} onClick={onRestart} data-testid="leader-reply-restart">
              已核对最新请求，重新开始
            </Button>
          </div>
        ) : null}
      </>
    );
  }
  if (phase.kind === 'unknown') {
    return (
      <>
        {staleNote}
        <ErrorNotice
          error={phase.error}
          title="Leader 答复结果未知"
          outcomeNote="请求可能已被服务端接纳。可显式原键重放一次（不重新执行），或先刷新核对 Leader 投影与任务回执。"
          onReplay={onReplay}
          onRefresh={onRefresh}
        />
      </>
    );
  }
  return null;
}

function AcceptedReplyNotice({onRefresh}: {onRefresh: () => void}) {
  return <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status" data-testid="leader-reply-accepted">
    <p className="text-sm font-medium text-success">答复已受理（受理仅证明该 Leader 请求被接纳）。</p>
    <p className="mt-1 text-sm text-text-secondary">无需重复答复，正在等待 Leader 更新。服务端投影尚未更新不代表需要你再次操作。</p>
    <p className="mt-1 text-sm text-text-secondary">这不代表任务已继续、已发布或后验通过，也不是 Worker 的投递/ACK；后续以 Leader 与任务投影为准。</p>
    <div className="mt-2"><Button size="sm" variant="outline" onClick={onRefresh}>刷新查看投影</Button></div>
  </div>;
}
