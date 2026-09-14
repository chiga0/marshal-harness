import test from 'node:test';
import assert from 'node:assert/strict';
import {createReviewPort, parseManagedOutput, renderReviewPrompt} from '../task-application/application.mjs';
import {receipt} from '../task-application/leader-ports.mjs';
import {parseReviewProposal, renderReviewProposalPrompt, createGenericFilesReviewWireConfig, REVIEW_WIRE_PROFILE} from './review-wire.mjs';
import {createGenericFilesShortWireConfig} from './short-wire.mjs';
const hash='sha256:'+'a'.repeat(64), selectionHash='sha256:'+'b'.repeat(64);
const input={profile:'task-independent-review/v1',inputDigest:hash,selectionDigest:selectionHash,selection:[{nodeId:'author'}],materials:[{content:'original candidate'}]};
const ticket={executionType:'review',providerId:'controlled',input:{review:input}};
const wire={profile:REVIEW_WIRE_PROFILE,verdict:'accept',summary:'独立阅读后的判断',findings:[]};
async function consume(output,{parser=parseReviewProposal,original=ticket,raw={},providerId='controlled'}={}) {
  const port=createReviewPort({id:'review-wire-test',providerId:'controlled',policy:{id:'test',version:'1',description:'测试'},prepare:()=>({prompt:'test'}),parseReport:parser});
  const started={executionId:'execution-test',startedAt:new Date().toISOString()};
  const provider={id:providerId,start(){return {started:Promise.resolve(started),stop(){},completion:Promise.resolve({providerId:'controlled',status:'completed',stopReason:'end_turn',
    outputText:output,cleanup:{started,cleaned:true,scope:'controlled-fixture'},...raw})};}};
  const result=await port.start({ticket:original,provider,prepared:{prompt:'test'}}).completion;
  return {port,result,data:receipt(port,original,result)};
}
test('review transport bindings come from only original ticket and business judgment is unchanged',async()=>{
  const before=structuredClone(ticket),output=JSON.stringify(wire),{data}=await consume(output);
  assert.deepEqual(data.value,{...wire,profile:input.profile,inputDigest:hash,selectionDigest:selectionHash});
  assert.deepEqual(ticket,before);assert.equal(output,JSON.stringify(wire));
  const other=structuredClone(ticket);other.input.review.inputDigest='sha256:'+'c'.repeat(64);
  assert.equal((await consume(output,{original:other})).data.value.inputDigest,other.input.review.inputDigest);
  const finding={id:'finding-1',nodeIds:['author'],requirement:'应保留原数据',observation:'候选漏项',requestedChange:'补齐缺项'};
  assert.deepEqual((await consume(JSON.stringify({...wire,verdict:'rework',findings:[finding]}))).data.value.findings,[finding]);
});
test('strict new wire rejects repairs, ambiguous JSON and invalid business shape',async()=>{
  const old={...wire,profile:input.profile,inputDigest:hash,selectionDigest:selectionHash};
  const finding={id:'finding-1',nodeIds:['other'],requirement:'需求',observation:'证据',requestedChange:'修正'};
  for(const value of [old,{...wire,inputDigest:hash},{...wire,selectionDigest:selectionHash},{...wire,extra:1},{...wire,profile:'unknown'},
    {...wire,verdict:'approve'},{...wire,summary:'x'.repeat(4097)},{...wire,findings:[finding]},{...wire,verdict:'rework',findings:[finding]},
    {...wire,summary:'\ud800'},{...wire,summary:'\0'}]) assert.equal((await consume(JSON.stringify(value))).result.status,'failed');
  for(const output of ['\ufeff'+JSON.stringify(wire),JSON.stringify(wire).replace('"summary":','"summary":"duplicate","summary":'),JSON.stringify({...wire,summary:'x'.repeat(65537)})])
    assert.equal((await consume(output)).result.status,'failed');
  assert.equal((await consume(JSON.stringify({...old,inputDigest:'sha256:'+'f'.repeat(64)}),{parser:parseManagedOutput})).result.status,'failed');
  assert.equal((await consume(JSON.stringify(wire),{parser:parseManagedOutput})).result.status,'failed');
});
test('mapper cannot make failed, cancelled, incomplete cleanup or wrong provider authoritative',async()=>{
  for(const raw of [{status:'failed'},{status:'unknown'},{status:'cancelled'},{stopReason:'cancelled'},{cleanup:{started:null,cleaned:false}}])
    assert.equal((await consume(JSON.stringify(wire),{raw})).result.status,'failed');
  await assert.rejects(consume(JSON.stringify(wire),{providerId:'foreign'}));
  await assert.rejects(consume(JSON.stringify(wire),{raw:{providerId:'foreign'}}));
  const {port,result}=await consume(JSON.stringify(wire));
  assert.throws(()=>receipt(port,{...ticket,taskId:'foreign'},result));
  assert.throws(()=>parseReviewProposal({ticket:{...ticket,executionType:'leader'},completion:{outputText:JSON.stringify(wire)}}));
});
test('new review prompt contains exact complete input and only four output fields; legacy is unchanged',()=>{
  const prompt=renderReviewProposalPrompt(input),example=JSON.parse(prompt.split('\n返回结构：')[1].split('\n完整冻结输入：')[0]);
  assert.deepEqual(Object.keys(example).sort(),['findings','profile','summary','verdict']);
  assert.ok(prompt.endsWith(JSON.stringify(input)));assert.ok(renderReviewPrompt(input).includes('profile/inputDigest/selectionDigest/verdict/summary/findings'));
});
test('new configuration freezes a distinct policy and explicit prompt retention without changing old profiles',()=>{
  const provider={id:'controlled',start(){throw Error('no model');}},old=createGenericFilesShortWireConfig({provider}),modern=createGenericFilesReviewWireConfig({provider});
  assert.notEqual(old.review.id,modern.review.id);assert.notEqual(old.review.policyDigest,modern.review.policyDigest);
  assert.notEqual(old.leader.policyDigest,modern.leader.policyDigest);
  assert.equal(modern.review.policyDigest,createGenericFilesReviewWireConfig({provider}).review.policyDigest);
  assert.deepEqual(modern.observability,{profile:'task-observation/v1',retainPrompts:true});assert.equal(old.observability,undefined);assert.equal(modern.publication,null);
});
