import test from 'node:test';
import assert from 'node:assert/strict';
import {assessmentFixture, assessmentProposal, produceReview, requestReview} from './review-assessment.fixture.ts';
import {registerReviewAssessments, reviewAssessmentsEnabled, reviewAssessmentEvidence} from './review-assessment.ts';
import {receipt, configuration, createReviewPort, parseManagedOutput} from './leader-ports.ts';
import {fixture,hash} from './leader.fixture.ts';
import {parseAssessmentProposal} from './review-assessment-contract.ts';
import {TaskApplication} from './application.ts';

const view=async(f,id)=>await f.call({operation:'task.leader',taskId:id});
async function evidence(f,id){const decision=(await view(f,id)).review;
  const result=await f.call({operation:'artifact.content',artifactId:decision.evidenceIds[0]});
  return JSON.parse(Buffer.from(result.content).toString());}

test('one original Provider completion and receipt bind a v2 Artifact; the original receipt survives the controller outcome container without minting new authority',async t=>{
  const f=await assessmentFixture(t),ticket=f.reviewTicket,native=await produceReview(f,ticket,assessmentProposal(ticket));
  assert.equal(native.calls,1);assert.equal(native.actualPrompt,'原始评审输入，逐字保持');
  await native.handle.stop();assert.equal(native.stops,1);
  assert.equal(receipt(f.reviewPort,ticket,native.result).value.verdict,'accept');
  const sidecar=reviewAssessmentEvidence(f.reviewPort,ticket,native.result);assert.ok(sidecar);
  sidecar.checks[0].reason='mutated';assert.notEqual(reviewAssessmentEvidence(f.reviewPort,ticket,native.result).checks[0].reason,'mutated');
  assert.deepEqual(reviewAssessmentEvidence(f.reviewPort,ticket,{...native.result}),reviewAssessmentEvidence(f.reviewPort,ticket,native.result));
  for(const altered of [{...native.result,receipt:structuredClone(native.result.receipt)},{...native.result,status:'failed'},{...native.result,cleanup:{...native.result.cleanup,cleaned:false}}])assert.throws(()=>reviewAssessmentEvidence(f.reviewPort,ticket,altered));
  assert.throws(()=>reviewAssessmentEvidence(f.reviewPort,{...ticket,workerId:'foreign'},native.result));
  assert.throws(()=>reviewAssessmentEvidence(f.reviewPort,ticket,{...native.result,receipt:{}}));
  assert.equal(await f.app.execution.finish(ticket,native.result).status,'completed');
  const stored=await evidence(f,f.taskId);assert.equal(stored.profile,'task-independent-review/v2');
  assert.equal(stored.report.profile,'task-independent-review/v1');assert.equal(stored.ticketDigest,hash(ticket));
  assert.equal(stored.assessment.reportDigest,hash(stored.report));assert.deepEqual(stored.assessment,reviewAssessmentEvidence(f.reviewPort,ticket,native.result));
  const before=await f.read(tx=>({head:tx.head(f.taskId),attempts:f.app.get(tx,f.taskId).attempts}));
  await f.app.execution.finish(ticket,native.result);assert.deepEqual(await f.read(tx=>({head:tx.head(f.taskId),attempts:f.app.get(tx,f.taskId).attempts})),before);
  await f.reopen();assert.deepEqual(await evidence(f,f.taskId),stored);assert.equal(native.calls,1);
});

test('registration requires the original Review port, closed fixed options and exactly one registration',()=>{
  const make=()=>createReviewPort({id:'review',providerId:'fixture',policy:{id:'review',version:'1',description:'fixture'},prepare:()=>({prompt:'original'}),parseReport:parseManagedOutput});
  const port=make();assert.equal(reviewAssessmentsEnabled(port),false);
  for(const object of [{},{...port},new Proxy(port,{})])assert.throws(()=>registerReviewAssessments(object,{profile:'task-review-assessment/v1'}));
  for(const options of [null,[],{}, {profile:'wrong'}, {profile:'task-review-assessment/v1',extra:true},
    Object.defineProperty({profile:'task-review-assessment/v1'},'hidden',{value:true}),{profile:'task-review-assessment/v1',[Symbol()]:true}])
    assert.throws(()=>registerReviewAssessments(make(),options));
  let called=false;assert.throws(()=>registerReviewAssessments(make(),{get profile(){called=true;return 'task-review-assessment/v1';}}));assert.equal(called,false);
  assert.equal(registerReviewAssessments(port,{profile:'task-review-assessment/v1'}),port);assert.equal(reviewAssessmentsEnabled(port),true);
  assert.throws(()=>registerReviewAssessments(port,{profile:'task-review-assessment/v1'}));
});

