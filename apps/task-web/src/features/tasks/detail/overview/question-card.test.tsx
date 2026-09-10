import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {ApiError, type Transport} from '@/lib/transport/types';
import {QuestionCard} from './question-card';
import {callsOf, makeFakeTransport, makeQuestion, TASK_ID} from '../testing/fixtures';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

describe('待回答问题（P06 / E10 / E11）', () => {
  it('选项提交走 task.answer：questionId+revision+原 value+幂等键，绝不走 leader.reply', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} revision={'7'} question={makeQuestion()} transport={transport} onChanged={() => {}} />);

    await user.click(screen.getByRole('button', {name: '中文'}));
    await user.click(screen.getByTestId('question-submit'));

    await waitFor(() => expect(callsOf(calls, 'answerTask')).toHaveLength(1));
    const body = callsOf(calls, 'answerTask')[0]!.args[1] as {questionId: string; revision: string; value: string; idempotencyKey: string};
    expect(body.questionId).toBe('q-0001');
    expect(body.revision).toBe('7');
    expect(body.value).toBe('zh'); // 提交原 value 而非 label「中文」
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    expect(callsOf(calls, 'leaderReply')).toHaveLength(0);
  });

  it('受理后如实叙事：受理不等于已被执行方消费，无 Worker ACK 展示', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} revision={'7'} question={makeQuestion()} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByRole('button', {name: '英文'}));
    await user.click(screen.getByTestId('question-submit'));
    const accepted = await screen.findByTestId('question-accepted');
    expect(accepted).toHaveTextContent('答复已受理');
    expect(accepted).toHaveTextContent('受理不等于已被执行方消费');
    expect(accepted).toHaveTextContent('不显示 Worker 投递/ACK');
  });

  it('过期问题不可作答', () => {
    const {transport, calls} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} revision={'7'} question={makeQuestion({deadlineAt: '2000-01-01T00:00:00.000Z'})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-expired')).toBeInTheDocument();
    expect(screen.queryByTestId('question-submit')).toBeNull();
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });

  it('缺任务 revision 时禁用提交并说明原因', () => {
    const {transport} = makeFakeTransport();
    wrap(<QuestionCard taskId={TASK_ID} revision={null} question={makeQuestion()} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('question-submit')).toBeDisabled();
    expect(screen.getByText(/未获得任务 revision/)).toBeInTheDocument();
  });

  it('结果未知重放复用同键；拒绝后重选新值是新逻辑动作必须换新键', async () => {
    const answerTask = vi.fn()
      .mockRejectedValueOnce(new TypeError('fetch failed'))
      .mockRejectedValueOnce(new ApiError(400, 'invalid_request', '请求无效', 'req-400-1'))
      .mockResolvedValue({});
    const {transport, calls} = makeFakeTransport({answerTask: answerTask as Transport['answerTask']});
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} revision={'7'} question={makeQuestion()} transport={transport} onChanged={() => {}} />);

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

    const keys = callsOf(calls, 'answerTask').map(call => (call.args[1] as {idempotencyKey: string; value: string}));
    expect(keys).toHaveLength(3);
    expect(keys[1]!.idempotencyKey).toBe(keys[0]!.idempotencyKey); // 同键重放
    expect(keys[2]!.value).toBe('en');
    expect(keys[2]!.idempotencyKey).not.toBe(keys[0]!.idempotencyKey); // 新逻辑动作新键
  });

  it('question_expired（410/400 系）显示过期指引而非成功', async () => {
    const {transport} = makeFakeTransport({
      answerTask: vi.fn(async () => {
        throw new ApiError(410, 'question_expired', '问题已过期', 'req-exp-1');
      }) as Transport['answerTask'],
    });
    const user = userEvent.setup();
    wrap(<QuestionCard taskId={TASK_ID} revision={'7'} question={makeQuestion()} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByRole('button', {name: '中文'}));
    await user.click(screen.getByTestId('question-submit'));
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('问题已过期');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-exp-1');
    expect(screen.queryByTestId('question-accepted')).toBeNull();
  });
});
