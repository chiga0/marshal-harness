import {reviewPolicies} from './review-policies';
import type {PlanRecord} from '@/lib/transport/types';
import {canonical} from '../overview/leader-decision';
import {sha256Hex} from '../../../artifacts/downloader';
export const CRITERIA_PROFILE='task-review-criteria/v1';
export interface Criterion {id:string;index:number;requirement:string;requirementDigest:string;method:'text-review';allowNotApplicable:boolean;policyId:null|'scope'|'facts'|'effects'|'recovery'}
export interface EvidenceSource {id:string;kind:'task-input'|'approved-plan'|'confirmed-answer'|'input-artifact'|'candidate'|'context';label:string;digest:string;locator:string}
export interface AssessmentCheck {itemId:string;assessment:'pass'|'fail'|'unknown'|'not-applicable';method:'text-review';reason:string;evidence:{sourceId:string;quote:string}[];counterexample:null|{initial:string;operation:string;failure:string;result:string;recovery:string};findingIds:string[]}
export interface Assessment {profile:'task-review-assessment/v1';inputDigest:string;selectionDigest:string;planDigest:string;reportDigest:string;criteriaDigest:string;criteria:Criterion[];sources:EvidenceSource[];checks:AssessmentCheck[]}
export const closed=(v:unknown,keys:string[]):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).sort().join(',')===[...keys].sort().join(',');
const requireValid=(v:unknown):void=>{if(!v)throw new Error('review_assessment_binding');};
const text=(v:unknown,max:number)=>typeof v==='string'&&v.trim().length>0&&!v.includes('\0')&&new TextDecoder().decode(new TextEncoder().encode(v))===v&&new TextEncoder().encode(v).length<=max;
const digest=(v:unknown)=>typeof v==='string'&&/^sha256:[a-f0-9]{64}$/.test(v);
const EMPTY_DIGEST='sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
const quoteText=(v:unknown)=>typeof v==='string'&&!v.includes('\0')&&new TextDecoder().decode(new TextEncoder().encode(v))===v&&new TextEncoder().encode(v).length<=512;
const identifier=(v:unknown)=>typeof v==='string'&&/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
export const reviewHash=async(v:unknown)=>'sha256:'+await sha256Hex(new Blob([canonical(v)]));
const criterionKeys=['id','index','requirementDigest','method','allowNotApplicable','policyId'];
function directoryRecords(acceptance:string[]):unknown[]{return acceptance.flatMap(value=>{try{const parsed=JSON.parse(value);return parsed&&typeof parsed==='object'&&typeof parsed.profile==='string'&&parsed.profile.startsWith('task-review-criteria/')?[parsed]:[];}catch{return [];}});}
export function hasReviewCriteria(plan:PlanRecord){return directoryRecords(plan.acceptance).length>0;}
async function validateCriteria(items:unknown[],acceptance?:string[]):Promise<Criterion[]> {
 requireValid(items.length>=4&&items.length<=16);
 const result:Criterion[]=[];const policies=['scope','facts','effects','recovery'];
 for(let index=0;index<items.length;index++){
  const value=items[index];requireValid(closed(value,acceptance?criterionKeys:[...criterionKeys,'requirement']));
  const item=value as unknown as Criterion;const requirement=acceptance?acceptance[item.index]:item.requirement;
  requireValid(item.index===index&&text(requirement,2048)&&digest(item.requirementDigest));
  requireValid('sha256:'+await sha256Hex(new Blob([requirement!]))===item.requirementDigest);
  requireValid(item.id===`criterion-${index}-${item.requirementDigest.slice(7,23)}`&&item.method==='text-review');
  const policyId=index<items.length-4?null:policies[index-(items.length-4)];
  requireValid(policyId===null||requirement===reviewPolicies.find(p=>p.id===policyId)!.text);
  requireValid(item.policyId===policyId&&item.allowNotApplicable===(policyId==='effects'||policyId==='recovery'));
  result.push({...item,requirement:requirement!});
 }
 return result;
}
export async function readPlanCriteria(plan:PlanRecord):Promise<Criterion[]|null>{
 const records=directoryRecords(plan.acceptance);if(!records.length)return null;
 requireValid(records.length===1&&closed(records[0],['profile','items']));const record=records[0] as Record<string,unknown>;
 requireValid(record.profile===CRITERIA_PROFILE&&Array.isArray(record.items));
 const items=record.items as unknown[];
 const criteria=await validateCriteria(items,plan.acceptance);
 // 目录紧随原业务项与四项政策，不把后继技术记录递归计入。
 requireValid(plan.acceptance[items.length]!==undefined&&canonical(JSON.parse(plan.acceptance[items.length]!))===canonical(record));
 return criteria;
}
interface BoundReport {inputDigest:string;selectionDigest:string;verdict:string;findings:{id:string;requirement:string}[]}
export async function validateAssessment(value:unknown,report:BoundReport,plan:PlanRecord):Promise<Assessment>{
 requireValid(closed(value,['profile','inputDigest','selectionDigest','planDigest','reportDigest','criteriaDigest','criteria','sources','checks']));
 const data=value as unknown as Assessment;
 requireValid(data.profile==='task-review-assessment/v1'&&[data.inputDigest,data.selectionDigest,data.planDigest,data.reportDigest,data.criteriaDigest].every(digest));
 requireValid(data.inputDigest===report.inputDigest&&data.selectionDigest===report.selectionDigest&&data.planDigest===plan.digest&&data.reportDigest===await reviewHash(report));
 requireValid(Array.isArray(data.criteria)&&Array.isArray(data.sources)&&Array.isArray(data.checks));
 const criteria=await validateCriteria(data.criteria);const planCriteria=await readPlanCriteria(plan);
 requireValid(planCriteria!==null&&canonical(criteria)===canonical(planCriteria)&&data.criteriaDigest===await reviewHash(criteria));
 requireValid(data.sources.length>=2&&data.sources.length<=128);
 const sourceIds=new Set<string>();
 for(let i=0;i<data.sources.length;i++){const source=data.sources[i]!;
  requireValid(closed(source,['id','kind','label','digest','locator'])&&source.id===`source-${i}`&&['task-input','approved-plan','confirmed-answer','input-artifact','candidate','context'].includes(source.kind)&&text(source.label,1024)&&text(source.locator,1024)&&digest(source.digest));sourceIds.add(source.id);
 }
 requireValid(data.sources[0]!.kind==='task-input'&&data.sources[0]!.locator==='snapshot.task.input'&&data.sources[1]!.kind==='approved-plan'&&data.sources[1]!.locator==='snapshot.plan');
 requireValid(data.checks.length===criteria.length);const checked=new Set<string>(),linked=new Set<string>(),cited=new Set<string>();
 for(const check of data.checks){
  requireValid(closed(check,['itemId','assessment','method','reason','evidence','counterexample','findingIds']));
  const criterion=criteria.find(c=>c.id===check.itemId);requireValid(criterion&&!checked.has(check.itemId));checked.add(check.itemId);
  requireValid(['pass','fail','unknown','not-applicable'].includes(check.assessment)&&check.method==='text-review'&&text(check.reason,1024)&&Array.isArray(check.evidence)&&check.evidence.length<=16&&Array.isArray(check.findingIds)&&check.findingIds.length<=1);
  if(check.assessment==='not-applicable')requireValid(criterion!.allowNotApplicable&&check.counterexample===null);
  if(['pass','not-applicable'].includes(check.assessment))requireValid(check.evidence.length>0&&check.findingIds.length===0);
  if(check.assessment==='pass'&&['effects','recovery'].includes(criterion!.policyId??''))requireValid(check.counterexample!==null);
  if(check.counterexample!==null)requireValid(closed(check.counterexample,['initial','operation','failure','result','recovery'])&&Object.values(check.counterexample).every(v=>text(v,768)));
  for(const ref of check.evidence){requireValid(closed(ref,['sourceId','quote'])&&sourceIds.has(ref.sourceId)&&quoteText(ref.quote)&&(ref.quote.length>0||data.sources.find(s=>s.id===ref.sourceId)!.digest===EMPTY_DIGEST));cited.add(ref.sourceId);}
  const negative=['fail','unknown'].includes(check.assessment);requireValid(!negative||check.findingIds.length===1);requireValid(report.verdict!=='accept'||!negative);
  for(const id of check.findingIds){requireValid(!linked.has(id)&&identifier(id)&&report.findings.some(f=>f.id===id&&f.requirement===criterion!.requirement));linked.add(id);}
 }
 requireValid(report.verdict==='accept'||data.checks.some(c=>['fail','unknown'].includes(c.assessment)));
 requireValid(data.sources.some(s=>s.kind==='candidate'));
 requireValid(report.findings.every(f=>linked.has(f.id))&&data.sources.filter(s=>s.kind==='candidate').every(s=>cited.has(s.id)));
 return data;
}