test('missing private capture never downgrades a valid original receipt to v1 acceptance',async t=>{
  const f=await assessmentFixture(t),ticket=f.reviewTicket;
  const native=await produceReview(f,ticket,assessmentProposal(ticket),{bypass:true});
  assert.equal(receipt(f.reviewPort,ticket,native.result).value.verdict,'accept');
  assert.equal(await f.app.execution.finish(ticket,{...native.result}).status,'failed');
  assert.equal((await view(f,f.taskId)).review,null);
  assert.equal(await f.read(tx=>f.app.get(tx,f.taskId)).failureCode,'invalid_review_report');assert.equal(native.calls,1);
});

test('a mapper returning a different valid original report cannot attach captured checks to it',async t=>{
  const f=await assessmentFixture(t,{reviewParser:args=>({...parseAssessmentProposal(args).report,summary:'不同原报告'})});
  const ticket=f.reviewTicket,native=await produceReview(f,ticket,assessmentProposal(ticket));
  assert.equal(receipt(f.reviewPort,ticket,native.result).value.summary,'不同原报告');
  assert.equal(reviewAssessmentEvidence(f.reviewPort,ticket,native.result),null);
  assert.equal(await f.app.execution.finish(ticket,native.result).status,'failed');assert.equal((await view(f,f.taskId)).review,null);
});

test('Depot staging failure publishes no partial assessment and retrying finish uses the original Provider result once',async t=>{
  const f=await assessmentFixture(t),ticket=f.reviewTicket,native=await produceReview(f,ticket,assessmentProposal(ticket));
  const stage=f.app.artifacts.stageOutputs.bind(f.app.artifacts),before=await f.read(tx=>tx.head(f.taskId));
  f.app.artifacts.stageOutputs=()=>{throw new Error('controlled_depot_failure');};
  assert.throws(()=>await f.app.execution.finish(ticket,native.result),/controlled_depot_failure/);
  assert.deepEqual(await f.read(tx=>tx.head(f.taskId)),before);assert.equal((await view(f,f.taskId)).review,null);
  f.app.artifacts.stageOutputs=stage;assert.equal(await f.app.execution.finish(ticket,native.result).status,'completed');assert.equal(native.calls,1);
});

test('invalid checks, failed/cancelled Provider and unconfirmed cleanup cannot create a v2 acceptance',async t=>{
  for(const mode of ['missing-check','unknown-accept','foreign-quote','failed','cancelled','unknown-cleanup'])await t.test(mode,async t=>{
    const f=await assessmentFixture(t),ticket=f.reviewTicket,raw=assessmentProposal(ticket);
    if(mode==='missing-check')raw.checks.pop();
    if(mode==='unknown-accept')raw.checks[0].assessment='unknown';
    if(mode==='foreign-quote')raw.checks[0].evidence[0].quote='没有出现在原输入中的证据';
    const native=await produceReview(f,ticket,raw,{...(['failed','cancelled'].includes(mode)?{status:mode}:{}),...(mode==='unknown-cleanup'?{cleaned:false}:{})});
    assert.equal(reviewAssessmentEvidence(f.reviewPort,ticket,native.result),null);
    await f.app.execution.finish(ticket,native.result);assert.equal((await view(f,f.taskId)).review,null);assert.equal(native.calls,1);
    if(['failed','cancelled'].includes(mode))assert.equal(await f.read(tx=>f.app.get(tx,f.taskId)).failureCode,'leader_result_rejected');
  });
});

