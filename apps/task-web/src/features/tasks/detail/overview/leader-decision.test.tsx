import {describe,it,expect,vi} from 'vitest';
import {render,screen} from '@testing-library/react';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {LeaderDecision,readLeaderDecision,leaderActionLabel} from './leader-decision';
import {sha256Hex} from '../../../artifacts/downloader';
import {makeArtifact,makeFakeTransport,makeLeader,TASK_ID} from '../testing/fixtures';
const digest=async (text:string)=>'sha256:'+await sha256Hex(new Blob([text]));
async function fixture() {
  // UTF-16排序的固定对象，故意与传输JSON键顺序不同。
  const canonical='{"actions":[{"kind":"review","type":"work"}],"callId":"call-one","inputDigest":"sha256:'+'a'.repeat(64)+'","profile":"task-managed-leader/v1","summary":"对两份成果做独立检查"}';
  const report=JSON.parse(canonical),text=JSON.stringify({report:{summary:report.summary,...report}});
  const artifact=makeArtifact({id:'decision-evidence',taskId:TASK_ID,kind:'evidence',mediaType:'application/json',bytes:new TextEncoder().encode(text).length,digest:await digest(text)});
  const leader=makeLeader({lastDecision:{digest:await digest(canonical),callId:'call-one',evidenceId:artifact.id}});
  const getArtifactContent=vi.fn(async()=>new Blob([text]));
  const {transport}=makeFakeTransport({getArtifact:async()=>artifact,getArtifactContent});
  return {leader,transport,artifact,getArtifactContent};
}
describe('可读Leader决定绑定',()=>{
  it('默认只显示有依据的行动，原始说明渐进披露且不宣称收尾已执行',async()=>{
    const {leader,transport}=await fixture();
    render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><LeaderDecision leader={leader} transport={transport}/></QueryClientProvider>);
    expect(await screen.findByTestId('leader-action-summary')).toHaveTextContent('组织独立评审');
    expect(screen.getByText('对两份成果做独立检查').closest('details')).not.toHaveAttribute('open');
    expect(leaderActionLabel({type:'conclude',outcome:'succeeded'})).toBe('建议完成交付收尾');
  });

  it.each([['execute','安排成员执行'],['review','组织独立评审'],['verify','安排独立验收']])('真实work.%s呈现动作含义但不假称已执行', (kind,label)=>{
    expect(leaderActionLabel({type:'work',kind})).toBe(label);
  });

  it('只呈现精确当前决定的业务summary和动作',async()=>{
    const {leader,transport}=await fixture();
    expect(await readLeaderDecision(leader,transport)).toEqual({summary:'对两份成果做独立检查',actions:[{kind:'review',type:'work'}]});
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
