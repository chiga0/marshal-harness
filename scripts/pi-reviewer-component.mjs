// 显式 Pi Reviewer 组件；prepare 仅原生握手，run 需独立审查后另行授权。
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {randomBytes,randomUUID,createHash} from 'node:crypto';
import assert from 'node:assert/strict';
import {verify} from '../packages/task-distribution/index.mjs';
export const PI_ARGS=Object.freeze(['--mode','rpc','--provider','pai-eas','--model','DeepSeek/deepseek-v4-pro','--no-tools','--no-extensions','--no-skills','--no-prompt-templates','--no-context-files','--no-themes','--no-approve','--no-session','--offline']);
export async function configuration({installed,manifest,executable,sdkEntry}) {
 const admission=verify({root:installed,manifestDigest:manifest});
 assert.ok(path.isAbsolute(executable)&&path.isAbsolute(sdkEntry));
 const load=file=>import(pathToFileURL(path.join(installed,file)).href);
 const {createPiProvider}=await load('packages/agent-provider-pi/index.mjs');
 const {createGenericFilesReviewWireConfig}=await load('packages/task-generic-files/review-wire.mjs');
 const {receipt}=await load('packages/task-application/leader-ports.mjs');
 const env={};for(const key of ['HOME','PATH','LANG','LC_ALL','LC_CTYPE','TMPDIR'])if(typeof process.env[key]==='string')env[key]=process.env[key];env.PI_TELEMETRY='0';
 const provider=createPiProvider({id:'pi-review-component',executable,args:[...PI_ARGS],env,bridge:{sdkEntry}});
 return {admission,provider,config:createGenericFilesReviewWireConfig({provider}),receipt,env,load};
}
export async function prepare(options) {
 const {installed,executable,sdkEntry,suite,output}=options;assert.ok(path.isAbsolute(output)&&!fs.existsSync(output));fs.mkdirSync(output,{recursive:true,mode:0o700});
 const setup=await configuration(options),{admission,provider,config,env,load}=setup;
 const hash=value=>'sha256:'+createHash('sha256').update(value).digest('hex');
 const suiteBytes=fs.readFileSync(suite),input=JSON.parse(suiteBytes);assert.equal(hash(suiteBytes),'sha256:36a9071dfd3bdbbf01ad679ad0b90305de84cc8f385b8b1549647914df753dd5');assert.equal(input.profile,'reviewer-component-suite/v1');assert.deepEqual(input.cases.map(c=>c.id),['C01-tags-negative','C01-tags-positive']);
 const prompts=[];
 for(const item of input.cases){const cwd=fs.realpathSync(fs.mkdtempSync('/private/tmp/pi-review-prepare-'));const ticket={executionType:'review',providerId:provider.id,taskId:'component-'+randomUUID(),workerId:'component-'+randomUUID(),input:{review:structuredClone(item.input)}};const prepared=await config.review.prepare(ticket,{cwd},{});fs.writeFileSync(path.join(output,item.id+'.prompt.txt'),prepared.prompt,{mode:0o600,flag:'wx'});prompts.push({id:item.id,bytes:Buffer.byteLength(prepared.prompt),digest:hash(prepared.prompt)});}
 const {launchProtocol}=await load('packages/agent-runtime/index.mjs');const {BRIDGE_PROFILE,BRIDGE_ENV}=await load('packages/agent-provider-pi/bridge-contract.mjs');
 const cwd=fs.realpathSync(fs.mkdtempSync('/private/tmp/pi-handshake-')),deadline=Date.now()+20000,nonce=randomBytes(32).toString('hex');
 const bridge=path.join(installed,'packages/agent-provider-pi/native-bridge.mjs'),events=[],sent=[];let state,ready,runtime,problem;
 try {
  runtime=await launchProtocol({executable,args:[...PI_ARGS,'--extension',bridge],cwd,deadline,env:{...env,[BRIDGE_ENV]:JSON.stringify({profile:BRIDGE_PROFILE,sdkEntry,nonce,cwd,deadline})},createClient({readable,writable}){
   let buffer='';const listener=chunk=>{try{buffer+=chunk.toString();assert.ok(Buffer.byteLength(buffer)<1024*1024);let at;while((at=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,at);buffer=buffer.slice(at+1);if(!line.trim())continue;const event=JSON.parse(line);
    if(event.type==='response'&&event.command==='get_state'){assert.equal(event.success,true);const s=event.data;state={provider:s.model?.provider,model:s.model?.id,reasoningSupported:s.model?.reasoning===true,thinkingLevel:s.thinkingLevel??null,isStreaming:s.isStreaming,isCompacting:s.isCompacting,messageCount:s.messageCount,pendingMessageCount:s.pendingMessageCount};}
    else if(event.type==='extension_ui_request'&&event.method==='notify'){let value;try{value=JSON.parse(event.message);}catch{continue;}if(value.profile===BRIDGE_PROFILE&&value.type==='ready'){assert.equal(value.nonce,nonce);assert.equal(value.cwd,cwd);ready={profile:value.profile,type:value.type,tools:value.tools,scope:value.scope};}}
    else events.push({type:event.type,command:event.command??null});
   }}catch{problem={name:'Error',code:'handshake_invalid_event'};}};readable.on('data',listener);sent.push('get_state');writable.write(JSON.stringify({id:'handshake-state',type:'get_state'})+'\n');return {close(){readable.off('data',listener);}};
  }});
  while(!state||!ready){assert.equal(problem,undefined);assert.ok(Date.now()<deadline-1000,'handshake_timeout');await new Promise(r=>setTimeout(r,25));}
  assert.equal(state.provider,'pai-eas');assert.equal(state.model,'DeepSeek/deepseek-v4-pro');assert.equal(state.isStreaming,false);assert.equal(state.isCompacting,false);assert.equal(state.messageCount,0);assert.equal(state.pendingMessageCount,0);assert.deepEqual(ready.tools,[]);
 }catch(error){problem??={name:error.name,code:error.code??'handshake_failed'};}
 finally{if(runtime)await runtime.stop();}
 const cleanup=runtime?await runtime.completion:null;
 const evidence={candidate:admission.sourceHead,manifest:options.manifest,suiteDigest:hash(suiteBytes),executable,sdkEntry,bridge,bridgeDigest:hash(fs.readFileSync(bridge)),args:PI_ARGS,explicitExtensionOnly:true,state,ready,sent,events,prompts,problem,cleanup,boundary:'无模型握手；只发送get_state。offline只关闭原生启动网络操作，并不表示后续模型调用离线；普通宿主进程及原登录，不宣称OS/凭据隔离；无全局配置修改。'};
 fs.writeFileSync(path.join(output,'handshake.json'),JSON.stringify(evidence,null,2)+'\n',{mode:0o600,flag:'wx'});assert.equal(problem,undefined);assert.equal(cleanup.cleaned,true);return evidence;
}
if(process.argv[1]&&pathToFileURL(path.resolve(process.argv[1])).href===import.meta.url){const [mode,...args]=process.argv.slice(2);assert.ok(['prepare','run'].includes(mode));const options={};for(let i=0;i<args.length;i+=2){assert.ok(['installed','manifest','executable','sdkEntry','suite','output'].includes(args[i]));options[args[i]]=args[i+1];}const result=await (mode==='prepare'?prepare(options):run(options));console.log(JSON.stringify(mode==='prepare'?{candidate:result.candidate,state:result.state,tools:result.ready.tools,cleaned:result.cleanup.cleaned}:result));}

