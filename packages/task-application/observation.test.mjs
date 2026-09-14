import test from 'node:test';
import assert from 'node:assert/strict';
import {normalizedObservation, observedUsage} from './observation.mjs';
import {validate, contract} from '../task-api/contract.mjs';

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
