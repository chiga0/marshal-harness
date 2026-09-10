import {describe, expect, it} from 'vitest';
import {casRevisionOf, isTerminalStatus, leaderPendingRequest, preferFreshTask, questionStage, questionStageBadge} from './derive';
import {makeLeader, makeLeaderRequest, makePreapprovalQuestion, makeRunningQuestion, makeTask} from './test-fakes';

describe('派生读数（合同类型直接映射，无防御性加宽）', () => {
  it('casRevisionOf：TaskRecord.revision 为必返数字，CAS 永远可用', () => {
    expect(casRevisionOf(makeTask({revision: 9}))).toBe(9);
  });

  it('isTerminalStatus：仅 completed/failed/cancelled 为终态', () => {
    expect(isTerminalStatus('completed')).toBe(true);
    expect(isTerminalStatus('failed')).toBe(true);
    expect(isTerminalStatus('cancelled')).toBe(true);
    expect(isTerminalStatus('running')).toBe(false);
    expect(isTerminalStatus('intervention')).toBe(false);
  });

  it('preferFreshTask：乱序旧 revision 不得覆盖新快照（E21）', () => {
    const older = makeTask({revision: 3});
    const newer = makeTask({revision: 4});
    expect(preferFreshTask(older, newer)).toBe(newer);
    expect(preferFreshTask(newer, older)).toBe(newer); // 旧快照到达被拦截
    expect(preferFreshTask(undefined, newer)).toBe(newer);
  });

  it('preferFreshTask：revision 相同时回退 updatedAt 比较', () => {
    const earlier = makeTask({revision: 5, updatedAt: '2026-09-10T01:00:00.000Z'});
    const later = makeTask({revision: 5, updatedAt: '2026-09-10T01:05:00.000Z'});
    expect(preferFreshTask(later, earlier)).toBe(later); // 更早的 updatedAt 不得覆盖
    expect(preferFreshTask(earlier, later)).toBe(later);
  });

  it('questionStage：RunningQuestion 必有节点；预批准问题 nodeId 可空', () => {
    const running = questionStage(makeRunningQuestion());
    expect(running.kind).toBe('business');
    expect(running.nodeId).toBe('east');
    const preapproval = questionStage(makePreapprovalQuestion({kind: 'permission'}));
    expect(preapproval.kind).toBe('permission');
    expect(preapproval.nodeId).toBeNull();
  });

  it('questionStageBadge：预批准三类固定措辞；运行问题带节点；运行 = kind business', () => {
    expect(questionStageBadge(makePreapprovalQuestion({kind: 'clarification'}))).toBe('预批准澄清问题');
    expect(questionStageBadge(makePreapprovalQuestion({kind: 'permission'}))).toBe('预批准许可问题');
    expect(questionStageBadge(makePreapprovalQuestion({kind: 'acceptance'}))).toBe('验收口径问题');
    expect(questionStageBadge(makeRunningQuestion())).toBe('Worker 运行问题（节点 east）');
  });

  it('leaderPendingRequest：合同保证投影，null=无待处理请求，对象原样透传', () => {
    expect(leaderPendingRequest(null)).toBeNull();
    expect(leaderPendingRequest(makeLeader())).toBeNull();
    const request = makeLeaderRequest();
    expect(leaderPendingRequest(makeLeader({pendingRequest: request}))).toBe(request);
  });
});
