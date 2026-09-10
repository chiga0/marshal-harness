import {describe, expect, it} from 'vitest';
import {render, screen} from '@testing-library/react';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {OverviewView} from './overview-view';
import {makeFakeTransport, makeLeader, makeLeaderRequest, makePlan, makePreapprovalQuestion, makeQuestions, makeRunningQuestion, makeTask, makeWorker} from '../shared/test-fakes';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('概览（P04/P05）：等待、进展、验收、计划', () => {
  it('聚齐三类待处理：预批准问题走 task.answer、Leader 请求走 leader.reply、计划走 approvePlan，入口互不串用', () => {
    const {transport} = makeFakeTransport();
    const task = makeTask({status: 'awaiting-answer', allowedActions: ['answer', 'approve', 'cancel']});
    const questions = makeQuestions({items: [makePreapprovalQuestion()]});
    const leader = makeLeader({
      pendingRequest: makeLeaderRequest(),
      review: {
        digest: 'sha256:9999999999999999999999999999999999999999999999999999999999999999',
        verdict: 'rework',
        selectionDigest: 'sha256:8888888888888888888888888888888888888888888888888888888888888888',
        policyDigest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
        workerId: 'worker-reviewer',
        evidenceIds: ['ev-1'],
      },
    });
    wrap(<OverviewView task={task} plan={makePlan()} questions={questions} workers={[makeWorker()]} leader={leader} audit={null} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('需要你的处理（3 项）')).toBeInTheDocument();
    // 三类入口各有归属
    expect(screen.getByTestId('question-card')).toBeInTheDocument();
    expect(screen.getByTestId('leader-request-card')).toBeInTheDocument();
    expect(screen.getByTestId('plan-card')).toBeInTheDocument();
    // 独立验收读数来自 Leader 集中评审（rework 不冒充通过）
    expect(screen.getByTestId('acceptance-panel')).toHaveTextContent('要求返工');
    // 原需求可见
    expect(screen.getByTestId('task-intent')).toHaveTextContent('按窗口汇总两个地区的销售清单');
  });

  it('零问题零等待任务不制造问卷（E06）', () => {
    const {transport} = makeFakeTransport();
    const task = makeTask({status: 'running'});
    wrap(<OverviewView task={task} plan={null} questions={makeQuestions()} workers={[]} leader={makeLeader()} audit={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('question-card')).toBeNull();
    expect(screen.queryByTestId('leader-request-card')).toBeNull();
    expect(screen.getByTestId('plan-unavailable')).toBeInTheDocument();
  });

  it('等待批准的计划只显示一次批准入口（approve 在 allowedActions 中）', () => {
    const {transport} = makeFakeTransport();
    const task = makeTask({status: 'awaiting-approval', allowedActions: ['approve', 'cancel']});
    wrap(<OverviewView task={task} plan={makePlan()} questions={makeQuestions()} workers={[]} leader={makeLeader()} audit={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(screen.getByTestId('plan-approve-open')).toBeInTheDocument();
    expect(screen.getByTestId('plan-digest')).toHaveTextContent('sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890');
  });

  it('已答复的 Leader 请求不计入待处理，也不提供答复操作', () => {
    const {transport} = makeFakeTransport();
    const task = makeTask({status: 'running'});
    const leader = makeLeader({pendingRequest: makeLeaderRequest({status: 'replied', replyDigest: 'sha256:1234'})});
    wrap(<OverviewView task={task} plan={null} questions={makeQuestions()} workers={[]} leader={leader} audit={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('leader-request-card')).toBeNull();
  });

  it('Leader 投影不可用时如实说明（不猜有/无待处理请求）', () => {
    const {transport} = makeFakeTransport();
    wrap(<OverviewView task={makeTask()} plan={null} questions={makeQuestions()} workers={[]} leader={null} audit={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toHaveTextContent('Leader 投影不可用');
    expect(screen.getByTestId('leader-projection')).toHaveTextContent('暂无 Leader 投影');
    expect(screen.getByTestId('acceptance-panel')).toHaveTextContent('Leader 投影不可用');
  });

  it('问题投影未加载时如实说明', () => {
    const {transport} = makeFakeTransport();
    wrap(<OverviewView task={makeTask()} plan={null} questions={null} workers={[]} leader={makeLeader()} audit={null} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toHaveTextContent('问题投影未加载');
  });

  it('UI-08：运行问题已答复但 Worker ACK 未落定时保留待核对，已 ACK 的不再占位', () => {
    const {transport} = makeFakeTransport();
    const task = makeTask({status: 'awaiting-answer', allowedActions: ['answer']});
    const unacked = makeRunningQuestion({id: 'q-run-1', status: 'answered', answer: '北', deliveryStatus: 'pending'});
    const acked = makeRunningQuestion({id: 'q-run-2', status: 'answered', answer: '南', deliveryStatus: 'acknowledged'});
    const questions = makeQuestions({items: [unacked, acked]});
    wrap(<OverviewView task={task} plan={null} questions={questions} workers={[]} leader={makeLeader()} audit={null} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    const cards = screen.getAllByTestId('question-card');
    expect(cards).toHaveLength(1);
    expect(cards[0]).toHaveAttribute('data-question-id', 'q-run-1');
    expect(cards[0]).toHaveTextContent('Worker 消费状态：已受理（待投递 Worker）');
  });
});
