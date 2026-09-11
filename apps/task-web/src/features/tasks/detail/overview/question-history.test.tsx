import {describe, expect, it} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QuestionHistory} from './question-history';
import {makeQuestions, makeRunningQuestion, TASK_ID} from '../shared/test-fakes';

describe('已处理运行问题：仅当前服务只读投影', () => {
  it('冷载入ACK可展开核对原绑定和答复，不提供再次提交', async () => {
    const user = userEvent.setup();
    const question = makeRunningQuestion({status: 'answered', deliveryStatus: 'acknowledged', answer: 'north'});
    const view = () => <QuestionHistory taskId={TASK_ID} questions={makeQuestions({items: [question]})} />;
    const first = render(view());
    expect(screen.getByTestId('question-history')).not.toHaveAttribute('open');
    await user.click(screen.getByText('已处理的运行问题（当前已载入 1 项）'));
    const record = screen.getByTestId('question-history-item');
    expect(record).toHaveTextContent('已消费（Worker 已确认）');
    for (const text of [question.id, question.taskId, question.workerId, question.questionDigest, 'north']) expect(record).toHaveTextContent(text);
    expect(within(record).queryByRole('button')).toBeNull();
    expect(within(record).queryByRole('textbox')).toBeNull();
    first.unmount();
    render(view());
    expect(screen.getByTestId('question-history-item')).toHaveTextContent('已消费（Worker 已确认）');
  });

  it.each(['pending', 'dispatched', 'unknown', null] as const)('已答复但%s不当作已消费历史', deliveryStatus => {
    render(<QuestionHistory taskId={TASK_ID} questions={makeQuestions({items: [makeRunningQuestion({status: 'answered', deliveryStatus})]})} />);
    expect(screen.queryByTestId('question-history-item')).toBeNull();
    expect(screen.queryByText(/已消费/)).toBeNull();
  });

  it.each(['cancelled', 'expired'] as const)('原问题%s诚实展示，null消费状态不猜成功', status => {
    render(<QuestionHistory taskId={TASK_ID} questions={makeQuestions({items: [makeRunningQuestion({status, deliveryStatus: null})]})} />);
    expect(screen.getByTestId('question-history-item')).toHaveTextContent(status === 'cancelled' ? '已取消' : '已过期');
    expect(screen.getByTestId('question-history-item')).toHaveTextContent('消费状态未知');
    expect(screen.queryByText(/已消费/)).toBeNull();
  });

  it('null与外Task投影不声称空历史，不显示他Task数据', () => {
    const view = render(<QuestionHistory taskId={TASK_ID} questions={null} />);
    expect(screen.getByText(/问题投影不可用/)).toBeInTheDocument();
    view.rerender(<QuestionHistory taskId={TASK_ID} questions={makeQuestions({taskId: 'foreign', items: [makeRunningQuestion({status: 'answered', deliveryStatus: 'acknowledged'})]})} />);
    expect(screen.queryByTestId('question-history-item')).toBeNull();
    expect(screen.getByText(/问题投影不可用/)).toBeInTheDocument();
  });

  it('分页仅承诺当前载入范围，混入外Task记录不展示', () => {
    render(<QuestionHistory taskId={TASK_ID} questions={makeQuestions({nextCursor: 'next', items: [makeRunningQuestion({taskId: 'foreign', status: 'answered', deliveryStatus: 'acknowledged'})]})} />);
    expect(screen.getByText(/服务端还有后续分页，本区不是完整历史/)).toBeInTheDocument();
    expect(screen.queryByTestId('question-history-item')).toBeNull();
  });
});
