import {useQuery} from '@tanstack/react-query';
import type {LeaderRecord,Transport} from '@/lib/transport/types';
import {checkArtifact,matchesContract} from '../../../artifacts/traceability';
import {blobBytes,fetchVerifiedArtifact,sha256Hex} from '../../../artifacts/downloader';
import {taskKeys} from '../query-keys';
import {canonical} from './leader-decision';
import {validateAssessment,type Assessment} from '../shared/review-assessment';
import {ReviewAssessmentView} from '../shared/review-assessment-view';
interface Finding {id:string;nodeIds:string[];requirement:string;observation:string;requestedChange:string}
interface ReviewReport {profile:'task-independent-review/v1';inputDigest:string;selectionDigest:string;verdict:'accept'|'rework'|'reject';summary:string;findings:Finding[]}
const closed=(value:unknown,keys:string[]):value is Record<string,unknown> => !!value && typeof value==='object' && !Array.isArray(value) && Object.keys(value).sort().join(',')===keys.sort().join(',');
const text=(value:unknown,max:number):value is string => typeof value==='string' && value.length>0 && new TextDecoder().decode(new TextEncoder().encode(value))===value && !value.includes('\0') && new TextEncoder().encode(value).length<=max;
export async function readReviewExplanation(leader:LeaderRecord,transport:Transport):Promise<ReviewReport & {assessment?:Assessment}> {
  if(!matchesContract(leader,'LeaderView') || !leader.review || leader.review.evidenceIds.length!==1)throw new Error('review_reference');
  const {digest,...decision}=leader.review;
  if('sha256:'+await sha256Hex(new Blob([canonical(decision)]))!==digest)throw new Error('review_digest');
  const artifact=checkArtifact(await transport.getArtifact(decision.evidenceIds[0]!),decision.evidenceIds[0]!,leader.taskId);
  if(artifact.kind!=='evidence'||artifact.status!=='ready'||artifact.bytes>262144)throw new Error('review_artifact');
  const {blob}=await fetchVerifiedArtifact(transport,{artifactId:artifact.id,fileName:artifact.name,expectedBytes:artifact.bytes,expectedDigest:artifact.digest});
  const envelope:unknown=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(await blobBytes(blob)));
  const v2=!!envelope&&typeof envelope==='object'&&'profile' in envelope&&envelope.profile==='task-independent-review/v2';
  if(!closed(envelope,v2?['profile','ticketDigest','report','assessment']:['profile','ticketDigest','report']) || !['task-independent-review/v1','task-independent-review/v2'].includes(envelope.profile as string) || !matchesContract(envelope.ticketDigest,'Digest') || v2&&artifact.bytes>131072)throw new Error('review_envelope');
  if(v2&&!matchesContract(envelope,'ReviewEvidenceEnvelope'))throw new Error('review_envelope');
  const report=envelope.report;
  if(!closed(report,['profile','inputDigest','selectionDigest','verdict','summary','findings']) || report.profile!=='task-independent-review/v1' || !matchesContract(report.inputDigest,'Digest') || report.selectionDigest!==decision.selectionDigest || report.verdict!==decision.verdict || !text(report.summary,4096) || !Array.isArray(report.findings) || report.findings.length>16 || report.verdict==='accept'&&report.findings.length!==0)throw new Error('review_binding');
  if(!report.findings.every(f=>closed(f,['id','nodeIds','requirement','observation','requestedChange']) && matchesContract(f.id,'Id') && Array.isArray(f.nodeIds) && f.nodeIds.length>0 && f.nodeIds.length<=64 && new Set(f.nodeIds).size===f.nodeIds.length && f.nodeIds.every(id=>matchesContract(id,'Id')) && ['requirement','observation','requestedChange'].every(k=>text(f[k],2048))) || new Set(report.findings.map(f=>f.id)).size!==report.findings.length)throw new Error('review_findings');
  if(v2){
    const plan=await transport.getPlan(leader.taskId);
    if(!matchesContract(plan,'Plan')||plan.taskId!==leader.taskId)throw new Error('review_plan_binding');
    const assessment=await validateAssessment(envelope.assessment,report as unknown as ReviewReport,plan);
    return {...report as unknown as ReviewReport,assessment};
  }
  return report as unknown as ReviewReport;
}
const verdictLabels={accept:'接受',rework:'需要修改',reject:'拒绝'};
function summaryPreview(summary:string) {
  const characters=Array.from(summary);
  // 技术标识不进入节选；完整报告保持原字节语义，可在下方展开。
  if(/sha256:|\b[a-f0-9]{64}\b|\b(?:artifact|worker)-[A-Za-z0-9_-]+/i.test(summary))return null;
  return characters.length>160 ? characters.slice(0,160).join('')+'…' : summary;
}
export function ReviewExplanation({leader,transport}:{leader:LeaderRecord|null;transport:Transport}) {
  const query=useQuery({queryKey:[...taskKeys.all(leader?.taskId??''),'review-explanation',leader?.review?.digest],enabled:!!leader?.review,retry:false,queryFn:()=>readReviewExplanation(leader!,transport)});
  if(!leader?.review)return null;
  const report=query.data;
  const preview=report ? summaryPreview(report.summary) : null;
  return <section aria-label="独立评审理由" data-testid="review-explanation" className="space-y-3 border-t border-border pt-5">
    <h2 className="text-base font-semibold">独立评审理由</h2>
    {report ? <>
      <p className="text-sm font-medium" data-testid="review-verdict">{verdictLabels[report.verdict]} · {report.findings.length} 项评审发现</p>
      {preview===null ? <p className="text-sm text-text-secondary">已记录评审理由，展开阅读完整原文。</p> : <p className="whitespace-pre-wrap break-words text-sm leading-6" data-testid="review-summary">{preview}{preview!==report.summary && <span className="block text-xs text-text-secondary">原文节选</span>}</p>}
      {(preview===null || preview!==report.summary) && <details className="workspace-disclosure" data-testid="review-full-summary"><summary>查看完整评审原文</summary><p className="mt-3 whitespace-pre-wrap break-words text-sm leading-6">{report.summary}</p></details>}
      {report.findings.length ? <div className="divide-y divide-border" aria-label="评审发现">{report.findings.map((finding,index)=><details key={finding.id} open className="workspace-disclosure py-3">
        <summary>{index+1}. {finding.requirement}</summary>
        <dl className="mt-3 space-y-2 text-sm"><div><dt className="font-medium">实际发现</dt><dd className="whitespace-pre-wrap break-words">{finding.observation}</dd></div><div><dt className="font-medium">建议修改</dt><dd className="whitespace-pre-wrap break-words">{finding.requestedChange}</dd></div></dl>
      </details>)}</div> : <p className="text-xs text-text-secondary">本次评审未列出需修改的问题。</p>}
      {report.assessment ? <ReviewAssessmentView assessment={report.assessment}/> : <p className="text-xs text-text-secondary" data-testid="review-coverage-unavailable">此历史格式未提供逐项评审证据，不能据总体结论推断全部要求已覆盖。</p>}
      <details className="workspace-disclosure"><summary>评审技术证据与原始数据</summary><p className="break-all text-xs">评审记录：{leader.review.digest} · 评审执行：{leader.review.workerId}</p><p className="break-all text-xs">证据：{leader.review.evidenceIds.join('，')}</p><pre className="whitespace-pre-wrap break-words text-xs">{JSON.stringify(report,null,2)}</pre></details>
      <p className="text-xs text-text-secondary">来自绑定当前评审记录的原始意见；评审结论与独立验收结果分别核对。</p>
    </> : <p className="text-sm text-text-secondary">{query.isError?'评审正文暂不可读取或绑定校验未通过；不根据结论猜测理由。':'正在读取已记录的评审理由…'}</p>}
  </section>;
}
