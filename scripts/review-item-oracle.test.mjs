import test from 'node:test';import assert from 'node:assert/strict';
import {compareItemExpectations,evaluateItemOracle} from './review-item-oracle.mjs';
const counter={initial:'初态',operation:'动作',failure:'中断',result:'后态',recovery:'来源'};
function minimal(kind,overrides={}) {
 const criteria=[{id:'process',policyId:null,requirement:'制作过程未运行或部署该页面，交付后交由独立消费者检查'},...['scope','facts','effects','recovery'].map(id=>({id,policyId:id}))];
 const states={process:'unknown',scope:'unknown',facts:'pass',effects:'pass',recovery:'pass',...overrides};
 return {report:{verdict:'rework'},assessment:{criteria,checks:criteria.map(c=>({itemId:c.id,assessment:states[c.id],counterexample:['effects','recovery'].includes(c.id)?counter:null}))}};
}
const failures=(id,r)=>compareItemExpectations(id,r.report,r.assessment).filter(c=>!c.matched).map(c=>c.name);
test('事实正例不等于过程正例：无过程证据unknown且非accept',()=>{
 const r=minimal();assert.deepEqual(failures('S02-positive',r),[]);
 r.assessment.checks.find(c=>c.itemId==='process').assessment='pass';r.report.verdict='accept';
 assert.deepEqual(failures('S02-positive',r),['process-evidence-missing','aggregate-not-accept']);
});
test('原负例不能用合法accept及结构化反例掩盖缺失恢复分支',()=>{
 const r=minimal();r.report.verdict='accept';assert.deepEqual(failures('C01-negative',r),['recovery','aggregate-not-accept']);
 r.report.verdict='rework';r.assessment.checks.find(c=>c.itemId==='recovery').assessment='fail';assert.deepEqual(failures('C01-negative',r),[]);
});
test('恢复正例仍须适用effects并保留过程缺口',()=>{
 const r=minimal();assert.deepEqual(failures('C01-positive',r),[]);
 const c=r.assessment.checks.find(c=>c.itemId==='effects');c.assessment='not-applicable';c.counterexample=null;
 r.assessment.checks.find(c=>c.itemId==='scope').assessment='pass';r.report.verdict='accept';
 assert.deepEqual(failures('C01-positive',r),['effects','effects-counterexample-required','scope','aggregate-not-accept']);
});
test('缺行/重复行不能靠位置或相同标签蒙混，effects pass缺反例失败',()=>{
 const r=minimal();r.assessment.checks.push({...r.assessment.checks[0]});
 r.assessment.checks.find(c=>c.itemId==='effects').counterexample=null;
 assert.deepEqual(failures('S02-positive',r),['process-evidence-missing','effects-counterexample-required']);
});
test('负例facts独立要求fail，不把aggregate rework当正确识别',()=>{
 const r=minimal();assert.deepEqual(failures('S02-negative',r),['facts']);
});
test('原expected标签不授予结果资格；未知和漂移输入拒绝',()=>{
 assert.equal(evaluateItemOracle('S02-positive',{valid:true,expected:['accept']},{inputDigest:'invented'}).status,'INVALID_INPUT');
 assert.equal(evaluateItemOracle('unknown',{},{}).reason,'unknown_oracle_case');
 assert.throws(()=>compareItemExpectations('unknown',{},{}));
});
