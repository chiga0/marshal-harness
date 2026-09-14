import {describe,it,expect,vi} from 'vitest';
import {readLeaderDecision} from './leader-decision';
import {sha256Hex} from '../../../artifacts/downloader';
import {makeArtifact,makeFakeTransport,makeLeader,TASK_ID} from '../testing/fixtures';
const digest=async (text:string)=>'sha256:'+await sha256Hex(new Blob([text]));
async function fixture() {
  // UTF-16排序的固定对象，故意与传输JSON键顺序不同。
  const canonical='{"actions":[{"type":"review"}],"callId":"call-one","inputDigest":"sha256:'+'a'.repeat(64)+'","profile":"task-managed-leader/v1","summary":"对两份成果做独立检查"}';
  const report=JSON.parse(canonical),text=JSON.stringify({report:{summary:report.summary,...report}});
  const artifact=makeArtifact({id:'decision-evidence',taskId:TASK_ID,kind:'evidence',mediaType:'application/json',bytes:new TextEncoder().encode(text).length,digest:await digest(text)});
  const leader=makeLeader({lastDecision:{digest:await digest(canonical),callId:'call-one',evidenceId:artifact.id}});
  const getArtifactContent=vi.fn(async()=>new Blob([text]));
  const {transport}=makeFakeTransport({getArtifact:async()=>artifact,getArtifactContent});
  return {leader,transport,artifact,getArtifactContent};
}
describe('可读Leader决定绑定',()=>{
  it('只呈现精确当前决定的业务summary和动作',async()=>{
    const {leader,transport}=await fixture();
    expect(await readLeaderDecision(leader,transport)).toEqual({summary:'对两份成果做独立检查',actions:[{type:'review'}]});
  });
  it('制品归属错误时不读取正文',async()=>{
    const f=await fixture();
    f.transport.getArtifact=async()=>({...f.artifact,taskId:'other-task'});
    await expect(readLeaderDecision(f.leader,f.transport)).rejects.toThrow();
    expect(f.getArtifactContent).not.toHaveBeenCalled();
  });
  it('同一制品不允许绑定到另一次决定摘要',async()=>{
    const f=await fixture();
    f.leader.lastDecision!.digest='sha256:'+'b'.repeat(64);
    await expect(readLeaderDecision(f.leader,f.transport)).rejects.toThrow('decision_digest');
  });
});
