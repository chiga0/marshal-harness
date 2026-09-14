import test from 'node:test';
import assert from 'node:assert/strict';
import {receipt} from '../task-application/leader-ports.mjs';
import {createGenericFilesReviewWireConfig, FACT_GROUNDING} from './review-wire.mjs';
import {bindGenericFilesPlan} from './layout.mjs';
import {groundingCases} from './grounding.fixture.mjs';
const hash='sha256:'+'a'.repeat(64);
const outputProvider = output => ({id:'controlled',start(){const started={executionId:'controlled-grounding',startedAt:new Date().toISOString()};return {started:Promise.resolve(started),stop(){},completion:Promise.resolve({providerId:'controlled',status:'completed',stopReason:'end_turn',outputText:JSON.stringify(output),cleanup:{started,cleaned:true,scope:'controlled-fixture'}})};}});

for(const sample of groundingCases)test('complete frozen evidence + original Review receipt: '+sample.id,async()=>{
  // Expected judgments are independent test fixtures, not a mock claimed to
  // understand prose. A real model semantic run is a separate acceptance line.
  const config=createGenericFilesReviewWireConfig({provider:outputProvider(null)});
  const input={profile:'task-independent-review/v1',inputDigest:hash,selectionDigest:hash,selection:[{nodeId:'writer'}],snapshot:{task:{input:{intent:sample.intent,context:'仅根据原始资料'}},plan:{nodes:[{id:'writer',role:'author',scope:['仅保留已知事实',FACT_GROUNDING]}],acceptance:[FACT_GROUNDING]},interactions:{replies:[{answer:'没有其他已确认的服务安排'}]}},materials:[{nodeId:'writer',content:sample.candidate},{inputId:'source-original',content:sample.intent}]};
  const ticket={executionType:'review',providerId:'controlled',input:{review:input}};
  const prepared=await config.review.prepare(ticket,{cwd:process.cwd()},{});
  assert.deepEqual(JSON.parse(prepared.prompt.split('\n完整冻结输入：').at(-1)),input,'full intent/scope/replies/source/candidate delivered intact');
  const findings=sample.verdict==='accept'?[]:[{id:sample.id,nodeIds:['writer'],requirement:sample.intent,observation:sample.observation,requestedChange:sample.requestedChange}];
  const wire={profile:'generic-files-review-proposal/v1',verdict:sample.verdict,summary:sample.observation,findings};
  const result=await config.review.start({ticket,prepared,provider:outputProvider(wire)}).completion;
  const value=receipt(config.review,ticket,result).value;
  assert.equal(value.verdict,sample.verdict);assert.deepEqual(value.findings,findings);assert.equal(value.inputDigest,input.inputDigest);
  if(findings.length){const invalid={...wire,verdict:'accept'};assert.equal((await config.review.start({ticket,prepared,provider:outputProvider(invalid)}).completion).status,'failed','cannot silently accept a report containing factual defects');}
});

// Minimized regression from C01 real proposal (artifact 76ee638873889ffdaec18fd0b54409e09b5b939f508c00dfb309c8aa236c6d07).
// Runtime artifacts themselves stay out of source control.
test('integration is an author dependency role, never a widened generic layout capability',()=>{
  const proposal={nodes:[{id:'requirements',role:'author'},{id:'risks',role:'author'},{id:'integrator',role:'integrator'},{id:'verifier',role:'verifier'}],edges:[{from:'requirements',to:'integrator'},{from:'risks',to:'integrator'},{from:'integrator',to:'verifier'}]};
  const before=structuredClone(proposal);
  assert.throws(()=>bindGenericFilesPlan({inputArtifacts:[],proposal}),/generic_files_plan_unsupported/);
  assert.deepEqual(proposal,before,'rejected business roles are not repaired');
  const corrected=structuredClone(proposal);corrected.nodes[2].role='author';
  const bound=bindGenericFilesPlan({inputArtifacts:[],proposal:corrected});
  assert.deepEqual(bound.deliveries,[{nodeId:'integrator',path:'result.md',targetPath:'results/integrator.md'}]);
  assert.deepEqual(new Set(bound.layouts.find(x=>x.nodeId==='integrator').inputs.map(x=>x.source.nodeId)),new Set(['requirements','risks']));
  assert.deepEqual(bound.layouts.find(x=>x.nodeId==='verifier').allowedPaths,[]);
});
