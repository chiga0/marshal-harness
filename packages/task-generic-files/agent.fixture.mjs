// Deterministic test-only ACP peer; never shipped as a production Agent.
import fs from 'node:fs';
import path from 'node:path';
import {createInterface} from 'node:readline';
import {encode, digest} from '../task-store/store.mjs';
import {MAX_FILE} from './policy.mjs';
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const reply = (q, result) => send({jsonrpc:'2.0',id:q.id,result});
const hash = value => digest(encode(value));
async function prompt(q) {
  const text=q.params.prompt[0].text; let out;
  if(process.env.MARSHAL_TEST_PROMPT_LOG) fs.writeFileSync(path.join(process.env.MARSHAL_TEST_PROMPT_LOG,digest(Buffer.from(text)).slice(7)+'.txt'),text,{flag:'wx',mode:0o600});
  if (text.includes('\n完整冻结输入：')) {
    const input=JSON.parse(text.split('\n完整冻结输入：').at(-1)), s=input.snapshot;
    if (input.profile==='task-independent-review/v1') {
      const files=input.materials.filter(x=>x.nodeId);
      if (!files.length || !files.every(x=>x.content === s.task.input.intent + ':' + x.nodeId)) throw Error('bad candidate');
      out={profile:input.profile,inputDigest:input.inputDigest,selectionDigest:input.selectionDigest,verdict:'accept',summary:'独立核对原目标和每份实际候选',findings:[]};
      if (process.argv.includes('review-wire')) {
        if (!text.includes('返回顶层必须且只能是profile、verdict、summary、findings四个字段')) throw Error('review prompt missing');
        out={profile:'generic-files-review-proposal/v1',verdict:out.verdict,summary:out.summary,findings:out.findings};
      }
    } else {
      const review=s.evidence.find(x=>x.kind==='review'), verified=s.evidence.find(x=>x.kind==='verification'); let action;
      if (!s.plan) {
        const ids=s.task.input.intent.includes('依赖')?['research','synthesis']:['proposal','risk'];
        action={type:'plan',proposal:{summary:s.task.input.intent,nodes:[...ids.map(id=>({id,role:'author',goal:s.task.input.intent+':'+id,scope:[],providerId:null})),{id:'check',role:'verifier',goal:'核验字节',scope:[],providerId:null}],
          edges:s.task.input.intent.includes('依赖')?[{from:ids[0],to:ids[1]},{from:ids[1],to:'check'}]:ids.map(from=>({from,to:'check'})),deliverables:['文件'],acceptance:['符合原目标'],assumptions:[]}};
      } else if (!review) action={type:'work',kind:'review',nodeIds:s.selection.map(x=>x.nodeId),selectionDigest:hash(s.selection)};
      else if (!verified) action={type:'work',kind:'verify',nodeIds:['check'],selectionDigest:hash(s.selection)};
      else if (s.obligation.some(x=>x.reason==='delivery-ready')) action={type:'conclude',outcome:'succeeded',summary:'完成文件交付，未执行外部操作',basisDigests:[review.digest,verified.digest]};
      else action={type:'deliver',artifactId:input.materials.find(x=>x.kind==='delivery').id,acceptanceDigest:verified.digest,reviewDigest:review.digest};
      out={profile:input.profile,callId:input.callId,inputDigest:input.inputDigest,summary:'消费当前事实',actions:[action]};
      if (process.argv.includes('short-wire')) {
        if (!text.includes('返回顶层必须且只能是profile、summary、actions三个字段')) throw Error('short prompt missing');
        out={profile:'generic-files-leader-proposal/v1',summary:out.summary,actions:out.actions};
      }
    }
  } else {
    const input=JSON.parse(text.slice(text.indexOf('{"task":')));
    if (!input.plan.acceptance.some(item => item.includes(`${MAX_FILE} UTF-8 字节`))) throw Error('author byte limit missing');
    fs.writeFileSync('result.md',input.task.intent+':'+input.node.id,{flag:'wx',mode:0o600}); out={summary:'候选已写，非权威验收'};
  }
  send({jsonrpc:'2.0',method:'session/update',params:{sessionId:q.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:JSON.stringify(out)}}}});
  reply(q,{stopReason:'end_turn'});
}
for await(const line of createInterface({input:process.stdin})) {
  const q=JSON.parse(line);
  if(q.method==='initialize')reply(q,{protocolVersion:1,agentCapabilities:{loadSession:false}});
  else if(q.method==='session/new')reply(q,{sessionId:'generic-fixture'});
  else if(q.method==='session/prompt')void prompt(q).catch(()=>process.exit(1));
}
