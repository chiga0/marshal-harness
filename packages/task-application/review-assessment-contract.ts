import {encode, digest} from '../task-store/store.ts';
import {parseManagedOutput} from './leader-ports.ts';

export const REVIEW_CRITERIA_PROFILE = 'task-review-criteria/v1';
export const REVIEW_ASSESSMENT_PROFILE = 'task-review-assessment/v1';
export const REVIEW_ASSESSMENT_PROPOSAL_PROFILE = 'task-review-assessment-proposal/v1';
export const REVIEW_POLICIES = Object.freeze([
  Object.freeze({id:'scope', allowNotApplicable:false, text:'满足原需求和批准范围中的全部必需要求，不擅自增加禁令、降低目标或豁免缺项。交付物是方案时，文本评审检查设计与依据是否闭合，不表示本轮已实现软件或实测运行效果。'}),
  Object.freeze({id:'facts', allowNotApplicable:false, text:'允许用户要求的创作、文案和拟议方案。已确定的事实只能来自原需求、原材料或用户已确认回答；计划和作者自述不是新增事实来源。未确认的日程、服务、参与方式须在相关内容处明确为拟议或待确认，不冒充既定安排。需要真实渠道才能完成的任务应澄清；允许展示待确认信息时不要求作者编造答案。'}),
  Object.freeze({id:'effects', allowNotApplicable:true, text:'涉及按钮、链接、表单或其他操作承诺时，核对声明的完成路径、反馈与实现或方案依据。文本评审不能冒充真实浏览器或外部操作实测；演示、占位和真实能力必须清楚区分。没有相关操作要求才可说明不适用。'}),
  Object.freeze({id:'recovery', allowNotApplicable:true, text:'涉及数据变化、幂等或恢复时，区分权威来源与派生数据，并推演初态、动作、关键中途失败、重跑后态和恢复依据。不能替候选补出提交标记、事务、前值或续建规则；方案须有闭合分支而非只声称可恢复。没有相关要求或承诺才可说明不适用。'}),
]);
const hash = value => digest(encode(value));
const copy = value => JSON.parse(encode(value).toString());
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const closed = (value, names) => object(value) && Reflect.ownKeys(value).length === names.length && names.every(name => Object.hasOwn(value,name));
const text = (value, max) => typeof value === 'string' && value.isWellFormed() && value.trim().length > 0 && !value.includes('\0') && Buffer.byteLength(value)<=max;
const contentText = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value)<=max;
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
function check(value, code='invalid_review_report') {if(!value)throw Object.assign(new Error(code),{code});}
function reserved(value) {try {const parsed=JSON.parse(value);return typeof parsed?.profile==='string' && parsed.profile.startsWith('task-review-criteria/');}catch{return false;}}
function item(requirement,index,policyId) {
  const requirementDigest=digest(Buffer.from(requirement));
  return {id:'criterion-'+index+'-'+requirementDigest.slice(7,23),index,requirementDigest,method:'text-review',
    allowNotApplicable:policyId==='effects'||policyId==='recovery',policyId};
}

/** Called only by the opted-in trusted mapper BEFORE plan approval. */
export function withReviewCriteria(proposal) {
  check(object(proposal)&&Array.isArray(proposal.acceptance)&&proposal.acceptance.length<=12&&
    proposal.acceptance.every(value=>text(value,2048)&&!reserved(value)),'invalid_leader_decision');
  const plan=copy(proposal),business=plan.acceptance.length;
  plan.acceptance.push(...REVIEW_POLICIES.map(policy=>policy.text));
  const items=plan.acceptance.map((requirement,index)=>item(requirement,index,index<business?null:REVIEW_POLICIES[index-business].id));
  const directory=JSON.stringify({profile:REVIEW_CRITERIA_PROFILE,items});
  check(Buffer.byteLength(directory)<=4096&&plan.acceptance.length+1<=32,'invalid_leader_decision');
  plan.acceptance.push(directory);return plan;
}

/** Technical records appended by Core occur AFTER this directory. */
export function reviewCriteria(plan) {
  check(object(plan)&&Array.isArray(plan.acceptance)&&plan.acceptance.length<=32);
  const positions=plan.acceptance.flatMap((value,index)=>reserved(value)?[index]:[]);
  check(positions.length===1);
  const position=positions[0],directory=JSON.parse(plan.acceptance[position]),business=position-REVIEW_POLICIES.length;
  check(business>=0&&business<=12&&closed(directory,['profile','items'])&&directory.profile===REVIEW_CRITERIA_PROFILE&&
    Array.isArray(directory.items)&&directory.items.length===position&&Buffer.byteLength(plan.acceptance[position])<=4096);
  const criteria=directory.items.map((entry,index)=>{
    const requirement=plan.acceptance[index],policy=index<business?null:REVIEW_POLICIES[index-business];
    check(text(requirement,2048)&&(!policy||requirement===policy.text));
    const expected=item(requirement,index,policy?.id??null);check(hash(entry)===hash(expected));
    return {...expected,requirement};
  });
  check(sha(plan.digest));return criteria;
}

