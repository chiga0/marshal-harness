import {describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ApiError} from '@/lib/transport/types';
import {ErrorNotice} from './error-notice';

describe('ErrorNotice（通用错误展示 / E27 不展示敏感信息）', () => {
  it('展示错误码、中文指引与 requestId；技术详情默认折叠', () => {
    render(<ErrorNotice error={new ApiError(409, 'revision_conflict', '对象版本已变化。', 'req-abc-1')} title="确认计划失败" />);
    expect(screen.getByRole('alert')).toHaveTextContent('确认计划失败');
    expect(screen.getByRole('alert')).toHaveTextContent('版本已过期');
    expect(screen.getByTestId('error-code')).toHaveTextContent('revision_conflict');
    expect(screen.getByTestId('error-code')).toHaveTextContent('HTTP 409');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-abc-1');
    // 技术详情默认折叠（details 无 open 属性）
    const details = document.querySelector('details');
    expect(details).not.toBeNull();
    expect(details!.hasAttribute('open')).toBe(false);
  });

  it('已知码的恢复动作按钮真实绑定；原键重放可点击', async () => {
    const onReplay = vi.fn();
    const onRefresh = vi.fn();
    render(<ErrorNotice error={new ApiError(504, 'request_timeout', '请求超时', 'req-9')} onReplay={onReplay} onRefresh={onRefresh} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', {name: '原键重放（不重新执行）'}));
    expect(onReplay).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', {name: '刷新查看'}));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it('未知错误码不猜测含义，仍显示原始 code', () => {
    render(<ErrorNotice error={new ApiError(500, 'brand_new_code', '???', 'req-1')} />);
    expect(screen.getByRole('alert')).toHaveTextContent('未识别的错误');
    expect(screen.getByTestId('error-code')).toHaveTextContent('brand_new_code');
  });

  it('idempotency_conflict 指引「先去核对」', () => {
    render(<ErrorNotice error={new ApiError(409, 'idempotency_conflict', '同键冲突', 'req-idem-1')} onRefresh={() => {}} />);
    expect(screen.getByRole('alert')).toHaveTextContent('请勿再次提交，先刷新核对任务状态');
  });

  it('从不渲染 token/Bearer 内容', () => {
    render(<ErrorNotice error={new ApiError(401, 'unauthorized', '未授权', 'req-u1')} />);
    expect(document.body.innerHTML).not.toContain('Bearer');
  });
});
