// UI-04：评审（leader.review）、独立验收（audit.acceptance）、交付、后验分开呈现；
// 独立验收检查实际成果，评审 accept 不等于验收通过，不以 review 推导验收。
import {describe, expect, it} from 'vitest';
import {render, screen} from '@testing-library/react';
import type {AcceptanceStatus} from '@/lib/transport/types';
import {AcceptancePanel} from './acceptance-panel';
import {makeAudit, makeLeader} from '../testing/fixtures';

const ACCEPT_REVIEW = {
  digest: 'sha256:6666666666666666666666666666666666666666666666666666666666666666',
  verdict: 'accept' as const,
  selectionDigest: 'sha256:7777777777777777777777777777777777777777777777777777777777777777',
  policyDigest: 'sha256:8888888888888888888888888888888888888888888888888888888888888888',
  workerId: 'worker-reviewer',
  evidenceIds: ['ev-1'],
};

function renderPanel(status: AcceptanceStatus) {
  const leader = makeLeader({review: ACCEPT_REVIEW});
  const audit = makeAudit({acceptance: {status, evidenceIds: ['ev-a1'], digest: 'sha256:9999999999999999999999999999999999999999999999999999999999999999'}});
  return render(<AcceptancePanel leader={leader} audit={audit} />);
}

describe('独立验收面板（UI-04）', () => {
  it('Review accept + acceptance pending：评审通过与验收待定分开显示', () => {
    renderPanel('pending');
    expect(screen.getByTestId('review-readout')).toHaveTextContent('评审通过');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('配置检查待完成');
    // 从不把 Review accept 显示成验收通过
    expect(screen.getByTestId('acceptance-readout')).not.toHaveTextContent('配置检查通过');
  });

  it('Review accept + acceptance failed：评审通过但验收未通过', () => {
    renderPanel('failed');
    expect(screen.getByTestId('review-readout')).toHaveTextContent('评审通过');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('配置检查失败');
    expect(screen.getByTestId('acceptance-note')).toHaveTextContent('计划要求、评审接受或任务结束均不证明逐项业务已经实际验证');
  });

  it('Review accept + acceptance passed：验收摘要与证据数量可见', () => {
    renderPanel('passed');
    const readout = screen.getByTestId('acceptance-readout');
    expect(readout).toHaveTextContent('配置检查通过');
    expect(readout).toHaveTextContent('逐项业务验证覆盖：未确认');
    expect(readout).not.toHaveTextContent('文件完整性检查通过');
    expect(readout).not.toHaveTextContent('页面未测');
    expect(readout).toHaveTextContent('sha256:99999999');
    expect(readout).toHaveTextContent('1 条');
  });

  it('Review accept + acceptance unknown：如实显示验收状态未知', () => {
    renderPanel('unknown');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('配置检查结果未知');
  });

  it('无 publication/postverify 不推导未交付：文件交付引导至成果页', () => {
    renderPanel('passed');
    expect(screen.getByText('发布与后验')).toBeInTheDocument();
    expect(screen.getByText('无发布/后验动作；文件交付请查看成果页')).toBeInTheDocument();
    expect(screen.queryByText('暂无交付/后验动作。')).not.toBeInTheDocument();
    expect(screen.queryByTestId('publication-readout')).not.toBeInTheDocument();
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('配置检查通过');
  });

  it('publication 动作仅标为发布，不冒充一般文件交付', () => {
    render(<AcceptancePanel leader={makeLeader({publication: {
      actionId: 'action-publication', status: 'succeeded',
      authorizationDigest: `sha256:${'a'.repeat(64)}`, receiptArtifactId: 'receipt-publication',
    }})} audit={null} />);
    expect(screen.getByTestId('publication-readout')).toHaveTextContent('发布');
    expect(screen.getByTestId('publication-readout')).not.toHaveTextContent('交付发布');
    expect(screen.queryByText('无发布/后验动作；文件交付请查看成果页')).not.toBeInTheDocument();
  });

  it('audit 未加载：不以评审结果代替验收', () => {
    render(<AcceptancePanel leader={makeLeader({review: ACCEPT_REVIEW})} audit={null} />);
    expect(screen.getByTestId('review-readout')).toHaveTextContent('评审通过');
    expect(screen.getByTestId('acceptance-unloaded')).toHaveTextContent('不能以评审结果代替验收');
  });

  it('Leader 投影不可用与 audit 不可用分别如实降级', () => {
    render(<AcceptancePanel leader={null} audit={null} />);
    expect(screen.getByTestId('review-readout')).toHaveTextContent('Leader 投影不可用');
    expect(screen.getByTestId('acceptance-unloaded')).toBeInTheDocument();
  });
});
