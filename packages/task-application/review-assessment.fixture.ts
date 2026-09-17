import assert from 'node:assert/strict';
import {fixture, proposal, hash} from './leader.fixture.ts';
import {withReviewCriteria, reviewCriteria, reviewSources} from './review-assessment-contract.ts';
import {startReviewWithAssessments} from './review-assessment.ts';

export async function assessmentFixture(t, options = {}) {
  const f = await fixture(t, {reviewAssessments: true, maxCalls: 16, verificationCheck() {}, ...options});
  if (options.existingParent) return f;
  const task = await f.call({operation:'task.create',key:'create',body:{intent:'独立核验两份原始区域结果',
    limits:{timeoutMs:120000,maxAttempts:32,maxWorkers:3}}});
  assert.equal((await f.decision(await f.take('leader'),[{type:'plan',proposal:withReviewCriteria(proposal)}])).status,'completed');
  const plan = await f.call({operation:'task.plan',taskId:task.id}), current = await f.get(task.id);
  await f.call({operation:'task.approve',taskId:task.id,key:'approve',body:{expectedRevision:current.revision,planDigest:plan.digest,planRevision:plan.revision}});
  const command=await f.read(tx=>tx.commands().find(row=>JSON.parse(row.payload).action==='dispatch'));
  await f.app.execution.expandDispatch(command.id,command.revision);
  await f.author(await f.take('execute','east')); await f.author(await f.take('execute','west'));
  const ticket=await requestReview(f);
  return Object.assign(f,{taskId:task.id,reviewTicket:ticket,approvedPlan:plan});
}
export async function requestReview(f) {
  const leader=await f.take('leader'),selection=leader.input.leader.snapshot.selection;
  assert.equal((await f.decision(leader,[{type:'work',kind:'review',nodeIds:selection.map(row=>row.nodeId),selectionDigest:hash(selection)}])).status,'completed');
  return await f.take('review');
}
export function assessmentProposal(ticket, rejected = false) {
  const input=ticket.input.review,criteria=reviewCriteria(input.snapshot.plan),{sources,texts}=reviewSources(input);
  const original=sources.find(source=>source.kind==='task-input');
  const ref=source=>({sourceId:source.id,quote:Array.from(texts.get(source.id)).slice(0,64).join('')});
  const finding={id:'missing-rule',nodeIds:['east'],requirement:criteria[0].requirement,
    observation:'受控夹具中的候选尚缺一项明确规则',requestedChange:'补齐原规则后重新检查'};
  return {profile:'task-review-assessment-proposal/v1',verdict:rejected?'rework':'accept',summary:'受控文本依据，不代表模型或外部执行实测',
    findings:rejected?[finding]:[],checks:criteria.map((criterion,index)=>({itemId:criterion.id,
      assessment:rejected&&index===0?'fail':criterion.allowNotApplicable?'not-applicable':'pass',method:'text-review',
      reason:'受控夹具按原条目检查，例中没有用户操作或数据恢复承诺。',
      evidence:index===0?sources.filter(source=>source.kind==='candidate').map(ref).concat(ref(original)):[ref(original)],
      counterexample:null,findingIds:rejected&&index===0?[finding.id]:[]}))};
}
export async function produceReview(f, ticket, value, options = {}) {
  let calls=0,stops=0,actualPrompt=null;
  const started={executionId:'assessment-'+ticket.workerId,startedAt:new Date().toISOString()};
  const provider={id:'fixture',start(input){calls++;actualPrompt=input.prompt;
    return {started:Promise.resolve(started),stop(){stops++;return Promise.resolve();},completion:Promise.resolve({providerId:'fixture',
      status:options.status??'completed',stopReason:options.stopReason??'end_turn',outputText:typeof value==='string'?value:JSON.stringify(value),
      cleanup:{started,cleaned:options.cleaned!==false,scope:'controlled-fixture'}})};}};
  const port=options.port??f.reviewPort,run=options.bypass?(port,options)=>port.start(options):startReviewWithAssessments;
  const handle=run(port,{ticket,provider,prepared:{cwd:f.parent,prompt:'原始评审输入，逐字保持'}});
  await f.app.execution.started(ticket,await handle.started);const result=await handle.completion;
  return {result,handle,get calls(){return calls;},get stops(){return stops;},actualPrompt};
}
