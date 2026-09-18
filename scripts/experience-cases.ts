import assert from 'node:assert/strict';

const orders = [
  {region:'east',status:'paid',cents:1200}, {region:'east',status:'paid',cents:0},
  {region:'west',status:'paid',cents:800}, {region:'west',status:'refunded',cents:-200},
  {region:'east',status:'cancelled',cents:999},
];
export const cases = {
  S01: {intent:'为蓝杉读书会写一份简短中文参与说明。事实：每周六14:00，地点城市图书馆二层。标题必须是“蓝杉读书会”，有三条编号参与步骤和两条注意事项。交付一份Markdown文档，不需要其他成果。不添加报名费用或额外地点。这个简单任务不需要多作者分工。'},
  S02: {intent:'制作蓝杉读书会的单文件HTML邀请页。活动时间2026-10-17 14:00，地点城市图书馆二层。页面包括介绍、日程、报名三个区块，报名区id为signup；顶部报名按钮必须链接到#signup。浅色、留白、清晰中文排版、375px手机可读；无外部字体/图片/脚本/网络请求。交付文件内容直接为完整HTML，不要Markdown代码围栏；总计不超过7000字节。只需一位作者，不能在工作过程中运行页面或部署，交付后由独立消费者检查。'},
  M01: {intent:'根据附件orders.json生成订单汇总报告。paid与refunded均计入，cancelled排除；金额按整数分求和，零金额订单仍计数。只交付一个纯JSON文件内容，不要Markdown围栏，形状严格为 {"regions":{"east":{"count":整数,"cents":整数},"west":{"count":整数,"cents":整数}},"total":{"count":整数,"cents":整数}}。独立核对后交付，不调用外部工具或执行SQL。',files:[{name:'orders.json',content:JSON.stringify(orders)}]},
  M02: {intent:'附件中的两个上线规格互相冲突。必须先向用户询问上线日期和是否允许写入，得到答复后才确定最终方案。交付一份中文决定说明，包含上线日期、权限范围、迁移清单；清单必须包含备份、只读验证、回退检查三项。不要按自己偏好解决冲突。',files:[{name:'spec-a.txt',content:'规格A：10月17日上线，允许查询和写入；迁移要求备份、只读验证、回退检查。'},{name:'spec-b.txt',content:'规格B：10月20日上线，仅只读查询；迁移要求备份、只读验证、回退检查。'}],answer:'采用10月20日上线，仅只读查询，禁止写入。保留备份、只读验证、回退检查三项迁移要求。'},
  C01: {intent:'结合三个附件交付一个本地知识整理工具的完整实施方案。请先安排两位互补作者并行：一位分析需求/数据设计，一位分析风险/验收；再由整合作者依赖这两份成果生成最终方案，然后独立评审与验证。必须覆盖离线可用、仅本地存储、禁止外部发布、验收与回退。最终只交付整合方案，采用纯JSON不含代码围栏，结构为 {"requirements":["离线可用","仅本地存储","禁止外部发布"],"design":字符串,"acceptance":[字符串],"rollback":字符串,"sources":["requirements.txt","constraints.txt","risks.txt"]}。中间分析不得作为重复最终成果。',files:[{name:'requirements.txt',content:'需求：收集本地Markdown笔记，离线检索，按标签整理；所有功能离线可用。'},{name:'constraints.txt',content:'约束：仅本地存储；禁止外部发布及外网依赖；用户原笔记不得被修改。'},{name:'risks.txt',content:'风险：索引损坏和重复导入。方案要有独立验收标准、幂等导入及回退检查。'}]},
};

