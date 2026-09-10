import {describe, expect, it} from 'vitest';
import type {TaskDetail} from '@/lib/transport/types';
import {casRevisionOf, leaderPendingRequest, preferFreshTask, questionStage, rawTaskRevision} from './derive';
import {makeDetail, makeLeader, makePlan, makeQuestion} from '../testing/fixtures';

describe('防御性加宽（冻结 DTO 缺字段的如实处理）', () => {
  it('rawTaskRevision：真实服务的数字 revision 转字符串；缺失返回 null', () => {
    expect(rawTaskRevision({...makeDetail(), revision: 7} as unknown as TaskDetail)).toBe('7');
    expect(rawTaskRevision(makeDetail())).toBeNull();
    expect(rawTaskRevision({...makeDetail(), revision: 'abc'} as unknown as TaskDetail)).toBe('abc');
  });

  it('casRevisionOf：优先 task.revision，缺失时回退 plan.revision，全无返回 null', () => {
    expect(casRevisionOf({...makeDetail({plan: makePlan({revision: '3'})}), revision: 9} as unknown as TaskDetail)).toBe('9');
    expect(casRevisionOf(makeDetail({plan: makePlan({revision: '3'})}))).toBe('3');
    expect(casRevisionOf(makeDetail())).toBeNull();
  });

  it('preferFreshTask：乱序旧 revision 不得覆盖新快照（E21）', () => {
    const older = {...makeDetail(), revision: 3} as unknown as TaskDetail;
    const newer = {...makeDetail(), revision: 4} as unknown as TaskDetail;
    expect(preferFreshTask(older, newer)).toBe(newer);
    expect(preferFreshTask(newer, older)).toBe(newer); // 旧快照到达被拦截
    expect(preferFreshTask(undefined, newer)).toBe(newer);
  });

  it('leaderPendingRequest：未提供=undefined、无=null、有=视图对象', () => {
    expect(leaderPendingRequest(makeLeader())).toBeUndefined();
    expect(leaderPendingRequest(makeLeader({pendingRequest: null}))).toBeNull();
    const pending = leaderPendingRequest(makeLeader({
      pendingRequest: {
        id: 'req-1', kind: 'business', prompt: '采用哪个地区？',
        options: [{value: 'north', label: '北区'}, {bad: true}],
        deadlineAt: null, status: 'pending',
        requestDigest: 'sha256:abc', replyDigest: null, authorization: null,
      },
    }));
    expect(pending?.id).toBe('req-1');
    expect(pending?.kind).toBe('business');
    expect(pending?.options).toEqual([{value: 'north', label: '北区'}]); // 畸形选项被过滤
  });

  it('questionStage：nodeId 非空为运行问题；缺字段不猜测', () => {
    expect(questionStage(makeQuestion()).nodeId).toBeNull();
    expect(questionStage(Object.assign(makeQuestion(), {nodeId: 'east', kind: 'clarification'})).nodeId).toBe('east');
    expect(questionStage(Object.assign(makeQuestion(), {kind: 'clarification'})).kind).toBe('clarification');
  });
});