test('cancellation, expiration and changed semantic readset preserve original gates after a valid assessment',async t=>{
  for(const mode of ['cancel','deadline','stale'])await t.test(mode,async t=>{
    const f=await assessmentFixture(t),ticket=f.reviewTicket,native=await produceReview(f,ticket,assessmentProposal(ticket));
    assert.ok(reviewAssessmentEvidence(f.reviewPort,ticket,native.result));
    if(mode==='cancel')await f.call({operation:'task.cancel',taskId:f.taskId,key:'cancel',body:{expectedRevision:(await f.get(f.taskId)).revision}});
    if(mode==='deadline')f.app.clock=()=>ticket.deadline+1;
    if(mode==='stale')f.app.transaction(true,tx=>{const task=f.app.get(tx,f.taskId);task.input.intent='受控新输入';task.inputDigest=hash(task.input);task.task.revision++;f.app.save(tx,task,'fixture.changed',{});});
    await f.app.execution.finish(ticket,native.result);assert.equal((await view(f,f.taskId)).review,null);
  });
});

test('original rework evidence selects affected nodes, then a fresh selection receives a new complete assessment',async t=>{
  const f=await assessmentFixture(t),first=f.reviewTicket;
  await f.rawReview(first,assessmentProposal(first,true));
  const rejected=await evidence(f,f.taskId),review=(await view(f,f.taskId)).review;
  assert.equal(review.verdict,'rework');assert.equal(rejected.assessment.checks[0].assessment,'fail');
  const leader=await f.take('leader');await f.decision(leader,[{type:'repair',nodeIds:['east'],basis:{kind:'review',digest:review.digest},feedback:'按原拒收补齐规则'}]);
  const repair=await f.take('execute','east');assert.equal(repair.input.repair.evidence.digest,(await f.call({operation:'artifact.get',artifactId:review.evidenceIds[0]})).digest);
  await f.author(repair);const second=await requestReview(f);
  assert.notEqual(second.input.review.selectionDigest,first.input.review.selectionDigest);
  await f.rawReview(second,assessmentProposal(second));
  const accepted=await evidence(f,f.taskId);assert.equal(accepted.report.verdict,'accept');
  assert.notEqual(accepted.assessment.inputDigest,rejected.assessment.inputDigest);assert.equal(accepted.assessment.planDigest,rejected.assessment.planDigest);
  const record=await f.read(tx=>f.app.get(tx,f.taskId));assert.equal(record.reworkCount,1);assert.equal(record.leader.repairRounds,1);
  assert.deepEqual(record.plan,f.approvedPlan);assert.equal((await f.call({operation:'artifact.get',artifactId:review.evidenceIds[0]})).status,'ready');
  await f.reopen();assert.deepEqual(await evidence(f,f.taskId),accepted);
});

test('direct Application reopen rejects an immutable assessment identity toggle without rewriting the task',async t=>{
  for(const enabled of [false,true])await t.test(String(enabled),async t=>{
    const original=await assessmentFixture(t,{reviewAssessments:enabled}),id=original.taskId;
    const before=original.read(tx=>tx.head(id));
    const opposite=createReviewPort({...configuration(original.reviewPort,'review'),prepare:()=>({prompt:'original'}),parseReport:parseManagedOutput});
    if(!enabled)registerReviewAssessments(opposite,{profile:'task-review-assessment/v1'});
    const sameOwner=new TaskApplication({store:original.app.store,owner:original.app.owner,
      execution:{maxWorkers:3,providerIds:['fixture'],defaultProvider:'fixture'},depot:original.app.artifacts.depot,
      leader:original.leaderPort,review:opposite,verification:original.app.verification.port});
    assert.throws(()=>sameOwner.execution.finish(original.reviewTicket,{cleanup:{cleaned:false,started:null}}),error=>error.code==='unsupported_task');
    original.closeStore();
    const opened=await fixture(t,{existingParent:original.parent,reviewAssessments:!enabled,maxCalls:16,verificationCheck(){}});
    await assert.rejects(view(opened,id),error=>error.code==='unsupported_task');
    // The old-generation ticket hits the original recovery fence even earlier.
    assert.throws(()=>opened.app.execution.finish(original.reviewTicket,{cleanup:{cleaned:false,started:null}}),error=>error.code==='recovery_required');
    assert.deepEqual(opened.read(tx=>tx.head(id)),before);
  });
});

