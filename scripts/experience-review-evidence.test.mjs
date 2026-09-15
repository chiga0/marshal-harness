import test from 'node:test';import assert from 'node:assert/strict';
import {readBoundReview,pendingAnswerGap,finalReviewer} from './experience-review-evidence.mjs';
import {encode,digest} from '../packages/task-store/store.mjs';
import {withReviewCriteria,reviewCriteria,reviewSources,parseAssessmentProposal,validateStoredReviewAssessment} from '../packages/task-application/review-assessment-contract.mjs';
const hash=v=>digest(encode(v));
function fixture(){
 const plan={...withReviewCriteria({acceptance:['原要求']}),digest:'sha256:'+'a'.repeat(64)};
 const content='候选没有依据';const input={profile:'task-independent-review/v1',snapshot:{task:{input:{intent:'原任务'},inputArtifacts:[]},plan,interactions:{replies:[]}},selection:[{nodeId:'author',workerId:'author-old'}],materials:[{nodeId:'author',workerId:'author-old',path:'result.md',content,bytes:Buffer.byteLength(content),digest:digest(Buffer.from(content))}]};input.selectionDigest=hash(input.selection);input.inputDigest=hash(input);
 const ticket={executionType:'review',input:{review:input}},criteria=reviewCriteria(plan),{sources}=reviewSources(input),candidate=sources.find(s=>s.kind==='candidate');
 const proposal={profile:'task-review-assessment-proposal/v1',verdict:'accept',summary:'文本审查完成，不是运行证明。',findings:[],checks:criteria.map(c=>({itemId:c.id,assessment:c.allowNotApplicable?'not-applicable':'pass',method:'text-review',reason:'依据文本核对。',evidence:[{sourceId:candidate.id,quote:content}],counterexample:null,findingIds:[]}))};
 const {report,assessment}=parseAssessmentProposal({ticket,completion:{status:'completed',outputText:JSON.stringify(proposal)}});
 const envelope={profile:'task-independent-review/v2',ticketDigest:hash(ticket),report,assessment};
 const body={verdict:'accept',selectionDigest:input.selectionDigest,policyDigest:plan.digest,workerId:'review-last',evidenceIds:['artifact-review']};
 const leader={taskId:'task-one',review:{...body,digest:hash(body)}};
 const bytes=encode(envelope),metadata={id:'artifact-review',taskId:'task-one',kind:'evidence',status:'ready',bytes:bytes.length,digest:digest(bytes)};
 return {leader,envelope,metadata,bytes};
}
const args=f=>({taskId:'task-one',leader:f.leader,api:async()=>f.metadata,readContent:async()=>f.bytes,validateStored:validateStoredReviewAssessment});
test('实际shared合法v2按唯一Decision/Artifact/字节绑定读取，不把检查当实测',async()=>{
 const f=fixture(),r=await readBoundReview(args(f));assert.deepEqual(r.envelope,f.envelope);assert.equal(r.content,f.bytes.toString());assert.match(r.boundary,/不是浏览器/);
});
for(const mode of ['decision','task','artifact-id','artifact-task','artifact-kind','artifact-status','bytes','digest','assessment','unknown-profile','old-profile','selection','verdict'])test('绑定负例 '+mode,async()=>{
 const f=fixture();if(mode==='decision')f.leader.review.digest='sha256:'+'b'.repeat(64);if(mode==='task')f.leader.taskId='foreign';
 if(mode==='artifact-id')f.metadata.id='foreign';if(mode==='artifact-task')f.metadata.taskId='foreign';if(mode==='artifact-kind')f.metadata.kind='delivery';if(mode==='artifact-status')f.metadata.status='pending';
 if(mode==='bytes')f.metadata.bytes++;if(mode==='digest')f.metadata.digest='sha256:'+'b'.repeat(64);
 if(['assessment','unknown-profile','old-profile','selection','verdict'].includes(mode)){
  if(mode==='assessment')f.envelope.assessment.reportDigest='sha256:'+'b'.repeat(64);if(mode==='unknown-profile')f.envelope.profile='task-independent-review/v99';if(mode==='old-profile')f.envelope.profile='task-independent-review/v1';
  if(mode==='selection')f.envelope.report.selectionDigest='sha256:'+'b'.repeat(64);if(mode==='verdict')f.envelope.report.verdict='rework';
  f.bytes=encode(f.envelope);f.metadata.bytes=f.bytes.length;f.metadata.digest=digest(f.bytes);
 }
 await assert.rejects(readBoundReview(args(f)));
});
test('最终Reviewer按绑定worker而不是首位选取',()=>{
 const workers=[{id:'review-first',role:'reviewer'},{id:'review-last',role:'reviewer'}];assert.equal(finalReviewer(workers,fixture().leader).id,'review-last');assert.throws(()=>finalReviewer(workers.slice(0,1),fixture().leader));
});
test('缺答只对应实际待答业务请求，终态失败/无请求不能变BLOCKED',()=>{
 const leader={taskId:'task-one',pendingRequest:{kind:'business',status:'pending',prompt:'请提供实际报名方式'}};
 assert.equal(pendingAnswerGap({id:'task-one',status:'awaiting-answer'},leader).code,'case_answer_unavailable');
 for(const status of ['failed','completed','running'])assert.throws(()=>pendingAnswerGap({id:'task-one',status},leader));
 assert.throws(()=>pendingAnswerGap({id:'task-one',status:'awaiting-answer'},{...leader,pendingRequest:null}));
});
