import {describe,it,expect} from 'vitest';
import {currentStage,taskFocus} from './task-journey';
import {makeTask,makeLeader} from '../testing/fixtures';

describe('任务阶段以当前可行动事实表达',()=>{
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
