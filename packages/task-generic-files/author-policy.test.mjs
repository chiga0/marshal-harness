import test from 'node:test';import assert from 'node:assert/strict';import fs from 'node:fs';import os from 'node:os';import path from 'node:path';
import {createGenericFilesReviewWireConfig,AUTHOR_GUIDANCE} from './review-wire.mjs';
import {createReviewPort,createLeaderPort,configuration} from '../task-application/leader-ports.mjs';
import {startTaskService} from '../task-service/composition.mjs';import {Store,LEADER_FORMAT,encode,digest} from '../task-store/store.mjs';
test('仅作者指导文本或注册源码指纹变化，同new profile开根在claim前拒绝',async t=>{
 const parent=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'marshal-author-policy-'))),root=path.join(parent,'data');t.after(()=>fs.rmSync(parent,{recursive:true,force:true}));
 const provider={id:'fixture',custodyProfile:{id:'fixture-inherited-v1',scope:'inherited-process-group',eligible:true},start(){throw Error('no model');}};
 const config=()=>({...createGenericFilesReviewWireConfig({provider}),root});const first=config();
 const description=configuration(first.review,'review').policy.description;assert.ok(description.includes(AUTHOR_GUIDANCE));
 const files=['../task-business/index.mjs','review-wire.mjs','qwen-review-service-config.mjs','qwen-file-tools.mjs','short-wire.mjs','../task-application/leader-ports.mjs','../task-application/review-assessment-contract.mjs','../task-application/review-assessment.mjs','../task-application/leader.mjs','../task-service/composition.mjs'];
 const code=digest(encode(files.map(name=>({name,digest:digest(fs.readFileSync(new URL(name,import.meta.url)))}))));assert.ok(description.endsWith(code),'实际注册实现源码必须纳入新策略指纹');
 let service=await startTaskService({...first,mode:'create'});await service.shutdown();const marker=fs.readFileSync(path.join(root,'profile.json'));
 const generation=()=>{const s=Store.openExisting(path.join(root,'store'),{format:LEADER_FORMAT});try{return s.info().generation;}finally{s.close();}};const before=generation();
 for(const mode of ['text','registrar-source']){
  const changed=config(),review=configuration(changed.review,'review'),leader=configuration(changed.leader,'leader');
  review.policy.description=mode==='text'?review.policy.description.replace(AUTHOR_GUIDANCE,AUTHOR_GUIDANCE+'变更'):review.policy.description.slice(0,-code.length)+'sha256:'+'f'.repeat(64);
  changed.review=createReviewPort({...review,policy:review.policy,parseReport(){throw Error('no execution');},prepare(){throw Error('must reject before prepare');}});
  leader.policy.review.policyDigest=changed.review.policyDigest;changed.leader=createLeaderPort({...leader,policy:leader.policy,parseDecision(){throw Error('no execution');},prepare(){throw Error('must reject before prepare');}});
  await assert.rejects(startTaskService({...changed,mode:'open'}));assert.equal(generation(),before);assert.deepEqual(fs.readFileSync(path.join(root,'profile.json')),marker);
 }
 service=await startTaskService({...config(),mode:'open'});await service.shutdown();assert.equal(generation(),before+1n);
});
