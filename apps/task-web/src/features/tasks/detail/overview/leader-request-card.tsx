// Leader pendingRequest 卡片：kind=business 用 leader.reply 的 outcome 分支答复，
// kind=publication 完整展示 Core 授权正文并允许/拒绝；两者都绑定 requestId+Task revision（幂等键内存持有）。
// 与 task.answer 严格分开：202 仅证明该 Leader 请求被接纳，不代表已继续/已发布，也不显示 Worker 投递/ACK。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {Badge} from '@/components/ui/badge';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {Transport} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import type {LeaderPendingRequestView} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction, type ActionPhase} from '../shared/logical-action';
import {formatBytes, formatDateTime} from '../shared/format';

type LeaderOutcome = 'accepted' | 'rejected' | 'unknown';

const OUTCOME_COPY: Record<LeaderOutcome, {button: string; confirmTitle: string; tone: 'default' | 'destructive' | 'secondary'}> = {
  accepted: {button: '采纳该请求', confirmTitle: '确认采纳该 Leader 请求？', tone: 'default'},
  rejected: {button: '拒绝该请求', confirmTitle: '确认拒绝该 Leader 请求？', tone: 'destructive'},
  unknown: {button: '标记为无法确定', confirmTitle: '把该 Leader 请求答复为无法确定？', tone: 'secondary'},
};

export interface LeaderRequestCardProps {
  taskId: string;
  revision: string | null;
  request: LeaderPendingRequestView;
  transport: Transport;
  onChanged: () => void;
}

export function LeaderRequestCard({taskId, revision, request, transport, onChanged}: LeaderRequestCardProps) {
  const actionable = request.status === 'pending';
  return (
    <Card aria-label="Leader 待处理请求" className="space-y-2" data-testid="leader-request-card" data-request-kind={request.kind}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={request.kind === 'publication' ? 'default' : 'warning'}>
          {request.kind === 'publication' ? 'Leader 发布授权请求' : request.kind === 'business' ? 'Leader 业务请求' : `Leader 请求（${request.kind}）`}
        </Badge>
        <code className="text-xs text-text-secondary">{request.id}</code>
        <span className="text-xs text-text-secondary">状态：<code>{request.status}</code></span>
        {request.deadlineAt ? <span className="text-xs text-text-secondary">期限：{formatDateTime(request.deadlineAt)}</span> : null}
      </div>

      <p className="whitespace-pre-wrap text-sm leading-[22px]">{request.prompt}</p>

      {request.kind === 'publication' ? (
        <PublicationAuthorization authorization={request.authorization} />
      ) : null}

      {request.options.length > 0 ? (
        <div>
          <h4 className="text-xs font-medium text-text-secondary">请求自带选项</h4>
          <ul className="mt-1 space-y-1">
            {request.options.map(option => (
              <li key={option.value} className="text-sm leading-[22px]">
                <span>{option.label}</span>
                <code className="ml-2 text-xs text-text-secondary">{option.value}</code>
              </li>
            ))}
          </ul>
          <p className="mt-1 text-xs text-text-secondary">
            当前 transport 合同的 leader.reply 只携带 outcome（accepted/rejected/unknown），不携带选项值；选项内容如上原文展示。
          </p>
        </div>
      ) : null}

      {request.requestDigest ? (
        <p className="break-all text-xs text-text-secondary">
          请求摘要：<code>{request.requestDigest}</code>
        </p>
      ) : null}

      {!actionable ? (
        <p className="text-sm text-text-secondary" data-testid="leader-request-closed">
          该请求已答复或已关闭{request.replyDigest ? <>，答复摘要：<code className="break-all text-xs">{request.replyDigest}</code></> : null}。
        </p>
      ) : (
        <LeaderReplyActions taskId={taskId} revision={revision} request={request} transport={transport} onChanged={onChanged} />
      )}
    </Card>
  );
}

function PublicationAuthorization({authorization}: {authorization: Record<string, unknown> | null}) {
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
    ['大小 bytes', typeof authorization.bytes === 'number' ? formatBytes(authorization.bytes) : authorization.bytes],
    ['过期时间 expiresAt', typeof authorization.expiresAt === 'string' ? formatDateTime(authorization.expiresAt) : authorization.expiresAt],
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
      <details className="mt-1 text-xs text-text-secondary">
        <summary className="cursor-pointer select-none">原始 JSON</summary>
        <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-surface p-2">{JSON.stringify(authorization, null, 2)}</pre>
      </details>
    </div>
  );
}

