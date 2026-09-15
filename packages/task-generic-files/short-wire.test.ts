import test from 'node:test';
import assert from 'node:assert/strict';
import {createLeaderPort, parseManagedOutput, renderLeaderPrompt} from '../task-application/application.mjs';
import {receipt} from '../task-application/leader-ports.mjs';
import {parseLeaderProposal, createGenericFilesShortWireConfig, LEADER_WIRE_PROFILE} from './short-wire.mjs';
import {createGenericFilesConfig} from './index.mjs';
const hash='sha256:'+'a'.repeat(64);
const policy={profile:'task-managed-leader/v1',maxCalls:3,maxActions:1,maxRequests:1,
  repair:{nodeIds:[],maxRounds:0},review:{providerId:'controlled',policyDigest:hash},publication:null};
const input={profile:policy.profile,callId:'call-original',inputDigest:hash,snapshot:{policy,evidence:[],readSet:[]}};
const ticket={executionType:'leader',providerId:'controlled',input:{leader:input}};
const action={type:'conclude',outcome:'failed',summary:'本次无法完成',basisDigests:[]};
const wire={profile:LEADER_WIRE_PROFILE,summary:'本次业务决定',actions:[action]};
async function consume(output, parser=parseLeaderProposal) {
  const port=createLeaderPort({id:'short-test',providerId:'controlled',policy,prepare:()=>({prompt:'test'}),parseDecision:parser});
  const started={executionId:'controlled-test',startedAt:new Date().toISOString()};
  const provider={id:'controlled',start(){return {started:Promise.resolve(started),stop(){},completion:Promise.resolve({providerId:'controlled',status:'completed',stopReason:'end_turn',
    outputText:output,cleanup:{started,cleaned:true,scope:'controlled-fixture'}})};}};
  const result=await port.start({ticket,provider,prepared:{prompt:'test'}}).completion;
  return {result,data:receipt(port,ticket,result)};
}
test('strict proposal binds only original ticket and preserves raw versus mapped action bytes',async()=>{
  const output=JSON.stringify(wire), before=structuredClone(ticket);
  const {result,data}=await consume(output);
  assert.equal(result.status,'completed');assert.deepEqual(ticket,before);
  assert.deepEqual(data.value,{profile:policy.profile,callId:input.callId,inputDigest:input.inputDigest,summary:wire.summary,actions:wire.actions});
  assert.equal(output,JSON.stringify(wire));assert.equal(Object.hasOwn(JSON.parse(output),'callId'),false);
});
test('both formats are explicit; wrong old binding and wire extras cannot be repaired',async()=>{
  const old={profile:policy.profile,callId:'foreign',inputDigest:hash,summary:wire.summary,actions:[action]};
  for(const value of [old,{...wire,callId:input.callId},{...wire,inputDigest:hash},{...wire,extra:1},{...wire,profile:'unknown'},
    {...wire,actions:[{...action,basisDigests:['wrong']}]},{...wire,actions:[]},{...wire,summary:'x'.repeat(4097)}]) {
    assert.equal((await consume(JSON.stringify(value))).result.status,'failed');
  }
  assert.equal((await consume(JSON.stringify(old),parseManagedOutput)).result.status,'failed');
  assert.equal((await consume(JSON.stringify(wire),parseManagedOutput)).result.status,'failed');
  for(const output of ['\ufeff'+JSON.stringify(wire),JSON.stringify(wire).replace('"summary":','"summary":"duplicate","summary":'),
    JSON.stringify({...wire,summary:'x'.repeat(65537)})])assert.equal((await consume(output)).result.status,'failed');
});
test('renderer selects exact short examples without changing complete frozen input or legacy default',()=>{
  const prompt=renderLeaderPrompt(input,{wireProfile:LEADER_WIRE_PROFILE});
  const examples=prompt.split('\n独立返回示例：')[1].split('\n完整冻结输入：')[0];
  for(const block of examples.split('\n示例名称（不是返回字段）：').slice(1)){
    const value=JSON.parse(block.slice(block.indexOf('\n')+1));
    if(typeof value==='object')assert.deepEqual(Object.keys(value).sort(),['actions','profile','summary']);
  }
  assert.ok(prompt.endsWith(JSON.stringify(input)));assert.ok(!prompt.includes('回显 profile/callId/inputDigest'));
  assert.ok(renderLeaderPrompt(input).includes('返回顶层必须且只能是profile、callId、inputDigest、summary、actions五个字段'));
  assert.throws(()=>renderLeaderPrompt(input,{wireProfile:'unknown'}));
});
test('short configuration freezes distinct implementation and policy identity, no publication or default replacement',()=>{
  const provider={id:'controlled',start(){throw Error('no model');}};
  const original=createGenericFilesConfig({provider}), short=createGenericFilesShortWireConfig({provider}), same=createGenericFilesShortWireConfig({provider});
  assert.notEqual(original.leader.id,short.leader.id);assert.notEqual(original.leader.policyDigest,short.leader.policyDigest);
  assert.equal(short.leader.policyDigest,same.leader.policyDigest);assert.equal(short.publication,null);
  assert.equal(original.leader.id,'generic-files-leader');
});
