import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {startTaskService} from './composition.mjs';
import {createGenericFilesReviewWireConfig} from '../task-generic-files/review-wire.mjs';
import {registerReviewAssessments, reviewAssessmentSourceDigest} from '../task-application/review-assessment.mjs';
import {Store, LEADER_FORMAT} from '../task-store/store.mjs';

for(const enabled of [false,true])test('assessment registration identity is frozen before owner claim; original mode='+enabled,async t=>{
  const parent=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'marshal-assessment-config-'))),root=path.join(parent,'data');
  t.after(()=>fs.rmSync(parent,{recursive:true,force:true}));let starts=0;
  const provider={id:'fixture',custodyProfile:{id:'fixture-inherited-v1',scope:'inherited-process-group',eligible:true},start(){starts++;throw Error('must not execute');}};
  const config=active=>{const value=createGenericFilesReviewWireConfig({provider});if(active)registerReviewAssessments(value.review,{profile:'task-review-assessment/v1'});return {...value,root};};
  let service=await startTaskService({...config(enabled),mode:'create'});await service.shutdown();
  const marker=fs.readFileSync(path.join(root,'profile.json'));
  assert.equal(marker.toString().includes(reviewAssessmentSourceDigest),enabled);
  const generation=()=>{const store=Store.openExisting(path.join(root,'store'),{format:LEADER_FORMAT});try{return store.info().generation;}finally{store.close();}};
  const before=generation();await assert.rejects(startTaskService({...config(!enabled),mode:'open'}));
  assert.equal(generation(),before);assert.deepEqual(fs.readFileSync(path.join(root,'profile.json')),marker);
  service=await startTaskService({...config(enabled),mode:'open'});await service.shutdown();assert.equal(generation(),before+1n);assert.equal(starts,0);
});
