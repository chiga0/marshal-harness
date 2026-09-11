import {describe, expect, it, vi} from 'vitest';
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {useState, type ReactNode} from 'react';
import {ApiError} from '@/lib/transport/types';
import type {LeaderBusinessReplyBody, LeaderPublicationReplyBody, Transport} from '@/lib/transport/types';
import {LeaderRequestCard} from './leader-request-card';
import {REQUEST_DIGEST, TASK_ID, callsOf, makeAuthorization, makeFakeTransport, makeLeaderRequest} from '../shared/test-fakes';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

describe('Leader 答复（P06 / E31 / P10）', () => {
  it.each(['business', 'publication'] as const)('%s：409 刷新后显式核对重开，保留草稿并以新键/CAS 再确认', async kind => {
    const leaderReply = vi.fn().mockRejectedValueOnce(new ApiError(409, 'revision_conflict', '版本冲突', 'req-conflict')).mockResolvedValueOnce({});
    const {transport, calls} = makeFakeTransport({leaderReply: leaderReply as Transport['leaderReply']});
    const user = userEvent.setup();
    const nextDigest = `sha256:${'b'.repeat(64)}`;
    function RefreshedCard() {
      const [revision, setRevision] = useState(7);
      return <LeaderRequestCard taskId={TASK_ID} expectedRevision={revision} transport={transport} onChanged={() => setRevision(8)} request={makeLeaderRequest({
        kind, options: [], requestDigest: revision === 7 ? REQUEST_DIGEST : nextDigest,
        prompt: revision === 7 ? '原请求' : '最新请求正文',
        authorization: kind === 'publication' ? makeAuthorization({targetId: revision === 7 ? 'old-target' : 'new-target'}) : null,
      })} />;
    }
    wrap(<RefreshedCard />);
    if (kind === 'business') await user.type(screen.getByLabelText('答复内容'), '保留的答复草稿');
    await user.click(screen.getByTestId(kind === 'business' ? 'leader-answer-open' : 'leader-reply-allow'));
    await user.click(screen.getByRole('button', {name: kind === 'business' ? '确认答复' : '确认允许'}));
    expect(await screen.findByTestId('leader-reply-restart')).toBeDisabled();
    await user.click(screen.getByRole('button', {name: /刷新/}));
    expect(screen.getByText('最新请求正文')).toBeInTheDocument();
    expect(screen.getByTestId('leader-reply-restart')).toBeEnabled();
    expect(callsOf(calls, 'leaderReply')).toHaveLength(1);
    expect(screen.queryByTestId('leader-answer-open')).toBeNull();
    await user.click(screen.getByTestId('leader-reply-restart'));
    expect(callsOf(calls, 'leaderReply')).toHaveLength(1);
    if (kind === 'business') expect(screen.getByLabelText('答复内容')).toHaveValue('保留的答复草稿');
    else expect(screen.getByTestId('publication-authorization')).toHaveTextContent('new-target');
    await user.click(screen.getByTestId(kind === 'business' ? 'leader-answer-open' : 'leader-reply-allow'));
    expect(screen.getByRole('dialog')).toHaveTextContent(kind === 'business' ? 'revision 8' : 'new-target');
    await user.click(screen.getByRole('button', {name: kind === 'business' ? '确认答复' : '确认允许'}));
    await screen.findByTestId('leader-reply-accepted');
    const first = callsOf(calls, 'leaderReply')[0]!.args[2] as LeaderBusinessReplyBody | LeaderPublicationReplyBody;
    const second = callsOf(calls, 'leaderReply')[1]!.args[2] as typeof first;
    expect(second).toEqual({...first, expectedRevision: 8, requestDigest: nextDigest, idempotencyKey: expect.any(String)});
    expect(second.idempotencyKey).not.toBe(first.idempotencyKey);
    expect(callsOf(calls, 'leaderReply')).toHaveLength(2);
  });

  it('未知结果刷新到新版仍不能重开；连击重放仅提交一次且冻结原 body', async () => {
    let finish!: (value: unknown) => void;
    const leaderReply = vi.fn().mockRejectedValueOnce(new TypeError('fetch failed')).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const {transport, calls} = makeFakeTransport({leaderReply: leaderReply as Transport['leaderReply']});
    const user = userEvent.setup();
    function RefreshedCard() {
      const [revision, setRevision] = useState(7);
      return <LeaderRequestCard taskId={TASK_ID} expectedRevision={revision} request={makeLeaderRequest()} transport={transport} onChanged={() => setRevision(8)} />;
    }
    wrap(<RefreshedCard />);
    await user.click(screen.getByTestId('leader-answer-option-north'));
    await user.click(screen.getByRole('button', {name: '确认答复'}));
    await screen.findByText(/Leader 答复结果未知/);
    await user.click(screen.getByRole('button', {name: /刷新/}));
    expect(screen.getByTestId('leader-reply-deps-stale')).toBeInTheDocument();
    expect(screen.queryByTestId('leader-reply-restart')).toBeNull();
    expect(callsOf(calls, 'leaderReply')).toHaveLength(1);
    const replay = screen.getByRole('button', {name: /原键重放/});
    act(() => { fireEvent.click(replay); fireEvent.click(replay); });
    expect(callsOf(calls, 'leaderReply')).toHaveLength(2);
    expect(callsOf(calls, 'leaderReply')[1]!.args).toEqual(callsOf(calls, 'leaderReply')[0]!.args);
    await act(async () => finish({}));
  });

  it('业务请求走 leader.reply：requestId 入路由、body={expectedRevision,requestDigest,answer=选项 value,幂等键}，绝不走 task.answer', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest()} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('报告采用哪个地区的数据？')).toBeInTheDocument();

    await user.click(screen.getByTestId('leader-answer-option-north')); // 选项「北区」
    await user.click(await screen.findByRole('button', {name: '确认答复'}));

    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    const call = callsOf(calls, 'leaderReply')[0]!;
    expect(call.args[0]).toBe(TASK_ID);
    expect(call.args[1]).toBe('req-biz-1'); // requestId 走路由而非 body
    const body = call.args[2] as LeaderBusinessReplyBody;
    expect(body.expectedRevision).toBe(7);
    expect(body.requestDigest).toBe(REQUEST_DIGEST);
    expect(body.answer).toBe('north'); // 提交选项 value 而非 label
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    // 合同闭集：无 outcome/requestId/revision 字段
    expect(body).not.toHaveProperty('outcome');
    expect(body).not.toHaveProperty('requestId');
    expect(body).not.toHaveProperty('revision');
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });

  it('业务请求无选项时自由文本答复', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({options: []})} transport={transport} onChanged={() => {}} />);

    await user.type(screen.getByLabelText('答复内容'), '采用北区的数据');
    await user.click(screen.getByTestId('leader-answer-open'));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));

    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    const body = callsOf(calls, 'leaderReply')[0]!.args[2] as LeaderBusinessReplyBody;
    expect(body.answer).toBe('采用北区的数据');
  });

  it('受理叙事如实：不代表已继续/已发布，不显示 Worker 投递/ACK', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest()} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('leader-answer-option-north'));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));
    const accepted = await screen.findByTestId('leader-reply-accepted');
    expect(accepted).toHaveTextContent('答复已受理');
    expect(accepted).toHaveTextContent('不代表任务已继续、已发布或后验通过');
    expect(accepted).toHaveTextContent('也不是 Worker 的投递/ACK');
  });

  it('发布授权请求：完整授权正文展示，允许=allow', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({id: 'req-pub-1', kind: 'publication', options: [], authorization: makeAuthorization(), prompt: '申请把验收摘要发布到本机报告目录。'})} transport={transport} onChanged={() => {}} />);

    const authorization = screen.getByTestId('publication-authorization');
    expect(authorization).toHaveTextContent('local-reports');
    expect(authorization).toHaveTextContent('create-if-absent');
    expect(authorization).toHaveTextContent('task-1-aaaa.json');

    await user.click(screen.getByTestId('leader-reply-allow'));
    await user.click(await screen.findByRole('button', {name: '确认允许'}));
    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    const body = callsOf(calls, 'leaderReply')[0]!.args[2] as LeaderPublicationReplyBody;
    expect(body.decision).toBe('allow');
    expect(body.expectedRevision).toBe(7);
    expect(body.requestDigest).toBe(REQUEST_DIGEST);
    expect(body).not.toHaveProperty('outcome');
  });

  it('发布授权请求：拒绝=deny（独立二次确认）', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({id: 'req-pub-1', kind: 'publication', options: [], authorization: makeAuthorization()})} transport={transport} onChanged={() => {}} />);

    await user.click(screen.getByTestId('leader-reply-deny'));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('拒绝是最终决定');
    await user.click(await screen.findByRole('button', {name: '确认拒绝'}));
    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    expect((callsOf(calls, 'leaderReply')[0]!.args[2] as LeaderPublicationReplyBody).decision).toBe('deny');
  });

  it('授权正文缺失：允许被禁用并给出警示，拒绝仍可用', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({id: 'req-pub-1', kind: 'publication', options: [], authorization: null})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('publication-authorization-missing')).toHaveTextContent('不得批准');
    expect(screen.getByTestId('leader-reply-allow')).toBeDisabled();
    expect(screen.getByTestId('leader-reply-deny')).toBeEnabled();
  });

  it('已答复/已关闭请求不再提供操作', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({status: 'replied', replyDigest: 'sha256:9999999999999999999999999999999999999999999999999999999999999999'})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('leader-request-closed')).toBeInTheDocument();
    expect(screen.queryByTestId('leader-answer-option-north')).toBeNull();
  });

  it('结果未知可显式原键重放：两次调用同一幂等键', async () => {
    const leaderReply = vi.fn()
      .mockRejectedValueOnce(new TypeError('fetch failed'))
      .mockResolvedValueOnce({});
    const {transport, calls} = makeFakeTransport({leaderReply: leaderReply as Transport['leaderReply']});
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest()} transport={transport} onChanged={() => {}} />);

    await user.click(screen.getByTestId('leader-answer-option-north'));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));
    expect(await screen.findByText(/Leader 答复结果未知/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', {name: /原键重放/}));
    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(2));
    const first = callsOf(calls, 'leaderReply')[0]!.args[2] as LeaderBusinessReplyBody;
    const second = callsOf(calls, 'leaderReply')[1]!.args[2] as LeaderBusinessReplyBody;
    expect(second.idempotencyKey).toBe(first.idempotencyKey);
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });
});

