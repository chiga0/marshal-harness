import {describe,it,expect} from 'vitest';
import {MemoryRouter} from 'react-router-dom';
import {render,screen} from '@testing-library/react';
import {TaskJourney,TeamSummary,workerTitle,currentStage,taskFocus} from './task-journey';
import {makeTask,makeLeader,makeWorker} from '../testing/fixtures';

describe('任务阶段以当前可行动事实表达',()=>{
  it('无计划的Leader合同拒绝直接说明原因，不虚构过去失败阶段',()=>{
    const task=makeTask({status:'failed',phase:'terminal',plan:null,code:'invalid_leader_decision'});
    render(<MemoryRouter><TaskJourney task={task} leader={null} workers={[]} audit={null}/></MemoryRouter>);
    expect(screen.getByTestId('task-failure-explanation')).toHaveTextContent('Leader 提交的决定未通过合同校验');
    expect(screen.getByTestId('task-failure-explanation')).toHaveTextContent('尚未产生可用的执行计划');
    expect(screen.getByText('invalid_leader_decision').closest('details')).not.toHaveAttribute('open');
    expect(currentStage(task,null)).toBeNull();
  });

  it('同一Leader多次记录按执行序号呈现，完成后可直接看成果',()=>{
    expect(workerTitle(makeWorker({role:'planner',nodeId:'managed-leader-example',attempt:10}))).toBe('Leader 决策 · 执行 10');
    const task=makeTask({status:'completed'});
    render(<MemoryRouter><TaskJourney task={task} leader={null} workers={[]} audit={null}/></MemoryRouter>);
    expect(screen.getByText('流程已完成，查看成果与检查依据')).toBeInTheDocument();
    expect(screen.getByRole('list',{name:'任务阶段'})).toHaveTextContent('检查');
    expect(screen.getByRole('list',{name:'任务阶段'})).not.toHaveTextContent('验收');
    expect(screen.getByTestId('completed-view-delivery')).toHaveAttribute('href',`/tasks/${task.id}/artifacts`);
  });

  it('待批准优先于 Leader intake，不能显示仍在接收需求',()=>{
    const task=makeTask({status:'awaiting-approval',phase:'planning'});
    const leader=makeLeader({stage:'intake',taskRevision:task.revision});
    expect(currentStage(task,leader)).toBe(1);
    expect(taskFocus(task,leader)).toBe('计划已准备，等待你确认开始');
  });
  it.each(['failed','cancelled'] as const)('%s 终态不把阶段带标为交付成功',status=>{
    const task=makeTask({status,phase:'terminal'});
    expect(currentStage(task,makeLeader({stage:'delivery',taskRevision:task.revision}))).toBeNull();
    expect(taskFocus(task,null)).not.toContain('已完成');
  });
  it('陈旧 Leader 不能覆盖 Task 当前执行阶段',()=>{
    const task=makeTask({status:'running',phase:'execution',revision:9});
    expect(currentStage(task,makeLeader({stage:'review',taskRevision:8}))).toBe(2);
  });
  it('暂停保留明确调度边界，而不是暗示进程已经停止',()=>{
    expect(taskFocus(makeTask({status:'paused'}),null)).toBe('任务已暂停，不再安排新的执行');
  });
  it('发布授权定位交付而不是计划批准',()=>{
    const task=makeTask({status:'awaiting-confirmation',phase:'delivery'});
    const leader=makeLeader({stage:'delivery',taskRevision:task.revision});
    leader.pendingRequest={...leader.pendingRequest!,kind:'publication',status:'pending'};
    expect(currentStage(task,leader)).toBe(5);
    expect(taskFocus(task,leader)).toBe('等待你决定是否允许本次发布');
  });
  it('普通业务确认保留执行阶段，不伪称计划已准备',()=>{
    const task=makeTask({status:'awaiting-confirmation',phase:'execution'});
    const leader=makeLeader({stage:'work',taskRevision:task.revision});
    leader.pendingRequest={...leader.pendingRequest!,kind:'business',status:'pending'};
    expect(currentStage(task,leader)).toBe(2);
    expect(taskFocus(task,leader)).toBe('有业务事项等待你确认');
  });
  it('陈旧或其他Task发布请求不能被当作本次授权',()=>{
    const task=makeTask({status:'awaiting-confirmation',phase:'execution',revision:9});
    const leader=makeLeader({stage:'delivery',taskRevision:8});
    leader.pendingRequest={...leader.pendingRequest!,kind:'publication',status:'pending'};
    expect(currentStage(task,leader)).toBe(2);
    expect(taskFocus(task,leader)).not.toContain('发布');
    expect(taskFocus(task,{...leader,taskRevision:9,taskId:'other-task'})).not.toContain('发布');
  });

});


