// 只准备显式 Pi Reviewer 组件和无模型原生握手；此脚本没有 prompt 命令。
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
 const suiteBytes=fs.readFileSync(suite),input=JSON.parse(suiteBytes);assert.equal(input.profile,'reviewer-component-suite/v1');assert.deepEqual(input.cases.map(c=>c.id),['C01-tags-negative','C01-tags-positive']);
 const prompts=[];
 for(const item of input.cases){const cwd=fs.realpathSync(fs.mkdtempSync('/private/tmp/pi-review-prepare-'));const ticket={executionType:'review',providerId:provider.id,taskId:'component-'+randomUUID(),workerId:'component-'+randomUUID(),input:{review:structuredClone(item.input)}};const prepared=await config.review.prepare(ticket,{cwd},{});fs.writeFileSync(path.join(output,item.id+'.prompt.txt'),prepared.prompt,{mode:0o600,flag:'wx'});prompts.push({id:item.id,bytes:Buffer.byteLength(prepared.prompt),digest:hash(prepared.prompt)});}
 const {launchProtocol}=await load('packages/agent-runtime/index.mjs');const {BRIDGE_PROFILE,BRIDGE_ENV}=await load('packages/agent-provider-pi/bridge-contract.mjs');
 const cwd=fs.realpathSync(fs.mkdtempSync('/private/tmp/pi-handshake-')),deadline=Date.now()+20000,nonce=randomBytes(32).toString('hex');
 const bridge=path.join(installed,'packages/agent-provider-pi/native-bridge.mjs'),events=[],sent=[];let state,ready,runtime,problem;
 try {
  runtime=await launchProtocol({executable,args:[...PI_ARGS,'--extension',bridge],cwd,deadline,env:{...env,[BRIDGE_ENV]:JSON.stringify({profile:BRIDGE_PROFILE,sdkEntry,nonce,cwd,deadline})},createClient({readable,writable}){
   let buffer='';const listener=chunk=>{buffer+=chunk.toString();assert.ok(Buffer.byteLength(buffer)<1024*1024);let at;while((at=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,at);buffer=buffer.slice(at+1);if(!line.trim())continue;const event=JSON.parse(line);
    if(event.type==='response'&&event.command==='get_state'){assert.equal(event.success,true);const s=event.data;state={provider:s.model?.provider,model:s.model?.id,isStreaming:s.isStreaming,isCompacting:s.isCompacting,messageCount:s.messageCount,pendingMessageCount:s.pendingMessageCount};}
    else if(event.type==='extension_ui_request'&&event.method==='notify'){let value;try{value=JSON.parse(event.message);}catch{continue;}if(value.profile===BRIDGE_PROFILE&&value.type==='ready'){assert.equal(value.nonce,nonce);assert.equal(value.cwd,cwd);ready={profile:value.profile,type:value.type,tools:value.tools,scope:value.scope};}}
    else events.push({type:event.type,command:event.command??null});
   }};readable.on('data',listener);sent.push('get_state');writable.write(JSON.stringify({id:'handshake-state',type:'get_state'})+'\n');return {close(){readable.off('data',listener);}};
  }});
  while(!state||!ready){assert.ok(Date.now()<deadline-1000,'handshake_timeout');await new Promise(r=>setTimeout(r,25));}
  assert.deepEqual(state,{provider:'pai-eas',model:'DeepSeek/deepseek-v4-pro',isStreaming:false,isCompacting:false,messageCount:0,pendingMessageCount:0});assert.deepEqual(ready.tools,[]);
 }catch(error){problem={name:error.name,code:error.code??'handshake_failed'};}
 finally{if(runtime)await runtime.stop();}
 const cleanup=runtime?await runtime.completion:null;
 const evidence={candidate:admission.sourceHead,manifest:options.manifest,suiteDigest:hash(suiteBytes),executable,sdkEntry,bridge,bridgeDigest:hash(fs.readFileSync(bridge)),args:PI_ARGS,explicitExtensionOnly:true,state,ready,sent,events,prompts,problem,cleanup,boundary:'无模型握手；只发送get_state。普通宿主进程及原登录，不宣称OS/凭据隔离；无全局配置修改。'};
 fs.writeFileSync(path.join(output,'handshake.json'),JSON.stringify(evidence,null,2)+'\n',{mode:0o600,flag:'wx'});assert.equal(problem,undefined);assert.equal(cleanup.cleaned,true);return evidence;
}
if(process.argv[1]&&pathToFileURL(path.resolve(process.argv[1])).href===import.meta.url){const [mode,...args]=process.argv.slice(2);assert.equal(mode,'prepare');const options={};for(let i=0;i<args.length;i+=2){assert.ok(['installed','manifest','executable','sdkEntry','suite','output'].includes(args[i]));options[args[i]]=args[i+1];}const result=await prepare(options);console.log(JSON.stringify({candidate:result.candidate,state:result.state,tools:result.ready.tools,cleaned:result.cleanup.cleaned}));}
