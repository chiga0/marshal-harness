import test from 'node:test';
import assert from 'node:assert/strict';
import {encode,digest} from '../task-store/store.mjs';
import {withReviewCriteria,reviewCriteria,reviewSources,parseAssessmentProposal,validateReviewAssessment,REVIEW_ASSESSMENT_PROPOSAL_PROFILE} from './review-assessment-contract.mjs';
const hash=value=>digest(encode(value));
function fixture(){
 const content='候选原文：只展示说明，不保存数据。';
 const plan={...withReviewCriteria({acceptance:['展示说明'],summary:'方案'}),digest:hash('plan')};
 const input={profile:'task-independent-review/v1',inputDigest:hash('input'),selectionDigest:hash('selection'),selection:[{nodeId:'author',workerId:'worker'}],
 snapshot:{task:{input:{intent:'展示说明'},inputArtifacts:[]},plan,interactions:{replies:[],requests:[{question:'未确认事实'}]}},
 materials:[{nodeId:'author',workerId:'worker',path:'result.txt',content,bytes:Buffer.byteLength(content),digest:digest(Buffer.from(content))}]};
 const criteria=reviewCriteria(plan),{sources}=reviewSources(input),candidate=sources.find(s=>s.kind==='candidate');
 const raw={profile:REVIEW_ASSESSMENT_PROPOSAL_PROFILE,verdict:'accept',summary:'文本审查完成，未运行软件',findings:[],checks:criteria.map(c=>({itemId:c.id,method:'text-review',assessment:c.allowNotApplicable?'not-applicable':'pass',reason:'引用原文说明检查范围',evidence:[{sourceId:candidate.id,quote:content}],counterexample:null,findingIds:[]}))};
 const ticket={executionType:'review',input:{review:input}};
 return {input,raw,ticket,parse:()=>parseAssessmentProposal({ticket,completion:{outputText:JSON.stringify(raw)}})};
}
test('批准前目录保持业务原文，最大容量可通过，超限和重复目录不截断',()=>{
 const proposal={acceptance:Array.from({length:12},(_,i)=>String(i)+'中'.repeat(682)),summary:'原样'};
 const before=structuredClone(proposal),plan=withReviewCriteria(proposal);
 assert.deepEqual(proposal,before);assert.deepEqual(plan.acceptance.slice(0,12),proposal.acceptance);
 assert.ok(Buffer.byteLength(plan.acceptance.at(-1))<=4096);assert.equal(reviewCriteria({...plan,digest:hash('p')}).length,16);
 assert.throws(()=>withReviewCriteria({...proposal,acceptance:[...proposal.acceptance,'第十三项']}));
 assert.throws(()=>withReviewCriteria({acceptance:['中'.repeat(683)]}));
 assert.throws(()=>withReviewCriteria(plan));
 assert.throws(()=>withReviewCriteria({acceptance:['{"profile":"task-review-criteria/v99"}']}));
});
test('目录不接受修改后的原文、固定政策、ID、许可和重复标记',()=>{
 const {input}=fixture();for(const mutate of [p=>p.acceptance[0]='改变',p=>p.acceptance[1]='放宽政策',p=>p.acceptance.push(p.acceptance.at(-1)),p=>{const d=JSON.parse(p.acceptance.at(-1));d.items[0].allowNotApplicable=true;p.acceptance[p.acceptance.length-1]=JSON.stringify(d);},p=>{const d=JSON.parse(p.acceptance.at(-1));d.items[0].id='criterion-unknown';p.acceptance[p.acceptance.length-1]=JSON.stringify(d);}]){
 const plan=structuredClone(input.snapshot.plan);mutate(plan);assert.throws(()=>reviewCriteria(plan));}
});
test('来源区分确认回答、原附件、候选；待答请求不成为事实来源',()=>{
 const {input}=fixture();input.snapshot.interactions.replies.push({answer:'已确认'});
 const content='附件';const ref={id:'input-1',name:'资料',bytes:Buffer.byteLength(content),digest:digest(Buffer.from(content))};
 input.snapshot.task.inputArtifacts.push(ref);input.materials.unshift({...ref,inputId:ref.id,content});
 const result=reviewSources(input);assert.deepEqual(result.sources.map(s=>s.kind),['task-input','approved-plan','confirmed-answer','input-artifact','candidate']);
 assert.ok([...result.texts.values()].every(s=>!s.includes('未确认事实')));
 input.materials[0].content='篡改';assert.throws(()=>reviewSources(input));
});
test('原输入绑定与报告保持原样，v2可复验，过期报告或目录不能借用',()=>{
 const f=fixture(),{report,assessment}=f.parse();assert.equal(report.verdict,f.raw.verdict);assert.deepEqual(report.findings,f.raw.findings);
 assert.deepEqual(validateReviewAssessment(assessment,report,f.input),assessment);
 for(const field of ['inputDigest','selectionDigest','planDigest','reportDigest','criteriaDigest'])assert.throws(()=>validateReviewAssessment({...assessment,[field]:hash('other')},report,f.input));
 assert.throws(()=>validateReviewAssessment(assessment,{...report,summary:'changed'},f.input));
 assert.throws(()=>validateReviewAssessment({...assessment,extra:1},report,f.input));
});
test('缺项、重复、伪引用、隐藏未知、必需项NA、非法方法及候选遗漏均失败',()=>{
 for(const mutate of [r=>r.checks.pop(),r=>r.checks[1]=r.checks[0],r=>r.checks[0].evidence[0].quote='不存在的原句',r=>r.checks[0].evidence[0].sourceId='source-99',r=>r.checks[0].assessment='unknown',r=>r.checks[0].assessment='not-applicable',r=>r.checks[0].method='browser-test',r=>r.checks[0].evidence=[],r=>r.checks.forEach(c=>c.evidence=[{sourceId:'source-0',quote:'展示说明'}]),r=>r.checks[0].reason='\ud800',r=>r.checks[0].extra=1]){
 const f=fixture();mutate(f.raw);assert.throws(f.parse);}
});
test('失败项严格关联原要求及独立finding，不改写模型结论',()=>{
 const f=fixture();f.raw.verdict='rework';f.raw.checks[0].assessment='fail';f.raw.checks[0].findingIds=['finding-1'];
 f.raw.findings=[{id:'finding-1',nodeIds:['author'],requirement:'展示说明',observation:'具体缺项',requestedChange:'补齐说明'}];
 assert.equal(f.parse().report.verdict,'rework');f.raw.findings[0].requirement='别的要求';assert.throws(f.parse);
 f.raw.findings[0].requirement='展示说明';f.raw.findings.push({...f.raw.findings[0],id:'unassociated'});assert.throws(f.parse);
});
test('操作与恢复通过需要公开反例推演，NA不能带假反例',()=>{
 const f=fixture(),check=f.raw.checks.at(-1);check.assessment='pass';assert.throws(f.parse);
 check.counterexample={initial:'初态',operation:'动作',failure:'中途失败',result:'后态',recovery:'恢复依据'};assert.doesNotThrow(f.parse);
 check.assessment='not-applicable';assert.throws(f.parse);
});
test('持久化读取校验不提供receipt，不假装重新获得原材料真值',async()=>{
 const {validateStoredReviewAssessment}=await import('./review-assessment-contract.mjs');
 const f=fixture(),value=f.parse(),envelope={profile:'task-independent-review/v2',ticketDigest:hash(f.ticket),...value};
 assert.deepEqual(validateStoredReviewAssessment(envelope),envelope);
 for(const mutate of [e=>e.profile='unknown',e=>e.extra=1,e=>e.report.summary='替换报告',e=>e.assessment.sources[0].extra=1,e=>e.assessment.checks[0].evidence[0].sourceId='unknown',e=>e.assessment.criteria[0].allowNotApplicable=true]){
 const altered=structuredClone(envelope);mutate(altered);assert.throws(()=>validateStoredReviewAssessment(altered));}
 const altered=structuredClone(envelope);altered.assessment.checks[0].evidence[0].quote='元数据读取不能重证原句';
 assert.doesNotThrow(()=>validateStoredReviewAssessment(altered));assert.throws(()=>validateReviewAssessment(altered.assessment,altered.report,f.input));
});
test('零字节和空白材料可被诚实评审；空引用仅匹配零字节来源',async()=>{
 const {validateStoredReviewAssessment}=await import('./review-assessment-contract.mjs');
 for(const content of ['', '   ', '\n\t']){
 const f=fixture(),m=f.input.materials[0];Object.assign(m,{content,bytes:Buffer.byteLength(content),digest:digest(Buffer.from(content))});
 f.raw.checks.forEach(c=>c.evidence[0].quote=content);
 f.raw.verdict='rework';f.raw.checks[0].assessment='fail';f.raw.checks[0].findingIds=['empty'];
 f.raw.findings=[{id:'empty',nodeIds:['author'],requirement:'展示说明',observation:'内容未提供说明',requestedChange:'补齐原要求'}];
 const parsed=f.parse();assert.equal(parsed.report.verdict,'rework');
 assert.doesNotThrow(()=>validateStoredReviewAssessment({profile:'task-independent-review/v2',ticketDigest:hash(f.ticket),...parsed}));
 }
 const f=fixture();f.raw.checks[0].evidence[0].quote='';assert.throws(f.parse);
});
