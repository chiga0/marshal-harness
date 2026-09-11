import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {ApiError, type AnswerBody, type Transport} from '@/lib/transport/types';
import {QuestionCard} from './question-card';
import {
  PREVIEW_DIGEST,
  QUESTION_DIGEST,
  TASK_ID,
  callsOf,
  makeFakeTransport,
  makePreapprovalQuestion,
  makeRunningQuestion,
} from '../shared/test-fakes';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

describe('待回答问题（P06 / E10 / E11）', () => {
  it('选项使用按钮组语义，键盘选择只切换选中项，不自动提交', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByRole('group', {name: '可选项（选择一个）'})).toBeInTheDocument();
    expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
    const chinese = screen.getByRole('button', {name: '中文', pressed: false});
    const english = screen.getByRole('button', {name: '英文', pressed: false});
    await user.tab();
    expect(chinese).toHaveFocus();
    await user.keyboard(' ');
    expect(chinese).toHaveAttribute('aria-pressed', 'true');
    await user.tab();
    expect(english).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(english).toHaveAttribute('aria-pressed', 'true');
    expect(chinese).toHaveAttribute('aria-pressed', 'false');
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
    await user.tab();
    expect(screen.getByRole('button', {name: '提交答复'})).toHaveFocus();
  });

  it('UI-08：运行问题展示 Worker 消费状态（受理/投递/消费三分，受理≠ACK）', () => {
    const {transport} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makeRunningQuestion({deliveryStatus: 'dispatched'})} previewDigest={null} transport={transport} onChanged={() => {}} />);
    const line = screen.getByTestId('question-delivery-status');
    expect(line).toHaveTextContent('Worker 消费状态：已投递（Worker 未确认消费）');
    expect(line).toHaveTextContent('只有 acknowledged 才是 Worker 已确认消费');
  });

  it('预批准问题不展示 Worker 消费状态行（Leader 问答不伪造 Worker ACK）', () => {
    const {transport} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.queryByTestId('question-delivery-status')).toBeNull();
  });

  it('预批准问题选项提交走 task.answer：questionId 入路由、body=preapproval 分支（previewDigest+原 value+幂等键），绝不走 leader.reply', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);

    await user.click(screen.getByRole('button', {name: '中文'}));
    await user.click(screen.getByTestId('question-submit'));

    await waitFor(() => expect(callsOf(calls, 'answerTask')).toHaveLength(1));
    const call = callsOf(calls, 'answerTask')[0]!;
    expect(call.args[0]).toBe(TASK_ID);
    expect(call.args[1]).toBe('q-0001'); // questionId 走路由而非 body
    const body = call.args[2] as AnswerBody & {branch: string};
    expect(body.branch).toBe('preapproval');
    expect(body.expectedRevision).toBe(7);
    expect(body.questionRevision).toBe(1);
    expect('previewDigest' in body && body.previewDigest).toBe(PREVIEW_DIGEST);
    expect(body.answer).toBe('zh'); // 提交原 value 而非 label「中文」
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    expect('questionDigest' in body).toBe(false); // 闭集分支互斥
    expect(callsOf(calls, 'leaderReply')).toHaveLength(0);
  });

  it('运行中 Worker 业务问题：body=runtime 分支（questionDigest），不需要 previewDigest', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makeRunningQuestion()} previewDigest={null} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('Worker 运行问题（节点 east）')).toBeInTheDocument();
    await user.type(screen.getByLabelText('答复内容'), '2026-09-01');
    await user.click(screen.getByTestId('question-submit'));

    await waitFor(() => expect(callsOf(calls, 'answerTask')).toHaveLength(1));
    const body = callsOf(calls, 'answerTask')[0]!.args[2] as AnswerBody & {branch: string};
    expect(body.branch).toBe('runtime');
    expect('questionDigest' in body && body.questionDigest).toBe(QUESTION_DIGEST);
    expect(body.answer).toBe('2026-09-01');
    expect('previewDigest' in body).toBe(false);
  });

  it('预批准问题缺 previewDigest 时禁用提交并说明原因', () => {
    const {transport} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-submit')).toBeDisabled();
    expect(screen.getByText(/previewDigest/)).toBeInTheDocument();
  });

  it('受理后如实叙事：受理不等于已被执行方消费，无 Worker ACK 展示', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByRole('button', {name: '英文'}));
    await user.click(screen.getByTestId('question-submit'));
    const accepted = await screen.findByTestId('question-accepted');
    expect(accepted).toHaveTextContent('答复已受理');
    expect(accepted).toHaveTextContent('受理不等于已被执行方消费');
    expect(accepted).toHaveTextContent('不显示 Worker 投递/ACK');
  });

  it('过期问题不可作答（期限已过）', () => {
    const {transport, calls} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion({deadlineAt: '2000-01-01T00:00:00.000Z'})} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-expired')).toBeInTheDocument();
    expect(screen.queryByTestId('question-submit')).toBeNull();
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });

  it('过期问题不可作答（status=expired）', () => {
    const {transport, calls} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion({status: 'expired'})} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-expired')).toBeInTheDocument();
    expect(screen.queryByTestId('question-submit')).toBeNull();
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });

  it('已答问题展示答复内容且不提供表单', () => {
    const {transport} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion({status: 'answered', answer: 'zh'})} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-not-open')).toHaveTextContent('已答内容：zh');
    expect(screen.queryByTestId('question-submit')).toBeNull();
  });

  it('阶段徽章：预批准三类固定措辞', () => {
    const {transport} = makeFakeTransport();
    const {rerender} = wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion({kind: 'permission'})} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByText('预批准许可问题')).toBeInTheDocument();
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion({kind: 'acceptance'})} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />
      </QueryClientProvider>,
    );
    expect(screen.getByText('验收口径问题')).toBeInTheDocument();
  });

  it('结果未知重放复用同键；拒绝后重选新值是新逻辑动作必须换新键', async () => {
    const answerTask = vi.fn()
      .mockRejectedValueOnce(new TypeError('fetch failed'))
      .mockRejectedValueOnce(new ApiError(400, 'invalid_request', '请求无效', 'req-400-1'))
      .mockResolvedValue({});
    const {transport, calls} = makeFakeTransport({answerTask: answerTask as Transport['answerTask']});
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);

    await user.click(screen.getByRole('button', {name: '中文'}));
    await user.click(screen.getByTestId('question-submit'));
    expect(await screen.findByText(/答复结果未知/)).toBeInTheDocument();

    // 原键重放：同键；这次服务端明确拒绝 -> 保留草稿、可重新编辑
    await user.click(screen.getByRole('button', {name: /原键重放/}));
    await screen.findByTestId('error-notice');
    expect(callsOf(calls, 'answerTask')).toHaveLength(2);

    // 换取另一个选择后提交：不同输入 => 新逻辑动作 => 必须换新键
    await user.click(screen.getByTestId('question-reedit'));
    await user.click(screen.getByRole('button', {name: '英文'}));
    await user.click(screen.getByTestId('question-submit'));
    await screen.findByTestId('question-accepted');

    const keys = callsOf(calls, 'answerTask').map(call => (call.args[2] as AnswerBody));
    expect(keys).toHaveLength(3);
    expect(keys[1]!.idempotencyKey).toBe(keys[0]!.idempotencyKey); // 同键重放
    expect(keys[2]!.answer).toBe('en');
    expect(keys[2]!.idempotencyKey).not.toBe(keys[0]!.idempotencyKey); // 新逻辑动作新键
  });

  it('question_expired 显示过期指引而非成功', async () => {
    const {transport} = makeFakeTransport({
      answerTask: vi.fn(async () => {
        throw new ApiError(410, 'question_expired', '问题已过期', 'req-exp-1');
      }) as Transport['answerTask'],
    });
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} expectedRevision={7} question={makePreapprovalQuestion()} previewDigest={PREVIEW_DIGEST} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByRole('button', {name: '中文'}));
    await user.click(screen.getByTestId('question-submit'));
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('问题已过期');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-exp-1');
    expect(screen.queryByTestId('question-accepted')).toBeNull();
  });
});
