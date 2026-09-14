// 显式组件评测：不创建Task、不提交Core回执、不声称整链验收。
import fs from 'node:fs';import path from 'node:path';import {pathToFileURL} from 'node:url';
import {randomUUID} from 'node:crypto';
import {DatabaseSync} from 'node:sqlite';import assert from 'node:assert/strict';
import {verify} from '../packages/task-distribution/index.mjs';
import {makePairs,digest} from './reviewer-component-cases.mjs';
const [mode,...args]=process.argv.slice(2),options={};
for(let i=0;i<args.length;i+=2){assert.ok(['--source','--suite','--output','--installed','--manifest','--agent'].includes(args[i]));assert.ok(args[i+1]);options[args[i]]=args[i+1];}
assert.ok(['prepare','run'].includes(mode));const out=options['--output'];assert.ok(out&&path.isAbsolute(out)&&!fs.existsSync(out));fs.mkdirSync(out,{recursive:true,mode:0o700});
const save=(file,value)=>fs.writeFileSync(path.join(out,file),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:'wx'});
if(mode==='prepare') {
 const inputs={},provenance={};
 for(const id of ['S02','C01']) {
  const source=path.join(options['--source'],id+'-d14a660e-1'),e=JSON.parse(fs.readFileSync(path.join(source,'evidence.json')));
  assert.equal(e.candidate,'d14a660e74195fea4b67235f52c80f15fc49a085');
  const db=new DatabaseSync(path.join(e.runtime,'data/store/authority.sqlite'),{readOnly:true});
  try {
   const row=db.prepare('SELECT bytes FROM projections WHERE kind=? AND id=?').get('attempt',e.review.workerId);
   const worker=JSON.parse(Buffer.from(row.bytes)),ref=worker.inputObservation.snapshot;
   const bytes=fs.readFileSync(path.join(e.runtime,'data/artifacts',ref.digest.slice(7)));assert.equal(digest(bytes),ref.digest);assert.equal(bytes.length,ref.bytes);
   inputs[id]=JSON.parse(bytes.toString('utf8').split('\n完整冻结输入：').at(-1));
   provenance[id]={sourceEvidence:source,sourcePromptDigest:ref.digest,sourceCandidateDigests:inputs[id].materials.filter(m=>m.nodeId).map(m=>({nodeId:m.nodeId,digest:m.digest}))};
  }finally{db.close();}
 }
 const suite={profile:'reviewer-component-suite/v1',boundary:'原失败材料及明确派生正例；非Core输入权威、非整链验收；标签不传给模型',provenance,cases:makePairs(inputs)};
 save('suite.json',suite);console.log(JSON.stringify({prepared:out,cases:suite.cases.map(c=>({id:c.id,expected:c.expected,derived:c.derived}))}));
} else {
 const admission=verify({root:options['--installed'],manifestDigest:options['--manifest']});
 assert.ok(path.isAbsolute(options['--agent']));process.env.MARSHAL_AGENT_EXECUTABLE=options['--agent'];
 const {default:config}=await import(pathToFileURL(path.join(options['--installed'],'packages/task-generic-files/qwen-review-service-config.mjs')).href);
 const {receipt}=await import(pathToFileURL(path.join(options['--installed'],'packages/task-application/leader-ports.mjs')).href);
 const provider=config.providers.get(config.review.providerId);assert.equal(provider.id,'qwen-managed-acp');
 const suiteBytes=fs.readFileSync(options['--suite']),suite=JSON.parse(suiteBytes);assert.equal(suite.profile,'reviewer-component-suite/v1');
 const results=[];save('admission.json',{candidate:admission.sourceHead,manifest:options['--manifest'],suiteDigest:digest(suiteBytes),boundary:suite.boundary});
 for(const example of suite.cases) {
  assert.match(example.id,/^(S02|C01)-(negative|positive)$/);const caseOutput=path.join(out,example.id);fs.mkdirSync(caseOutput,{mode:0o700});const cwd=fs.mkdtempSync(path.join(out,'runtime-'));fs.chmodSync(cwd,0o700);
  const input=structuredClone(example.input),ticket={executionType:'review',providerId:provider.id,taskId:'component-'+randomUUID(),workerId:'component-'+randomUUID(),input:{review:input}};
  const prepared=await config.review.prepare(ticket,{cwd},{});fs.writeFileSync(path.join(caseOutput,'prompt.txt'),prepared.prompt,{mode:0o600,flag:'wx'});
  const startedAt=new Date().toISOString(),deadline=Date.now()+180000;let handle,completion,rawCompletion,parsedReport=null,problem=null;const activity=[];
  try {
   ticket.deadline=deadline;
   const observedProvider={...provider,start(nativeInput){const native=provider.start(nativeInput);return {started:native.started,stop:(...args)=>native.stop(...args),completion:Promise.resolve(native.completion).then(raw=>{rawCompletion=raw;return raw;})};}};
   handle=config.review.start({ticket,provider:observedProvider,prepared:{...prepared,observability:true,onPermission:async()=>({outcome:{outcome:'cancelled'}})},onProgress:update=>{if(activity.length<200)activity.push({phase:update.phase,activity:update.activity,tool:update.tool,model:update.model,usage:update.usage,diagnostic:update.diagnostic});}});
   completion=await handle.completion;
   parsedReport=receipt(config.review,ticket,completion).value;
  }catch(error){problem={code:error.code??'component_provider_failed',name:error.name};}
  finally{if(handle&&!completion)try{await handle.stop();completion=await handle.completion;}catch{problem??={code:'component_cleanup_unconfirmed'};}}
  const verdict=parsedReport?.verdict??null,parseError=completion?.status==='completed'?null:'invalid_review_receipt';
  const record={id:example.id,expected:example.expected,candidate:admission.sourceHead,componentResult:!problem&&!parseError&&example.expected.includes(verdict)?'PASS':'FAIL',verdict,parseError,problem,startedAt,finishedAt:new Date().toISOString(),elapsedMs:Date.now()-Date.parse(startedAt),promptDigest:digest(prepared.prompt),promptBytes:Buffer.byteLength(prepared.prompt),runtime:cwd,parsedReport,rawCompletion:rawCompletion?{status:rawCompletion.status,stopReason:rawCompletion.stopReason,cleanup:rawCompletion.cleanup,outputText:rawCompletion.outputText}:null,completion:completion?{status:completion.status,stopReason:completion.stopReason,cleanup:completion.cleanup,outputText:completion.outputText}:null,activity};
  fs.writeFileSync(path.join(caseOutput,'result.json'),JSON.stringify(record,null,2)+'\n',{mode:0o600,flag:'wx'});results.push(record);console.log(JSON.stringify({id:record.id,result:record.componentResult,verdict,elapsedMs:record.elapsedMs}));
  if(completion?.cleanup?.cleaned!==true){process.exitCode=1;break;}
 }
 save('results.json',{candidate:admission.sourceHead,boundary:suite.boundary,results:results.map(({activity,completion,...record})=>record)});
 if(results.length!==suite.cases.length||results.some(r=>r.componentResult!=='PASS'))process.exitCode=1;
}
