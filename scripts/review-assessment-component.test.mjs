import test from 'node:test';
import assert from 'node:assert/strict';
import {syntheticInput,evaluate} from './review-assessment-component.mjs';
import * as ports from '../packages/task-application/leader-ports.mjs';
import * as contract from '../packages/task-application/review-assessment-contract.mjs';
import * as adapter from '../packages/task-application/review-assessment.mjs';
import * as normalization from '../packages/agent-observation/normalization.mjs';
import {bindGenericFilesPlan} from '../packages/task-generic-files/layout.mjs';
import {assessmentFixture,assessmentProposal} from '../packages/task-application/review-assessment.fixture.mjs';
const api={...ports,...contract,...adapter,...normalization,bindGenericFilesPlan};
function setup(input,options={}) {
  let starts=0,stops=0,actual;
  const port=ports.createReviewPort({id:'review',providerId:'qwen-managed-acp',policy:{id:'test',version:'1',description:'test'},
    prepare:()=>{if(options.prepareFail)throw Error('secret');return {prompt:JSON.stringify(input)};},parseReport:args=>contract.parseAssessmentProposal(args).report});
  adapter.registerReviewAssessments(port,{profile:'task-review-assessment/v1'});
  const provider={id:'qwen-managed-acp',start(value){starts++;actual=value;
    const started={executionId:'controlled',startedAt:new Date().toISOString()};
    if(options.progress)value.onProgress?.(options.progress);if(options.permission)value.onPermission?.({secret:'never retained'});
    return {started:Promise.resolve(started),stop(){stops++;return options.hang?new Promise(()=>{}):Promise.resolve();},
      completion:options.hang?new Promise(()=>{}):Promise.resolve({providerId:'qwen-managed-acp',status:'completed',stopReason:'end_turn',
        outputText:options.invalid?'broken':JSON.stringify(assessmentProposal({input:{review:input}})),cleanup:{started,cleaned:true,scope:'fixture'}})};}};
  return {config:{review:port,providers:new Map([[provider.id,provider]])},get starts(){return starts;},get stops(){return stops;},get actual(){return actual;}};
}
test('original one-start receipt and private assessment are required, without labels or synthetic side metadata',async t=>{
  const f=await assessmentFixture(t),input=f.reviewTicket.input.review,setupCase=setup(input);
  const result=await evaluate({config:setupCase.config,api,input,cwd:f.parent});
  assert.equal(result.valid,true);assert.equal(result.report.verdict,'accept');assert.ok(result.assessment);
  assert.equal(setupCase.starts,1);assert.equal(setupCase.stops,1);assert.equal(setupCase.actual.prompt,JSON.stringify(input));
  assert.equal(result.rawPublic.altered,false);
});
test('invalid report, prepare failure, missing side evidence never become acceptance',async t=>{
  const f=await assessmentFixture(t),input=f.reviewTicket.input.review;
  for(const options of [{invalid:true},{prepareFail:true},{}]) {
    const fixture=setup(input,options),runtime=Object.keys(options).length?api:{...api,reviewAssessmentEvidence:()=>null};
    const result=await evaluate({config:fixture.config,api:runtime,input,cwd:f.parent});
    assert.equal(result.valid,false);assert.equal(fixture.starts,options.prepareFail?0:1);
    if(options.prepareFail)assert.equal(result.cleanupStatus,'not-started');
  }
});
test('deadline plus independent cleanup bound retains initial failure and cannot hang the suite',async t=>{
  const f=await assessmentFixture(t),input=f.reviewTicket.input.review,fixture=setup(input,{hang:true}),start=Date.now();
  const result=await evaluate({config:fixture.config,api,input,cwd:f.parent,deadlineMs:20,cleanupMs:20});
  assert.equal(result.problem,'component_deadline');assert.equal(result.cleaned,false);assert.ok(Date.now()-start<1000);assert.equal(fixture.starts,1);
});
test('synthetic input rejects unknown acceptance instead of silently dropping a business requirement',async t=>{
  const f=await assessmentFixture(t),input=f.reviewTicket.input.review;
  // This fixture has a criteria directory already and is deliberately NOT a reviewed historical suite.
  assert.throws(()=>syntheticInput({input,derived:false},api));
});
test('historical business requirements remain byte-for-byte; new directory binds only a new synthetic plan/readset',async t=>{
  const f=await assessmentFixture(t),input=structuredClone(f.reviewTicket.input.review),plan=input.snapshot.plan;
  const business=['保留原始要求，不能减少。','允许说明两种方案。'];
  const layout=bindGenericFilesPlan({inputArtifacts:input.snapshot.task.inputArtifacts,proposal:plan});
  const sort=(values,key)=>[...values].sort((a,b)=>a[key]<b[key]?-1:1),policy=input.snapshot.policy;
  const repair=policy.repair.scope==='plan-authors'?{...policy.repair,nodeIds:plan.nodes.filter(n=>n.role==='author').map(n=>n.id).sort()}:policy.repair;
  plan.acceptance=[...business,JSON.stringify({policy:{id:'generic-files-check',version:'1',description:'原冻结源码身份'},description:layout.description}),
    ...sort(layout.layouts,'nodeId').map(layout=>JSON.stringify({layout:{...layout,inputs:sort(layout.inputs,'path')}})),
    ...sort(layout.deliveries,'targetPath').map(delivery=>JSON.stringify({delivery})),JSON.stringify({profile:'task-managed-leader/v1',policyDigest:api.hash(policy),repair,review:policy.review,publication:policy.publication,completion:'leader-delivery'})];
  const previous=plan.digest;delete plan.digest;plan.digest=api.hash(plan);
  for(const ref of input.snapshot.readSet)if(ref.kind==='plan'){assert.equal(ref.digest,previous);ref.digest=plan.digest;}
  delete input.inputDigest;input.inputDigest=api.hash(input);
  const before=structuredClone(input),output=syntheticInput({input,derived:false},api);
  assert.deepEqual(input,before);assert.deepEqual(output.provenance.businessAcceptance,business);
  assert.deepEqual(output.input.materials,input.materials);assert.deepEqual(output.input.selection,input.selection);
  assert.notEqual(output.input.snapshot.plan.digest,input.snapshot.plan.digest);assert.notEqual(output.input.inputDigest,input.inputDigest);
  assert.equal(output.input.snapshot.readSet.find(ref=>ref.kind==='plan').digest,output.input.snapshot.plan.digest);
  assert.equal(api.reviewCriteria(output.input.snapshot.plan).length,business.length+4);
  input.snapshot.plan.acceptance.push('{"unknown":"业务条款不能自动删除"}');delete input.inputDigest;input.inputDigest=api.hash(input);
  assert.throws(()=>syntheticInput({input,derived:false},api));
});

test('known retry, tool or permission activity cannot count as a single no-tool baseline',async t=>{
  const f=await assessmentFixture(t),input=f.reviewTicket.input.review;
  for(const options of [{progress:{activity:'retrying'}},{progress:{tool:{id:'tool'}}},{permission:true}]){
    const fixture=setup(input,options),result=await evaluate({config:fixture.config,api,input,cwd:f.parent});
    assert.equal(result.extraBehavior,true);assert.equal(result.valid,false);assert.equal(result.cleaned,true);assert.equal(fixture.starts,1);
    assert.ok(!JSON.stringify(result).includes('never retained'));
  }
});
