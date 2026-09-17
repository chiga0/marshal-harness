import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {fixture} from './leader.fixture.ts';
import {publicOutput} from './leader-quotes.fixture.ts';

test('真实漏转义引号与多语言同形缺陷仅消费一次原纠错预算',async t=>{
  assert.equal('sha256:'+createHash('sha256').update(publicOutput).digest('hex'),'sha256:9d33a259b024bfebecd828de7048e0e92ce0d67680b3892935dd01bce2753688');
  for(const text of [publicOutput,'{"summary":"title "Reading Club" today"}','{"summary":"titre "Café lecture" ici"}'])await t.test(text.slice(0,35),async t=>{
    const f=await fixture(t,{protocolCorrection:true});const task=await f.call({operation:'task.create',key:'create',body:{intent:'原目标',limits:{timeoutMs:60000,maxAttempts:20,maxWorkers:3}}});
    const first=await f.take('leader');await f.rawDecision(first,text);
    const view=await f.call({operation:'task.leader',taskId:task.id});assert.equal(view.protocolCorrection.used,1);assert.equal(view.protocolCorrection.original.workerId,first.workerId);
    assert.equal(await f.read(tx=>f.app.execution.worker(tx,first.workerId).record).worker.status,'failed');
    const next=await f.take('leader');await f.rawDecision(next,text);assert.equal((await f.call({operation:'task.leader',taskId:task.id})).protocolCorrection.used,1);
    assert.equal(await f.read(tx=>tx.commands().filter(c=>c.status==='pending'&&JSON.parse(c.payload).action==='leader').length),0);
  });
});

test('漏引号混合硬错误与不明确词法仍拒绝纠错，不截断或补全',async t=>{
  const gap='"summary":"title "Reading Club" today"';
  for(const text of [
    `{${gap},"x":1,"x":2}`,`{${gap},"x":1,"\\u0078":2}`,`{${gap},"x":1e999}`,`{${gap},"x":NaN}`,`{${gap},"x":Infinity}`,
    `{${gap},"x":${'['.repeat(33)}0${']'.repeat(33)}}`,`{${gap},"x":"\\ud800"}`,`{${gap},"x":"\\q"}`,
    `{${gap}} trailing`,`{${gap}} {}`,`\ufeff{${gap}}`,`{${gap},"x":"\u0001"}`,
    '{"s":"title "Infinity" end"}','{"s":"title "NaN" end"}','{"s":"title "word":1}','{"key" word "other"}',
    '{"s":"title "word-with-punctuation" end"}',`{"s":"title "${'a'.repeat(129)}" end"}`,
  ])await t.test(text.slice(0,60),async t=>{
    const f=await fixture(t,{protocolCorrection:true});const task=await f.call({operation:'task.create',key:'create',body:{intent:'原目标',limits:{timeoutMs:60000,maxAttempts:20,maxWorkers:3}}});
    await f.rawDecision(await f.take('leader'),text);assert.equal((await f.call({operation:'task.leader',taskId:task.id})).protocolCorrection.used,0);
  });
});
