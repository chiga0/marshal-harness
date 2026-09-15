// 只读绑定检查；既有证据只能来自本任务当前 ReviewDecision，不能自选制品。
import assert from 'node:assert/strict';
import {encode,digest} from '../packages/task-store/store.mjs';
const hash=value=>digest(encode(value));
export async function readBoundReview({taskId,leader,api,readContent,validateStored,requireV2=true}) {
  assert.equal(leader.taskId,taskId);const decision=leader.review;
  assert.ok(decision&&Array.isArray(decision.evidenceIds)&&decision.evidenceIds.length===1);
  const {digest:decisionDigest,...body}=decision;assert.equal(hash(body),decisionDigest,'ReviewDecision 摘要不匹配');
  const id=decision.evidenceIds[0];assert.match(id,/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/);
  const metadata=await api('/v1/artifacts/'+id);
  assert.equal(metadata.id,id);assert.equal(metadata.taskId,taskId);assert.equal(metadata.kind,'evidence');assert.equal(metadata.status,'ready');
  assert.ok(Number.isSafeInteger(metadata.bytes)&&metadata.bytes>0&&metadata.bytes<=262144);
  const bytes=await readContent(id);assert.equal(bytes.length,metadata.bytes);assert.equal(digest(bytes),metadata.digest);
  const envelope=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes));
  assert.ok(['task-independent-review/v1','task-independent-review/v2'].includes(envelope.profile));
  if(requireV2)assert.equal(envelope.profile,'task-independent-review/v2','本候选必须提供逐项评审证据');
  if(envelope.profile==='task-independent-review/v2') {assert.ok(bytes.length<=131072);assert.equal(typeof validateStored,'function');validateStored(envelope);}
  assert.equal(envelope.report.verdict,decision.verdict);assert.equal(envelope.report.selectionDigest,decision.selectionDigest);
  return {metadata,envelope,content:bytes.toString('utf8'),boundary:'绑定原评审Artifact的公开文本证据；不是浏览器或业务效果实测'};
}
export function pendingAnswerGap(task,leader) {
  assert.equal(task.status,'awaiting-answer');assert.equal(leader.taskId,task.id);
  const request=leader.pendingRequest;
  assert.ok(request&&request.kind==='business'&&request.status==='pending'&&typeof request.prompt==='string');
  return {code:'case_answer_unavailable',taskStatus:task.status,request,
    boundary:'用例没有预设该问题的用户答复；保留实际请求，不编造答案。是否必要另作语义审查，不记通过。'};
}
export function finalReviewer(workers,leader) {
  const worker=workers.find(value=>value.id===leader.review?.workerId);
  assert.ok(worker&&worker.role==='reviewer','当前绑定Review成员缺失');return worker;
}
