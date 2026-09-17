import test from 'node:test';
import assert from 'node:assert/strict';
import {validateDelivery,SemanticReviewRequired,hasInvitationDate} from './experience-cases.ts';
const wrap=content=>({profile:'generic-files-delivery/v1',scope:'independently-reviewed-files-not-external-effects',files:[{content}]});
test('订单独立oracle拒绝取消订单混入、漏零金额与退款符号错误',()=>{
  const correct={regions:{east:{count:2,cents:1200},west:{count:2,cents:600}},total:{count:4,cents:1800}};
  assert.doesNotThrow(()=>validateDelivery('M01',wrap(JSON.stringify(correct))));
  for(const mutate of [v=>{v.regions.east.count=3;v.regions.east.cents+=999;v.total.count++;v.total.cents+=999;},
    v=>{v.regions.east.count--;v.total.count--;},v=>{v.regions.west.cents=1000;v.total.cents=2200;}]) {
    const value=structuredClone(correct);mutate(value);assert.throws(()=>validateDelivery('M01',wrap(JSON.stringify(value))));
  }
});
test('说明文档不能只因含关键字通过，必须有三步骤及两注意事项',()=>{
  const text='# 蓝杉读书会\n每周六14:00，城市图书馆二层\n1. 确认安排\n2. 前往给定地点\n3. 参与交流\n## 注意事项\n- 保持安静\n- 爱护图书';
  assert.doesNotThrow(()=>validateDelivery('S01',wrap(text)));
  assert.throws(()=>validateDelivery('S01',wrap(text.replace('3. 参与交流','参与交流'))));
  assert.throws(()=>validateDelivery('S01',wrap(text.replace('- 爱护图书',''))));
});
test('复杂方案拒绝漏资料、空验收与额外顶层替代方案；只读决定不能同时允许写入',()=>{
  const value={requirements:['离线可用','仅本地存储','禁止外部发布'],design:'本地笔记建立可重建索引，所有查询离线完成，幂等导入保护原文件，重复输入不会产生重复记录。',acceptance:['断网后检索成功','重复导入结果一致','原笔记保持不变'],rollback:'关闭新索引并恢复备份，独立读取原文件核对内容及数量。',sources:['requirements.txt','constraints.txt','risks.txt']};
  assert.doesNotThrow(()=>validateDelivery('C01',wrap(JSON.stringify(value))));
  for(const mutate of [v=>{v.sources.pop();},v=>{v.acceptance[1]='';},v=>{v.alternative='允许外部发布';}]) {const bad=structuredClone(value);mutate(bad);assert.throws(()=>validateDelivery('C01',wrap(JSON.stringify(bad))));}
  const decision='10月20日上线，仅只读，禁止写入。迁移：备份、只读验证、回退检查。';
  assert.doesNotThrow(()=>validateDelivery('M02',wrap(decision)));
  assert.throws(()=>validateDelivery('M02',wrap(decision+'\n允许写入。')));
});

test('HTML字节限制按UTF-8而非字符数验收',()=>{
  const base='<!doctype html><html>蓝杉读书会 2026-10-17 14:00 城市图书馆二层</html>';
  const exact=base+' '.repeat(7000-Buffer.byteLength(base));
  assert.doesNotThrow(()=>validateDelivery('S02',wrap(exact)));
  assert.throws(()=>validateDelivery('S02',wrap(exact+' ')));
  assert.throws(()=>validateDelivery('S02',wrap(base+'蓝'.repeat(2400))));
});

test('S01独立区块与标题拒绝借编号、错标题、多步骤与空注意项',()=>{
  const source='# 蓝杉读书会\n每周六14:00，城市图书馆二层\n## 参与步骤\n1. 确认时间\n2. 前往地点\n3. 参与交流\n## 注意事项\n1. 保持安静\n2. 爱护图书';
  assert.doesNotThrow(()=>validateDelivery('S01',wrap(source)));
  for(const wrong of [source.replace('1. 确认时间\n2. 前往地点\n',''),source.replace('# 蓝杉读书会','# 香樟读书会\n蓝杉读书会'),source.replace('## 注意事项','4. 离开\n## 注意事项'),source.replace('2. 爱护图书','2. ')])assert.throws(()=>validateDelivery('S01',wrap(wrong)),error=>!(error instanceof SemanticReviewRequired));
  const chinese=source.replace('1. 确认时间','一、确认时间').replace('2. 前往地点','二、前往地点').replace('3. 参与交流','三、参与交流');
  assert.doesNotThrow(()=>validateDelivery('S01',wrap(chinese)));
  assert.doesNotThrow(()=>validateDelivery('S01',wrap(source.replace('2. 前往地点','1. 前往地点').replace('3. 参与交流','1. 参与交流'))),'Markdown允许重复1.，渲染后仍为三条编号步骤');
  const metadata=source.replace('每周六14:00，城市图书馆二层\n## 参与步骤','- 时间：每周六14:00\n- 地点：城市图书馆二层');
  assert.doesNotThrow(()=>validateDelivery('S01',wrap(metadata)));
});
test('无法可靠识别结构明确要求语义审查，不能算自动通过或确定业务失败',()=>{
  const text='# 蓝杉读书会\n每周六14:00，城市图书馆二层\n参加方法：先确认时间，再前往地点，最后交流。\n温馨提醒：保持安静；爱护图书。';
  assert.throws(()=>validateDelivery('S01',wrap(text)),{name:'SemanticReviewRequired',code:'semantic_review_required'});
  assert.throws(()=>validateDelivery('S01',wrap(text+'报名费100元')),error=>!(error instanceof SemanticReviewRequired));
});
test('S02中文与ISO日期等价，错误年份与日期仍拒绝；DOM复用同一事实判断',()=>{
  const base='<!doctype html><html><body>蓝杉读书会 DATE 14:00 城市图书馆二层</body></html>';
  for(const date of ['2026-10-17','2026年10月17日']) {
    assert.doesNotThrow(()=>validateDelivery('S02',wrap(base.replace('DATE',date))));
    assert.equal(hasInvitationDate('活动日期：'+date),true);
  }
  for(const date of ['2026-10-18','2027年10月17日','2026-10-170'])assert.throws(()=>validateDelivery('S02',wrap(base.replace('DATE',date))));
});
test('C01短表述与复合验收不因未授权长度或条数阈值误拒，语义仍须独立检查',()=>{
  const value={requirements:['离线可用','仅本地存储','禁止外部发布'],design:'只读原文派生本地索引，幂等导入。',acceptance:['断网查询及重复导入核对。','破坏索引后重建并逐项比对原结果。'],rollback:'只读原文重建，缺失则中止。',sources:['requirements.txt','constraints.txt','risks.txt']};
  assert.doesNotThrow(()=>validateDelivery('C01',wrap(JSON.stringify(value))));
  for(const key of ['design','rollback'])assert.throws(()=>validateDelivery('C01',wrap(JSON.stringify({...value,[key]:' '}))));
  assert.throws(()=>validateDelivery('C01',wrap(JSON.stringify({...value,acceptance:[]}))));
});