interface LeaderReplyActionsProps {
  taskId: string;
  revision: string | null;
  request: LeaderPendingRequestView;
  transport: Transport;
  onChanged: () => void;
}

function LeaderReplyActions({taskId, revision, request, transport, onChanged}: LeaderReplyActionsProps) {
  const queryClient = useQueryClient();
  const [chosen, setChosen] = useState<LeaderOutcome | null>(null);
  const action = useLogicalAction([taskId, 'leader.reply', request.id, revision ?? '', chosen ?? '']);
  const refresh = () => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); };

  const doSubmit = (key: string) => {
    if (revision === null || chosen === null) return Promise.reject(new Error('缺少 revision 或答复'));
    return transport.leaderReply(taskId, {requestId: request.id, revision, outcome: chosen, idempotencyKey: key});
  };

  return (
    <div className="space-y-2">
      {action.phase.kind === 'idle' ? (
        <>
          <div className="flex flex-wrap gap-2">
            {(Object.keys(OUTCOME_COPY) as LeaderOutcome[]).map(outcome => (
              <Button
                key={outcome}
                size="sm"
                variant={OUTCOME_COPY[outcome].tone}
                disabled={revision === null}
                onClick={() => setChosen(outcome)}
                data-testid={`leader-reply-${outcome}`}
              >
                {request.kind === 'publication' && outcome === 'accepted' ? '允许发布' : request.kind === 'publication' && outcome === 'rejected' ? '拒绝发布' : OUTCOME_COPY[outcome].button}
              </Button>
            ))}
          </div>
          {revision === null ? (
            <p className="text-xs text-text-secondary">未获得任务 revision，无法安全答复；请刷新任务。</p>
          ) : null}
          <p className="text-xs text-text-secondary">
            答复走 leader.reply（绑定 requestId 与当前 Task revision），不走 task.answer；不回改 revision/批准状态。
          </p>
        </>
      ) : null}

      <LeaderReplyOutcome phase={action.phase} onReplay={() => void action.replay(doSubmit)} onRefresh={refresh} />

      <ConfirmDialog
        open={chosen !== null && action.phase.kind === 'idle'}
        title={chosen ? OUTCOME_COPY[chosen].confirmTitle : ''}
        description={
          chosen
            ? `将以 outcome=${chosen} 答复请求 ${request.id}（revision ${revision ?? '未知'}）。提交后由服务端绑定原请求摘要；受理（202）仅代表该 Leader 请求被接纳，不代表已执行或已发布。`
            : ''
        }
        destructive={chosen === 'rejected'}
        confirmText="确认答复"
        onConfirm={() => {
          if (chosen !== null) void action.submit(doSubmit);
          setChosen(null);
        }}
        onCancel={() => setChosen(null)}
      />
    </div>
  );
}

function LeaderReplyOutcome({phase, onReplay, onRefresh}: {phase: ActionPhase; onReplay: () => void; onRefresh: () => void}) {
  if (phase.kind === 'submitting') return <p className="text-sm text-text-secondary" role="status">正在提交 Leader 答复…</p>;
  if (phase.kind === 'accepted') {
    return (
      <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status" data-testid="leader-reply-accepted">
        <p className="text-sm font-medium text-success">答复已受理（202 仅证明该 Leader 请求被接纳）。</p>
        <p className="mt-1 text-sm text-text-secondary">
          这不代表任务已继续、已发布或后验通过，也不是 Worker 的投递/ACK；后续以 Leader 与任务投影为准。
        </p>
        <div className="mt-2"><Button size="sm" variant="outline" onClick={onRefresh}>刷新查看投影</Button></div>
      </div>
    );
  }
  if (phase.kind === 'rejected') {
    return <ErrorNotice error={phase.error} title="Leader 答复失败" onRefresh={onRefresh} />;
  }
  if (phase.kind === 'unknown') {
    return (
      <ErrorNotice
        error={phase.error}
        title="Leader 答复结果未知"
        outcomeNote="请求可能已被服务端接纳。可显式原键重放一次（不重新执行），或先刷新核对 Leader 投影与任务回执。"
        onReplay={onReplay}
        onRefresh={onRefresh}
      />
    );
  }
  return null;
}
