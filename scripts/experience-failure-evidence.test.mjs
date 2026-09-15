import test from 'node:test';
import assert from 'node:assert/strict';
import {captureFailureProjections} from './experience-failure-evidence.mjs';

test('仅请求已知任务两项公开投影并保留 worker 诊断',async()=>{
  const routes=[];
  const audit={workers:[{id:'worker-1',observation:{diagnostic:{stage:'collect',code:'collection_failed'}}}]};
  const leader={review:null};
  const result=await captureFailureProjections({taskId:'task/a',api:async route=>{routes.push(route);return route.endsWith('/audit')?audit:leader;}});
  assert.deepEqual(routes,['/v1/tasks/task%2Fa/audit','/v1/tasks/task%2Fa/leader']);
  assert.deepEqual(result,{status:'complete',audit:{status:'captured',projection:audit},leader:{status:'captured',projection:leader}});
});

test('单项读取失败保留另一项，不保存异常正文且不抛出覆盖原失败',async()=>{
  const result=await captureFailureProjections({taskId:'task-1',api:route=>{
    if(route.endsWith('/audit')) throw new Error('native private body or credential');
    return Promise.resolve({status:'failed'});
  }});
  assert.equal(result.status,'partial');
  assert.deepEqual(result.audit,{status:'failed',code:'public_projection_read_failed'});
  assert.deepEqual(result.leader,{status:'captured',projection:{status:'failed'}});
  assert.doesNotMatch(JSON.stringify(result),/credential|private body/);
});

test('两项读取失败明确标记，不回退到原生日志',async()=>{
  let calls=0;
  const result=await captureFailureProjections({taskId:'task-1',api:async()=>{calls++;throw new Error('timeout');}});
  assert.equal(calls,2);assert.equal(result.status,'failed');
  assert.equal(result.audit.status,'failed');assert.equal(result.leader.status,'failed');
});

test('创建任务或连接前失败不发请求',async()=>{
  let calls=0;
  assert.equal((await captureFailureProjections({api:async()=>{calls++;}})).status,'skipped');
  assert.equal((await captureFailureProjections({taskId:'task-1'})).status,'skipped');
  assert.equal(calls,0);
});

test('只沿当前绑定Review读取正文，读取失败保留原投影并不吞原失败',async()=>{
 const leader={taskId:'task-one',review:{evidenceIds:['artifact-1']}};
 let seen;const base={taskId:'task-one',api:async route=>route.endsWith('/leader')?leader:{workers:[]}};
 const result=await captureFailureProjections({...base,readReview:async value=>{seen=value;return {envelope:{profile:'task-independent-review/v2'}};}});
 assert.equal(seen,leader);assert.equal(result.reviewEvidence.status,'captured');
 const failed=await captureFailureProjections({...base,readReview:async()=>{throw new Error('private credential');}});
 assert.deepEqual(failed.reviewEvidence,{status:'failed',code:'bound_review_read_failed'});assert.equal(failed.leader.status,'captured');assert.doesNotMatch(JSON.stringify(failed),/credential/);
 const absent=await captureFailureProjections({...base,api:async()=>({review:null}),readReview:async()=>assert.fail('无绑定不得猜artifact')});assert.equal(absent.reviewEvidence.status,'absent');
});
