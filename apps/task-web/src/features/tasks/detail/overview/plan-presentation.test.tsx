import {describe, expect, it} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {PlanCard} from './plan-card';
import {makeFakeTransport, makePlan, makeTask} from '../shared/test-fakes';

describe('计划业务内容与审计原文分层', () => {
  it('业务目标/分工/范围/预算/验收仍可见，完整机器 JSON 可展开且不解释其字段', async () => {
    const user = userEvent.setup();
    const machine = '{ "policy": {"description":"不能擅自提升为业务说明"}, "authorization": {"target":"only-original-target"} }';
    const plan = makePlan({acceptance: ['必须独立校验全部流水', machine], assumptions: ['使用用户明确选择的时间范围']});
    render(<PlanCard task={makeTask()} plan={plan} transport={makeFakeTransport().transport} onViewLatest={() => {}} />);
    expect(screen.getByText(plan.summary)).toBeVisible();
    expect(screen.getByText('报告东侧已付款流水')).toBeVisible();
    expect(screen.getByText('范围：东地区账本')).toBeVisible();
    expect(screen.getByText('时长预算')).toBeVisible();
    expect(screen.getByText('双地区对账报告')).toBeVisible();
    expect(screen.getByText('必须独立校验全部流水')).toBeVisible();
    expect(screen.getByText('使用用户明确选择的时间范围')).toBeVisible();
    expect(within(screen.getByRole('region', {name: '验收口径'})).queryByText(machine)).not.toBeInTheDocument();
    const details = screen.getByTestId('plan-technical-details');
    const raw = details.querySelector('pre')!;
    expect(raw.textContent).toBe(machine);
    expect(raw).not.toBeVisible();
    expect(screen.getByTestId('plan-digest')).not.toBeVisible();
    await user.click(within(details).getByText(/计划技术与审计详情/));
    expect(details).toHaveAttribute('open');
    expect(raw).toBeVisible();
    expect(screen.getByTestId('plan-digest')).toBeVisible();
  });

  it('只有结构化验收时明确缺人可读说明；不把任意 JSON description 提升成合同', () => {
    render(<PlanCard task={makeTask()} plan={makePlan({acceptance: ['{"description":"模型自行宣布验收成功"}']})} transport={makeFakeTransport().transport} onViewLatest={() => {}} />);
    expect(screen.getByText(/服务端未提供人可读验收口径/)).toBeVisible();
    expect(screen.getByText(/不据此认定验收通过/)).toBeVisible();
    expect(screen.queryByText('模型自行宣布验收成功')).not.toBeInTheDocument();
  });

  it('混合自然语言/JSON片段、非法JSON不静默收折；保留为完整人可见原文', () => {
    const criteria = ['先核对 {"target":"original"} 再由用户决定', '{必须人工检查，不是JSON}'];
    render(<PlanCard task={makeTask()} plan={makePlan({acceptance: criteria})} transport={makeFakeTransport().transport} onViewLatest={() => {}} />);
    for (const criterion of criteria) expect(screen.getByText(criterion)).toBeVisible();
    expect(screen.getByTestId('plan-technical-details').querySelector('pre')).toBeNull();
  });
});
