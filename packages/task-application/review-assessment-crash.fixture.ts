import fs from 'node:fs';
import path from 'node:path';
import {assessmentFixture, assessmentProposal, produceReview} from './review-assessment.fixture.ts';
const phase=process.argv[2],f=await assessmentFixture({after(){}}),ticket=f.reviewTicket;
const native=await produceReview(f,ticket,assessmentProposal(ticket));
await new Promise(resolve=>process.send({parent:f.parent,taskId:f.taskId,ticket},resolve));
const crash=()=>process.kill(process.pid,'SIGKILL');
if(phase==='before-finish')crash();
if(phase==='after-staging'){
  const original=f.app.artifacts.stageOutputs.bind(f.app.artifacts);
  f.app.artifacts.stageOutputs=(...args)=>{const staged=original(...args);fs.writeFileSync(path.join(f.parent,'staging-evidence.json'),JSON.stringify(staged));crash();};
}
if(phase==='inside-transaction'){
  const transaction=f.app.transaction.bind(f.app);
  f.app.transaction=(write,callback)=>transaction(write,tx=>{const result=callback(tx);if(write)crash();return result;});
}
await f.app.execution.finish(ticket,native.result);
crash();
