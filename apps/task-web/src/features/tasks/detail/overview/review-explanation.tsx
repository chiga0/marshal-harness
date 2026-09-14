import {useQuery} from '@tanstack/react-query';
import type {LeaderRecord,Transport} from '@/lib/transport/types';
import {checkArtifact,matchesContract} from '../../../artifacts/traceability';
import {blobBytes,fetchVerifiedArtifact,sha256Hex} from '../../../artifacts/downloader';
import {taskKeys} from '../query-keys';
import {canonical} from './leader-decision';
interface Finding {id:string;nodeIds:string[];requirement:string;observation:string;requestedChange:string}
interface ReviewReport {profile:'task-independent-review/v1';inputDigest:string;selectionDigest:string;verdict:'accept'|'rework'|'reject';summary:string;findings:Finding[]}
const closed=(value:unknown,keys:string[]):value is Record<string,unknown> => !!value && typeof value==='object' && !Array.isArray(value) && Object.keys(value).sort().join(',')===keys.sort().join(',');
const text=(value:unknown,max:number):value is string => typeof value==='string' && value.length>0 && new TextDecoder().decode(new TextEncoder().encode(value))===value && !value.includes('\0') && new TextEncoder().encode(value).length<=max;
export async function readReviewExplanation(leader:LeaderRecord,transport:Transport):Promise<ReviewReport> {
  if(!matchesContract(leader,'LeaderView') || !leader.review || leader.review.evidenceIds.length!==1)throw new Error('review_reference');
  const {digest,...decision}=leader.review;
  if('sha256:'+await sha256Hex(new Blob([canonical(decision)]))!==digest)throw new Error('review_digest');
  const artifact=checkArtifact(await transport.getArtifact(decision.evidenceIds[0]!),decision.evidenceIds[0]!,leader.taskId);
  if(artifact.kind!=='evidence'||artifact.status!=='ready'||artifact.bytes>262144)throw new Error('review_artifact');
  const {blob}=await fetchVerifiedArtifact(transport,{artifactId:artifact.id,fileName:artifact.name,expectedBytes:artifact.bytes,expectedDigest:artifact.digest});
  const envelope:unknown=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(await blobBytes(blob)));
  if(!closed(envelope,['profile','ticketDigest','report']) || envelope.profile!=='task-independent-review/v1' || !matchesContract(envelope.ticketDigest,'Digest'))throw new Error('review_envelope');
  const report=envelope.report;
  if(!closed(report,['profile','inputDigest','selectionDigest','verdict','summary','findings']) || report.profile!=='task-independent-review/v1' || !matchesContract(report.inputDigest,'Digest') || report.selectionDigest!==decision.selectionDigest || report.verdict!==decision.verdict || !text(report.summary,4096) || !Array.isArray(report.findings) || report.findings.length>16 || report.verdict==='accept'&&report.findings.length!==0)throw new Error('review_binding');
  if(!report.findings.every(f=>closed(f,['id','nodeIds','requirement','observation','requestedChange']) && matchesContract(f.id,'Id') && Array.isArray(f.nodeIds) && f.nodeIds.length>0 && f.nodeIds.length<=64 && new Set(f.nodeIds).size===f.nodeIds.length && f.nodeIds.every(id=>matchesContract(id,'Id')) && ['requirement','observation','requestedChange'].every(k=>text(f[k],2048))) || new Set(report.findings.map(f=>f.id)).size!==report.findings.length)throw new Error('review_findings');
  return report as unknown as ReviewReport;
}
export function ReviewExplanation({leader,transport}:{leader:LeaderRecord|null;transport:Transport}) {
  const query=useQuery({queryKey:[...taskKeys.all(leader?.taskId??''),'review-explanation',leader?.review?.digest],enabled:!!leader?.review,retry:false,queryFn:()=>readReviewExplanation(leader!,transport)});
  if(!leader?.review)return null;
  const report=query.data;
  return <section aria-label="独立评审理由" data-testid="review-explanation" className="space-y-3 border-t border-border pt-5">
    <h2 className="text-base font-semibold">独立评审理由</h2>
    {report ? <>
      <p className="whitespace-pre-wrap break-words text-sm leading-6" data-testid="review-summary">{report.summary}</p>
      {report.findings.length ? <div className="divide-y divide-border" aria-label="评审发现">{report.findings.map((finding,index)=><details key={finding.id} className="workspace-disclosure py-3">
        <summary>{index+1}. {finding.requirement}</summary>
        <dl className="mt-3 space-y-2 text-sm"><div><dt className="font-medium">实际发现</dt><dd className="whitespace-pre-wrap break-words">{finding.observation}</dd></div><div><dt className="font-medium">建议修改</dt><dd className="whitespace-pre-wrap break-words">{finding.requestedChange}</dd></div></dl>
      </details>)}</div> : <p className="text-xs text-text-secondary">本次评审未列出需修改的问题。</p>}
      <details className="workspace-disclosure"><summary>评审原文与技术证据</summary><p className="break-all text-xs">评审记录：{leader.review.digest} · 评审执行：{leader.review.workerId}</p><p className="break-all text-xs">证据：{leader.review.evidenceIds.join('，')}</p><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(report,null,2)}</pre></details>
      <p className="text-xs text-text-secondary">来自绑定当前评审记录的原始意见；评审结论与独立验收结果分别核对。</p>
    </> : <p className="text-sm text-text-secondary">{query.isError?'评审正文暂不可读取或绑定校验未通过；不根据结论猜测理由。':'正在读取已记录的评审理由…'}</p>}
  </section>;
}