export function reviewSources(input) {
  check(object(input)&&object(input.snapshot)&&object(input.snapshot.task)&&object(input.snapshot.task.input)&&
    Array.isArray(input.selection)&&Array.isArray(input.materials)&&Array.isArray(input.snapshot.task.inputArtifacts));
  const sources=[],texts=new Map();
  const add=(kind,label,locator,content)=>{
    check(contentText(content,196608)&&text(label,1024)&&text(locator,1024)&&sources.length<128);
    const id='source-'+sources.length,source={id,kind,label,digest:digest(Buffer.from(content)),locator};
    sources.push(source);texts.set(id,content);
  };
  add('task-input','原始需求与上下文','snapshot.task.input',encode(input.snapshot.task.input).toString());
  add('approved-plan','批准计划（不是新增事实来源）','snapshot.plan',encode(input.snapshot.plan).toString());
  const replies=input.snapshot.interactions?.replies??[];check(Array.isArray(replies));
  replies.forEach((reply,index)=>add('confirmed-answer','已确认回答 '+(index+1),'snapshot.interactions.replies['+index+']',encode(reply).toString()));
  input.materials.forEach((material,index)=>{
    check(object(material)&&contentText(material.content,196608)&&Number.isSafeInteger(material.bytes)&&material.bytes===Buffer.byteLength(material.content)&&
      sha(material.digest)&&digest(Buffer.from(material.content))===material.digest);
    let kind='context',label=material.name??material.path??'上下文材料 '+(index+1);
    if(material.inputId!==undefined){
      const ref=input.snapshot.task.inputArtifacts.find(ref=>ref.id===material.inputId);
      check(ref&&ref.digest===material.digest&&ref.bytes===material.bytes);kind='input-artifact';label=ref.name??label;
    }else if(input.selection.some(selected=>selected.workerId===material.workerId&&selected.nodeId===material.nodeId)){
      check(text(material.path,1024));kind='candidate';label=material.nodeId+' / '+material.path;
    }
    add(kind,label,'materials['+index+'].content',material.content);
  });
  for(const selected of input.selection)check(input.materials.some(material=>material.workerId===selected.workerId&&material.nodeId===selected.nodeId));
  return {sources,texts};
}

function validateChecks(checks,report,criteria,sources,texts) {
  check(Array.isArray(checks)&&checks.length===criteria.length&&checks.length<=16&&
    Array.isArray(report.findings)&&report.findings.length<=16&&['accept','rework','reject'].includes(report.verdict));
  const seen=new Set(),usedFindings=new Set(),cited=new Set();
  for(const entry of checks){
    check(closed(entry,['itemId','assessment','method','reason','evidence','counterexample','findingIds']));
    const criterion=criteria.find(value=>value.id===entry.itemId);
    check(criterion&&!seen.has(entry.itemId));seen.add(entry.itemId);
    check(['pass','fail','unknown','not-applicable'].includes(entry.assessment)&&entry.method===criterion.method&&text(entry.reason,1024)&&
      Array.isArray(entry.evidence)&&entry.evidence.length<=16&&Array.isArray(entry.findingIds)&&entry.findingIds.length<=1);
    if(entry.assessment==='not-applicable')check(criterion.allowNotApplicable);
    if(['pass','not-applicable'].includes(entry.assessment))check(entry.evidence.length>0&&entry.findingIds.length===0);
    for(const reference of entry.evidence){
      check(closed(reference,['sourceId','quote'])&&contentText(reference.quote,512)&&sources.some(source=>source.id===reference.sourceId&&(reference.quote.length>0||source.digest===digest(Buffer.alloc(0))))&&(!texts||texts.get(reference.sourceId)?.includes(reference.quote)));
      cited.add(reference.sourceId);
    }
    if(entry.counterexample!==null){
      const fields=['initial','operation','failure','result','recovery'];
      check(closed(entry.counterexample,fields)&&fields.every(field=>text(entry.counterexample[field],768)));
    }
    if(['effects','recovery'].includes(criterion.policyId)&&entry.assessment==='pass')check(entry.counterexample!==null);
    if(entry.assessment==='not-applicable')check(entry.counterexample===null);
    if(['fail','unknown'].includes(entry.assessment)){
      check(report.verdict!=='accept'&&entry.findingIds.length===1&&!usedFindings.has(entry.findingIds[0]));
      const finding=report.findings.find(value=>value.id===entry.findingIds[0]);
      check(finding&&finding.requirement===criterion.requirement);usedFindings.add(finding.id);
    }
  }
  check(report.findings.length===usedFindings.size);
  if(report.verdict==='accept')check(report.findings.length===0&&checks.every(entry=>['pass','not-applicable'].includes(entry.assessment)));
  else check(checks.some(entry=>['fail','unknown'].includes(entry.assessment)));
  check(sources.filter(source=>source.kind==='candidate').every(source=>cited.has(source.id)));
}

