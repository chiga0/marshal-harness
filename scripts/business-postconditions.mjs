// 纯离线后验实验：不登记产品 Verifier、不生成 Core 权威。
import {createHash} from 'node:crypto';
import {parseJson} from '../packages/task-api/http-boundary.mjs';
const hash=value=>'sha256:'+createHash('sha256').update(typeof value==='string'?value:JSON.stringify(value)).digest('hex');
const closed=(value,keys)=>value!==null&&typeof value==='object'&&!Array.isArray(value)&&Object.keys(value).length===keys.length&&keys.every(key=>Object.hasOwn(value,key));
const same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
const text=value=>typeof value==='string'&&value.isWellFormed()&&!value.includes('\0');
function evidence(kind,input,candidate,status,code,details={}) {
  return {profile:'business-postcondition-experiment/v1',kind,status,code,inputDigest:hash(input),candidateDigest:hash(candidate),...details,
    authority:false,boundary:kind==='orders'?'仅验证该冻结订单输入的数值与输出形状；不证明过程未调用工具或已获交付授权':kind==='guide'?'仅在显式批准的有限语言内验证内容；不证明任意自然语言事实或作者过程':'仅检查声明的有限恢复模型；不证明自然语言方案与模型一致或真实实现满足该模型'};
}
function json(value,max=65536){if(!text(value)||Buffer.byteLength(value)>max)throw Error('bounded_json');return parseJson(Buffer.from(value));}

/** 精确整数订单汇总：不接受小数、未知状态/区域、重复字段或溢出。 */
export function verifyOrders(inputText,candidateText) {
  let orders,candidate;
  try {orders=json(inputText);if(!Array.isArray(orders)||orders.length>2048)throw Error();
    for(const row of orders)if(!closed(row,['region','status','cents'])||!['east','west'].includes(row.region)||!['paid','refunded','cancelled'].includes(row.status)||!Number.isSafeInteger(row.cents))throw Error();
  }catch{return evidence('orders',inputText,candidateText,'not-verified','unsupported_order_input');}
  const totals={east:{count:0n,cents:0n},west:{count:0n,cents:0n}};
  for(const row of orders)if(row.status!=='cancelled'){totals[row.region].count++;totals[row.region].cents+=BigInt(row.cents);}
  const total={count:totals.east.count+totals.west.count,cents:totals.east.cents+totals.west.cents};
  if([...Object.values(totals),total].some(row=>row.cents>BigInt(Number.MAX_SAFE_INTEGER)||row.cents<BigInt(Number.MIN_SAFE_INTEGER)))
    return evidence('orders',inputText,candidateText,'not-verified','unrepresentable_order_total');
  const numeric=row=>({count:Number(row.count),cents:Number(row.cents)});
  const expected={regions:{east:numeric(totals.east),west:numeric(totals.west)},total:numeric(total)};
  try {candidate=json(candidateText);if(!closed(candidate,['regions','total'])||!closed(candidate.regions,['east','west']))throw Error();
    for(const row of [candidate.regions.east,candidate.regions.west,candidate.total])if(!closed(row,['count','cents'])||!Number.isSafeInteger(row.count)||row.count<0||!Number.isSafeInteger(row.cents))throw Error();
  }catch{return evidence('orders',inputText,candidateText,'fail','invalid_order_report');}
  const equal=['east','west'].every(region=>candidate.regions[region].count===expected.regions[region].count&&candidate.regions[region].cents===expected.regions[region].cents)&&candidate.total.count===expected.total.count&&candidate.total.cents===expected.total.cents;
  return evidence('orders',inputText,candidateText,equal?'pass':'fail',equal?'exact_order_total':'order_total_mismatch',{expected});
}