describe('到期禁用（UI-10）', () => {
  it('到期的业务请求：回答按钮禁用并明确提示，不只提示仍放行', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({deadlineAt: '2026-09-09T00:00:00.000Z'})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('leader-answer-option-north')).toBeDisabled();
    expect(screen.getByTestId('leader-request-expired-block')).toBeInTheDocument();
  });

  it('到期的发布授权请求：允许与拒绝均禁用', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({id: 'req-pub-1', kind: 'publication', options: [], authorization: makeAuthorization(), deadlineAt: '2026-09-09T00:00:00.000Z'})} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('leader-reply-allow')).toBeDisabled();
    expect(screen.getByTestId('leader-reply-deny')).toBeDisabled();
    expect(screen.getByTestId('leader-request-expired-block')).toBeInTheDocument();
  });

  it('确认框打开期间到期：确认框关闭且绝不发送请求（后端拒绝仍是兜底）', () => {
    vi.useFakeTimers({now: Date.now()});
    try {
      const {transport, calls} = makeFakeTransport();
      const deadlineAt = new Date(Date.now() + 7000).toISOString();
      wrap(<LeaderRequestCard taskId={TASK_ID} expectedRevision={7} request={makeLeaderRequest({deadlineAt})} transport={transport} onChanged={() => {}} />);

      fireEvent.click(screen.getByTestId('leader-answer-option-north'));
      expect(screen.getByRole('dialog')).toBeInTheDocument();

      // 越过答复期限：useNow 的 5s tick 触发重渲染，到期后确认框必须关闭
      act(() => {
        vi.advanceTimersByTime(11000);
      });
      expect(screen.queryByRole('dialog')).toBeNull();
      expect(screen.getByTestId('leader-request-expired-block')).toBeInTheDocument();
      expect(screen.getByTestId('leader-answer-option-north')).toBeDisabled();
      expect(callsOf(calls, 'leaderReply')).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });
});
