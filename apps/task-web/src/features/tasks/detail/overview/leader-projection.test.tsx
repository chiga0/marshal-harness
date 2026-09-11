import {describe, expect, it} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {LeaderProjection} from './leader-projection';
import {makeLeader, makeLeaderRequest, makeWorker} from '../shared/test-fakes';

describe('团队协调进展的业务优先展示', () => {
  it('当前阶段/待处理类型可见，hash和协议引用在展开后完整可查', async () => {
    const user = userEvent.setup();
    const worker = makeWorker();
    const leader = makeLeader({activeWorkerId: worker.id, pendingRequest: makeLeaderRequest({kind: 'publication'}), lastDecision: {digest: 'sha256:original-decision', callId: 'original-call', evidenceId: 'evidence-1'}, summaryArtifactId: 'original-summary'});
    render(<LeaderProjection leader={leader} workers={[worker]} />);
    expect(screen.getByRole('heading', {name: '团队协调进展'})).toBeVisible();
    expect(screen.getByText(/发布授权 · 待处理/)).toBeVisible();
    expect(screen.getByText(/不能据此推断执行结果/)).toBeVisible();
    expect(screen.getByText(/内容和交付结果请在成果页核对/)).toBeVisible();
    const details = screen.getByTestId('leader-technical-details');
    expect(screen.getByText(leader.policyDigest)).not.toBeVisible();
    expect(screen.getByText(leader.profile)).not.toBeVisible();
    await user.click(within(details).getByText('Leader 技术与审计详情'));
    expect(screen.getByText(leader.policyDigest)).toBeVisible();
    expect(screen.getByText('sha256:original-decision / original-call')).toBeVisible();
    expect(screen.getByText('original-summary')).toBeVisible();
    expect(screen.getByText(worker.id)).toBeVisible();
  });

  it('成员引用未加载不猜角色或运行状态；不可用投影仍明确', () => {
    const view = render(<LeaderProjection leader={makeLeader({activeWorkerId: 'unloaded-member'})} workers={[]} />);
    expect(screen.getByText('已有成员引用，成员详情尚未加载')).toBeVisible();
    view.rerender(<LeaderProjection leader={null} workers={[]} />);
    expect(screen.getByTestId('leader-unavailable')).toBeVisible();
  });

  it('Leader 投影不可用时，已加载成员的观察读数仍独立可展开核对', async () => {
    const user = userEvent.setup();
    const workers = [makeWorker({id: 'worker-a', nodeId: 'author-a', lastObservedAt: '2026-09-10T10:00:00.000Z'}), makeWorker({id: 'worker-b', nodeId: 'reviewer-b', lastObservedAt: '2026-09-10T11:00:00.000Z'})];
    render(<LeaderProjection leader={null} workers={workers} />);
    expect(screen.getByTestId('leader-unavailable')).toBeVisible();
    expect(screen.queryByTestId('leader-technical-details')).not.toBeInTheDocument();
    const details = screen.getByTestId('worker-observation-details');
    await user.click(within(details).getByText('执行成员最近观察（2 个）'));
    expect(details).toHaveAttribute('open');
    for (const worker of workers) {
      expect(within(details).getByText(new RegExp(worker.nodeId))).toBeVisible();
      expect(within(details).getByTitle(worker.lastObservedAt!)).toHaveAttribute('datetime', worker.lastObservedAt);
    }
    expect(screen.getByText(/最后观察时间不代表模型仍在持续工作/)).toBeVisible();
  });
});
