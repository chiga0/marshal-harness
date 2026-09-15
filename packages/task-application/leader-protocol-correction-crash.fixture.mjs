import {fixture} from './leader.fixture.mjs';
const phase=process.argv[2],f=fixture({after(){}},{protocolCorrection:true});
const task=await f.call({operation:'task.create',key:'create',body:{intent:'纠错崩溃恢复',limits:{timeoutMs:120000,maxAttempts:20,maxWorkers:3}}});
const first=f.take('leader');
await new Promise(resolve=>process.send({parent:f.parent,taskId:task.id,first},resolve));
const crash=()=>process.kill(process.pid,'SIGKILL');
if(phase==='before-finish')f.app.leader.finish=crash;
if(phase==='inside-commit'){
 const original=f.app.transaction.bind(f.app);let finishing=false;const finish=f.app.leader.finish.bind(f.app.leader);
 f.app.leader.finish=(...args)=>{finishing=true;return finish(...args);};
 f.app.transaction=(write,callback)=>original(write,tx=>{const result=callback(tx);if(write&&finishing)crash();return result;});
}
await f.rawDecision(first,'{"profile":"task-managed-leader/v1","actions":[}');
if(phase==='after-commit')crash();
const second=f.take('leader');
await new Promise(resolve=>process.send({second},resolve));
if(phase==='claimed')crash();
f.app.execution.started(second,{executionId:'controlled-start-'+second.workerId,startedAt:new Date().toISOString()});
crash();
