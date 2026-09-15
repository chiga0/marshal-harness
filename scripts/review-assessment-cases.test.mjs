import test from 'node:test';import assert from 'node:assert/strict';
import {correctCandidate,patches,makeAssessmentPairs,validateComponentInput,businessSnapshot,bindComponentReferences,validateExample,digest} from './review-assessment-cases.mjs';
const sample=id=>id==='S02'?'<html>2026-10-17 14:00 城市图书馆二层 <a href="#signup"'+patches.S02.map(p=>p.before).join('\n')+'<section id="signup"></section></html>':JSON.stringify({requirements:['离线可用','仅本地存储','禁止外部发布'],design:'设计前文；'+patches.C01.design[0].before+'后文不变',acceptance:['前项不变',patches.C01.acceptance[0].before,'后项不变'],rollback:'1.原文；'+patches.C01.rollback[0].before+'5.原文',sources:['requirements.txt','constraints.txt','risks.txt']});
const input=id=>{const nodeId=id==='S02'?'author':'integrator';const materials=[{nodeId,path:'result.md',content:sample(id)},{path:'inputs/source.txt',content:'原始约束不得改写'}].map(m=>({...m,bytes:Buffer.byteLength(m.content),digest:digest(m.content)}));const v={snapshot:{task:{input:{intent:'原目标'}},plan:{acceptance:['原批准要求']},interactions:[],readSet:[{kind:'selected',digest:'pending'}]},materials,selection:[{nodeId}]};bindComponentReferences(v);return v;};
test('S02只替换明确列出的片段：区块锚点/已给事实/日程仍保留',()=>{
 const original=sample('S02'),correct=correctCandidate('S02',original);let restored=correct;
 for(const {before,after} of [...patches.S02].reverse())restored=restored.replace(after,before);
 assert.equal(restored,original);for(const value of ['2026-10-17','14:00','城市图书馆二层','href="#signup"','id="signup"'])assert.ok(correct.includes(value));
});
test('C01只改幂等机制与相应验收/回退，不替换全文或授权范围',()=>{
 const original=sample('C01'),correct=correctCandidate('C01',original),a=JSON.parse(original),b=JSON.parse(correct);assert.deepEqual(a.requirements,b.requirements);assert.deepEqual(a.sources,b.sources);assert.equal(a.acceptance[0],b.acceptance[0]);assert.equal(a.acceptance[2],b.acceptance[2]);
 let restored=correct;for(const group of Object.values(patches.C01).reverse())for(const {before,after} of group)restored=restored.replace(JSON.stringify(after).slice(1,-1),JSON.stringify(before).slice(1,-1));assert.equal(restored,original);
});
test('来源或失败段发生漂移时拒绝生成，不挑近似文本修成绿',()=>{
 for(const id of ['S02','C01']){const original=sample(id);assert.throws(()=>correctCandidate(id,original.replace(id==='S02'?patches.S02[0].before:patches.C01.design[0].before,'已漂移')));}
 assert.throws(()=>correctCandidate('S02',sample('S02')+patches.S02[0].before));
});
test('四例原输入保持、全引用重算，标签仅外层，未知新目录不伪造',()=>{
 const sources={S02:input('S02'),C01:input('C01')},copy=structuredClone(sources),cases=makeAssessmentPairs(sources);assert.deepEqual(sources,copy);assert.equal(cases.length,4);
 for(const c of cases){validateComponentInput(c.input);assert.deepEqual(businessSnapshot(c.input),businessSnapshot(sources[c.id.slice(0,3)]));assert.ok(!JSON.stringify(c.input).includes(c.id));if(!c.derived)assert.deepEqual(c.input,sources[c.id.slice(0,3)]);else assert.notEqual(c.input.inputDigest,sources[c.id.slice(0,3)].inputDigest);}
 assert.deepEqual(cases.map(c=>c.expected),[['rework'],['accept'],['rework'],['accept']]);
});
test('摘要与字节验证不能接受篡改或仅更新单个材料引用',()=>{
 const v=input('S02');v.materials[0].content+='变更';assert.throws(()=>validateComponentInput(v));v.materials[0].bytes=Buffer.byteLength(v.materials[0].content);v.materials[0].digest=digest(v.materials[0].content);assert.throws(()=>validateComponentInput(v));
});

test('派生旁证必须与实际材料闭合，不能只重算顶层摘要掩盖错绑定',()=>{
 const examples=makeAssessmentPairs({S02:input('S02'),C01:input('C01')});for(const c of examples)validateExample(c);
 const bad=structuredClone(examples[1]);bad.componentReferences[0].manifest[0].bytes++;assert.throws(()=>validateExample(bad));
 const stale=structuredClone(examples[3]);stale.input.snapshot.selection=[];assert.throws(()=>validateExample(stale));
});
