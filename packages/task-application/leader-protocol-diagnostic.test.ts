import test from 'node:test';
import assert from 'node:assert/strict';
import {setImmediate as turn} from 'node:timers/promises';
import {fixture} from './leader.fixture.ts';
import {TaskExecutionCoordinator} from '../task-execution/controller.ts';

test('原Port解析诊断只改变显示，关闭/外来/迟到事件不伪造阶段',async t=>{
  for(const mode of ['json','shape','disabled','foreign','late','provider'])await t.test(mode,async t=>{
    const enabled=mode!=='disabled',f=await fixture(t,{observability:enabled?{profile:'task-observation/v1',retainPrompts:false}:null});
    const task=await f.call({operation:'task.create',key:'create',body:{intent:'原目标',limits:{timeoutMs:60000,maxAttempts:20,maxWorkers:3}}});
    let observed,callback,ticket;
    const provider={id:'fixture',start(options){console.log('[PROVIDER_START]', 'onProgress:', typeof options?.onProgress, 'onDiagnostic:', typeof options?.onDiagnostic);const fact={executionId:'fixed-provider',startedAt:new Date().toISOString()};
      const completion=(async()=>{await options.onProgress?.({phase:'running',tool:null,activity:'output',publicText:'完整公开片段',diagnostic:{stage:'protocol',code:'invalid_json',source:'controller'}});
        return {providerId:'fixture',status:mode==='provider'?'failed':'completed',stopReason:'end_turn',outputText:mode==='shape'?'{}':'{"s":}',cleanup:{started:fact,cleaned:true,scope:'controlled-fixture'}};})();
      return {started:Promise.resolve(fact),completion,stop:()=>completion};}};
    const coordinator=new TaskExecutionCoordinator({execution:f.app.execution,providers:new Map([['fixture',provider]]),observability:enabled,
      prepare:async()=>({cwd:f.parent,prompt:'fixed'}),collect:async()=>({}),
      managed:{prepare:async()=>({cwd:f.parent,prompt:'fixed'}),validate(){},provider:()=>provider,start(options){
        ticket=options.ticket;callback=options.onDiagnostic;
        return f.leaderPort.start({...options,provider,onDiagnostic:report=>{observed=report;
          if(mode==='foreign')callback({...report,workerId:'foreign'});else if(mode!=='late')callback(report);}});
      }}});
    t.after(()=>coordinator.close());
    for(let i=0;i<500;i++){await coordinator.tick();await turn();if(ticket&&!coordinator.snapshot().owned.length)break;} await new Promise(r=>setImmediate(r)); await new Promise(r=>setImmediate(r));
    assert.ok(ticket);assert.equal(coordinator.snapshot().owned.length,0);
    const get=async()=>await f.read(tx=>f.app.execution.worker(tx,ticket.workerId).record.worker);const worker=await get();assert.equal(worker.status,'failed');
    if(!enabled)assert.equal(Object.hasOwn(worker,'observation'),false);
    else {
      const protocol=['json','shape'].includes(mode);
      assert.deepEqual(worker.observation.diagnostic,{stage:protocol?'protocol':'provider',code:mode==='json'?'invalid_json':mode==='shape'?'invalid_leader_decision':'provider_failed',source:'controller'});
      assert.ok(worker.observation.history.some(frame=>frame.publicText==='完整公开片段'));
      assert.equal(worker.observation.history.some(frame=>frame.publicText==='完整公开片段'&&frame.diagnostic?.stage==='protocol'),false);
      if(mode==='late'){const before=structuredClone(worker);callback(observed);assert.deepEqual(await get(),before);}
    }
    assert.equal((await f.call({operation:'task.leader',taskId:task.id})).protocolCorrection,undefined);
  });
});
