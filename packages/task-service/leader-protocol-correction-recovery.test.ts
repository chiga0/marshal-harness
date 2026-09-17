import test from 'node:test';
import assert from 'node:assert/strict';
import {fixture} from '../task-application/leader.fixture.ts';
import {bound} from './leader-recovery-core.test.ts';

for(const started of [false,true])test('format successor with signed original cleanup recovers only through original recovery; started='+started,{timeout:20000},async t=>{
 const f=fixture(t,{protocolCorrection:true});const task=await f.call({operation:'task.create',key:'create',body:{intent:'有界格式恢复',limits:{timeoutMs:120000,maxAttempts:20,maxWorkers:3}}});
 const first=f.take('leader');await f.rawDecision(first,'{"actions":[}');const second=f.take('leader'),observation=await bound(f,second,started);
 const before=f.read(tx=>f.app.get(tx,task.id));assert.equal(before.attempts,2);assert.equal(before.leader.protocolCorrection.used,1);
 f.reopen();f.app.execution.reconcileCleanup(second.workerId,observation);f.app.leader.recover(task.id);
 assert.equal(f.read(tx=>f.app.get(tx,task.id)).attempts,2);
 const third=f.take('leader');assert.notEqual(third.workerId,second.workerId);assert.equal(third.deadline,second.deadline);
 assert.equal(third.input.leader.snapshot.protocolCorrection.used,1);
 assert.equal(third.input.leader.snapshot.protocolCorrection.successorWorkerId,second.workerId,'format successor relation must not be rewritten as ordinary crash recovery');
 await f.rawDecision(third,'{"actions":[}');const after=f.read(tx=>f.app.get(tx,task.id));
 assert.equal(after.attempts,3);assert.equal(after.leader.protocolCorrection.used,1);assert.equal(after.leader.protocolCorrection.original.workerId,first.workerId);assert.equal(after.task.status,'cancelling');
 assert.equal(f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&JSON.parse(c.payload).action==='leader').length),0);
});
