import {describe, it, expect} from 'vitest';
import {render, screen} from '@testing-library/react';
import {MemoryRouter} from 'react-router-dom';
import {LeaderCorrection} from './leader-correction';
import {workerStatusLabel} from '../shared/format';
import {makeLeader, makeTask, makeWorker} from '../testing/fixtures';
import type {LeaderProtocolCorrection, WorkerRecord} from '@/lib/transport/types';
const digest = `sha256:${'a'.repeat(64)}` as const;
const correction: LeaderProtocolCorrection = {profile:'leader-json-correction/v1',used:1,max:1,reason:'wire-json',original:{stage:'wire-json',code:'invalid_json',outputDigest:digest,outputBytes:20,workerId:'original',callId:'call-original',ticketDigest:digest,cleanupDigest:digest,at:'2026-09-14T00:00:00Z'},successorWorkerId:null,successorCallId:null};
function show(value: LeaderProtocolCorrection | undefined, workers: WorkerRecord[] | null = [], stale = false) {
  const task=makeTask(); const leader=makeLeader({taskId:task.id,taskRevision:stale ? task.revision-1 : task.revision,...(value ? {protocolCorrection:value} : {})});
  render(<MemoryRouter><LeaderCorrection task={task} leader={leader} workers={workers}/></MemoryRouter>); return task;
}
describe('有限格式纠错仅显示真实记录',()=>{
  it.each([undefined,{...correction,used:0,reason:null,original:null} as LeaderProtocolCorrection])('旧配置与未使用预算不宣称纠错进行中',value=>{
    show(value); expect(screen.queryByTestId('leader-correction')).not.toBeInTheDocument();
  });
  it('陈旧Leader投影不显示当前纠错',()=>{show(correction,[],true);expect(screen.queryByTestId('leader-correction')).not.toBeInTheDocument();});
  it('已用预算但尚未保留后续执行，不伪称正在运行',()=>{
    const task=show(correction);
    expect(screen.getByTestId('correction-execution-status')).toHaveTextContent('尚未记录已保留');
    expect(screen.getByTestId('correction-original')).toHaveAttribute('href',`/tasks/${task.id}/team/original`);
    expect(screen.queryByTestId('correction-successor')).not.toBeInTheDocument();
    expect(screen.getByText('纠错绑定与原失败证据').closest('details')).not.toHaveAttribute('open');
  });
  it('分页未加载后续执行不推断排队或正在执行',()=>{
    show({...correction,successorWorkerId:'next',successorCallId:'next-call'},null);
    expect(screen.getByTestId('correction-execution-status')).toHaveTextContent('状态暂不可确认');
  });
  it.each(['running','failed','completed','cancelled'] as const)('后续%s不改写为业务纠错成功',status=>{
    const task=makeTask(); show({...correction,successorWorkerId:'next',successorCallId:'next-call'},[makeWorker({id:'next',taskId:task.id,status})]);
    expect(screen.getByTestId('correction-execution-status')).toHaveTextContent(`后续执行记录：${workerStatusLabel(status)}`);
    expect(screen.getByTestId('leader-correction')).toHaveTextContent('不代表后续决定、业务结果或验收已通过');
  });
});
