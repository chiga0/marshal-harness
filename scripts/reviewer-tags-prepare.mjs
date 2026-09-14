// 只读原任务公开输入，构造派生组件材料；不调用模型、不签发Core证据。
import fs from 'node:fs';import path from 'node:path';import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';import {digest} from './reviewer-component-cases.mjs';import {makeTagPair} from './reviewer-tags-cases.mjs';
const [source,out]=process.argv.slice(2);assert.ok(path.isAbsolute(source)&&path.isAbsolute(out)&&!fs.existsSync(out));
const e=JSON.parse(fs.readFileSync(path.join(source,'evidence.json')));assert.equal(e.candidate,'5bb31cbd84cc39f4ed2edd3f8930a92f8bbb152c');
const db=new DatabaseSync(path.join(e.runtime,'data/store/authority.sqlite'),{readOnly:true});let input,ref;
try{const w=JSON.parse(Buffer.from(db.prepare('select bytes from projections where kind=? and id=?').get('attempt',e.review.workerId).bytes));ref=w.inputObservation.snapshot;const b=fs.readFileSync(path.join(e.runtime,'data/artifacts',ref.digest.slice(7)));assert.equal(digest(b),ref.digest);assert.equal(b.length,ref.bytes);input=JSON.parse(b.toString().split('\n完整冻结输入：').at(-1));}finally{db.close();}
const suite={profile:'reviewer-component-suite/v1',boundary:'原始失败与候选派生正例；仅Reviewer组件，非Core/Task权威；标签不传模型',provenance:{sourceEvidence:source,sourcePromptDigest:ref.digest},cases:makeTagPair(input)};
fs.mkdirSync(out,{recursive:true,mode:0o700});fs.writeFileSync(path.join(out,'suite.json'),JSON.stringify(suite,null,2)+'\n',{mode:0o600,flag:'wx'});console.log(JSON.stringify({out,cases:suite.cases.map(c=>({id:c.id,materials:c.input.materials.filter(m=>m.nodeId).map(m=>({nodeId:m.nodeId,bytes:m.bytes}))}))}));
