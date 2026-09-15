import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {startTaskService} from './composition.mjs';
import {Store,LEADER_FORMAT} from '../task-store/store.mjs';
import {createGenericFilesReviewWireConfig} from '../task-generic-files/review-wire.mjs';
import {registerLeaderJsonCorrection} from '../task-application/leader-protocol-correction.mjs';

test('explicit correction identity freezes same root; disable or changed source cannot advance owner; identical cold config reopens',async t=>{
 const parent=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'marshal-correction-config-'))),root=path.join(parent,'data');t.after(()=>fs.rmSync(parent,{recursive:true,force:true}));
 const provider={id:'fixture',custodyProfile:{id:'fixture-inherited-v1',scope:'inherited-process-group',eligible:true},start(){throw Error('configuration only');}};
 const config=enabled=>{const value=createGenericFilesReviewWireConfig({provider});if(enabled)registerLeaderJsonCorrection(value.leader,{profile:'leader-json-correction/v1',maxPerTask:1});return {...value,root};};
 let service=await startTaskService({...config(true),mode:'create'});await service.shutdown();
 const markerPath=path.join(root,'profile.json'),bytes=fs.readFileSync(markerPath),marker=JSON.parse(bytes);
 assert.equal(marker.leader.protocolCorrection.profile,'leader-json-correction/v1');assert.equal(marker.leader.protocolCorrection.maxPerTask,1);assert.match(marker.leader.protocolCorrection.sourceDigest,/^sha256:[a-f0-9]{64}$/);
 const generation=()=>{const store=Store.openExisting(path.join(root,'store'),{format:LEADER_FORMAT});try{return store.info().generation;}finally{store.close();}};
 const before=generation();await assert.rejects(startTaskService({...config(false),mode:'open'}));assert.equal(generation(),before);
 marker.leader.protocolCorrection.sourceDigest='sha256:'+'f'.repeat(64);fs.writeFileSync(markerPath,JSON.stringify(marker)+'\n');
 await assert.rejects(startTaskService({...config(true),mode:'open'}));assert.equal(generation(),before);fs.writeFileSync(markerPath,bytes);
 service=await startTaskService({...config(true),mode:'open'});await service.shutdown();assert.equal(generation(),before+1n);
});
