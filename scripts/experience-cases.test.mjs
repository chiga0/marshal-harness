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
