import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture,proposal} from './leader.fixture.mjs';
import {startLeaderWithJsonCorrection,leaderJsonFailure,prepareLeaderWithJsonCorrection} from './leader-protocol-correction.mjs';
import {validate} from '../task-api/contract.mjs';
const malformed='{"profile":"task-managed-leader/v1","actions":[}';
const create=f=>f.call({operation:'task.create',key:'create',body:{intent:'交付已知结果',limits:{timeoutMs:60000,maxAttempts:20,maxWorkers:3}}});
const view=(f,id)=>f.call({operation:'task.leader',taskId:id});

test('syntax rejection retains failed outcome and creates one budgeted successor; duplicate finish and cold reopen do not duplicate',async t=>{
 const f=fixture(t,{protocolCorrection:true}),task=await create(f),first=f.take('leader');
 assert.equal((await f.rawDecision(first,malformed)).status,'failed');
 let v=await view(f,task.id);assert.equal(validate(v,'LeaderView'),true);assert.equal(v.protocolCorrection.used,1);assert.equal(v.protocolCorrection.original.workerId,first.workerId);
 assert.equal((await f.get(task.id)).status,'running');assert.equal(v.protocolCorrection.successorWorkerId,null);
 const count=()=>f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&JSON.parse(c.payload).action==='leader').length);
 assert.equal(count(),1);f.app.execution.finish(first,f.results.get(first.workerId));assert.equal(count(),1);
 f.reopen();f.app.leader.recoverUnreserved?.(task.id);
 const pending=f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&c.generation===f.app.owner.generation&&JSON.parse(c.payload).action==='leader'));
 // Original unreserved recovery translates a prior-generation pending command.
 if(!pending.length)f.app.execution.reconcile(task.id);
 const second=f.take('leader');assert.ok(second);assert.notEqual(second.workerId,first.workerId);assert.equal(second.deadline,first.deadline);
 v=await view(f,task.id);assert.equal(v.protocolCorrection.successorWorkerId,second.workerId);assert.equal(v.protocolCorrection.successorCallId,second.input.leader.callId);
 assert.equal((await f.decision(second,[{type:'plan',proposal}])).status,'completed');
 assert.equal((await f.get(task.id)).status,'awaiting-approval');
 assert.equal(f.read(tx=>f.app.get(tx,task.id)).attempts,2);assert.equal(f.read(tx=>f.app.get(tx,task.id)).leader.calls,2);
 assert.equal(f.read(tx=>f.app.execution.worker(tx,first.workerId).record).worker.status,'failed');
});

test('second syntax failure, closed profile, empty text and valid wrong shape never loop',async t=>{
 for(const mode of ['second','disabled','empty','shape'])await t.test(mode,async t=>{
  const f=fixture(t,{protocolCorrection:mode!=='disabled'}),task=await create(f);let ticket=f.take('leader');
  await f.rawDecision(ticket,mode==='empty'?'':mode==='shape'?'{}':malformed);
  if(mode==='second'){ticket=f.take('leader');await f.rawDecision(ticket,malformed);}
  const record=f.read(tx=>f.app.get(tx,task.id));assert.equal(record.task.status,'cancelling');
  assert.equal(record.leader.protocolCorrection?.used??0,mode==='second'?1:0);
  assert.equal(f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&JSON.parse(c.payload).action==='leader').length),0);
 });
});

async function originalResult(f,ticket,extra={}) {
 const fact={executionId:'fixture-'+ticket.workerId,startedAt:new Date().toISOString()};
 const provider={id:'fixture',start(options){
  const completion=(async()=>{
   if(extra.permissionDiagnostic)await options.onProgress?.({phase:'running',tool:null,diagnostic:{stage:'permission',code:'permission_denied',source:'provider-permission'}});
   if(extra.tool)await options.onProgress?.({phase:'running',tool:{id:'write:0',kind:'edit',status:'pending'}});
   if(extra.permission)await options.onPermission?.({toolCall:{},options:[]},{signal:new AbortController().signal});
   return {providerId:'fixture',status:extra.status??'completed',stopReason:extra.stopReason??'end_turn',outputText:extra.text??malformed,
    cleanup:{started:fact,cleaned:extra.clean!==false,scope:'controlled-fixture'}};
  })();return {started:Promise.resolve(fact),completion,stop:()=>completion};}};
 const handle=startLeaderWithJsonCorrection(f.leaderPort,{ticket,provider,prepared:{cwd:f.parent,prompt:'original',...(extra.noPermissionCallback?{}:{onPermission:()=>({outcome:{outcome:'cancelled'}})})}});
 f.app.execution.started(ticket,await handle.started);return handle.completion;
}

