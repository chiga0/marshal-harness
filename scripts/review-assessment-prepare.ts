// 只读已授权合成任务的 Audit 输入，不读取 native 对话或隐藏推理。
import fs from 'node:fs';import path from 'node:path';import assert from 'node:assert/strict';import {DatabaseSync} from 'node:sqlite';
import {makeAssessmentPairs,validateComponentInput,validateExample,digest} from './review-assessment-cases.ts';
const [source,out]=process.argv.slice(2);assert.ok(path.isAbsolute(source)&&path.isAbsolute(out)&&!fs.existsSync(out));
const inputs={},provenance={};
for(const id of ['S02','C01']) {
  const dir=path.join(source,id+'-1eccece1-1'),e=JSON.parse(fs.readFileSync(path.join(dir,'evidence.json')));
  assert.equal(e.candidate,'1eccece1a2168dc5b9dafbb640d1fa286b15d375');
  const db=new DatabaseSync(path.join(e.runtime,'data/store/authority.sqlite'),{readOnly:true});
  try {
    const w=JSON.parse(Buffer.from(db.prepare('select bytes from projections where kind=? and id=?').get('attempt',e.review.workerId).bytes));
    const ref=w.inputObservation.snapshot,raw=fs.readFileSync(path.join(e.runtime,'data/artifacts',ref.digest.slice(7)));
    assert.equal(raw.length,ref.bytes);assert.equal(digest(raw),ref.digest);
    const input=JSON.parse(raw.toString().split('\n完整冻结输入：').at(-1));validateComponentInput(input);
    const delivered=JSON.parse(fs.readFileSync(path.join(dir,'deliverables.json'))).files[0];
    const material=input.materials.find(m=>m.nodeId===(id==='S02'?'author':'integrator'));
    assert.equal(material.content,delivered.content);assert.equal(material.digest,delivered.digest);
    inputs[id]=input;provenance[id]={candidate:e.candidate,taskId:e.taskId,planDigest:e.plan.digest,promptDigest:ref.digest,deliveryDigest:delivered.digest};
  }finally{db.close();}
}
const cases=makeAssessmentPairs(inputs);for(const c of cases)validateExample(c);
const suite={profile:'review-assessment-fixtures/v1',boundary:'组件材料；非原Task新增批准，非Core权威；禁止将标签/预期传给模型或置于运行cwd',criteriaAdmission:'PENDING_TRUSTED_HELPER',provenance,cases};
fs.mkdirSync(out,{recursive:true,mode:0o700});const raw=JSON.stringify(suite,null,2)+'\n';fs.writeFileSync(path.join(out,'suite.json'),raw,{flag:'wx',mode:0o600});
console.log(JSON.stringify({out,suiteDigest:digest(raw),criteriaAdmission:suite.criteriaAdmission,cases:cases.map(c=>({id:c.id,expected:c.expected,bytes:c.input.materials.filter(m=>m.nodeId).map(m=>({nodeId:m.nodeId,bytes:m.bytes}))}))}));
