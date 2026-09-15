import {describe,it,expect} from 'vitest';
import {makePlan} from '../testing/fixtures';
import {readPlanCriteria,validateAssessment,reviewHash} from './review-assessment';
import {sha256Hex} from '../../../artifacts/downloader';
import {assessmentFixture} from './review-assessment.fixture';
describe('逐项评审证据边界',()=>{
 it('合法方案文本通过与实测覆盖分离，目录按原文校验',async()=>{const f=await assessmentFixture();expect(await readPlanCriteria(f.plan)).toEqual(f.assessment.criteria);expect(await validateAssessment(f.assessment,f.report,f.plan)).toEqual(f.assessment);});
 it('v1无目录不猜测覆盖',async()=>expect(await readPlanCriteria(makePlan({acceptance:['普通要求']}))).toBeNull());
 it.each(['missing','duplicate','foreign','report','plan','quote','unknown-accept','mandatory-na','false-method','counterexample','candidate-unread','unicode'])('拒绝 %s',async(kind)=>{
  const f=await assessmentFixture();const a=f.assessment;
  if(kind==='missing')a.checks.pop();if(kind==='duplicate')a.checks[1]=a.checks[0]!;
  if(kind==='foreign')a.checks[0]!.itemId='foreign';if(kind==='report')a.reportDigest='sha256:'+'0'.repeat(64);
  if(kind==='plan')a.planDigest='sha256:'+'0'.repeat(64);if(kind==='quote')a.checks[0]!.evidence[0]!.sourceId='foreign';
  if(kind==='unknown-accept')a.checks[0]!.assessment='unknown';if(kind==='mandatory-na')a.checks[0]!.assessment='not-applicable';
  if(kind==='false-method')a.checks[0]!.method='browser' as 'text-review';
  if(kind==='counterexample')a.checks[4]!.assessment='pass';
  if(kind==='candidate-unread')a.sources.push({...a.sources[2]!,id:'source-3'});
  if(kind==='unicode')a.checks[0]!.reason='\ud800';
  await expect(validateAssessment(a,f.report,f.plan)).rejects.toThrow();
 });
 it.each(['pass-finding','rework-positive-only','duplicate-finding','policy-text'])('拒绝与存储合同不一致的 %s',async(kind)=>{
  const f=await assessmentFixture(kind==='duplicate-finding'?['成果符合原需求','成果符合原需求']:undefined);
  if(kind==='policy-text'){
   f.plan.acceptance[1]='篡改固定政策';const directory=JSON.parse(f.plan.acceptance[5]!);const digest='sha256:'+await sha256Hex(new Blob([f.plan.acceptance[1]!]));directory.items[1].requirementDigest=digest;directory.items[1].id='criterion-1-'+digest.slice(7,23);f.plan.acceptance[5]=JSON.stringify(directory);
   await expect(readPlanCriteria(f.plan)).rejects.toThrow();return;
  }
  f.report.verdict='rework';
  if(kind==='pass-finding'){f.report.findings=[{id:'finding',nodeIds:['author'],requirement:f.assessment.criteria[0]!.requirement,observation:'缺项',requestedChange:'补充'}];f.assessment.checks[0]!.findingIds=['finding'];}
  if(kind==='duplicate-finding'){
   // 重复原业务文字仍须独立finding；这里重复关联先由唯一性规则拒绝。
   f.report.findings=[{id:'finding',nodeIds:['author'],requirement:f.assessment.criteria[0]!.requirement,observation:'缺项',requestedChange:'补充'}];f.assessment.checks[0]!.assessment='fail';f.assessment.checks[0]!.findingIds=['finding'];f.assessment.checks[1]!.assessment='unknown';f.assessment.checks[1]!.findingIds=['finding'];
  }
  f.assessment.reportDigest=await reviewHash(f.report);
  await expect(validateAssessment(f.assessment,f.report,f.plan)).rejects.toThrow();
 });
 it('空引文只绑定原0字节摘要，空白引文合法但不能丢失来源覆盖',async()=>{
  const f=await assessmentFixture();
  f.assessment.checks.forEach(c=>c.evidence[0]!.quote='');
  await expect(validateAssessment(f.assessment,f.report,f.plan)).rejects.toThrow();
  f.assessment.sources[2]!.digest='sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
  await expect(validateAssessment(f.assessment,f.report,f.plan)).resolves.toBeDefined();
  f.assessment.sources[2]!.digest='sha256:'+'c'.repeat(64);
  f.assessment.checks.forEach(c=>c.evidence[0]!.quote=' \n\t');
  await expect(validateAssessment(f.assessment,f.report,f.plan)).resolves.toBeDefined();
 });
 it('unknown可绑定真实finding而非被改写为accept',async()=>{const f=await assessmentFixture();const c=f.assessment.criteria[0]!;f.report.verdict='rework';f.report.findings=[{id:'finding',nodeIds:['author'],requirement:c.requirement,observation:'依据未提供',requestedChange:'澄清所需事实'}];f.assessment.checks[0]!.assessment='unknown';f.assessment.checks[0]!.findingIds=['finding'];f.assessment.reportDigest=await reviewHash(f.report);await expect(validateAssessment(f.assessment,f.report,f.plan)).resolves.toBeDefined();});
 it('完整digest不能用短ID碰撞替代，未知目录不能按旧版降级',async()=>{const f=await assessmentFixture();f.plan.acceptance[0]='改变原要求';await expect(readPlanCriteria(f.plan)).rejects.toThrow();f.plan.acceptance[5]=JSON.stringify({profile:'task-review-criteria/v99',items:[]});await expect(readPlanCriteria(f.plan)).rejects.toThrow();});
});
