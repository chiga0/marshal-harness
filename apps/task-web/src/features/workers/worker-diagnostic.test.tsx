import {render,screen} from '@testing-library/react';
import {describe,it,expect} from 'vitest';
import {makeWorker} from '../tasks/detail/testing/fixtures';
import {diagnosticReason,WorkerDiagnostic} from './worker-diagnostic';
describe('有来源的成员诊断',()=>{
  it.each([['task_files_missing_output','缺少约定的输出文件'],['task_files_input_changed','输入文件内容已改变'],['task_files_identity_changed','文件身份或属性已改变'],['task_files_changed','采集期间文件发生变化'],['task_files_unallowed_output','发现未授权的输出文件'],['task_files_depot_integrity','制品存储校验失败'],['task_files_limit','文件数量或大小超过限制'],['task_files_unavailable','文件采集条件不满足'],['business_cleanup_required','缺少确认清理的证据'],['business_execution_mismatch','执行身份不匹配'],['business_unapproved_layout','输出布局绑定不匹配'],['business_report_limit','最终报告格式或大小不符合限制']] as const)('采集细因%s映射准确且不暴露文件路径', (code,label)=>{
    expect(diagnosticReason({stage:'collecting',code,source:'controller'})).toBe(label);
  });

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


it.each([['invalid_json','返回文本不是合法 JSON'],['invalid_leader_result','受管执行结果不符合要求'],['invalid_leader_decision','Leader 提案不符合合同'],['invalid_review_report','独立评审报告不符合合同']] as const)('受信协议诊断%s不冒称Provider运行故障或当前阻塞',(code,label)=>{
  render(<WorkerDiagnostic worker={makeWorker({status:'completed',observation:{profile:'task-observation/v1',activity:'terminal',observedAt:'2026-09-15T00:00:00Z',sequence:8,tool:null,model:null,usage:null,publicText:'',history:[],historyTruncated:false,diagnostic:{stage:'protocol',code,source:'controller'}}})}/>);
  const section=screen.getByTestId('worker-diagnostic');expect(section).toHaveTextContent(label);expect(section).toHaveTextContent('受管结果协议解析');expect(section).toHaveTextContent('执行控制器记录');expect(section).toHaveTextContent('不一定是最终失败原因');
  expect(section).not.toHaveTextContent('Agent 执行失败');expect(section).not.toHaveTextContent('当前阻塞');
  expect(screen.getByText(`protocol / ${code} / controller`).closest('details')).not.toHaveAttribute('open');
});
