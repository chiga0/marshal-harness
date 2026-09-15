import {reviewPolicies} from './review-policies';
import {makePlan} from '../testing/fixtures';
import {reviewHash,type Assessment} from './review-assessment';
import {sha256Hex} from '../../../artifacts/downloader';
export async function assessmentFixture(business=['成果符合原需求']){
 const texts=[...business,...reviewPolicies.map(p=>p.text)];
 const criteria=await Promise.all(texts.map(async(requirement,index)=>{const requirementDigest='sha256:'+await sha256Hex(new Blob([requirement]));return {id:`criterion-${index}-${requirementDigest.slice(7,23)}`,index,requirement,requirementDigest,method:'text-review' as const,allowNotApplicable:index>=business.length+2,policyId:index<business.length?null:['scope','facts','effects','recovery'][index-business.length] as 'scope'|'facts'|'effects'|'recovery'};}));
 const directory={profile:'task-review-criteria/v1',items:criteria.map(({requirement,...item})=>item)};
 const plan=makePlan({acceptance:[...texts,JSON.stringify(directory)]});
 const report={profile:'task-independent-review/v1',inputDigest:'sha256:'+'a'.repeat(64),selectionDigest:'sha256:'+'b'.repeat(64),verdict:'accept',summary:'文本设计满足要求；未执行软件。',findings:[] as {id:string;nodeIds:string[];requirement:string;observation:string;requestedChange:string}[]};
 const assessment:Assessment={profile:'task-review-assessment/v1',inputDigest:report.inputDigest,selectionDigest:report.selectionDigest,planDigest:plan.digest,reportDigest:await reviewHash(report),criteriaDigest:await reviewHash(criteria),criteria,sources:[{id:'source-0',kind:'task-input',label:'原需求',digest:'sha256:'+'d'.repeat(64),locator:'snapshot.task.input'},{id:'source-1',kind:'approved-plan',label:'批准计划',digest:'sha256:'+'e'.repeat(64),locator:'snapshot.plan'},{id:'source-2',kind:'candidate',label:'方案正文',digest:'sha256:'+'c'.repeat(64),locator:'materials[0]'}],checks:criteria.map(item=>({itemId:item.id,assessment:item.allowNotApplicable?'not-applicable':'pass',method:'text-review',reason:item.allowNotApplicable?'本文不包含该业务操作。':'文本内容支持要求。',evidence:[{sourceId:'source-2',quote:'候选正文'}],counterexample:null,findingIds:[]}))};
 return {plan,report,assessment};
}
