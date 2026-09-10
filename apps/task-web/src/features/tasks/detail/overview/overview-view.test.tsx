import {describe, expect, it} from 'vitest';
import {render, screen} from '@testing-library/react';
import {MemoryRouter} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import {OverviewView} from './overview-view';
import {makeDetail, makeFakeTransport, makeLeader, makePlan, makeQuestion, makeWorker} from '../testing/fixtures';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('概览（P04/P05）：等待、进展、验收、计划', () => {
  it('聚齐三类待处理：澄清问题走 task.answer、Leader 请求走 leader.reply、计划走 approve，入口互不串用', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({
      status: 'awaiting-answer',
      allowedActions: ['answer', 'approve', 'cancel'],
      plan: makePlan(),
      pendingQuestions: [makeQuestion()],
      acceptance: {status: 'pending', digest: null, evidenceIds: []},
    });
    const leader = makeLeader({
      pendingRequest: {id: 'req-1', kind: 'business', prompt: '用哪个地区？', options: [], deadlineAt: null, status: 'pending', requestDigest: null, replyDigest: null, authorization: null},
    });
    wrap(<OverviewView detail={detail} workers={[makeWorker()]} leader={leader} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('需要你的处理（3 项）')).toBeInTheDocument();
    // 三类入口各有归属
    expect(screen.getByTestId('question-card')).toBeInTheDocument();
    expect(screen.getByTestId('leader-request-card')).toBeInTheDocument();
    expect(screen.getByTestId('plan-card')).toBeInTheDocument();
    // 独立验收读数（pending 不冒充通过）
    expect(screen.getByTestId('acceptance-panel')).toHaveTextContent('独立验收进行中');
    // 原需求可见
    expect(screen.getByTestId('task-intent')).toHaveTextContent('按窗口汇总两个地区的销售清单');
  });

  it('零问题零等待任务不制造问卷（E06）', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({status: 'running', plan: null, pendingQuestions: []});
    wrap(<OverviewView detail={detail} workers={[]} leader={makeLeader()} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('question-card')).toBeNull();
    expect(screen.queryByTestId('leader-request-card')).toBeNull();
    expect(screen.getByTestId('plan-unavailable')).toBeInTheDocument();
  });

  it('等待确认的计划只显示一次确认入口（approve 在 allowedActions 中）', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({status: 'awaiting-confirmation', allowedActions: ['approve', 'cancel'], plan: makePlan()});
    wrap(<OverviewView detail={detail} workers={[]} leader={makeLeader()} transport={transport} onChanged={() => {}} />);
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(screen.getByTestId('plan-approve-open')).toBeInTheDocument();
    expect(screen.getByTestId('plan-decision-digest')).toHaveTextContent('sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed');
  });

  it('该服务未提供 Leader 待处理请求投影时如实说明（不猜有/无）', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail();
    wrap(<OverviewView detail={detail} workers={[]} leader={makeLeader()} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('waiting-empty')).toHaveTextContent('未提供 Leader 待处理请求投影');
  });
});
