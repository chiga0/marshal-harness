import {render,screen} from '@testing-library/react';
import {describe,it,expect} from 'vitest';
import {makeWorker} from '../tasks/detail/testing/fixtures';
import {diagnosticReason,WorkerDiagnostic} from './worker-diagnostic';
describe('有来源的成员诊断',()=>{
  it.each([['permission_denied','工具请求未获授权'],['permission_shape_denied','工具授权请求格式不符合要求'],['permission_kind_denied','该类工具操作未获授权'],['permission_path_denied','请求访问的路径未获授权']] as const)('权限诊断%s只映射固定类别', (code,label)=>{
    expect(diagnosticReason({stage:'permission',code,source:'provider-permission'})).toBe(label);
  });

  it('只展示已报告原因，不从failed推测权限或模型故障',()=>{
    render(<WorkerDiagnostic worker={makeWorker({status:'failed'})}/>);
    expect(screen.getByTestId('worker-diagnostic')).toHaveTextContent('未报告具体原因');
  });
  it('收集失败与Agent执行失败区分，诊断不覆盖成员状态',()=>{
    render(<WorkerDiagnostic worker={makeWorker({status:'completed',observation:{profile:'task-observation/v1',activity:'terminal',observedAt:new Date().toISOString(),sequence:1,tool:null,model:null,usage:null,publicText:'',history:[],historyTruncated:false,diagnostic:{stage:'collecting',code:'collection_failed',source:'controller'}}})}/>);
    expect(screen.getByText('成果收集失败')).toBeInTheDocument();
    expect(screen.getByTestId('worker-diagnostic')).toHaveTextContent('执行状态以上方成员状态为准');
    expect(screen.getByText('collecting / collection_failed / controller').closest('details')).not.toHaveAttribute('open');
  });
});
