import {describe,it,expect} from 'vitest';
import {render,screen} from '@testing-library/react';
import type {WorkerObservation} from '@/lib/transport/types';
import {makeWorker} from '../tasks/detail/testing/fixtures';
import {observationLabel,WorkerObservationView} from './worker-observation';
const observation = ():WorkerObservation=>({profile:'task-observation/v1',activity:'tool',observedAt:new Date().toISOString(),sequence:2,tool:{id:'read-1',kind:'read',status:'completed'},model:{id:'test-model',source:'provider-reported'},usage:{inputTokens:10,outputTokens:2,totalTokens:12,source:'provider-reported',complete:false},publicText:'',history:[],historyTruncated:false});
describe('有来源的执行观察',()=>{
  it('工具结束不能继续称为正在调用；终态覆盖旧活动',()=>{
    const w=makeWorker({observation:observation()});
    expect(observationLabel(w)).toBe('工具调用已结束');
    expect(observationLabel({...w,status:'failed'})).toBe('失败');
  });
  it('陈旧输出明确是上次观察，缺失用量不填0',()=>{
    render(<WorkerObservationView worker={makeWorker({observation:{...observation(),activity:'output',observedAt:'2020-01-01T00:00:00Z',usage:{inputTokens:null,outputTokens:2,totalTokens:null,source:'provider-reported',complete:false}}})} />);
    expect(screen.getByText('上次活动：正在输出文本')).toBeInTheDocument();
    expect(screen.getByText(/暂未收到新活动/)).toBeInTheDocument();
    expect(screen.getAllByText('未报告')).toHaveLength(2);
    expect(screen.getByTestId('observed-usage')).not.toHaveTextContent('0');
  });
  it('重复累计快照不相加',()=>{
    const frame=observation();
    render(<WorkerObservationView worker={makeWorker({observation:{...frame,history:[{...frame,sequence:1},{...frame,sequence:2}]}})} />);
    expect(screen.getByTestId('observed-usage')).toHaveTextContent('12');
    expect(screen.getByTestId('observed-usage')).not.toHaveTextContent('24');
  });
});
