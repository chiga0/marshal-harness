import fs from 'node:fs';
import {createHash} from 'node:crypto';
import {configuration, receipt, hash} from './leader-ports.ts';

const registrations = new WeakMap(), classifications = new WeakMap();
const sourceDigest = 'sha256:' + createHash('sha256').update(fs.readFileSync(new URL(import.meta.url))).update(fs.readFileSync(new URL('./leader.ts',import.meta.url))).update(fs.readFileSync(new URL('../task-service/composition.ts',import.meta.url))).digest('hex');
const policy = Object.freeze({profile:'leader-json-correction/v1',maxPerTask:1,sourceDigest});
export function registerLeaderJsonCorrection(port, options) {
  configuration(port,'leader');
  if (!options || Object.keys(options).sort().join(',') !== 'maxPerTask,profile' || options.profile !== policy.profile || options.maxPerTask !== 1)
    throw new TypeError('invalid_leader_protocol_correction');
  registrations.set(port,policy); return port;
}
export const leaderJsonCorrectionPolicy = port => registrations.get(port) ?? null;

// Scan the entire text before syntax classification, including after the first
// syntax error. Mixed duplicate/limit/encoding/envelope violations fail closed.
function syntaxFailure(raw) {
  if (typeof raw !== 'string' || !raw.isWellFormed() || (/[\u0000-\u0008\u000b\u000c\u000e-\u001f\ufeff]/.test(raw)) || Buffer.byteLength(raw)>65536) return false;
  const text=raw.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g,''); if (!text.startsWith('{')) return false;
  const scopes=[];let at=0,closed=false,valueString=false,quoteGap=false;
  while(at<text.length){
    const char=text[at];
    if(/[ \t\r\n]/.test(char)){at++;continue;}
    // Extra closing punctuation may itself be the syntax defect. Any new
    // value/token after a closed root is an envelope violation, never stripped.
    if(closed && char!=='}' && char!==']')return false;
    // A missing escape can leave a bounded word between two complete JSON
    // string tokens. Recognize the lexical defect without repairing either
    // string. The whole remainder is still scanned for hard violations.
    if(valueString && char!=='"') {
      const gap=text.slice(at).match(/^[\p{L}\p{M}][\p{L}\p{M} ]{0,127}(?=")/u)?.[0];
      if(gap) {
        if(/(?:^| )(?:NaN|Infinity)(?: |$)/i.test(gap))return false;
        at+=gap.length;valueString=false;quoteGap=true;continue;
      }
    }
    if(char!=='"')valueString=false;
    if(char==='{'||char==='['){scopes.push({kind:char,keys:new Set()});if(scopes.length>32)return false;at++;continue;}
    if(char==='}'||char===']'){const scope=scopes.pop();if(scopes.length===0||scope?.kind!==(char==='}'?'{':'['))closed=true;at++;continue;}
    if(char==='"'){
      const begin=at++;let escaped=false,finished=false;
      while(at<text.length){const current=text[at++];if(escaped){escaped=false;continue;}if(current==='\\'){escaped=true;continue;}if(current==='"'){finished=true;break;}}
      if(!finished)return false;
      let value;try{value=JSON.parse(text.slice(begin,at));}catch{return false;}
      if(!value.isWellFormed())return false;
      const rest=text.slice(at).replace(/^[ \t\r\n]+/,'');
      if(quoteGap&&rest.startsWith(':'))return false;
      let previous=begin-1;while(previous>=0&&/[ \t\r\n]/.test(text[previous]))previous--;
      valueString=!rest.startsWith(':')&&(quoteGap||text[previous]===':'||
        scopes.at(-1)?.kind==='['&&['[',','].includes(text[previous]));
      quoteGap=false;
      if(rest.startsWith(':')){const scope=scopes.at(-1);if(!scope||scope.kind!=='{'||scope.keys.has(value))return false;scope.keys.add(value);}
      continue;
    }
    if(char===','||char===':'){at++;continue;}
    const token=text.slice(at).match(/^(?:true|false|null|-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)/)?.[0];
    if(!token)return false;
    if(token!=='true'&&token!=='false'&&token!=='null'&&!Number.isFinite(Number(token)))return false;
    at+=token.length;
  }
  try {JSON.parse(text);return false;} catch(error){return error instanceof SyntaxError;}
}

export async function prepareLeaderWithJsonCorrection(port,ticket,prepared,context) {
  const result=await port.prepare(ticket,prepared,context);
  const correction=ticket.input.leader?.snapshot.protocolCorrection;
  if(!registrations.has(port)||correction?.used!==1||correction.successorObligationId!==ticket.input.leader.obligationId)return result;
  return {...result,prompt:'协议格式纠错（本Task唯一一次）：上一调用没有形成可解析的严格JSON。请基于上述完整冻结输入和原合同重新返回完整JSON提案。不得调用工具、写文件、修改需求、权限或验收；不得只返回字符串补丁。\n'+result.prompt};
}

/** Keep the ORIGINAL registered port/receipt identity and its completion once. */
export function startLeaderWithJsonCorrection(port, options) {
  if (!registrations.has(port) || options.ticket.executionType !== 'leader') return port.start(options);
  const binding=hash(options.ticket);let raw=null,tools=false,calls=0;
  const native=options.provider;
  const provider={...native,start(input){
    if(++calls!==1)throw new TypeError('invalid_leader_protocol_correction');
    const onProgress=input.onProgress,onPermission=input.onPermission;
    const handle=native.start({...input,
      onProgress:async(value,...rest)=>{if(value?.tool||value?.diagnostic?.stage==='permission')tools=true;return onProgress?.(value,...rest);},
      onPermission:async(...args)=>{tools=true;return onPermission?onPermission(...args):{outcome:{outcome:'cancelled'}};}});
    return {...handle,started:handle.started,stop:(...args)=>handle.stop(...args),
      completion:Promise.resolve(handle.completion).then(value=>{raw=value;return value;})};
  }};
  const handle=port.start({...options,provider});
  return {...handle,started:handle.started,stop:(...args)=>handle.stop(...args),completion:Promise.resolve(handle.completion).then(result=>{
    const data=receipt(port,options.ticket,result);
    if(calls===1&&!tools&&data.value===null&&data.reason==='invalid_leader_decision'&&raw?.providerId===options.ticket.providerId&&
      raw.status==='completed'&&raw.stopReason==='end_turn'&&raw.cleanup?.cleaned===true&&raw.cleanup.started&&syntaxFailure(raw.outputText)) {
      classifications.set(result.receipt,{port,binding,value:Object.freeze({stage:'wire-json',code:'invalid_json',
        outputDigest:'sha256:'+createHash('sha256').update(raw.outputText).digest('hex'),outputBytes:Buffer.byteLength(raw.outputText)})});
    }
    return result;
  })};
}
export function leaderJsonFailure(port,ticket,result) {
  // Caller is not allowed to manufacture a sidecar or bypass original receipt.
  receipt(port,ticket,result);
  const stored=classifications.get(result.receipt);
  return registrations.has(port)&&stored?.port===port&&stored.binding===hash(ticket)?structuredClone(stored.value):null;
}
