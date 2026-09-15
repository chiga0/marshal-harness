import {it,expect,vi} from 'vitest';
import {render,screen} from '@testing-library/react';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {ReviewExplanation,readReviewExplanation} from './review-explanation';
import {PlanCard} from './plan-card';
import {assessmentFixture} from '../shared/review-assessment.fixture';
import {reviewHash} from '../shared/review-assessment';
import {makeArtifact,makeFakeTransport,makeLeader,makeTask,TASK_ID} from '../testing/fixtures';
import {sha256Hex} from '../../../artifacts/downloader';
async function fixture(change?:(f:Awaited<ReturnType<typeof assessmentFixture>>)=>void){
 const f=await assessmentFixture();change?.(f);
 const raw=JSON.stringify({profile:'task-independent-review/v2',ticketDigest:'sha256:'+'c'.repeat(64),report:f.report,assessment:f.assessment});
 const artifact=makeArtifact({id:'review-evidence',taskId:TASK_ID,kind:'evidence',status:'ready',mediaType:'application/json',bytes:new TextEncoder().encode(raw).length,digest:'sha256:'+await sha256Hex(new Blob([raw]))});
 const decision={verdict:f.report.verdict as 'accept',selectionDigest:f.report.selectionDigest,policyDigest:'sha256:'+'d'.repeat(64),workerId:'worker-review',evidenceIds:[artifact.id]};
 const leader=makeLeader({review:{...decision,digest:await reviewHash(decision)}});
 const {transport}=makeFakeTransport({getPlan:async()=>f.plan,getArtifact:async()=>artifact,getArtifactContent:async()=>new Blob([raw])});return {...f,leader,transport};
}
it('真实v2外层经原artifact读取与报告绑定后显示文本方法/来源，旧配置检查不提升',async()=>{
 const f=await fixture();render(<QueryClientProvider client={new QueryClient()}><ReviewExplanation leader={f.leader} transport={f.transport}/></QueryClientProvider>);
 expect(await screen.findByTestId('review-assessment')).toHaveTextContent('检查方法：文本评审');
 expect(screen.getAllByTestId('review-assessment-item')).toHaveLength(5);
 expect(screen.getByTestId('review-assessment')).toHaveTextContent('不表示软件、浏览器或外部效果已实际测试');
 expect(screen.queryByTestId('review-coverage-unavailable')).not.toBeInTheDocument();
});
it.each(['broken-report','old-plan','unknown-version','missing-assessment'])('实际v2读取拒绝%s不降级总体accept',async(kind)=>{
 const f=await fixture();if(kind==='old-plan')f.transport.getPlan=async()=>({...f.plan,digest:'sha256:'+'0'.repeat(64)});
 else{const raw=JSON.stringify({profile:kind==='unknown-version'?'task-independent-review/v3':'task-independent-review/v2',ticketDigest:'sha256:'+'c'.repeat(64),report:f.report,...(kind==='missing-assessment'?{}:{assessment:{...f.assessment,reportDigest:'sha256:'+'0'.repeat(64)}})});const a=await f.transport.getArtifact('review-evidence');f.transport.getArtifact=async()=>({...a,bytes:new TextEncoder().encode(raw).length,digest:'sha256:'+await sha256Hex(new Blob([raw]))});f.transport.getArtifactContent=async()=>new Blob([raw]);}
 await expect(readReviewExplanation(f.leader,f.transport)).rejects.toThrow();
});
it('批准前业务项与固定政策原文可见，核验完成才可批准',async()=>{
 const f=await fixture();render(<PlanCard task={makeTask({allowedActions:['approve']})} plan={f.plan} transport={f.transport} onViewLatest={vi.fn()}/>);
 expect(screen.getByTestId('plan-approve-open')).toBeDisabled();
 expect(await screen.findByText('配置固定政策')).toBeInTheDocument();expect(screen.getByText('业务验收条目')).toBeInTheDocument();
 expect(screen.getByTestId('plan-review-criteria')).toHaveTextContent('成果符合原需求');expect(screen.getByTestId('plan-approve-open')).toBeEnabled();
});
it('坏目录不冒已校验政策、不能批准；原始技术正文仍可读',async()=>{
 const f=await fixture();f.plan.acceptance[0]='已篡改';render(<PlanCard task={makeTask({allowedActions:['approve']})} plan={f.plan} transport={f.transport} onViewLatest={vi.fn()}/>);
 expect(await screen.findByRole('alert')).toHaveTextContent('验收目录校验未通过');expect(screen.getByTestId('plan-approve-open')).toBeDisabled();expect(screen.getByTestId('plan-technical-details')).toHaveTextContent('task-review-criteria/v1');
});

it.each(['empty','whitespace'])('实际v2空来源提示 %s，保留引用而非显示不存在正文',async(mode)=>{
 const f=await fixture(f=>{f.assessment.sources[2]!.digest=mode==='empty'?'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855':'sha256:'+'c'.repeat(64);f.assessment.checks.forEach(c=>c.evidence[0]!.quote=mode==='empty'?'':' \n\t');});
 render(<QueryClientProvider client={new QueryClient()}><ReviewExplanation leader={f.leader} transport={f.transport}/></QueryClientProvider>);
 await screen.findByTestId('review-assessment');
 expect(screen.getAllByText(mode==='empty'?'原材料为空（0字节）':'引用内容仅含空白字符',{exact:true})).toHaveLength(5);
});
