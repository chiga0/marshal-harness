// UI-04：评审（leader.review）、独立验收（audit.acceptance）、交付、后验分开呈现；
// acceptance=passed 才是验收通过，评审 accept 不等于验收通过，不以 review 推导验收。
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
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('验收待定');
    // 从不把 Review accept 显示成验收通过
    expect(screen.getByTestId('acceptance-readout')).not.toHaveTextContent('验收通过');
  });

  it('Review accept + acceptance failed：评审通过但验收未通过', () => {
    renderPanel('failed');
    expect(screen.getByTestId('review-readout')).toHaveTextContent('评审通过');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('验收未通过');
    expect(screen.getByTestId('acceptance-note')).toHaveTextContent('acceptance=passed 才是验收通过');
  });

  it('Review accept + acceptance passed：验收摘要与证据数量可见', () => {
    renderPanel('passed');
    const readout = screen.getByTestId('acceptance-readout');
    expect(readout).toHaveTextContent('验收通过');
    expect(readout).toHaveTextContent('sha256:99999999');
    expect(readout).toHaveTextContent('1 条');
  });

  it('Review accept + acceptance unknown：如实显示验收状态未知', () => {
    renderPanel('unknown');
    expect(screen.getByTestId('acceptance-readout')).toHaveTextContent('验收状态未知');
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