test('bound v2 Review continues through the original verification, delivery and completion and survives cold reopen',async t=>{
  const f=await assessmentFixture(t);await f.rawReview(f.reviewTicket,assessmentProposal(f.reviewTicket));
  const review=(await view(f,f.taskId)).review,stored=await evidence(f,f.taskId);
  let ticket=await f.take('leader');assert.ok(ticket.input.leader.materials.some(material=>material.content.includes('task-independent-review/v2')));
  assert.equal((await f.decision(ticket,[{type:'work',kind:'verify',nodeIds:['verify'],selectionDigest:review.selectionDigest}])).status,'completed');
  assert.equal((await f.verify(await f.take('execute','verify'))).status,'completed');
  const audit=await f.call({operation:'task.audit',taskId:f.taskId}),current=await f.get(f.taskId);
  const artifact=await f.read(tx=>current.artifactIds.map(id=>await f.app.artifacts.metadata(tx,id)).find(row=>row.kind==='delivery'));
  assert.equal((await f.decision(await f.take('leader'),[{type:'deliver',artifactId:artifact.id,acceptanceDigest:audit.acceptance.digest,reviewDigest:review.digest}])).status,'completed');
  assert.equal((await f.decision(await f.take('leader'),[{type:'conclude',outcome:'succeeded',summary:'原独立检查完成',basisDigests:[audit.acceptance.digest,review.digest]}])).status,'completed');
  assert.equal((await f.get(f.taskId)).status,'completed');const before=await f.read(tx=>tx.head(f.taskId));
  await f.reopen();assert.equal((await f.get(f.taskId)).status,'completed');assert.deepEqual(await f.read(tx=>tx.head(f.taskId)),before);assert.deepEqual(await evidence(f,f.taskId),stored);
});

test('real SIGKILL before finish, after staging, inside transaction and after commit never remints evidence or repeats Review',async t=>{
  const {fork}=await import('node:child_process'),{once}=await import('node:events'),fs=await import('node:fs'),path=await import('node:path');
  for(const phase of ['before-finish','after-staging','inside-transaction','after-commit'])await t.test(phase,async t=>{
    const child=fork(new URL('./review-assessment-crash.fixture.ts',import.meta.url),[phase],{stdio:['ignore','ignore','ignore','ipc']});
    let state;child.on('message',value=>state=value);const [code,signal]=await once(child,'exit');
    assert.equal(code,null);assert.equal(signal,'SIGKILL');assert.ok(state?.parent);
    const f=await assessmentFixture(t,{existingParent:state.parent}),before=await view(f,state.taskId),committed=phase==='after-commit';
    assert.equal(!!before.review,committed);
    if(phase==='after-staging'){
      const staged=JSON.parse(fs.readFileSync(path.join(state.parent,'staging-evidence.json')));assert.equal(staged.length,1);
      const bytes=await f.app.artifacts.bytes(staged[0].ref);assert.equal(JSON.parse(bytes.toString()).profile,'task-independent-review/v2');
      assert.equal(await f.read(tx=>tx.projections('artifact').some(row=>JSON.parse(row.bytes).artifact?.digest===staged[0].ref.digest)),false);
    }
    if(committed){
      const stored=await evidence(f,state.taskId);assert.equal(stored.report.verdict,'accept');assert.equal(stored.assessment.reportDigest,hash(stored.report));
      await f.app.leader.recover(state.taskId);
      const leader=await f.take('leader');assert.equal(leader.executionType,'leader');
      assert.equal(await f.read(tx=>tx.commands().filter(row=>row.status==='pending'&&JSON.parse(row.payload).action==='review').length),0);
    }else{
      await f.app.execution.reconcile(state.taskId);await f.app.leader.recover(state.taskId);
      assert.equal((await view(f,state.taskId)).review,null);
      const after=await f.read(tx=>({head:tx.head(state.taskId),attempts:f.app.get(tx,state.taskId).attempts}));
      await f.app.execution.reconcile(state.taskId);await f.app.leader.recover(state.taskId);
      assert.deepEqual(await f.read(tx=>({head:tx.head(state.taskId),attempts:f.app.get(tx,state.taskId).attempts})),after);
      assert.equal(await f.read(tx=>tx.commands().filter(row=>row.status==='pending'&&JSON.parse(row.payload).action==='review').length),0);
    }
  });
});
