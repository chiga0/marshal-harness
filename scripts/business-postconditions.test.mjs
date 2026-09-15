import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {verifyOrders,verifyGuide,verifyRecoveryModel} from './business-postconditions.mjs';
import {cases} from './experience-cases.mjs';
const fixtures=JSON.parse(fs.readFileSync(new URL('./fixtures/business-postconditions/contracts.json',import.meta.url)));
const expected={regions:{east:{count:2,cents:1200},west:{count:2,cents:600}},total:{count:4,cents:1800}};
const source=cases.M01.files[0].content;
test('M01 original frozen orders: exact cents including zero and refunds, excluding cancelled',()=>{
  const r=verifyOrders(source,JSON.stringify(expected));assert.equal(r.status,'pass');assert.equal(r.authority,false);
  assert.deepEqual(r.expected,expected);assert.match(r.inputDigest,/^sha256:[a-f0-9]{64}$/);
  const reordered={total:{cents:1800,count:4},regions:{west:{cents:600,count:2},east:{cents:1200,count:2}}};
  assert.equal(verifyOrders(source,JSON.stringify(reordered)).status,'pass');
});
test('M01 independent arithmetic catches zero exclusion, refund exclusion/sign errors and region/total inconsistencies',()=>{
  for(const mutate of [r=>r.regions.east.count--,r=>{r.regions.west.cents=800;r.total.cents=2000;},r=>r.total.cents=2200,r=>r.regions.east.cents=2199]) {
    const bad=structuredClone(expected);mutate(bad);assert.equal(verifyOrders(source,JSON.stringify(bad)).code,'order_total_mismatch');
  }
});
test('M01 malformed/duplicate/extra/wrong numeric output is never normalized into success',()=>{
  for(const value of ['```json\n'+JSON.stringify(expected)+'\n```',JSON.stringify(expected).replace('"total":','"total":{},"total":'),JSON.stringify({...expected,note:'extra'}),JSON.stringify({...expected,total:{count:4,cents:'1800'}}),'NaN','{"regions":{}}'])
    assert.equal(verifyOrders(source,value).code,'invalid_order_report');
});
test('M01 unsupported input and sum overflow are not verified rather than rounded or silently ignored',()=>{
  for(const rows of [[{region:'north',status:'paid',cents:1}],[{region:'east',status:'other',cents:1}],[{region:'east',status:'paid',cents:0.1}]])assert.equal(verifyOrders(JSON.stringify(rows),JSON.stringify(expected)).status,'not-verified');
  const rows=[{region:'east',status:'paid',cents:Number.MAX_SAFE_INTEGER},{region:'west',status:'paid',cents:1}];
  assert.equal(verifyOrders(JSON.stringify(rows),JSON.stringify(expected)).code,'unrepresentable_order_total');
});
test('M01 rebinding source changes expectation; reordering orders preserves numerical result',()=>{
  const rows=JSON.parse(source);assert.equal(verifyOrders(JSON.stringify(rows.reverse()),JSON.stringify(expected)).status,'pass');
  rows.push({region:'east',status:'paid',cents:7});const result=verifyOrders(JSON.stringify(rows),JSON.stringify(expected));
  assert.equal(result.status,'fail');assert.equal(result.expected.total.cents,1807);assert.equal(result.expected.total.count,5);
});
const guide=()=>['# '+fixtures.guide.title,'时间：'+fixtures.guide.schedule,'地点：'+fixtures.guide.location,'## 参与步骤',...fixtures.guide.steps.map((s,i)=>(i+1)+'. '+s),'## 注意事项',...fixtures.guide.notes.map(s=>'- '+s)].join('\n');
test('S01 explicitly approved finite text has exact facts, three steps, two notes',()=>{
  assert.equal(verifyGuide(fixtures.guide,guide()).status,'pass');
  assert.equal(verifyGuide(fixtures.guide,guide().replaceAll('\n','\r\n')).status,'pass');
});
test('S01 additional facilities, obligations, fee/free promises and hidden contradictions cannot receive deterministic pass',()=>{
  for(const extra of ['现场提供材料。','请先到签到处报名。','免报名费。','费用20元。','实际改为三层。','<!-- 实际在别处 -->'])
    assert.equal(verifyGuide(fixtures.guide,guide()+'\n'+extra).status,'not-verified');
  assert.equal(verifyGuide(fixtures.guide,guide().replace('时间：每周六14:00','时间：每周日14:00')).code,'explicit_fact_mismatch');
  assert.equal(verifyGuide(fixtures.guide,guide().replace('地点：城市图书馆二层','地点：城市图书馆三层')).code,'explicit_fact_mismatch');
});
test('S01 legitimate prose outside grammar remains unknown; fixture does not rewrite original user requirement',()=>{
  assert.equal(verifyGuide(fixtures.guide,guide().replace('## 参与步骤','## 参加方式')).status,'not-verified');
  assert.equal(verifyGuide(fixtures.guide,guide().replace('1. 查看','1. 建议查看')).status,'not-verified');
});
test('C01 complete-and-consistent model passes all 15 bounded crash/restart scenarios without altering source',()=>{
  const before=JSON.stringify(fixtures.notes),r=verifyRecoveryModel(fixtures.recoveryPositive,fixtures.notes);
  assert.equal(r.status,'pass');assert.equal(r.scenarios.length,15);assert.ok(r.scenarios.every(s=>s.passed));assert.equal(JSON.stringify(fixtures.notes),before);
  assert.ok(r.unverified.includes('真实文件系统持久化/锁实现'));
});
test('C01 record-exists skip produces concrete failed second-step recovery rather than inventing supplement',()=>{
  const r=verifyRecoveryModel(fixtures.recoveryNegative,fixtures.notes),failure=r.scenarios.find(s=>s.initial==='empty'&&s.crashAfter===1);
  assert.equal(r.status,'fail');assert.equal(failure.restart.skipped,true);assert.equal(failure.final.indexMatchesSource,false);assert.equal(failure.passed,false);
});
test('C01 invalid execution ordering and corrupt index are exercised rather than checked as keywords',()=>{
  const bad={...fixtures.recoveryPositive,steps:['record-pending','replace-index','build-temporary','mark-complete']};
  assert.equal(verifyRecoveryModel(bad,fixtures.notes).status,'fail');
  const badSkip=verifyRecoveryModel(fixtures.recoveryNegative,fixtures.notes);
  assert.ok(badSkip.scenarios.filter(s=>s.initial==='corrupt-index').every(s=>!s.passed));
});
test('C01 natural-language claims, missing model, arbitrary callbacks and unsupported sources cannot become proof',()=>{
  for(const model of ['先记录后建索引，重跑等价',{},null,{...fixtures.recoveryPositive,repair:()=>true}])assert.equal(verifyRecoveryModel(model,fixtures.notes).status,'not-verified');
  assert.equal(verifyRecoveryModel(fixtures.recoveryPositive,[...fixtures.notes,fixtures.notes[0]]).status,'not-verified');
});

test('unsupported undefined and null-row inputs return not-verified without inventing an input digest',()=>{
  const orders=verifyOrders(undefined,JSON.stringify(expected));assert.equal(orders.status,'not-verified');assert.equal(orders.inputDigest,null);
  const guideResult=verifyGuide(undefined,guide());assert.equal(guideResult.status,'not-verified');assert.equal(guideResult.inputDigest,null);
  const recovery=verifyRecoveryModel(fixtures.recoveryPositive,[null]);assert.equal(recovery.status,'not-verified');
  const cycle={};cycle.self=cycle;assert.equal(verifyGuide(cycle,guide()).inputDigest,null);
});
