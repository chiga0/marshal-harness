// 待回答问题卡片：走 task.answer（questionId+revision+value），选项提交原 value 而非 label；
// 与 Leader pendingRequest（leader.reply）严格分开，不能串用。过期不可答，202 只表示受理。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {Badge} from '@/components/ui/badge';
import type {PendingQuestion, Transport} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {isPast, questionStage} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction} from '../shared/logical-action';
import {formatDateTime} from '../shared/format';

export interface QuestionCardProps {
  taskId: string;
  /** 所属 Task revision（CAS）；null 时禁用提交并说明原因。 */
  revision: string | null;
  question: PendingQuestion;
  transport: Transport;
  onChanged: () => void;
}

export function QuestionCard({taskId, revision, question, transport, onChanged}: QuestionCardProps) {
  const queryClient = useQueryClient();
  const [selection, setSelection] = useState<string | null>(null);
  const [freeText, setFreeText] = useState('');
  const stage = questionStage(question);
  const expired = isPast(question.deadlineAt);

  const value = question.options.length > 0 ? selection : (freeText.trim() === '' ? null : freeText.trim());
  // 逻辑动作：同问题同 revision 同选择值 => 同键可重放；改选择 => 新键。
  const action = useLogicalAction([taskId, 'answer', question.questionId, revision ?? '', value ?? '']);
  const canSubmit = revision !== null && value !== null && !expired && action.phase.kind === 'idle';

  const doSubmit = (key: string) => {
    if (revision === null || value === null) return Promise.reject(new Error('缺少 revision 或答复内容'));
    return transport.answerTask(taskId, {questionId: question.questionId, revision, value, idempotencyKey: key});
  };

  const stageBadge = stage.nodeId !== null
    ? `Worker 运行问题（节点 ${stage.nodeId}）`
    : stage.kind === 'clarification'
      ? '预批准澄清问题'
      : stage.kind !== null
        ? `预批准问题（${stage.kind}）`
        : '待回答问题';

  return (
    <Card aria-label="待回答问题" className="space-y-2" data-testid="question-card" data-question-id={question.questionId}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="warning">{stageBadge}</Badge>
        <code className="text-xs text-text-secondary">{question.questionId}</code>
        {question.deadlineAt ? (
          <span className={`text-xs ${expired ? 'text-danger' : 'text-text-secondary'}`}>
            期限：{formatDateTime(question.deadlineAt)}{expired ? '（已过期，不可作答）' : ''}
          </span>
        ) : null}
      </div>
      <p className="text-sm leading-[22px]">{question.text}</p>

      {expired ? (
        <p className="text-sm text-danger" data-testid="question-expired">该问题已超过答复期限，无法作答；请刷新查看任务最新等待项。</p>
      ) : null}

      {action.phase.kind === 'idle' && !expired ? (
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
              <label className="block text-xs text-text-secondary" htmlFor={`answer-${question.questionId}`}>答复内容</label>
              <textarea
                id={`answer-${question.questionId}`}
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
            {revision === null ? (
              <span className="text-xs text-text-secondary">未获得任务 revision，无法安全提交；请刷新任务。</span>
            ) : value === null ? (
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
