import test from 'node:test';
import assert from 'node:assert/strict';
import {createReviewPort, parseManagedOutput, renderReviewPrompt} from '../task-application/application.mjs';
import {receipt, configuration} from '../task-application/leader-ports.mjs';
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

test('独立 managed Provider 仅绑定 Leader/Review，作者默认仍为文件 Provider',async()=>{
  const provider={id:'files',start(){throw Error('author must not start here');}};
  let calls=0;
  const managedProvider={id:'managed',start(){calls++;const started={executionId:'managed-execution',startedAt:new Date().toISOString()};return {
    started:Promise.resolve(started),stop(){},completion:Promise.resolve({providerId:'managed',status:'completed',stopReason:'end_turn',outputText:JSON.stringify(wire),cleanup:{started,cleaned:true}})};}};
  const config=createGenericFilesReviewWireConfig({provider,managedProvider});
  assert.deepEqual([...config.providers.keys()],['files','managed']);
  assert.equal(config.providers.get('managed'),managedProvider);
  assert.equal(config.leader.providerId,'managed');assert.equal(config.review.providerId,'managed');
  assert.equal(configuration(config.leader,'leader').policy.review.providerId,'managed');
  const bound={...ticket,providerId:'managed'};
  const result=await config.review.start({ticket:bound,provider:config.providers.get('managed'),prepared:{prompt:'complete review input'}}).completion;
  assert.equal(receipt(config.review,bound,result).value.verdict,'accept');assert.equal(calls,1);
  assert.throws(()=>config.review.start({ticket:bound,provider:config.providers.get('files'),prepared:{}}));
  assert.throws(()=>createGenericFilesReviewWireConfig({provider,managedProvider:{...managedProvider,id:'files'}}));
  const legacy=createGenericFilesReviewWireConfig({provider});
  assert.deepEqual([...legacy.providers.keys()],['files']);assert.equal(legacy.leader.providerId,'files');
});

test('Qwen 新组合让 managed 原生目录排除全部文件工具，旧作者参数原样保留',async()=>{
  const {QWEN_FILE_ARGS,QWEN_FILE_TOOLS,QWEN_EXCLUDED_TOOLS}=await import('./qwen-file-tools.mjs');
  const saved=process.env.MARSHAL_AGENT_EXECUTABLE;
  try {
    process.env.MARSHAL_AGENT_EXECUTABLE='/unused/qwen';
    const {default:config,QWEN_MANAGED_ARGS}=await import('./qwen-review-service-config.mjs');
    assert.deepEqual([...config.providers.keys()],['qwen-acp','qwen-managed-acp']);
    assert.equal(config.leader.providerId,'qwen-managed-acp');assert.equal(config.review.providerId,'qwen-managed-acp');
    const deny=QWEN_MANAGED_ARGS[QWEN_MANAGED_ARGS.indexOf('--exclude-tools')+1].split(',');
    assert.deepEqual(deny,[...QWEN_EXCLUDED_TOOLS,...QWEN_FILE_TOOLS]);
    assert.equal(QWEN_MANAGED_ARGS[QWEN_MANAGED_ARGS.indexOf('--core-tools')+1],QWEN_FILE_TOOLS.join(','));
    assert.equal(QWEN_MANAGED_ARGS[QWEN_MANAGED_ARGS.indexOf('--approval-mode')+1],'default');
    assert.ok(Object.isFrozen(QWEN_MANAGED_ARGS));
    assert.equal(QWEN_FILE_ARGS[QWEN_FILE_ARGS.indexOf('--exclude-tools')+1],QWEN_EXCLUDED_TOOLS.join(','));
  } finally {if(saved===undefined)delete process.env.MARSHAL_AGENT_EXECUTABLE;else process.env.MARSHAL_AGENT_EXECUTABLE=saved;}
});
