import assert from 'node:assert/strict';
import {encode} from '../packages/task-store/store.mjs';
import {digest} from './reviewer-component-cases.mjs';
const hash=value=>digest(encode(value));
const authority='数据来源与恢复边界：原Markdown文件是正文及源内标签的权威来源；工具内用户创建、重命名、归并的标签及挂接关系是独立的用户创造数据，保存在authoritative.sqlite的user_tags/user_links表，不能从原笔记推导。固定规范化路径映射到持久note_id，标签关联指向该ID；同一路径正文变化仍保留ID，content_hash只用于检测变化及辅助去重，不是身份主键。不同路径即使内容相同仍登记不同note_id，避免错误合并各自标签；不自动推断跨路径移动身份。重复导入同一路径复用ID、不覆盖用户标签或关联。只有FTS与解析得到的源标签缓存属于可重建派生数据，另存index.sqlite。导入及标签整理的权威写入采用本地事务；重建期间取得本地写锁并暂停整理/导入，以稳定原笔记快照和完好的权威标签表构建临时索引，核对成功后替换旧索引，再释放锁。源文件读取前后摘要变化则本次重建失败，不报告一致；不修改原笔记。';
const recovery='恢复只针对损坏的派生index.sqlite，绝不删除authoritative.sqlite、user_tags/user_links或持久note_id/路径映射。以原笔记当前稳定正文和完好的工具权威标签/关联联合重建索引；检索损坏时停止返回不可信结果，重建完成且校验通过后恢复查询。重复导入异常只重建派生索引，不通过清空权威标签修复。若权威标签表也损坏且没有可验证的本地完整备份，必须报告无法恢复并停止，不声称仅从原笔记可重建；本方案不承诺恢复用户主动修改前的原笔记历史版本。';
const check='独立恢复验收：准备原文不含标签T的笔记A，导入后在工具创建T并挂接A，记录权威标签/关联及检索基线；只破坏index.sqlite，确认查询拒绝，再恢复索引；标签T、A-T关联、相同输入快照的检索结果必须与基线一致，原笔记摘要不变。重复导入A一次仍须等价，不得以新note_id断开关联。只损坏权威标签表且无备份必须明确失败，不得误报恢复成功。';
export function makeTagPair(source){
 const input=structuredClone(source),positive=structuredClone(source);
 for(const m of positive.materials.filter(m=>m.nodeId)){
  if(m.nodeId==='integrator'){
   const v=JSON.parse(m.content);assert.ok(v.design.includes('工具内用户自建标签'));assert.ok(v.rollback.includes('标签映射'));
   v.design=v.design.replace('索引为派生数据，可从原笔记完整重建，与源数据分离','索引为派生数据，可从原笔记与工具权威标签联合重建，与权威数据分离');
   v.design=v.design.replace('导入批次记录与索引可整体重建支撑索引损坏时的检测、回退与恢复','保留权威登记与用户标签，仅派生索引可重建支撑索引损坏时的检测与恢复');
   v.design=v.design.replace('以内容哈希作为同一笔记的确定性去重主键支撑幂等导入','以规范化路径映射持久note_id支撑幂等导入，内容哈希仅辅助检测变化');
   v.design+=' '+authority;v.rollback=recovery;
   v.acceptance=v.acceptance.map(x=>x.startsWith('回退检查：')?check:x);m.content=JSON.stringify(v);
  }else if(m.nodeId==='author-requirements'){
   assert.ok(m.content.includes('可从原笔记完整重建'));m.content=m.content.replace('可从原笔记完整重建','可从原笔记与工具权威标签联合重建');m.content=m.content.replace('以内容哈希作为同一笔记的识别主键','以规范化路径映射持久note_id识别同一笔记，内容哈希仅辅助检测变化').replace('内容哈希主键（支撑幂等导入与重复导入检查）','规范化路径到持久note_id的映射（支撑幂等导入与重复导入检查）');m.content+='\n\n## 权威数据与恢复接缝\n'+authority+'\n'+recovery+'\n';
  }else if(m.nodeId==='author-risks'){
   assert.ok(m.content.includes('可整体删除并重建索引与标签数据'));
   m.content=m.content.replace('导入过程只写派生数据（索引、标签映射）','导入过程写工具登记及派生索引，不覆盖用户创造的权威标签与关联');
   m.content=m.content.replace('可整体删除并重建索引与标签数据','仅可删除并重建派生索引，权威标签与关联保留');
   m.content=m.content.replace('全程不打开/改写原笔记文件','全程仅只读打开原笔记，不改写原文件').replace('笔记内容/路径的确定性标识','规范化路径到持久note_id的固定映射');
   m.content+='\n\n## 来源分类与独立恢复检查\n'+authority+'\n'+recovery+'\n'+check+'\n';
  }else assert.fail('unknown frozen author');
  m.bytes=Buffer.byteLength(m.content);m.digest=digest(m.content);assert.ok(m.bytes<=8192);
 }
 const componentReferences=positive.materials.filter(m=>m.nodeId).map(m=>{const manifest=[{path:m.path,digest:m.digest,bytes:m.bytes}],result={profile:'reviewer-component-result/v1',nodeId:m.nodeId,workerId:m.workerId,manifest};return {nodeId:m.nodeId,manifest,result,manifestDigest:digest(JSON.stringify(manifest)),resultDigest:hash(result)};});
 positive.selection=positive.selection.map(s=>{const r=componentReferences.find(r=>r.nodeId===s.nodeId);return {...s,resultDigest:r.resultDigest,manifestDigest:r.manifestDigest};});
 positive.selectionDigest=hash(positive.selection);positive.snapshot.selection=structuredClone(positive.selection);
 for(const r of positive.snapshot.readSet)if(r.kind==='selected')r.digest=positive.selectionDigest;
 delete positive.inputDigest;positive.inputDigest=hash(positive);
 return [{id:'C01-tags-negative',expected:['rework','reject'],input,derived:false},{id:'C01-tags-positive',expected:['accept'],input:positive,derived:true,componentReferences}];
}
