import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizedObservation, observedUsage} from './observation.ts';
import {validate, contract} from '../task-api/contract.ts';

test('maximum runtime observation history preserves existing list, aggregate and event HTTP budgets', () => {
  const frame = normalizedObservation({activity:'tool', tool:{id:'x'.repeat(256),kind:'execute',status:'in_progress'},
    model:{id:'m'.repeat(256),source:'provider-reported'}, usage:{inputTokens:9007199254740991,outputTokens:null,totalTokens:null,source:'provider-reported',complete:false},
    publicText:'\u0001'.repeat(512)},4096,'2026-09-14T01:00:00.000Z');
  const history = [];
  while (history.length < 64 && Buffer.byteLength(JSON.stringify([...history,frame])) <= 16384) history.push(frame);
  const observed = {...frame,history,historyTruncated:true}; assert.equal(validate(observed,'WorkerObservation'),true);
  const worker = {...structuredClone(contract.components.schemas.Worker.examples[0]),observation:observed};
  assert.ok(Buffer.byteLength(JSON.stringify({items:Array.from({length:100},()=>worker)})) < 8*1024*1024);
  const compact = {...worker,observation:{...observed,history:[]}};
  assert.ok(Buffer.byteLength(JSON.stringify({workers:Array.from({length:256},()=>compact)})) < 2*1024*1024);
  assert.ok(Buffer.byteLength(JSON.stringify({items:Array.from({length:100},()=>({...contract.components.schemas.Event.examples[0],observation:frame}))})) < 8*1024*1024);
});

test('malformed optional readings are discarded rather than gaining authority or preserving arbitrary fields', () => {
  for (const value of [{activity:'fabricated'}, {activity:'output',tool:{id:'x',kind:'read',status:'invented'}},
    {activity:'output',model:{id:'x',source:'guessed'}}, {activity:'output',usage:{inputTokens:1}}]) assert.equal(normalizedObservation(value,1,'2026-09-14T01:00:00Z'),null);
  const frame = normalizedObservation({activity:'thinking',thinking:'PRIVATE_THOUGHT',rawInput:'PRIVATE_INPUT'},1,'2026-09-14T01:00:00Z');
  assert.doesNotMatch(JSON.stringify(frame),/PRIVATE/); assert.equal(frame.publicText,'');
});


test('task usage sums latest provider snapshots once, declares eligible-worker coverage and never counts verification as a missing model', () => {
  const record = (executionType, totalTokens, complete = true) => ({ticket:{executionType},worker:{observation:{usage:totalTokens === null ? null : {totalTokens,complete}}}});
  assert.deepEqual(observedUsage([record('agent',10),record('leader',5),record('review',null),record('verification',null)]),
    {tokens:15,cost:null,currency:null,source:'reported',coverage:2/3});
  assert.equal(observedUsage([record('agent',10,false)]).coverage,0);
  assert.equal(observedUsage([record('agent',null)]).tokens,null);
  assert.equal(observedUsage([record('agent',Number.MAX_SAFE_INTEGER),record('agent',1)]).source,'unavailable');
});


test('Qwen last-response observation is bounded, closed and excluded from aggregate accounting', () => {
  const reading = {inputTokens:10,outputTokens:2,totalTokens:12,source:'qwen-acp-meta',scope:'last-response',complete:false,zeroMayBeDefault:true};
  const frame = normalizedObservation({activity:'waiting',lastResponseUsage:reading},1,'2026-09-14T01:00:00Z');
  assert.deepEqual(frame.lastResponseUsage,reading);
  assert.equal(validate(frame,'ObservationFrame'),true);
  assert.equal(validate({...frame,history:[frame],historyTruncated:false},'WorkerObservation'),true);
  assert.deepEqual(observedUsage([{ticket:{executionType:'agent'},worker:{observation:frame}}]),
    {tokens:null,cost:null,currency:null,source:'unavailable',coverage:0});
  for (const changed of [{totalTokens:-1},{inputTokens:1.5},{outputTokens:Number.MAX_SAFE_INTEGER+1},{complete:true},{zeroMayBeDefault:false},{source:'guessed'},{scope:'task'},{raw:'PRIVATE'}]) {
    const bad = {...reading,...changed};
    assert.equal(Object.hasOwn(normalizedObservation({activity:'waiting',lastResponseUsage:bad},1,'2026-09-14T01:00:00Z'),'lastResponseUsage'),false);
    assert.equal(validate({...frame,lastResponseUsage:bad},'ObservationFrame'),false);
  }
});

test('diagnostic projection is closed and cannot smuggle raw details or an invented authority', () => {
  const diagnostic={stage:'permission',code:'permission_shape_denied',source:'provider-permission'};
  const frame=normalizedObservation({activity:'tool',diagnostic},1,'2026-09-14T01:00:00Z');
  assert.deepEqual(frame.diagnostic,diagnostic);assert.equal(validate(frame,'ObservationFrame'),true);
  for(const bad of [{...diagnostic,code:'PRIVATE'},{...diagnostic,path:'/private'},{...diagnostic,stage:'cleanup'},{...diagnostic,source:'guessed'}]) {
    assert.equal(Object.hasOwn(normalizedObservation({activity:'tool',diagnostic:bad},1,'2026-09-14T01:00:00Z'),'diagnostic'),false);
  }
});

test('collecting fixed diagnostic causes close the normalized and HTTP schema contracts',async()=>{
  const {FILE_COLLECTION_CAUSES,BUSINESS_COLLECTION_CAUSES}=await import('../agent-observation/normalization.ts');
  for(const code of [...FILE_COLLECTION_CAUSES,...BUSINESS_COLLECTION_CAUSES]){
    const frame=normalizedObservation({activity:'terminal',diagnostic:{stage:'collecting',code,source:'controller'}},1,'2026-09-14T01:00:00Z');
    assert.equal(frame.diagnostic.code,code);assert.equal(validate(frame,'ObservationFrame'),true);
  }
});