/** 调用者必须先批准模板与允许句子；未知表述不推断为事实成立。 */
export function verifyGuide(contract,candidate) {
  if(!closed(contract,['profile','title','schedule','location','steps','notes'])||contract.profile!=='finite-guide/v1'||
    ![contract.title,contract.schedule,contract.location].every(text)||!Array.isArray(contract.steps)||contract.steps.length!==3||!contract.steps.every(text)||
    !Array.isArray(contract.notes)||contract.notes.length!==2||!contract.notes.every(text))return evidence('guide',contract,candidate,'not-verified','unsupported_guide_contract');
  if(!text(candidate)||Buffer.byteLength(candidate)>8192)return evidence('guide',contract,candidate,'fail','invalid_guide_text');
  const lines=candidate.replace(/\r\n/g,'\n').split('\n').map(line=>line.trim()).filter(Boolean);
  const expected=['# '+contract.title,'时间：'+contract.schedule,'地点：'+contract.location,'## 参与步骤',...contract.steps.map((value,i)=>(i+1)+'. '+value),'## 注意事项',...contract.notes.map(value=>'- '+value)];
  if(same(lines,expected))return evidence('guide',contract,candidate,'pass','finite_guide_exact');
  const time=lines.filter(line=>line.startsWith('时间：')),location=lines.filter(line=>line.startsWith('地点：'));
  if(time.length===1&&time[0]!==expected[1]||location.length===1&&location[0]!==expected[2])return evidence('guide',contract,candidate,'fail','explicit_fact_mismatch');
  return evidence('guide',contract,candidate,'not-verified','outside_approved_guide_language');
}

const actions=['record-pending','build-temporary','replace-index','mark-complete'];
const validModel=model=>closed(model,['profile','skip','steps'])&&model.profile==='finite-import-recovery/v1'&&
  ['record-exists','complete-and-consistent'].includes(model.skip)&&Array.isArray(model.steps)&&model.steps.length===4&&new Set(model.steps).size===4&&model.steps.every(step=>actions.includes(step));
function importOnce(model,state,source,crashAfter=-1) {
  const rows=source.map(row=>({id:row.id,body:row.body,tags:[...row.tags]}));
  const consistent=state.index!==null&&same(state.index,rows)&&same(state.manifest?.rows,rows);
  if(model.skip==='record-exists'&&state.manifest!==null||model.skip==='complete-and-consistent'&&state.manifest?.complete===true&&consistent)return {skipped:true};
  if(crashAfter===0)return {crashed:true};
  for(let i=0;i<model.steps.length;i++) {
    switch(model.steps[i]){
      case 'record-pending':state.manifest={rows:structuredClone(rows),complete:false};break;
      case 'build-temporary':state.temporary=structuredClone(rows);break;
      case 'replace-index':if(state.temporary===null)return {error:'no_temporary_index'};state.index=state.temporary;state.temporary=null;break;
      case 'mark-complete':if(state.manifest===null)return {error:'no_manifest'};state.manifest.complete=true;break;
    }
    if(crashAfter===i+1){state.temporary=null;return {crashed:true};}
  }
  return {completed:true};
}
/** 有界、原子步骤模型。崩溃发生于每个步骤边界；非文件系统断电模拟。 */
export function verifyRecoveryModel(model,source) {
  if(!validModel(model)||!Array.isArray(source)||source.length<1||source.length>8||new Set(source.map(row=>row.id)).size!==source.length||
    !source.every(row=>closed(row,['id','body','tags'])&&text(row.id)&&text(row.body)&&Array.isArray(row.tags)&&row.tags.length<=8&&row.tags.every(text)))
    return evidence('recovery-model',source,model,'not-verified','unsupported_recovery_model');
  const original=JSON.stringify(source),scenarios=[];
  for(const initial of ['empty','existing','corrupt-index'])for(let crash=0;crash<=4;crash++) {
    const rows=structuredClone(source),state={manifest:initial==='empty'?null:{rows:structuredClone(rows),complete:true},index:initial==='empty'?null:initial==='corrupt-index'?[]:structuredClone(rows),temporary:null};
    const first=importOnce(model,state,source,crash),restart=importOnce(model,state,source),stable=JSON.stringify(state),again=importOnce(model,state,source);
    const passed=!first.error&&!restart.error&&!again.error&&same(state.index,source)&&state.manifest?.complete===true&&same(state.manifest.rows,source)&&JSON.stringify(state)===stable&&JSON.stringify(source)===original;
    scenarios.push({initial,crashAfter:crash,passed,first,restart,again,final:{complete:state.manifest?.complete??false,indexMatchesSource:same(state.index,source),sourceUnchanged:JSON.stringify(source)===original}});
  }
  const passed=scenarios.every(value=>value.passed);
  return evidence('recovery-model',source,model,passed?'pass':'fail',passed?'bounded_recovery_invariants':'recovery_counterexample',{scenarios,modelAssumptions:['单写者','每个列明步骤原子完成','原笔记固定且只读','临时索引在崩溃时丢弃','无外部效果'],unverified:['自然语言方案与模型对应','真实文件系统持久化/锁实现','并发写入与原笔记变化','模型范围外的故障']});
}
