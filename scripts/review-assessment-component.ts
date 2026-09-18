// 独立组件测量：合成目录不是原 Task 批准；不向 Core 提交结果。
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {pathToFileURL} from 'node:url';
import {verify} from '../packages/task-distribution/index.ts';
import {validateExample, digest} from './review-assessment-cases.ts';

const clone = structuredClone;
const closed = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) &&
  Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key));
const sorted = (values, key) => [...values].sort((a,b) => a[key] < b[key] ? -1 : 1);
export function syntheticInput(example, api) {
  validateExample(example);
  const input = clone(example.input), original = input.snapshot.plan;
  const terms = original.acceptance.map(value => {try {return JSON.parse(value);} catch {return null;}});
  const start = terms.findIndex(value => value !== null);
  assert.ok(start > 0, '未知技术条款，拒绝自动删除');
  assert.ok(original.acceptance.slice(0,start).every(value => typeof value === 'string' && value.trim()));
  const layout = api.bindGenericFilesPlan({inputArtifacts: input.snapshot.task.inputArtifacts, proposal: original});
  const first = terms[start];
  assert.ok(closed(first,['policy','description']) && closed(first.policy,['id','version','description']));
  assert.equal(first.policy.id,'generic-files-check'); assert.equal(first.policy.version,'1');
  assert.equal(first.description,layout.description); assert.equal(typeof first.policy.description,'string');
  const policy = input.snapshot.policy;
  const repair = policy.repair.scope === 'plan-authors' ? {...policy.repair,nodeIds:original.nodes.filter(n=>n.role==='author').map(n=>n.id).sort()} : policy.repair;
  const expected = [first,
    ...sorted(layout.layouts,'nodeId').map(value=>({layout:{...value,inputs:sorted(value.inputs,'path')}})),
    ...sorted(layout.deliveries,'targetPath').map(delivery=>({delivery})),
    {profile:'task-managed-leader/v1',policyDigest:api.hash(policy),repair,review:policy.review,publication:policy.publication,completion:'leader-delivery'}];
  assert.deepEqual(terms.slice(start),expected,'技术后缀必须与原计划完整闭合');
  const business = original.acceptance.slice(0,start);
  const plan = api.withReviewCriteria({...original,acceptance:business});
  delete plan.digest; plan.digest = api.hash(plan);
  input.snapshot.plan = plan;
  const refs = input.snapshot.readSet.filter(ref=>ref.kind==='plan');
  assert.equal(refs.length,1); assert.equal(refs[0].digest,original.digest); refs[0].digest = plan.digest;
  delete input.inputDigest; input.inputDigest = api.hash(input);
  api.reviewCriteria(plan); api.reviewSources(input);
  assert.deepEqual(input.materials,example.input.materials);
  assert.deepEqual(input.selection,example.input.selection);
  return {input,provenance:{kind:'NEW_SYNTHETIC_COMPONENT_INPUT',approval:'组件合成目录，不是原 Task 批准或 Core 权威',
    originalInputDigest:example.input.inputDigest,originalPlanDigest:original.digest,
    inputDigest:input.inputDigest,planDigest:plan.digest,businessAcceptance:business,removedTechnicalAcceptance:original.acceptance.slice(start)}};
}

export async function evaluate({config,api,input,cwd,deadlineMs=180000,cleanupMs=10000,onPrepared=()=>{}}) {
  const provider = config.providers.get(config.review.providerId);
  assert.equal(provider.id,'qwen-managed-acp'); assert.equal(api.reviewAssessmentsEnabled(config.review),true);
  const ticket={executionType:'review',providerId:provider.id,taskId:'component-'+randomUUID(),workerId:'component-'+randomUUID(),
    deadline:Date.now()+deadlineMs,input:{review:input}};
  let prepared;
  try {prepared=await config.review.prepare(ticket,{cwd},{}); onPrepared(prepared.prompt);} catch {return {valid:false,problem:'component_prepare_failed',cleaned:true,cleanupStatus:'not-started',extraBehavior:false,providerStarts:0,report:null,assessment:null};}
  let handle,result,raw,report=null,assessment=null,problem=null,calls=0,timer;
  const activity=[]; let extraBehavior=false,managedDiagnostic=null,cleanupStatus;
  const started=Date.now();
  const observed={...provider,start(options){assert.equal(++calls,1);const native=provider.start(options);
    return {...native,stop:(...args)=>native.stop(...args),completion:Promise.resolve(native.completion).then(value=>{raw=value;return value;})};}};
  try {
    handle=api.startReviewWithAssessments(config.review,{ticket,provider:observed,prepared:{...prepared,observability:true,onPermission:async()=>{extraBehavior=true;return {outcome:{outcome:'cancelled'}};}},
      onDiagnostic:value=>{const safe=api.safeManagedDiagnostic(value);if(safe)managedDiagnostic=safe;},
      onProgress:update=>{
        const unexpected=update.tool!=null||['retrying','compacting'].includes(update.activity)||update.diagnostic?.stage==='permission';
        extraBehavior ||= unexpected;
        if(activity.length<256)activity.push({activity:typeof update.activity==='string'?update.activity.slice(0,64):null,
          model:typeof update.model?.id==='string'?update.model.id.slice(0,128):null,toolObserved:update.tool!=null,unexpected});
      }});
    const timeout=new Promise((_,reject)=>{timer=setTimeout(()=>reject(Object.assign(new Error('deadline'),{code:'component_deadline'})),deadlineMs);});
    result=await Promise.race([handle.completion,timeout]);
    report=api.receipt(config.review,ticket,result).value;
    assessment=api.reviewAssessmentEvidence(config.review,ticket,result);
    if(!report||!assessment)problem='invalid_assessment_receipt';
  } catch(error) {problem=['component_deadline','invalid_review_report'].includes(error?.code)?error.code:'component_execution_failed';}
  finally {
    clearTimeout(timer);
    if(handle){let cleanupTimer;try {await Promise.race([(async()=>{await handle.stop();result ??= await handle.completion;})(),new Promise((_,reject)=>{cleanupTimer=setTimeout(()=>reject(new Error('cleanup')),cleanupMs);})]);} catch {problem ??='component_cleanup_unconfirmed'; result=undefined;} finally {clearTimeout(cleanupTimer);}}
  }
  const cleaned=result?.cleanup?.cleaned===true;cleanupStatus=cleaned?'confirmed':'unconfirmed';
  const diagnostic=api.safeManagedDiagnostic({code:'managed_provider_failure',authority:false,taskId:ticket.taskId,workerId:ticket.workerId,
    providerId:provider.id,executionType:'review',status:raw?.status,stopReason:raw?.stopReason,reason:raw?.reason,stage:'provider-result',parseCode:null});
  const text=typeof raw?.outputText==='string'?raw.outputText:null;
  const safe=text===null?null:api.boundedPublicText(text,65536);
  return {valid:!problem&&cleaned&&!extraBehavior&&calls===1&&!!assessment,problem,cleaned,extraBehavior,providerStarts:calls,
    nativeInternalRetries:'未知；仅记录公开 retrying/compacting 活动，不声称网络内部零重试',elapsedMs:Date.now()-started,
    promptDigest:digest(prepared.prompt),promptBytes:Buffer.byteLength(prepared.prompt),report,assessment,activity,cleanupStatus,managedDiagnostic,
    rawPublic:text===null?null:{text:safe,digest:digest(text),bytes:Buffer.byteLength(text),altered:safe!==text},diagnostic};
}