// 只有独立审查后显式调用 run 才请求模型；每个固定样本一次，无重试。
export async function run(options) {
 const {output}=options;assert.ok(path.isAbsolute(output)&&!fs.existsSync(output));fs.mkdirSync(output,{recursive:true,mode:0o700});
 const {admission,config,receipt}=await configuration(options),provider=config.providers.get(config.review.providerId);
 assert.equal(provider.id,'pi-review-component');
 const hash=value=>'sha256:'+createHash('sha256').update(value).digest('hex');
 const suiteBytes=fs.readFileSync(options.suite),suite=JSON.parse(suiteBytes);assert.equal(hash(suiteBytes),'sha256:36a9071dfd3bdbbf01ad679ad0b90305de84cc8f385b8b1549647914df753dd5');assert.equal(suite.profile,'reviewer-component-suite/v1');assert.deepEqual(suite.cases.map(c=>c.id),['C01-tags-negative','C01-tags-positive']);
 const save=(file,value)=>fs.writeFileSync(path.join(output,file),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:'wx'});
 const results=[];save('admission.json',{candidate:admission.sourceHead,manifest:options.manifest,suiteDigest:hash(suiteBytes),args:PI_ARGS,provider:'pai-eas',model:'DeepSeek/deepseek-v4-pro',thinking:'inherited native setting, no override',boundary:'显式Pi Reviewer能力测量，原tags-v2；不是Core全链。offline只限制启动网络操作，不禁止模型网络。'});
 for(const item of suite.cases) {
  const cwd=fs.realpathSync(fs.mkdtempSync('/private/tmp/pi-review-run-')),ticket={executionType:'review',providerId:provider.id,taskId:'component-'+randomUUID(),workerId:'component-'+randomUUID(),deadline:Date.now()+180000,input:{review:structuredClone(item.input)}};
  const prepared=await config.review.prepare(ticket,{cwd},{}),events=[];let handle,completion,rawCompletion,parsedReport=null,problem=null;
  fs.writeFileSync(path.join(output,item.id+'.prompt.txt'),prepared.prompt,{mode:0o600,flag:'wx'});
  const startedAt=new Date().toISOString();
  try {
   const observed={...provider,start(input){const native=provider.start(input);return {started:native.started,stop:(...args)=>native.stop(...args),completion:Promise.resolve(native.completion).then(raw=>{rawCompletion=raw;return raw;})};}};
   handle=config.review.start({ticket,provider:observed,prepared:{...prepared,observability:true,onPermission:async()=>({outcome:{outcome:'cancelled'}})},onProgress:event=>{if(events.length<256)events.push({phase:event.phase,activity:event.activity,tool:event.tool??null,model:event.model??null,usage:event.usage??null,diagnostic:event.diagnostic??null});}});
   completion=await handle.completion;parsedReport=receipt(config.review,ticket,completion).value;
  }catch(error){problem={name:error.name,code:error.code??'component_failed'};}
  finally{if(handle&&!completion)try{await handle.stop();completion=await handle.completion;}catch{problem??={code:'component_cleanup_unconfirmed'};}}
  const result={id:item.id,expected:item.expected,verdict:parsedReport?.verdict??null,result:!problem&&completion?.status==='completed'&&item.expected.includes(parsedReport?.verdict)?'PASS':'FAIL',candidate:admission.sourceHead,startedAt,elapsedMs:Date.now()-Date.parse(startedAt),cwd,promptBytes:Buffer.byteLength(prepared.prompt),promptDigest:hash(prepared.prompt),parsedReport,problem,events,rawCompletion:rawCompletion?{status:rawCompletion.status,stopReason:rawCompletion.stopReason,outputText:rawCompletion.outputText,cleanup:rawCompletion.cleanup,usage:rawCompletion.usage}:null,completion:completion?{status:completion.status,cleanup:completion.cleanup}:null};
  save(item.id+'.result.json',result);results.push(result);console.log(JSON.stringify({id:result.id,result:result.result,verdict:result.verdict,elapsedMs:result.elapsedMs}));
  if(completion?.cleanup?.cleaned!==true)break;
 }
 const summary={candidate:admission.sourceHead,results:results.map(({events,rawCompletion,...rest})=>rest)};save('results.json',summary);if(results.length!==2||results.some(r=>r.result!=='PASS'))process.exitCode=1;return {candidate:admission.sourceHead,results:results.map(({id,result})=>({id,result}))};
}