export class SemanticReviewRequired extends Error {
  constructor(message) {super(message);this.name='SemanticReviewRequired';this.code='semantic_review_required';}
}
// Both forms express the frozen date. This is not an arbitrary date parser.
export function hasInvitationDate(text) {
  return /(?<![0-9])(?:2026-10-17(?![0-9])|2026年\s*10月\s*17日)/u.test(text);
}
function validateGuideStructure(text) {
  const lines=text.split(/\r?\n/), nonempty=lines.map(line=>line.trim()).filter(Boolean);
  const heading=nonempty.find(line=>/^#\s+/.test(line));
  if (heading) assert.equal(heading.replace(/^#\s+/, '').replace(/\s+#+$/, '').trim().replace(/^\*\*(.*?)\*\*$/,'$1'),'蓝杉读书会','标题必须为蓝杉读书会');
  else if (nonempty[0]?.replace(/^\*\*(.*?)\*\*$/,'$1')!=='蓝杉读书会') throw new SemanticReviewRequired('标题结构未识别，需独立正文验收');
  const label=line=>line.trim().replace(/^#{1,6}\s+/, '').replace(/^\*\*(.*?)\*\*$/,'$1').replace(/[：:]$/,'').trim();
  const notes=lines.flatMap((line,index)=>/^(?:注意事项|注意|温馨提示)$/.test(label(line))?[index]:[]);
  const steps=lines.flatMap((line,index)=>/^(?:参与步骤|参加步骤|参与方式)$/.test(label(line))?[index]:[]);
  if(notes.length!==1||steps.length>1) throw new SemanticReviewRequired('参与步骤或注意事项区块未能唯一识别，需独立正文验收');
  const end=start=>{for(let i=start+1;i<lines.length;i++)if(/^\s*#{1,6}\s+/.test(lines[i])||notes.includes(i)||steps.includes(i))return i;return lines.length;};
  let stepLines=steps.length?lines.slice(steps[0]+1,end(steps[0])):lines.slice(0,notes[0]);
  if(!steps.length) {
    const start=stepLines.findIndex(line=>/^\s*(?:\d+|[一二三四五六七八九十]+)[.、）)]/.test(line));
    if(start<0) throw new SemanticReviewRequired('未识别独立编号步骤，需独立正文验收');
    stepLines=stepLines.slice(start);
  }
  const noteLines=lines.slice(notes[0]+1,end(notes[0]));
  const items=block=>{
    const found=block.map(line=>/^(\s*)(?:(\d+|[一二三四五六七八九十]+)[.、）)]|([-*•])(?:\s|$))\s*(.*)$/.exec(line)).filter(Boolean);
    const indent=Math.min(...found.map(match=>match[1].length));
    return found.filter(match=>match[1].length===indent).map(match=>({number:match[2]?({'一':1,'二':2,'三':3}[match[2]]??Number(match[2])):null,content:match[4].replace(/[*_`]/g,'').trim()}));
  };
  const participation=items(stepLines),cautions=items(noteLines);
  assert.equal(participation.length,3,'参与步骤必须有且仅有独立的三条编号，不能借用注意事项编号');
  assert.ok(participation.every(item=>item.number!==null),'参与步骤必须编号');
  assert.ok(participation.every(item=>item.content.length>0),'参与步骤不能为空');
  if(!cautions.length&&noteLines.some(line=>line.trim())) throw new SemanticReviewRequired('注意事项采用未识别格式，需独立正文验收');
  assert.equal(cautions.length,2,'注意事项必须有且仅有两条');
  assert.ok(cautions.every(item=>item.content.length>0),'注意事项不能为空');
}

export function validateDelivery(id, delivery) {
  assert.equal(delivery.profile,'generic-files-delivery/v1');
  assert.equal(delivery.scope,'independently-reviewed-files-not-external-effects');
  assert.equal(delivery.files.length,1,'最终只交付一份完整成果');
  const text=delivery.files[0].content;
  assert.equal(typeof text,'string');
  if(id==='S01') {
    for(const fact of ['蓝杉读书会','周六','14:00','城市图书馆二层']) assert.ok(text.includes(fact),'缺少事实:'+fact);
    assert.ok(!/收费|门票|报名费/.test(text),'不得添加费用');
    validateGuideStructure(text);
  } else if(id==='M01') {
    const expected={regions:{east:{count:0,cents:0},west:{count:0,cents:0}},total:{count:0,cents:0}};
    for(const order of orders) if(order.status!=='cancelled') {
      expected.regions[order.region].count++; expected.regions[order.region].cents+=order.cents;
      expected.total.count++;expected.total.cents+=order.cents;
    }
    assert.deepEqual(JSON.parse(text),expected);
  } else if(id==='M02') {
    for(const fact of ['10月20日','只读','备份','只读验证','回退检查']) assert.ok(text.includes(fact),'缺少冻结决定:'+fact);
    assert.ok(/禁止写入|不允许写入|不支持写入|禁用写入/.test(text),'必须明确禁止写入');
    assert.ok(!/(?:同时|并且|且|，|。|\n|^)\s*允许写入/.test(text),'与只读决定冲突');
  } else if(id==='C01') {
    const value=JSON.parse(text);
    assert.deepEqual(Object.keys(value).sort(),['requirements','design','acceptance','rollback','sources'].sort());
    assert.deepEqual([...value.requirements].sort(),['离线可用','仅本地存储','禁止外部发布'].sort());
    assert.deepEqual([...value.sources].sort(),['requirements.txt','constraints.txt','risks.txt'].sort());
    assert.ok(typeof value.design==='string'&&value.design.trim().length>0);
    assert.ok(Array.isArray(value.acceptance)&&value.acceptance.length>0);
    assert.ok(value.acceptance.every(item=>typeof item==='string'&&item.trim().length>0));
    assert.ok(typeof value.rollback==='string'&&value.rollback.trim().length>0);
    assert.match(JSON.stringify(value),/幂等|重复/);
  } else if(id==='S02') {
    assert.ok(Buffer.byteLength(text,'utf8')<=7000,'邀请页超过冻结7000字节上限');
    assert.match(text,/<!doctype html|<html/i);
    assert.ok(hasInvitationDate(text),'缺少活动日期事实');
    for(const fact of ['蓝杉读书会','14:00','城市图书馆二层']) assert.ok(text.includes(fact),'缺少页面事实:'+fact);
  }
  return text;
}
