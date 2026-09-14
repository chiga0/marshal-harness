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