test('known tools, permission request, empty text, suffix, duplicate-key, malformed receipt, unknown cleanup and wrong phase never become repair authority',async t=>{
 for(const mode of ['tool','permission','suffix','duplicate','unknown','provider-failed','receipt'])await t.test(mode,async t=>{
  const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader');
  const result=await originalResult(f,ticket,{[mode]:true,...(mode==='suffix'?{text:'{} trailing'}:{}),...(mode==='duplicate'?{text:'{"x":1,"x":2}'}:{}),...(mode==='unknown'?{clean:false}:{}),...(mode==='provider-failed'?{status:'failed'}:{})});
  if(mode==='receipt'){
   assert.throws(()=>leaderJsonFailure(f.leaderPort,ticket,{...result,receipt:{}}));
   assert.throws(()=>leaderJsonFailure(f.leaderPort,{...ticket,workerId:'foreign'},result));
   return;
  }
  f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,0);
 });
});

test('budget insufficient, cancellation, currentness change and expired task refuse successor without changing authority gates',async t=>{
 for(const mode of ['budget','cancel','stale','expired'])await t.test(mode,async t=>{
  const f=fixture(t,{protocolCorrection:true});
  const task=mode==='budget'?await f.call({operation:'task.create',key:'create',body:{intent:'limited',limits:{timeoutMs:60000,maxAttempts:8,maxWorkers:3}}}):await create(f);
  const ticket=f.take('leader'),result=await originalResult(f,ticket);
  if(mode==='cancel')await f.call({operation:'task.cancel',taskId:task.id,key:'cancel',body:{expectedRevision:(await f.get(task.id)).revision}});
  if(mode==='expired')f.app.clock=()=>ticket.deadline+1;
  if(mode==='stale'){
   // Controlled same-owner semantic change before the original completion.
   f.app.transaction(true,tx=>{const value=f.app.get(tx,task.id);value.input.intent='new semantic input';value.inputDigest='sha256:'+'f'.repeat(64);value.task.revision++;f.app.save(tx,value,'fixture.changed',{});});
  }
  f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,0);
 });
});


