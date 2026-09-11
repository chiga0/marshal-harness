import {expect, it} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {ApiError, type ControlBody} from '@/lib/transport/types';
import {CancelWorkerFlow} from './cancel-worker-flow';
import {makeFakeTransport, makeWorker} from '../tasks/detail/testing/fixtures';

it('未知 Worker 取消只能原键重放，不能直接重新开始', async () => {
  const bodies: ControlBody[] = [];
  const {transport} = makeFakeTransport({cancelWorker: async (_id, body) => { bodies.push(body); throw new TypeError('lost response'); }});
  const client = new QueryClient();
  const node = (revision: number) => <QueryClientProvider client={client}><CancelWorkerFlow taskId="task-1" taskRevision={revision} worker={makeWorker()} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
  const view = render(node(7));
  const user = userEvent.setup();
  await user.click(screen.getByTestId('cancel-worker-open'));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '确认取消该 Worker'}));
  await screen.findByTestId('error-notice');
  view.rerender(node(8));
  expect(screen.getByTestId('cancel-worker-open')).toBeDisabled();
  expect(screen.queryByText('已核对 Worker 状态，重新开始取消')).toBeNull();
  await user.click(screen.getByRole('button', {name: /原键重放/}));
  expect(bodies).toHaveLength(2);
  expect(bodies[0]).toEqual(bodies[1]);
});

it('Worker取消冻结确认CAS，409后核对重开而非更换未知请求键', async () => {
  let first = true;
  const bodies: ControlBody[] = [];
  const {transport} = makeFakeTransport({cancelWorker: async (_id, body) => {
    bodies.push(body);
    if (first) { first = false; throw new ApiError(409, 'revision_conflict', '版本冲突', null); }
    return {};
  }});
  const client = new QueryClient();
  const node = (revision: number) => <QueryClientProvider client={client}><CancelWorkerFlow taskId="task-1" taskRevision={revision} worker={makeWorker()} transport={transport} onChanged={() => {}} /></QueryClientProvider>;
  const view = render(node(7));
  const user = userEvent.setup();
  await user.click(screen.getByTestId('cancel-worker-open'));
  view.rerender(node(8));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '确认取消该 Worker'}));
  await screen.findByText('已核对 Worker 状态，重新开始取消');
  expect(screen.getByTestId('cancel-worker-open')).toBeDisabled();
  await user.click(screen.getByText('已核对 Worker 状态，重新开始取消'));
  await user.click(screen.getByTestId('cancel-worker-open'));
  await user.click(within(screen.getByRole('dialog')).getByRole('button', {name: '确认取消该 Worker'}));
  await screen.findByTestId('cancel-worker-accepted');
  expect(bodies.map(body => body.expectedRevision)).toEqual([7, 8]);
  expect(bodies[0]!.idempotencyKey).not.toBe(bodies[1]!.idempotencyKey);
});
