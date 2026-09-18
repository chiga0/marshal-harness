import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {encode} from '../packages/task-store/store.ts';
const hash=value=>digest(encode(value));
export const digest=bytes=>'sha256:'+createHash('sha256').update(bytes).digest('hex');
const replace=(text,old,next)=>{assert.ok(text.includes(old),'冻结反例内容不匹配');return text.replace(old,next);};
export function correctHtml(content) {
  let text=replace(content,'>立即报名</a>','>报名信息（演示）</a>');
  text=replace(text,'<p>本次聚会将围绕一本共读书目展开交流，不设门槛，欢迎喜欢阅读、愿意倾听与表达的朋友到来。带上你的思考与疑问，和一个安静的下午。</p>','<p>活动内容建议：阅读与交流。以下日程仅为邀请页布局示意，不代表已确定的具体安排；已提供的活动开始时间为14:00。</p>');
  text=replace(text,'<h2>活动日程</h2>','<h2>活动日程（建议示意）</h2>');
  text=replace(text,'签到入场，自由就座','活动开始');
  const start=text.indexOf('<section id="signup">'),end=text.indexOf('</section>',start);assert.ok(start>=0&&end>start);
  return text.slice(0,start)+'<section id="signup">\n<h2>报名（演示）</h2>\n<p>本页面仅展示邀请页布局，不办理报名、不收集个人信息。顶部按钮仅定位到本区块；实际报名方式未提供。</p>\n</section>'+text.slice(end+'</section>'.length);
}
const recoveryDesign='实施设计（建议方案）：只读收集用户本地Markdown，提供全文检索与按标签浏览/整理，全部离线运行，无账号、外网依赖或发布通道。用户原笔记不改写、不移动、不重命名、不删除；工具所有数据仅存于独占本地目录。权威数据用data.sqlite保存notes（note_id、source_path、content_hash、content_text、title、mtime、tags、row_version）、批次表和变更日志；全文FTS索引单独存index.sqlite，仅作派生缓存，标签视图由权威notes.tags生成，不回写原笔记。标签在本地解析front matter或正文标签。source_path唯一；相同路径和内容哈希重导入为no-op；相同内容不同路径按固定规则仅登记别名，不产生第二个内容条目；同路径内容变化更新该条目。命中相互冲突的现存行时整批拒绝、状态不变，不猜测合并。每批导入以一个data.sqlite事务提交：批次ID、顺序号、操作类型insert/update/alias、每个受影响对象的完整before-image（新增为null）、完整after-image及before/after row_version、批次状态applied、单调data_generation、索引待刷新标记均在该事务内落盘。before-image包含正文、标签、路径/别名和元数据；不能只保存哈希。提交失败整体回滚。索引刷新读取一致的权威数据快照，在本地临时index库重建，校验后关闭旧索引句柄并原子替换；新索引记录source_generation。检索只有在索引完整且generation与data_generation一致时可用，否则明确等待重建，不能把权威库与索引的跨文件更新伪称原子。索引损坏时仅删除/重建派生index.sqlite，来源是data.sqlite内已保存的完整正文/标签；不依赖被用户改动后的原笔记还原旧内容。权威data.sqlite损坏不在“只损坏索引”的无损恢复承诺中，须明确报告并使用已验证的本地备份，不能把路径或哈希当数据。每次导入报告新增/更新/跳过/失败计数；相同输入重复导入逻辑条目、正文和检索命中均不变。';
const recoveryRollback='按批次幂等撤销：仅允许撤销最新仍为applied的批次，须先获得工具本地独占写锁；如存在后继applied批次，必须先逆序撤销后继批次。撤销前在data.sqlite同一事务中检查当前相关对象版本和内容仍匹配该批次after-image；任何不匹配或并发写入冲突都拒绝且不改变数据。按日志逆序执行：insert仅删除该批新建对象，update恢复完整before-image，alias恢复原路径映射；不得按batch_id一概删除更新过的既有条目。对象前值、标签和正文恢复与批次状态标为undone、索引待刷新及data_generation递增一起原子提交。再次请求同一已undone批次返回无变化，不重复删除或覆盖后继数据；撤销日志保留供此判断。权威事务提交后按设计重建派生索引，期间阻止过期索引查询，完成后才报告检索恢复。检查：导入A并保存逻辑数据/检索快照，再执行B覆盖正文、增标签、增新笔记；撤销B后所有既有对象及检索结果与A快照一致，B新增对象消失，原笔记内容摘要不变；再次撤销B无变化。另加C后直接撤销B必须无副作用拒绝，先撤销C再撤销B才能通过；并发导入与撤销至多一个获得写锁，败者无部分写入。索引损坏检查：只破坏index.sqlite，确认检出后以完好data.sqlite重建，与同一generation的检索快照一致；权威数据也损坏时不得声称成功。整体卸载回退另可在停止工具后清空工具目录，明确这会删除工具历史，仅保留从未改动的用户原笔记。';
export function correctRecovery(input) {
  const result=structuredClone(input);
  for(const material of result.materials){
    if(material.nodeId==='integrator') {
      const value=JSON.parse(material.content);value.design=recoveryDesign;value.rollback=recoveryRollback;
      value.acceptance=value.acceptance.map(text=>text.startsWith('索引损坏验收：')?'索引损坏验收：记录同一data_generation的检索基线，只截断或改写派生index.sqlite；自检必须拒绝查询损坏/过期索引，再从完整data.sqlite正文和标签重建，检索结果与基线一致。权威库也损坏时必须失败，不以元数据冒充恢复材料。':text);
      value.acceptance.push('批次撤销验收：A导入后记录完整逻辑数据及检索；B包含更新、新增、标签或别名变化。撤销B后与A一致，重复撤销无变化；有后继C时直接撤销B必须无副作用拒绝，逆序撤销C再B可恢复。并发冲突或版本不匹配必须无部分写入，原笔记全程哈希不变。');
      material.content=JSON.stringify(value);
    } else if(material.nodeId==='req-design-author') {
      const start=material.content.indexOf('## 2. 数据设计'),end=material.content.indexOf('## 3. 边界声明');assert.ok(start>=0&&end>start);
      material.content=material.content.slice(0,start)+'## 2. 数据设计（修正建议）\n'+recoveryDesign+'\n\n按批次撤销采用完整before-image和版本检查，详见风险分析；不从哈希恢复正文。\n\n'+material.content.slice(end);
      material.content=replace(material.content,'批次号、幂等键、重建索引、单文件存储','批次号、幂等键、完整前值日志、派生索引重建');
    } else if(material.nodeId==='risk-acceptance-author') {
      material.content='# 风险分析、独立验收与回退设计（中间分析，修正建议）\n依据原三个附件，不改变离线、本地、禁止外部发布及原笔记只读约束。\n'+recoveryDesign+'\n\n'+recoveryRollback+'\n独立验收另须在断网环境完成导入、全文检索、标签整理；重复导入条目和命中不变；检查所有新增数据只在工具目录、源文件名位置和内容摘要不变。';
    }
    if(material.nodeId) {material.bytes=Buffer.byteLength(material.content);material.digest=digest(material.content);assert.ok(material.bytes<=8192);}
  }
  return result;
}
export function makePairs(sources) {
  return ['S02','C01'].flatMap(id=>{
    const input=structuredClone(sources[id]);
    const positive=id==='C01'?correctRecovery(input):structuredClone(input);
    if(id==='S02'){assert.equal(positive.materials.length,1);const m=positive.materials[0];m.content=correctHtml(m.content);m.bytes=Buffer.byteLength(m.content);m.digest=digest(m.content);assert.ok(m.bytes<=7000);}
    // Synthetic component references have no Core authority. Preserve business
    // task/plan/history, but bind every current selection to replacement bytes.
    const componentReferences=positive.materials.filter(m=>m.nodeId).map(m=>{
      const manifest=[{path:m.path??'result.md',digest:m.digest,bytes:m.bytes}];
      const result={profile:'reviewer-component-result/v1',nodeId:m.nodeId,workerId:m.workerId??null,manifest};
      return {nodeId:m.nodeId,manifest,result,manifestDigest:digest(JSON.stringify(manifest)),resultDigest:hash(result)};
    });
    positive.selection=(positive.selection??componentReferences.map(r=>({nodeId:r.nodeId}))).map(s=>{
      const r=componentReferences.find(r=>r.nodeId===s.nodeId);assert.ok(r);return {...s,resultDigest:r.resultDigest,manifestDigest:r.manifestDigest};
    });
    positive.selectionDigest=hash(positive.selection);
    if(positive.snapshot.selection)positive.snapshot.selection=structuredClone(positive.selection);
    for(const item of positive.snapshot.readSet??[])if(item.kind==='selected')item.digest=positive.selectionDigest;
    delete positive.inputDigest;positive.inputDigest=hash(positive);
    return [{id:id+'-negative',expected:['rework','reject'],input,derived:false},{id:id+'-positive',expected:['accept'],input:positive,derived:true,componentReferences}];
  });
}