/** Original Port subsequently validates the unchanged six-field report. */
export function parseAssessmentProposal({ticket,completion}) {
  check(ticket?.executionType==='review');const input=ticket.input?.review;
  check(input?.profile==='task-independent-review/v1'&&sha(input.inputDigest)&&sha(input.selectionDigest));
  const raw=parseManagedOutput({completion});
  check(closed(raw,['profile','verdict','summary','findings','checks'])&&raw.profile===REVIEW_ASSESSMENT_PROPOSAL_PROFILE&&text(raw.summary,4096));
  const report={profile:input.profile,inputDigest:input.inputDigest,selectionDigest:input.selectionDigest,
    verdict:raw.verdict,summary:raw.summary,findings:raw.findings};
  const criteria=reviewCriteria(input.snapshot.plan),{sources,texts}=reviewSources(input);
  validateChecks(raw.checks,report,criteria,sources,texts);
  const assessment={profile:REVIEW_ASSESSMENT_PROFILE,inputDigest:input.inputDigest,selectionDigest:input.selectionDigest,
    planDigest:input.snapshot.plan.digest,reportDigest:hash(report),criteriaDigest:hash(criteria),criteria,sources,checks:raw.checks};
  check(encode({profile:'task-independent-review/v2',ticketDigest:hash(ticket),report,assessment}).length<=131072);
  return copy({report,assessment});
}

export function validateReviewAssessment(assessment,report,input) {
  check(closed(assessment,['profile','inputDigest','selectionDigest','planDigest','reportDigest','criteriaDigest','criteria','sources','checks'])&&
    assessment.profile===REVIEW_ASSESSMENT_PROFILE&&assessment.inputDigest===input.inputDigest&&assessment.selectionDigest===input.selectionDigest&&
    assessment.planDigest===input.snapshot.plan.digest&&assessment.reportDigest===hash(report));
  const criteria=reviewCriteria(input.snapshot.plan),{sources,texts}=reviewSources(input);
  check(assessment.criteriaDigest===hash(criteria)&&hash(assessment.criteria)===hash(criteria)&&hash(assessment.sources)===hash(sources));
  validateChecks(assessment.checks,report,criteria,sources,texts);return copy(assessment);
}

/** Read-only persisted shape/binding check. Does NOT recreate an opaque receipt
 * or recheck source quotation truth without the original frozen input. */
export function validateStoredReviewAssessment(envelope) {
  check(closed(envelope,['profile','ticketDigest','report','assessment'])&&envelope.profile==='task-independent-review/v2'&&sha(envelope.ticketDigest));
  check(encode(envelope).length<=131072);
  const {report,assessment}=envelope;
  check(closed(report,['profile','inputDigest','selectionDigest','verdict','summary','findings'])&&report.profile==='task-independent-review/v1'&&
    sha(report.inputDigest)&&sha(report.selectionDigest)&&text(report.summary,4096)&&Array.isArray(report.findings)&&report.findings.length<=16);
  const id=value=>typeof value==='string'&&/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
  check(report.findings.every(finding=>closed(finding,['id','nodeIds','requirement','observation','requestedChange'])&&id(finding.id)&&
    Array.isArray(finding.nodeIds)&&finding.nodeIds.length>0&&finding.nodeIds.length<=64&&finding.nodeIds.every(id)&&new Set(finding.nodeIds).size===finding.nodeIds.length&&
    ['requirement','observation','requestedChange'].every(key=>text(finding[key],2048)))&&new Set(report.findings.map(f=>f.id)).size===report.findings.length);
  check(closed(assessment,['profile','inputDigest','selectionDigest','planDigest','reportDigest','criteriaDigest','criteria','sources','checks'])&&
    assessment.profile===REVIEW_ASSESSMENT_PROFILE&&assessment.inputDigest===report.inputDigest&&assessment.selectionDigest===report.selectionDigest&&
    sha(assessment.planDigest)&&assessment.reportDigest===hash(report)&&Array.isArray(assessment.criteria)&&assessment.criteria.length>=4&&assessment.criteria.length<=16);
  const requirements=assessment.criteria.map(criterion=>{check(object(criterion));return criterion.requirement;});
  const directory={profile:REVIEW_CRITERIA_PROFILE,items:assessment.criteria.map(({requirement,...entry})=>entry)};
  const criteria=reviewCriteria({digest:assessment.planDigest,acceptance:[...requirements,JSON.stringify(directory)]});
  check(hash(criteria)===hash(assessment.criteria)&&assessment.criteriaDigest===hash(criteria));
  const {sources}=assessment;check(Array.isArray(sources)&&sources.length>=2&&sources.length<=128);
  check(sources.every((source,index)=>closed(source,['id','kind','label','digest','locator'])&&source.id==='source-'+index&&
    ['task-input','approved-plan','confirmed-answer','input-artifact','candidate','context'].includes(source.kind)&&text(source.label,1024)&&text(source.locator,1024)&&sha(source.digest)));
  check(sources[0].kind==='task-input'&&sources[0].locator==='snapshot.task.input'&&sources[1].kind==='approved-plan'&&sources[1].locator==='snapshot.plan');
  validateChecks(assessment.checks,report,criteria,sources,null);
  return copy(envelope);
}
