import {describe, expect, it, vi} from 'vitest';
import {act, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {MemoryRouter, Route, Routes} from 'react-router-dom';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError, type Transport} from '@/lib/transport/types';
import {LogicalActionScope} from './shared/logical-action';
import {TaskDetailLayout} from './task-detail-layout';
import {makeFakeTransport, makeLeader, makeLeaderRequest, makeQuestions, makeRunningQuestion, makeTask, TASK_ID} from './shared/test-fakes';

let transport: Transport;
vi.mock('@/features/connection/connection', () => ({useConnection: () => ({transport})}));
vi.mock('@/lib/queries/polling', () => ({usePollMode: () => null}));

function renderDetail() {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
  return render(<QueryClientProvider client={client}><LogicalActionScope session={transport}><MemoryRouter initialEntries={[`/tasks/${TASK_ID}`]}><Routes><Route path="/tasks/:taskId/*" element={<TaskDetailLayout />} /></Routes></MemoryRouter></LogicalActionScope></QueryClientProvider>);
}
async function answer() {
  const user = userEvent.setup();
  await user.click(await screen.findByTestId('leader-answer-option-north'));
  await user.click(screen.getByRole('button', {name: '确认答复'}));
  return user;
}

describe('Leader 原请求受理后的统一等待提示', () => {
  it('收到受理回执后不再催原答复；旧pending投影与新Task revision不恢复重复操作', async () => {
    let revision = 7;
    const request = makeLeaderRequest();
    const reply = vi.fn(async () => ({}));
    transport = makeFakeTransport({getTask: async () => makeTask({status: 'awaiting-answer', revision, allowedActions: ['answer']}),
      getQuestions: async () => makeQuestions(), getLeader: async () => makeLeader({pendingRequest: request, taskRevision: revision}), leaderReply: reply}).transport;
    renderDetail();
    const user = await answer();
    expect(await screen.findByTestId('leader-reply-accepted')).toHaveTextContent('不代表任务已继续、已发布或后验通过');
    expect(screen.getByText('需要你的处理（0 项）')).toBeInTheDocument();
    expect(screen.getByText('答复已受理，等待 Leader 更新')).toBeInTheDocument();
    expect(screen.getByTestId('leader-projection')).toHaveTextContent('无需重复答复');
    expect(screen.getAllByTestId('machine-state')[0]).toHaveTextContent('awaiting-answer');
    revision = 8;
    await user.click(screen.getByTestId('detail-refresh'));
    await waitFor(() => expect(screen.getByText('需要你的处理（0 项）')).toBeInTheDocument());
    expect(screen.queryByTestId('leader-answer-option-north')).not.toBeInTheDocument();
    expect(screen.getByTestId('leader-reply-accepted')).toHaveTextContent('无需重复答复');
    await user.click(screen.getByRole('link', {name: '团队'}));
    await user.click(screen.getByRole('link', {name: '概览'}));
    expect(await screen.findByTestId('leader-reply-accepted')).toHaveTextContent('无需重复答复');
    expect(screen.queryByTestId('leader-answer-option-north')).not.toBeInTheDocument();
    expect(reply).toHaveBeenCalledTimes(1);
  });

  it.each(['id', 'digest'] as const)('新请求%s不同不能沿用旧受理证据隐藏真正待办', async change => {
    let request = makeLeaderRequest();
    const reply = vi.fn(async () => ({}));
    transport = makeFakeTransport({getTask: async () => makeTask({status: 'awaiting-answer', allowedActions: ['answer']}),
      getQuestions: async () => makeQuestions(), getLeader: async () => makeLeader({pendingRequest: request}), leaderReply: reply}).transport;
    renderDetail();
    const user = await answer();
    await screen.findByTestId('leader-reply-accepted');
    request = {...request, ...(change === 'id' ? {id: 'new-request'} : {requestDigest: `sha256:${'c'.repeat(64)}`}), prompt: '新的业务问题'};
    await user.click(screen.getByTestId('detail-refresh'));
    await screen.findByText('新的业务问题');
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(await screen.findByTestId('leader-answer-option-north')).toBeEnabled();
    expect(screen.queryByTestId('leader-reply-accepted')).not.toBeInTheDocument();
    expect(reply).toHaveBeenCalledTimes(1);
  });

  it.each([new ApiError(409, 'revision_conflict', '明确拒绝', null), new TypeError('未知回执')])('明确拒绝或未知结果不冒充已受理并减少待办：%s', async error => {
    transport = makeFakeTransport({getTask: async () => makeTask({status: 'awaiting-answer', allowedActions: ['answer']}),
      getQuestions: async () => makeQuestions(), getLeader: async () => makeLeader({pendingRequest: makeLeaderRequest()}), leaderReply: async () => { throw error; }}).transport;
    renderDetail();
    await answer();
    await screen.findByText(error instanceof ApiError ? /Leader 答复失败/ : /Leader 答复结果未知/);
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(screen.queryByTestId('leader-reply-accepted')).not.toBeInTheDocument();
    expect(screen.queryByText('答复已受理，等待 Leader 更新')).not.toBeInTheDocument();
  });

  it('旧请求的迟到受理不能抹掉期间出现的新请求', async () => {
    let request = makeLeaderRequest();
    let finish!: () => void;
    transport = makeFakeTransport({getTask: async () => makeTask({status: 'awaiting-answer', allowedActions: ['answer']}),
      getQuestions: async () => makeQuestions(), getLeader: async () => makeLeader({pendingRequest: request}),
      leaderReply: () => new Promise<void>(resolve => { finish = resolve; })}).transport;
    renderDetail();
    const user = await answer();
    request = {...request, id: 'next-request', requestDigest: `sha256:${'d'.repeat(64)}`, prompt: '另一个真正待答的问题'};
    await user.click(screen.getByTestId('detail-refresh'));
    await screen.findByText('另一个真正待答的问题');
    await act(async () => { finish(); });
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(screen.getByTestId('leader-answer-option-north')).toBeEnabled();
    expect(screen.queryByTestId('leader-reply-accepted')).not.toBeInTheDocument();
  });

  it('原Leader答复受理不能隐藏另一条尚未回答的Worker问题', async () => {
    transport = makeFakeTransport({getTask: async () => makeTask({status: 'awaiting-answer', allowedActions: ['answer']}),
      getQuestions: async () => makeQuestions({items: [makeRunningQuestion()]}), getLeader: async () => makeLeader({pendingRequest: makeLeaderRequest()}), leaderReply: async () => ({})}).transport;
    renderDetail();
    await answer();
    await screen.findByTestId('leader-reply-accepted');
    expect(screen.getByText('需要你的处理（1 项）')).toBeInTheDocument();
    expect(screen.getByTestId('question-card')).toBeInTheDocument();
    expect(screen.queryByText('答复已受理，等待 Leader 更新')).not.toBeInTheDocument();
  });
});
