// 无模型组件材料构造；不生成批准目录或 Core 权威。
import fs from 'node:fs';
import assert from 'node:assert/strict';
import {encode} from '../packages/task-store/store.mjs';
import {digest} from './reviewer-component-cases.mjs';
export {digest};
export const patches=JSON.parse(fs.readFileSync(new URL('./fixtures/review-assessment/patches.json',import.meta.url)));
const hash=value=>digest(encode(value));
export function replaceExactly(text,changes) {
  for(const {before,after} of changes){assert.equal(text.split(before).length,2,'补丁必须唯一匹配原候选');text=text.replace(before,after);}
  return text;
}
export function correctCandidate(id,text) {
  if(id==='S02')return replaceExactly(text,patches.S02);
  assert.equal(id,'C01');
  // Replace only JSON-encoded string interiors: preserve every other source byte.
  for(const changes of Object.values(patches.C01))for(const p of changes)
    text=replaceExactly(text,[{before:JSON.stringify(p.before).slice(1,-1),after:JSON.stringify(p.after).slice(1,-1)}]);
  JSON.parse(text);return text;
}
export function businessSnapshot(input) {
  const value=structuredClone(input.snapshot);delete value.selection;
  for(const ref of value.readSet??[])if(ref.kind==='selected')delete ref.digest;
  return value;
}
export function bindComponentReferences(input) {
  const refs=input.materials.filter(m=>m.nodeId).map(m=>{
    const manifest=[{path:m.path??'result.md',digest:m.digest,bytes:m.bytes}];
    const result={profile:'reviewer-component-result/v1',nodeId:m.nodeId,workerId:m.workerId??null,manifest};
    return {nodeId:m.nodeId,manifest,result,manifestDigest:digest(JSON.stringify(manifest)),resultDigest:hash(result)};
  });
  input.selection=input.selection.map(s=>{const r=refs.find(r=>r.nodeId===s.nodeId);assert.ok(r);return {...s,resultDigest:r.resultDigest,manifestDigest:r.manifestDigest};});
  input.selectionDigest=hash(input.selection);
  if(input.snapshot.selection)input.snapshot.selection=structuredClone(input.selection);
  for(const r of input.snapshot.readSet??[])if(r.kind==='selected')r.digest=input.selectionDigest;
  delete input.inputDigest;input.inputDigest=hash(input);return refs;
}
export function makeAssessmentPairs(sources) {
  return ['S02','C01'].flatMap(id=>{
    const negative=structuredClone(sources[id]),positive=structuredClone(negative),node=id==='S02'?'author':'integrator';
    const m=positive.materials.filter(m=>m.nodeId===node);assert.equal(m.length,1);
    m[0].content=correctCandidate(id,m[0].content);m[0].bytes=Buffer.byteLength(m[0].content);m[0].digest=digest(m[0].content);
    assert.ok(m[0].bytes<=(id==='S02'?7000:8192));const componentReferences=bindComponentReferences(positive);
    assert.deepEqual(businessSnapshot(negative),businessSnapshot(positive));
    assert.deepEqual(negative.materials.filter(m=>m.nodeId!==node),positive.materials.filter(m=>m.nodeId!==node));
    return [{id:id+'-negative',expected:['rework'],derived:false,input:negative},
      {id:id+'-positive',expected:['accept'],derived:true,input:positive,componentReferences}];
  });
}
export function validateComponentInput(input) {
  for(const m of input.materials){assert.equal(m.bytes,Buffer.byteLength(m.content));assert.equal(m.digest,digest(m.content));}
  const noDigest=structuredClone(input);delete noDigest.inputDigest;assert.equal(input.inputDigest,hash(noDigest));assert.equal(input.selectionDigest,hash(input.selection));
}
export function validateExample(example) {
  validateComponentInput(example.input);
  if(!example.derived){assert.equal(example.componentReferences,undefined);return;}
  const copy=structuredClone(example.input),expected=bindComponentReferences(copy);
  assert.deepEqual(example.componentReferences,expected,'合成结果/清单必须匹配当前材料');
  assert.deepEqual(copy,example.input,'所有选果/快照/输入引用必须匹配当前材料');
}
