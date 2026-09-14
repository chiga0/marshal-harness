import test from 'node:test';
import assert from 'node:assert/strict';
import {validateDelivery} from './experience-cases.mjs';
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
  const text='# 蓝杉读书会\n每周六14:00，城市图书馆二层\n1. 确认安排\n2. 到场签到\n3. 参与交流\n## 注意事项\n- 保持安静\n- 爱护图书';
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