test('mixed syntax errors never bypass duplicate, depth, numeric, encoding or envelope exclusion',async t=>{
 const cases={duplicate:'{"x":1,"x":2,}',escapedDuplicate:'{"x":1,"\\u0078":2,}',depth:'{"x":'+ '['.repeat(33)+'0'+']'.repeat(33)+',}',nonfinite:'{"x":1e999,}',bom:'\ufeff{"x":,}',control:'{"x":\u0001,}',suffix:'{"x":[} trailing',newRoot:'{"x":[} {}',badString:'{"x":"\\uZZZZ",}',unpaired:'{"x":"\\ud800",}'};
 for(const [name,text]of Object.entries(cases))await t.test(name,async t=>{const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader');const result=await originalResult(f,ticket,{text});assert.equal(leaderJsonFailure(f.leaderPort,ticket,result),null);f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,0);});
});
test('permission-only facts with no caller callback or no tool progress remain ineligible',async t=>{
 for(const extra of [{permission:true,noPermissionCallback:true},{permissionDiagnostic:true}])await t.test(JSON.stringify(extra),async t=>{const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader');const result=await originalResult(f,ticket,extra);assert.equal(leaderJsonFailure(f.leaderPort,ticket,result),null);f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,0);});
});
test('correction preparation is captured byte-for-byte before Provider start',async t=>{
 const f=fixture(t,{protocolCorrection:true,preparePrompt:'完整受控输入',observability:{profile:'task-observation/v1',retainPrompts:true}}),task=await create(f),first=f.take('leader');await f.rawDecision(first,malformed);const ticket=f.take('leader');
 const prepared=await prepareLeaderWithJsonCorrection(f.leaderPort,ticket,{cwd:f.parent},{});
 assert.match(prepared.prompt,/协议格式纠错/);
 f.app.execution.observeInput(ticket,'prepared',prepared.prompt);f.app.execution.observeInput(ticket,'handed-off');
 let actual;const fact={executionId:'exact-'+ticket.workerId,startedAt:new Date().toISOString()};
 const provider={id:'fixture',start(input){actual=input.prompt;return {started:Promise.resolve(fact),stop(){},completion:Promise.resolve({providerId:'fixture',status:'completed',stopReason:'end_turn',outputText:malformed,cleanup:{started:fact,cleaned:true}})};}};
 const handle=startLeaderWithJsonCorrection(f.leaderPort,{ticket,prepared,provider});await handle.completion;
 assert.equal(actual,prepared.prompt);
 const audit=await f.call({operation:'task.audit',taskId:task.id});const prompt=audit.prompts.find(item=>item.workerId===ticket.workerId);assert.ok(prompt);assert.equal(prompt.text,actual);
});

test('real SIGKILL before/inside/after finish commit and after claim/start never remints private classification or duplicates a handle',async t=>{
 const {fork}=await import('node:child_process');const {once}=await import('node:events');
 for(const phase of ['before-finish','inside-commit','after-commit','claimed','started'])await t.test(phase,async t=>{
  const child=fork(new URL('./leader-protocol-correction-crash.fixture.mjs',import.meta.url),[phase],{stdio:['ignore','ignore','ignore','ipc']});let state={};child.on('message',value=>Object.assign(state,value));
  const [,signal]=await once(child,'exit');assert.equal(signal,'SIGKILL');assert.ok(state.parent);
  const f=fixture(t,{protocolCorrection:true,existingParent:state.parent});
  let value=await view(f,state.taskId),record=f.read(tx=>f.app.get(tx,state.taskId));
  const committed=!['before-finish','inside-commit'].includes(phase);
  assert.equal(value.protocolCorrection.used,committed?1:0);
  assert.equal(record.attempts,state.second?2:1);
  if(!committed){assert.equal(value.protocolCorrection.original,null);assert.equal(f.read(tx=>f.app.execution.worker(tx,state.first.workerId).record).protocolFailure,undefined);}
  else assert.equal(f.read(tx=>f.app.execution.worker(tx,state.first.workerId).record).worker.status,'failed');
  if(phase==='after-commit'){
   f.app.leader.recover(state.taskId);const next=f.take('leader');assert.notEqual(next.workerId,state.first.workerId);assert.equal(next.deadline,state.first.deadline);
   await f.decision(next,[{type:'plan',proposal}]);assert.equal((await f.get(state.taskId)).status,'awaiting-approval');
  }else{
   f.app.execution.reconcile(state.taskId);f.app.leader.recover(state.taskId);
   const snapshot=f.read(tx=>({head:tx.head(state.taskId),attempts:f.app.get(tx,state.taskId).attempts}));
   f.app.execution.reconcile(state.taskId);f.app.leader.recover(state.taskId);
   assert.deepEqual(f.read(tx=>({head:tx.head(state.taskId),attempts:f.app.get(tx,state.taskId).attempts})),snapshot);
   assert.equal(f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&JSON.parse(c.payload).action==='leader').length),0);
   // No cleanup proof is invented from public diagnostic or a prior process's
   // lost receipt; the original unbound execution remains unresolved.
   assert.equal((await view(f,state.taskId)).protocolCorrection.used,committed?1:0);
  }
 });
});

test('Core action rejection cannot consume wire correction budget',async t=>{
 const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader');
 await f.decision(ticket,[{type:'conclude',outcome:'succeeded',summary:'尚无验收却声称完成',basisDigests:[]}]);
 assert.equal((await view(f,task.id)).protocolCorrection.used,0);assert.equal((await f.get(task.id)).status,'cancelling');
});

test('pending correction cancellation does not reserve a new attempt and does not refund budget',async t=>{
 const f=fixture(t,{protocolCorrection:true}),task=await create(f),first=f.take('leader');await f.rawDecision(first,malformed);
 await f.call({operation:'task.cancel',taskId:task.id,key:'cancel',body:{expectedRevision:(await f.get(task.id)).revision}});
 assert.equal(f.take('leader'),null);const record=f.read(tx=>f.app.get(tx,task.id));assert.equal(record.attempts,1);assert.equal(record.leader.protocolCorrection.used,1);assert.equal(record.leader.protocolCorrection.successorWorkerId,null);
});
test('unknown or stopping execution cannot gain syntax successor authority from a late valid receipt',async t=>{
 for(const status of ['unknown','stopping'])await t.test(status,async t=>{
  const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader'),result=await originalResult(f,ticket);
  f.app.transaction(true,tx=>{const {row,record}=f.app.execution.worker(tx,ticket.workerId),value=f.app.get(tx,task.id);record.worker.status=status;value.task.revision++;const source=f.app.save(tx,value,'fixture.execution-unresolved',{workerId:ticket.workerId});f.app.execution.putWorker(tx,row,record,source);});
  f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,0);
 });
});


test('observed real failure closing-bracket pattern remains eligible without extracting actions',async t=>{
 const f=fixture(t,{protocolCorrection:true}),task=await create(f),ticket=f.take('leader');
 const result=await originalResult(f,ticket,{text:'{"actions":[{"proposal":{"assumptions":[]}]}]}'});
 assert.equal(leaderJsonFailure(f.leaderPort,ticket,result)?.stage,'wire-json');f.app.execution.finish(ticket,result);assert.equal((await view(f,task.id)).protocolCorrection.used,1);
});
