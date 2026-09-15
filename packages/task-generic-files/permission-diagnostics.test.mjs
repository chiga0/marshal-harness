import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {withPermissionDiagnostics} from './review-wire.mjs';
import {filePermission} from './permission.mjs';
const options=[{kind:'allow_once',optionId:'allow'}];
const request=(rawInput,kind='edit')=>({toolCall:{rawInput,kind},options});
test('actual original file policy decides once; diagnostics classify known shape only and keep handles/facts',async t=>{
  const cwd=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'generic-permission-diag-')));t.after(()=>fs.rmSync(cwd,{recursive:true,force:true}));
  const ticket={role:'author',input:{fileLayout:{inputs:[],allowedPaths:['result.md']}}};
  let args,calls=0;
  const handle={started:Promise.resolve({}),completion:Promise.resolve({}),stop(){}};
  const original={id:'controlled',usageExtension:'qwen-transcript/v1',facts:{owned:true},start(value){args=value;return handle;}};
  const provider=withPermissionDiagnostics(original);
  const exactArgs={cwd,prompt:'private prompt'};assert.equal(provider.start(exactArgs),handle);assert.equal(args,exactArgs);
  assert.equal(provider.id,original.id);assert.equal(provider.facts,original.facts);assert.equal(provider.usageExtension,original.usageExtension);
  provider.start({...exactArgs,onPermission:async req=>{calls++;return filePermission(ticket,cwd,req);}});
  for(const [req,expected] of [[request({path:'result.md',content:'private payload'}),null],
    [request({path:'../private-target',content:'private payload'}),'permission_denied'],
    [request({file_path:'result.md',path:'private-target',content:'private payload'}),'permission_shape_denied'],
    [request({path:'result.md',content:'private payload',unexpected:1}),'permission_shape_denied'],
    [request({path:'result.md',content:'x'.repeat(8193)}),'permission_shape_denied'],
    [request({path:5,content:'private payload'}),'permission_path_denied'],
    [request({path:'result.md'},'execute'),'permission_kind_denied']]) {
    const before=structuredClone(req),previous=calls,result=await args.onPermission(req,{});
    assert.equal(calls,previous+1);assert.deepEqual(req,before);
    const originalResult=filePermission(ticket,cwd,req);
    assert.deepEqual(result.outcome,originalResult.outcome);assert.equal(result.diagnosticCode,expected??undefined);
    assert.doesNotMatch(JSON.stringify(result),/private|payload|target/);
  }
});
test('allow/unknown outcomes are unchanged, explicit native reject is diagnostic, thrown policy errors stay thrown',async()=>{
  let args;const provider=withPermissionDiagnostics({id:'controlled',start(value){args=value;return {};}});
  for(const response of [{outcome:{outcome:'selected',optionId:'allow'}},{outcome:{outcome:'selected',optionId:'missing'}},{outcome:{outcome:'unknown'}},undefined]) {
    provider.start({onPermission:async()=>response});assert.equal(await args.onPermission(request({}),{}),response);
  }
  const response={outcome:{outcome:'selected',optionId:'reject'}};
  provider.start({onPermission:async()=>response});const result=await args.onPermission({...request({path:'result.md',content:'ok'}),options:[{kind:'reject_once',optionId:'reject'}]},{});
  assert.equal(result.outcome,response.outcome);assert.equal(result.diagnosticCode,'permission_denied');
  const originalError=new Error('original error');provider.start({onPermission:async()=>{throw originalError;}});
  await assert.rejects(args.onPermission(request({}),{}),error=>error===originalError);
});
