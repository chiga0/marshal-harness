import test from 'node:test';import assert from 'node:assert/strict';
import {correctHtml,correctRecovery,makePairs,digest} from './reviewer-component-cases.ts';
const html='<a href="#signup">立即报名</a><p>本次聚会将围绕一本共读书目展开交流，不设门槛，欢迎喜欢阅读、愿意倾听与表达的朋友到来。带上你的思考与疑问，和一个安静的下午。</p><h2>活动日程</h2>签到入场，自由就座<section id="signup"><form><input><button type="submit">提交</button></form></section><footer>蓝杉读书会2026-10-17 14:00 城市图书馆二层</footer>';
const c01={snapshot:{plan:{acceptance:['允许按批次撤销'],nodes:[{goal:'幂等导入撤销'}]},task:{input:{intent:'原需求'}}},selection:[{nodeId:'integrator'}],materials:[{nodeId:'integrator',content:JSON.stringify({requirements:['离线可用','仅本地存储','禁止外部发布'],design:'old',acceptance:['索引损坏验收：旧方法','仅本地存储验收：原要求'],rollback:'old',sources:['requirements.txt','constraints.txt','risks.txt']})},{nodeId:'req-design-author',content:'原始需求\n## 2. 数据设计\n旧设计\n## 3. 边界声明\n批次号、幂等键、重建索引、单文件存储'},{nodeId:'risk-acceptance-author',content:'old'}]};
test('正例保留原需求与批准scope，负例逐字保留且互不污染',()=>{
 const source={S02:{snapshot:{task:{input:{intent:'原HTML需求'}}},materials:[{nodeId:'a',content:html}]},C01:c01},before=structuredClone(source);
 const pairs=makePairs(source);assert.deepEqual(source,before);assert.deepEqual(pairs[0].input,source.S02);assert.deepEqual(pairs[2].input,source.C01);
 assert.deepEqual(pairs[3].input.snapshot,c01.snapshot);assert.deepEqual(pairs[1].input.snapshot,source.S02.snapshot);
 assert.notEqual(pairs[0].input.materials[0].content,pairs[1].input.materials[0].content);
 for(const fixture of pairs.filter(x=>x.derived))for(const m of fixture.input.materials.filter(m=>m.nodeId)){assert.equal(m.digest,digest(m.content));assert.equal(m.bytes,Buffer.byteLength(m.content));}
});
test('HTML修正去掉真实输入/提交入口而保留原锚点与时间地点',()=>{
 const result=correctHtml(html);assert.doesNotMatch(result,/<form|<input|type="submit"/);for(const fact of ['href="#signup"','id="signup"','2026-10-17 14:00','城市图书馆二层'])assert.ok(result.includes(fact));assert.throws(()=>correctHtml('unexpected fixture'));
});
test('恢复正例保留批次撤销而非删掉要求，源材料不可变，结果满足原JSON形状',()=>{
 const before=structuredClone(c01),result=correctRecovery(c01);assert.deepEqual(c01,before);
 const old=JSON.parse(c01.materials[0].content),value=JSON.parse(result.materials[0].content);assert.deepEqual(Object.keys(value),Object.keys(old));assert.deepEqual(value.requirements,old.requirements);assert.deepEqual(value.sources,old.sources);assert.ok(value.acceptance.includes('仅本地存储验收：原要求'));
 assert.match(value.rollback,/update恢复完整before-image/);assert.match(value.rollback,/后继applied批次/);assert.match(value.rollback,/已undone批次返回无变化/);
});

test('派生引用闭合且不保留旧 selection 摘要',()=>{
 const s02={snapshot:{selection:[{nodeId:'a',resultDigest:'old'}],readSet:[{kind:'selected',digest:'old'}],task:{intent:'unchanged'}},selection:[{nodeId:'a',resultDigest:'old'}],materials:[{nodeId:'a',content:html}]};
 const positive=makePairs({S02:s02,C01:c01})[1];
 assert.deepEqual(positive.input.snapshot.selection,positive.input.selection);
 assert.equal(positive.input.snapshot.readSet[0].digest,positive.input.selectionDigest);
 assert.equal(positive.input.selection[0].resultDigest,positive.componentReferences[0].resultDigest);
 assert.equal(positive.componentReferences[0].manifest[0].digest,positive.input.materials[0].digest);
 assert.deepEqual(positive.input.snapshot.task,s02.snapshot.task);
});
