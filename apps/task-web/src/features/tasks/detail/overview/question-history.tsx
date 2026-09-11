import type {QuestionsResponse, RunningQuestion} from '@/lib/transport/types';
import {questionNeedsAttention} from '../shared/derive';
import {deliveryStatusLabel, formatDateTime} from '../shared/format';

/** 只读当前服务投影，不保存另一份历史，不以本地受理回执推导消费。 */
export function QuestionHistory({taskId, questions}: {taskId: string; questions: QuestionsResponse | null}) {
  const available = questions !== null && questions.taskId === taskId;
  const history = available ? questions.items.filter((question): question is RunningQuestion =>
    question.taskId === taskId && question.kind === 'business' && !questionNeedsAttention(question)) : [];
  return (
    <details className="rounded-md border border-border bg-surface p-3" data-testid="question-history">
      <summary className="cursor-pointer text-sm font-medium">已处理的运行问题（当前已载入 {history.length} 项）</summary>
      {!available ? (
        <p className="mt-2 text-sm text-text-secondary">问题投影不可用，无法核对已处理记录；不表示没有历史问题。</p>
      ) : (
        <>
          <p className="mt-2 text-xs text-text-secondary">
            仅展示当前服务已返回的运行问题，不计入需要处理，也不能在此重复答复。
            {questions.nextCursor !== null ? '服务端还有后续分页，本区不是完整历史。' : '这些记录不代表任务已完成或验收通过。'}
          </p>
          {history.length === 0 ? <p className="mt-2 text-sm text-text-secondary">当前已载入的问题中没有已处理的运行问题。</p> : null}
          <ul className="mt-2 space-y-3">
            {history.map(question => (
              <li key={question.id} className="min-w-0 rounded border border-border p-3 text-sm" data-testid="question-history-item" data-question-id={question.id}>
                <p className="whitespace-pre-wrap break-words font-medium">{question.prompt}</p>
                <p className="mt-1">问题状态：{question.status === 'cancelled' ? '已取消' : question.status === 'expired' ? '已过期' : '已答复'}（<code>{question.status}</code>）</p>
                <p>答复消费：{deliveryStatusLabel(question.deliveryStatus ?? 'unknown')}（<code>{question.deliveryStatus ?? 'null'}</code>）</p>
                <p className="mt-1 whitespace-pre-wrap break-words">原答复：{question.answer ?? '服务端未提供答复内容'}</p>
                <div className="mt-2 space-y-1 break-all text-xs text-text-secondary">
                  <div>问题 ID：<code>{question.id}</code></div>
                  <div>所属 Task：<code>{question.taskId}</code></div>
                  <div>Worker：<code>{question.workerId}</code> · 节点：<code>{question.nodeId}</code></div>
                  <div>原问题摘要：<code>{question.questionDigest}</code></div>
                  <div>问题期限：{formatDateTime(question.deadlineAt)}</div>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
    </details>
  );
}