describe('执行记录不把计划目标冒充实际检查',()=>{
  it('Verifier只以配置检查和执行序号命名',()=>{expect(workerTitle(makeWorker({role:'verifier',attempt:8}))).toBe('配置检查 · 执行 8');});
  it('当前活动优先，其后失败记录优于成功记录，同组最近执行先展示',()=>{
    const workers=[...Array.from({length:7},(_,i)=>makeWorker({id:`old-${i}`,status:'completed',attempt:i+1})),makeWorker({id:'failed-new',status:'failed',attempt:9}),makeWorker({id:'failed-old',status:'failed',attempt:8}),makeWorker({id:'active',status:'running',attempt:10})];
    render(<MemoryRouter><TeamSummary task={makeTask({status:'completed'})} plan={null} workers={workers}/></MemoryRouter>);
    const links=screen.getAllByRole('link').filter(link=>link.getAttribute('href')?.includes('/team/'));
    expect(links.slice(0,3).map(link=>link.getAttribute('href')?.split('/').at(-1))).toEqual(['active','failed-new','failed-old']);
    expect(links[1]).toHaveTextContent('该次执行失败');
    expect(screen.queryByText(/当前阻塞/)).not.toBeInTheDocument();
  });
});

describe('概览直接呈现需要关注的执行成员',()=>{
  it('没有运行、等待或问题成员时不增加成员关注区',()=>{
    render(<MemoryRouter><TaskJourney task={makeTask({status:'completed'})} leader={null} workers={[makeWorker({status:'completed'})]} audit={null}/></MemoryRouter>);
    expect(screen.queryByTestId('task-journey-focus')).not.toBeInTheDocument();
  });

  it('运行成员直接显示成员标识、节点、公开活动和下一步',()=>{
    const worker=makeWorker({id:'worker-running',nodeId:'sales-east',providerId:'qwen-code',status:'running',progress:{summary:'正在整理订单',tool:'read:completed',source:'agent'}});
    render(<MemoryRouter><TaskJourney task={makeTask()} leader={null} workers={[worker]} audit={null}/></MemoryRouter>);
    const focus=screen.getByTestId('task-journey-focus');
    expect(focus).toHaveTextContent('执行 · worker-running');
    expect(focus).toHaveTextContent('节点：sales-east');
    expect(focus).toHaveTextContent('最近公开活动：正在整理订单 · 最近工具 read:completed');
    expect(focus).toHaveTextContent('继续观察');
    expect(screen.getByRole('link',{name:/worker-running/})).toHaveAttribute('href','/tasks/task-test-0001/team/worker-running');
  });

  it('等待成员和诊断成员分别显示等待动作与已报告问题',()=>{
    const waiting=makeWorker({id:'worker-waiting',nodeId:'needs-answer',status:'awaiting-answer',progress:null});
    const blocked=makeWorker({id:'worker-blocked',nodeId:'protected-output',status:'unknown',observation:{profile:'task-observation/v1',activity:'unknown',observedAt:'2026-09-15T00:00:00Z',sequence:3,tool:null,model:null,usage:null,publicText:'执行状态暂不可确认',history:[],historyTruncated:false,diagnostic:{stage:'permission',code:'permission_path_denied',source:'provider-permission'}}});
    render(<MemoryRouter><TaskJourney task={makeTask()} leader={null} workers={[waiting,blocked]} audit={null}/></MemoryRouter>);
    const cards=screen.getAllByTestId('task-journey-worker');
    expect(cards).toHaveLength(2);
    expect(cards[0]).toHaveTextContent('worker-blocked');
    expect(cards[0]).toHaveTextContent('阻塞/问题：请求访问的路径未获授权');
    expect(cards[0]).toHaveTextContent('核对该诊断对应的活动与执行记录');
    expect(cards[1]).toHaveTextContent('worker-waiting');
    expect(cards[1]).toHaveTextContent('核对任务中的待答问题并提交答复');
  });
});
