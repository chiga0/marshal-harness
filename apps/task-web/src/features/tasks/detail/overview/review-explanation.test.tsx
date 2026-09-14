import {describe,it,expect,vi} from 'vitest';
import {render,screen,within} from '@testing-library/react';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {ReviewExplanation,readReviewExplanation} from './review-explanation';
import {canonical} from './leader-decision';
import {sha256Hex} from '../../../artifacts/downloader';
import {makeArtifact,makeFakeTransport,makeLeader,TASK_ID} from '../testing/fixtures';
const hash=async(value:unknown)=>'sha256:'+await sha256Hex(new Blob([typeof value==='string'?value:canonical(value)]));
async function fixture(change:Record<string,unknown>={}) {
  const report={profile:'task-independent-review/v1',inputDigest:'sha256:'+'a'.repeat(64),selectionDigest:'sha256:'+'b'.repeat(64),verdict:'rework',summary:'恢复路径缺少正文来源，需要补全。',findings:[{id:'finding-one',nodeIds:['author'],requirement:'索引损坏后应可重建',observation:'只记录路径和哈希，无法恢复全文。',requestedChange:'明确只读原文件或内容副本的数据来源。'}],...change};
  const raw=JSON.stringify({profile:'task-independent-review/v1',ticketDigest:'sha256:'+'c'.repeat(64),report});
  const artifact=makeArtifact({id:'review-evidence',taskId:TASK_ID,kind:'evidence',status:'ready',mediaType:'application/json',bytes:new TextEncoder().encode(raw).length,digest:await hash(raw)});
  const decision={verdict:'rework' as const,selectionDigest:'sha256:'+'b'.repeat(64),policyDigest:'sha256:'+'d'.repeat(64),workerId:'worker-review',evidenceIds:[artifact.id]};
  const leader=makeLeader({review:{...decision,digest:await hash(decision)}});
  const content=vi.fn(async()=>new Blob([raw]));
  const {transport}=makeFakeTransport({getArtifact:async()=>artifact,getArtifactContent:content});
  return {leader,transport,content,artifact,report};
}
describe('当前独立评审理由',()=>{
  it('展示精确原summary和发现，原文及技术ID折叠',async()=>{
    const f=await fixture();
    render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><ReviewExplanation leader={f.leader} transport={f.transport}/></QueryClientProvider>);
    expect(await screen.findByTestId('review-summary')).toHaveTextContent(f.report.summary);
    expect(screen.getByText('只记录路径和哈希，无法恢复全文。').closest('details')).not.toHaveAttribute('open');
    expect(within(screen.getByTestId('review-explanation')).getByText(/评审记录：/).closest('details')).not.toHaveAttribute('open');
  });
  it('摘要不匹配在读取正文之前拒绝',async()=>{const f=await fixture();f.leader.review!.digest='sha256:'+'0'.repeat(64);await expect(readReviewExplanation(f.leader,f.transport)).rejects.toThrow('review_digest');expect(f.content).not.toHaveBeenCalled();});
  it('跨任务制品在读取正文之前拒绝',async()=>{const f=await fixture();f.transport.getArtifact=async()=>({...f.artifact,taskId:'another-task'});await expect(readReviewExplanation(f.leader,f.transport)).rejects.toThrow();expect(f.content).not.toHaveBeenCalled();});
  it.each([{selectionDigest:'sha256:'+'f'.repeat(64)},{verdict:'accept'},{findings:[{id:'bad',nodeIds:[],requirement:'x',observation:'y',requestedChange:'z'}]}])('不显示不匹配或无效评审正文%j',async(change)=>{const f=await fixture(change);await expect(readReviewExplanation(f.leader,f.transport)).rejects.toThrow();});
  it('下载摘要损坏不显示正文',async()=>{const f=await fixture();f.transport.getArtifactContent=async()=>new Blob(['altered']);await expect(readReviewExplanation(f.leader,f.transport)).rejects.toThrow();});
});
