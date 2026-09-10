import {describe, expect, it, vi} from 'vitest';
import {render, screen, waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import type {ReactNode} from 'react';
import type {Transport} from '@/lib/transport/types';
import {LeaderRequestCard} from './leader-request-card';
import type {LeaderPendingRequestView} from '../shared/derive';
import {callsOf, makeFakeTransport, TASK_ID} from '../testing/fixtures';

function wrap(node: ReactNode) {
  const client = new QueryClient({defaultOptions: {queries: {retry: false}, mutations: {retry: 0}}});
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const BUSINESS_REQUEST: LeaderPendingRequestView = {
  id: 'req-biz-1',
  kind: 'business',
  prompt: '报告采用哪个地区的数据？',
  options: [{value: 'north', label: '北区'}, {value: 'south', label: '南区'}],
  deadlineAt: null,
  status: 'pending',
  requestDigest: 'sha256:0c57023789abb02551ac7837838ae3cb9dcd86ff0f3c58b4db372999eab51a7c',
  replyDigest: null,
  authorization: null,
  nodeIds: [],
};

const PUBLICATION_REQUEST: LeaderPendingRequestView = {
  ...BUSINESS_REQUEST,
  id: 'req-pub-1',
  kind: 'publication',
  prompt: '申请把验收摘要发布到本机报告目录。',
  options: [],
  authorization: {
    taskId: TASK_ID,
    targetId: 'local-reports',
    name: 'task-1-aaaa.json',
    operation: 'create-if-absent',
    bytes: 18,
    artifactId: 'artifact-1',
    artifactDigest: 'sha256:1111111111111111111111111111111111111111111111111111111111111111',
    planDigest: 'sha256:2222222222222222222222222222222222222222222222222222222222222222',
    acceptanceDigest: 'sha256:3333333333333333333333333333333333333333333333333333333333333333',
    reviewDigest: 'sha256:4444444444444444444444444444444444444444444444444444444444444444',
    targetPolicyDigest: 'sha256:5555555555555555555555555555555555555555555555555555555555555555',
    expiresAt: '2026-09-11T00:00:00.000Z',
  },
};

describe('Leader 答复（P06 / E31 / P10）', () => {
  it('业务请求走 leader.reply（requestId+revision+outcome+幂等键），绝不走 task.answer', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} revision={'7'} request={BUSINESS_REQUEST} transport={transport} onChanged={() => {}} />);

    expect(screen.getByText('报告采用哪个地区的数据？')).toBeInTheDocument();
    // 自带选项原文展示（value 与 label 均可见）
    expect(screen.getByText('北区')).toBeInTheDocument();
    expect(screen.getByText('north')).toBeInTheDocument();

    await user.click(screen.getByTestId('leader-reply-accepted'));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));

    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    const body = callsOf(calls, 'leaderReply')[0]!.args[1] as {requestId: string; revision: string; outcome: string; idempotencyKey: string};
    expect(body.requestId).toBe('req-biz-1');
    expect(body.revision).toBe('7');
    expect(body.outcome).toBe('accepted');
    expect(body.idempotencyKey.length).toBeGreaterThan(0);
    expect(callsOf(calls, 'answerTask')).toHaveLength(0);
  });

  it('202 受理叙事如实：不代表已继续/已发布，不显示 Worker 投递/ACK', async () => {
    const {transport} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} revision={'7'} request={BUSINESS_REQUEST} transport={transport} onChanged={() => {}} />);
    await user.click(screen.getByTestId('leader-reply-accepted'));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));
    const accepted = await screen.findByTestId('leader-reply-accepted');
    expect(accepted).toHaveTextContent('202 仅证明该 Leader 请求被接纳');
    expect(accepted).toHaveTextContent('不代表任务已继续、已发布或后验通过');
    expect(accepted).toHaveTextContent('也不是 Worker 的投递/ACK');
  });

  it('发布授权请求：完整授权正文展示，拒绝分支 outcome=rejected', async () => {
    const {transport, calls} = makeFakeTransport();
    const user = userEvent.setup();
    wrap(<LeaderRequestCard taskId={TASK_ID} revision={'7'} request={PUBLICATION_REQUEST} transport={transport} onChanged={() => {}} />);

    const authorization = screen.getByTestId('publication-authorization');
    expect(authorization).toHaveTextContent('local-reports');
    expect(authorization).toHaveTextContent('create-if-absent');
    expect(authorization).toHaveTextContent('task-1-aaaa.json');

    await user.click(screen.getByRole('button', {name: '拒绝发布'}));
    await user.click(await screen.findByRole('button', {name: '确认答复'}));
    await waitFor(() => expect(callsOf(calls, 'leaderReply')).toHaveLength(1));
    expect((callsOf(calls, 'leaderReply')[0]!.args[1] as {outcome: string}).outcome).toBe('rejected');
  });

  it('已答复/已关闭请求不再提供操作', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} revision={'7'} request={{...BUSINESS_REQUEST, status: 'replied', replyDigest: 'sha256:9999999999999999999999999999999999999999999999999999999999999999'}} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('leader-request-closed')).toBeInTheDocument();
    expect(screen.queryByTestId('leader-reply-accepted')).toBeNull();
  });

  it('缺 revision 禁用提交', () => {
    const {transport} = makeFakeTransport();
    wrap(<LeaderRequestCard taskId={TASK_ID} revision={null} request={BUSINESS_REQUEST} transport={transport} onChanged={() => {}} />);
    expect(screen.getByTestId('leader-reply-accepted')).toBeDisabled();
  });
});
