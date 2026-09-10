// 待回答问题卡片：走 task.answer（POST /v1/tasks/{taskId}/questions/{questionId}/answers）。
// 答复合同是闭集两分支，运行时按 question.kind 选择：预批准问题绑定 previewDigest，运行问题绑定 questionDigest；
// 选项提交原 value 而非 label；与 Leader pendingRequest（leader.reply）严格分开。
// 过期（status=expired 或超过 deadlineAt）不可答；2xx 只表示受理，不代表已被执行方消费。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {Badge} from '@/components/ui/badge';
import type {AnswerBody, QuestionItem, Revision, Transport} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {isPast, questionStageBadge} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction} from '../shared/logical-action';
import {deliveryStatusLabel, formatDateTime} from '../shared/format';

export interface QuestionCardProps {
  taskId: string;
  /** 所属 Task revision（CAS expectedRevision）。合同必返，永远可用。 */
  expectedRevision: Revision;
  question: QuestionItem;
  /** QuestionsResponse.previewDigest：预批准分支的答复绑定摘要；运行分支不需要。 */
  previewDigest: string | null;
  transport: Transport;
  onChanged: () => void;
}

export function QuestionCard({taskId, expectedRevision, question, previewDigest, transport, onChanged}: QuestionCardProps) {
  const queryClient = useQueryClient();
  const [selection, setSelection] = useState<string | null>(null);
  const [freeText, setFreeText] = useState('');
  const expired = question.status === 'expired' || isPast(question.deadlineAt);
  const open = question.status === 'open' && !expired;

  const answer = question.options.length > 0 ? selection : (freeText.trim() === '' ? null : freeText.trim());
  const runtime = question.kind === 'business';
  // 分支所需绑定摘要：运行问题用自身 questionDigest（合同必返）；预批准问题用投影的 previewDigest（可能未提供）。
  const branchDigest = runtime ? question.questionDigest : previewDigest;
  // 逻辑动作：同问题同分支同摘要同答复值 => 同键可重放；改答复 => 新键。
  const action = useLogicalAction([taskId, 'answer', question.id, expectedRevision, branchDigest ?? '', answer ?? '']);
  const canSubmit = answer !== null && branchDigest !== null && open && action.phase.kind === 'idle';

  const doSubmit = (key: string): Promise<unknown> => {
    if (answer === null) return Promise.reject(new Error('缺少答复内容'));
    const base = {expectedRevision, questionRevision: 1 as const, answer, idempotencyKey: key};
    let body: AnswerBody;
    if (question.kind === 'business') {
      body = {...base, branch: 'runtime', questionDigest: question.questionDigest};
    } else {
      if (previewDigest === null) return Promise.reject(new Error('缺少预批准预览摘要'));
      body = {...base, branch: 'preapproval', previewDigest};
    }
    return transport.answerTask(taskId, question.id, body);
  };

  return (
    <Card aria-label="待回答问题" className="space-y-2" data-testid="question-card" data-question-id={question.id}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="warning">{questionStageBadge(question)}</Badge>
        <code className="text-xs text-text-secondary">{question.id}</code>
        <span className="text-xs text-text-secondary">状态：<code>{question.status}</code></span>
        <span className={`text-xs ${expired ? 'text-danger' : 'text-text-secondary'}`}>
          期限：{formatDateTime(question.deadlineAt)}{expired ? '（已过期，不可作答）' : ''}
        </span>
      </div>
      {question.subject && !runtime ? (
        <p className="text-xs font-medium text-text-secondary">主题：{question.subject}</p>
      ) : null}
      <p className="whitespace-pre-wrap text-sm leading-[22px]">{question.prompt}</p>

      {question.kind === 'business' ? (
        <p className="text-xs leading-[18px] text-text-secondary" data-testid="question-delivery-status">
          Worker 消费状态：{deliveryStatusLabel(question.deliveryStatus ?? 'unknown')}
          （区分已受理/已投递/已消费；「受理」不是 Worker ACK，只有 acknowledged 才是 Worker 已确认消费）
        </p>
      ) : null}

      {expired ? (
        <p className="text-sm text-danger" data-testid="question-expired">该问题已超过答复期限，无法作答；请刷新查看任务最新等待项。</p>
      ) : null}
      {!open && !expired ? (
        <p className="text-sm text-text-secondary" data-testid="question-not-open">
          该问题当前不可作答（状态 {question.status}）。
          {question.status === 'answered' && question.answer ? <>已答内容：<span className="text-text-primary">{question.answer}</span></> : null}
        </p>
      ) : null}

      {action.phase.kind === 'idle' && open ? (
        <div className="space-y-2">
          {question.options.length > 0 ? (
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="可选项">
              {question.options.map(option => (
                <Button
                  key={option.value}
                  variant={selection === option.value ? 'default' : 'outline'}
                  size="sm"
                  aria-pressed={selection === option.value}
                  onClick={() => setSelection(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          ) : (
            <div>
              <label className="block text-xs text-text-secondary" htmlFor={`answer-${question.id}`}>答复内容</label>
              <textarea
                id={`answer-${question.id}`}
                className="mt-1 w-full rounded-md border border-border bg-surface px-2 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                rows={2}
                value={freeText}
                onChange={event => setFreeText(event.target.value)}
              />
            </div>
          )}
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={!canSubmit}
              onClick={() => void action.submit(doSubmit)}
              data-testid="question-submit"
            >
              提交答复
            </Button>
            {branchDigest === null ? (
              <span className="text-xs text-text-secondary">服务端未提供预批准预览摘要（previewDigest），无法安全提交；请刷新任务。</span>
            ) : answer === null ? (
              <span className="text-xs text-text-secondary">{question.options.length > 0 ? '请先选择一个选项' : '请先填写答复内容'}</span>
            ) : null}
          </div>
        </div>
      ) : null}

      {action.phase.kind === 'submitting' ? <p className="text-sm text-text-secondary" role="status">正在提交答复…</p> : null}
      {action.phase.kind === 'accepted' ? (
        <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status" data-testid="question-accepted">
          <p className="text-sm font-medium text-success">答复已受理（受理不等于已被执行方消费）。</p>
          <p className="mt-1 text-sm text-text-secondary">任务进展以轮询到的服务端状态为准；此视图不显示 Worker 投递/ACK。</p>
          <div className="mt-2"><Button size="sm" variant="outline" onClick={onChanged}>刷新任务查看进展</Button></div>
        </div>
      ) : null}
      {action.depsStale && (action.phase.kind === 'unknown' || action.phase.kind === 'rejected') ? (
        <p className="text-xs leading-[18px] text-text-secondary" data-testid="question-deps-stale">
          检测到任务已推进到新版本（轮询 revision 已变化）。本次提交的键与状态保持不变；
          请先刷新核对服务端进展，再决定原键重放或重新编辑。
        </p>
      ) : null}
      {action.phase.kind === 'rejected' ? (
        <ErrorNotice
          error={action.phase.error}
          title="提交答复失败"
          onRefresh={onChanged}
          extra={(
            <Button size="sm" variant="secondary" onClick={action.reset} data-testid="question-reedit">
              重新编辑答复（保留草稿）
            </Button>
          )}
        />
      ) : null}
      {action.phase.kind === 'unknown' ? (
        <ErrorNotice
          error={action.phase.error}
          title="答复结果未知"
          outcomeNote="请求可能已被服务端受理。可显式原键重放一次（不重新执行），或先刷新核对任务与回执。"
          onReplay={() => void action.replay(doSubmit)}
          onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); }}
        />
      ) : null}
    </Card>
  );
}