export async function main(argv) {
  const [mode,...args]=argv,opts={}; assert.ok(['prepare','run'].includes(mode)); assert.equal(args.length%2,0);
  for(let i=0;i<args.length;i+=2){assert.ok(['--suite','--suite-digest','--installed','--manifest','--agent','--output'].includes(args[i]));assert.ok(!Object.hasOwn(opts,args[i]));opts[args[i]]=args[i+1];}
  for(const key of ['--suite','--suite-digest','--installed','--manifest','--agent','--output'])assert.ok(opts[key]);
  assert.ok(path.isAbsolute(opts['--agent'])&&path.isAbsolute(opts['--output'])&&!fs.existsSync(opts['--output']));
  const admitted=verify({root:opts['--installed'],manifestDigest:opts['--manifest']});
  const bytes=fs.readFileSync(opts['--suite']);assert.equal(digest(bytes),opts['--suite-digest']);
  const suite=JSON.parse(bytes);assert.equal(suite.profile,'review-assessment-fixtures/v1');assert.equal(suite.cases.length,4);
  assert.deepEqual(suite.cases.map(c=>c.id),['S02-negative','S02-positive','C01-negative','C01-positive']);
  const load=relative=>import(pathToFileURL(path.join(opts['--installed'],relative)).href);
  const api=Object.assign({},...await Promise.all(['packages/task-application/leader-ports.ts','packages/task-application/review-assessment-contract.ts',
    'packages/task-application/review-assessment.ts','packages/task-generic-files/layout.ts','packages/agent-observation/normalization.ts'].map(load)));
  process.env.MARSHAL_AGENT_EXECUTABLE=opts['--agent'];
  const {default:config,QWEN_MANAGED_ARGS}=await load('packages/task-generic-files/qwen-review-service-config.ts');
  assert.equal(api.reviewAssessmentsEnabled(config.review),true);
  fs.mkdirSync(opts['--output'],{recursive:true,mode:0o700});
  const save=(name,value)=>fs.writeFileSync(path.join(opts['--output'],name),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:'wx'});
  save('admission.json',{candidate:admitted.sourceHead,manifest:opts['--manifest'],suiteDigest:digest(bytes),scriptDigest:digest(fs.readFileSync(new URL(import.meta.url))),
    kind:'NEW_SYNTHETIC_COMPONENT_INPUT',boundary:'非原 Task 批准；非 Core/整链验收；模型标签只在组件侧比较',mode,configuration:'installed-default',managedArgs:QWEN_MANAGED_ARGS,deadlineMs:180000});
  const results=[];
  for(const example of suite.cases){
    const synthetic=syntheticInput(example,api);save(example.id+'-input.json',synthetic);
    if(mode==='prepare')continue;
    const cwd=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'marshal-assessment-')));fs.chmodSync(cwd,0o700);
    const result=await evaluate({config,api,input:synthetic.input,cwd,onPrepared:prompt=>fs.writeFileSync(path.join(opts['--output'],example.id+'-prompt.txt'),prompt,{mode:0o600,flag:'wx'})});
    const record={id:example.id,candidate:admitted.sourceHead,runtime:cwd,expected:example.expected,...result,
      componentResult:result.valid&&example.expected.includes(result.report?.verdict)?'PASS':'FAIL'};
    save(example.id+'-result.json',record);results.push(record);console.log(JSON.stringify({id:record.id,result:record.componentResult,elapsedMs:record.elapsedMs}));
    if(!result.cleaned||result.extraBehavior)break;
  }
  save('results.json',{candidate:admitted.sourceHead,mode,results});
  if(mode==='run'&&(results.length!==4||results.some(r=>r.componentResult!=='PASS')))process.exitCode=1;
}
if(process.argv[1]&&import.meta.url===pathToFileURL(process.argv[1]).href)await main(process.argv.slice(2));
