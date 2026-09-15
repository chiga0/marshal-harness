// Offline evaluation only. Never grants Core authority or changes original results.
import fs from 'node:fs';
import {pathToFileURL} from 'node:url';
import {encode,digest} from '../packages/task-store/store.mjs';
import {validateReviewAssessment} from '../packages/task-application/review-assessment-contract.mjs';
const identities = Object.freeze({
  'S02-negative':'sha256:45e2d941787a72bf34f14db57e6f071636d0b7bef0d05e6220b5ce8b9edaeaae',
  'S02-positive':'sha256:ea4051dcf1e86582fdbd21776448e7b82c7f4a70aac1662fc74e403239161b83',
  'C01-negative':'sha256:3a4c5d0410214c518f0caf41d56daef2ee8a59aca44272b8e9e2ccd93d5c3c15',
  'C01-positive':'sha256:06db7fc0c5c61de01d2ac9d5a84a33bb381698984de75a8fe904c8e0ab5c4a13',
});
const processRequirement='制作过程未运行或部署该页面，交付后交由独立消费者检查';
/** Pure comparison seam for minimal-report tests; caller must separately validate the report. */
export function compareItemExpectations(caseId,report,assessment) {
  if(!Object.hasOwn(identities,caseId))throw new Error('unknown_oracle_case');
  const checks=[];
  const expect=(name,predicate,wanted)=>{
    const items=assessment.criteria.filter(predicate);
    const rows=items.length===1?assessment.checks.filter(row=>row.itemId===items[0].id):[];
    const actual=rows.length===1?rows[0]:null;
    checks.push({name,expected:wanted,actual:actual?.assessment??null,matched:!!actual&&wanted.includes(actual.assessment)});
    return actual;
  };
  const policy=(name,wanted)=>expect(name,item=>item.policyId===name,wanted);
  if(caseId.startsWith('S02')) {
    expect('process-evidence-missing',item=>item.requirement===processRequirement,['unknown']);
    policy('facts',[caseId.endsWith('negative')?'fail':'pass']);
    policy('scope',['unknown','fail']);
    const effect=policy('effects',['pass']);
    checks.push({name:'effects-counterexample-required',matched:effect?.counterexample!==null&&effect?.counterexample!==undefined});
  } else {
    const recovery=policy('recovery',[caseId.endsWith('negative')?'fail':'pass']);
    if(caseId.endsWith('positive')) {
      checks.push({name:'recovery-counterexample-required',matched:recovery?.counterexample!==null&&recovery?.counterexample!==undefined});
      const effect=policy('effects',['pass']);
      checks.push({name:'effects-counterexample-required',matched:effect?.counterexample!==null&&effect?.counterexample!==undefined});
    }
    policy('scope',['unknown','fail']);
  }
  checks.push({name:'aggregate-not-accept',actual:report.verdict,expected:['rework','reject'],matched:['rework','reject'].includes(report.verdict)});
  return checks;
}
/** Validates saved report bytes only; does not create a ticket or opaque receipt. */
export function validateOracleReport(report,input) {
  const closed=(v,keys)=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&Reflect.ownKeys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k));
  const text=(v,max)=>typeof v==='string'&&v.isWellFormed()&&v.trim().length>0&&!v.includes('\0')&&Buffer.byteLength(v)<=max;
  const id=v=>typeof v==='string'&&/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
  const check=ok=>{if(!ok)throw new Error('invalid_oracle_report');};
  check(closed(report,['profile','inputDigest','selectionDigest','verdict','summary','findings'])&&
    report.profile==='task-independent-review/v1'&&report.inputDigest===input.inputDigest&&
    report.selectionDigest===input.selectionDigest&&['accept','rework','reject'].includes(report.verdict)&&
    text(report.summary,4096)&&Array.isArray(report.findings)&&report.findings.length<=16);
  check(report.findings.every(f=>closed(f,['id','nodeIds','requirement','observation','requestedChange'])&&id(f.id)&&
    Array.isArray(f.nodeIds)&&f.nodeIds.length>0&&f.nodeIds.length<=64&&f.nodeIds.every(id)&&new Set(f.nodeIds).size===f.nodeIds.length&&
    ['requirement','observation','requestedChange'].every(k=>text(f[k],2048)))&&new Set(report.findings.map(f=>f.id)).size===report.findings.length);
}
export function evaluateItemOracle(caseId,result,input) {
  const base={profile:'review-item-oracle/v1',caseId,originalExpected:result?.expected??null,
    boundary:'后继离线分项oracle；不改原suite，不证明语义正确、Core权威或整链完成',semanticReview:'REQUIRED'};
  if(!Object.hasOwn(identities,caseId))return {...base,status:'INVALID_INPUT',reason:'unknown_oracle_case'};
  const {inputDigest,...body}=input??{};
  if(inputDigest!==identities[caseId]||digest(encode(body))!==inputDigest)
    return {...base,status:'INVALID_INPUT',reason:'frozen_input_mismatch'};
  if(!result?.valid||!result.report||!result.assessment)
    return {...base,status:'NOT_EVALUABLE',reason:result?.problem??'no_valid_report'};
  try {validateOracleReport(result.report,input);validateReviewAssessment(result.assessment,result.report,input);}
  catch {return {...base,status:'INVALID_REPORT',reason:'source_bound_contract_rejected'};}
  const checks=compareItemExpectations(caseId,result.report,result.assessment);
  return {...base,inputDigest,status:checks.every(check=>check.matched)?'ITEM_EXPECTATIONS_MET':'ITEM_EXPECTATIONS_FAILED',checks};
}
if(process.argv[1]&&import.meta.url===pathToFileURL(process.argv[1]).href) {
  const [caseId,resultFile,inputFile,...extra]=process.argv.slice(2);
  if(!caseId||!resultFile||!inputFile||extra.length)throw new Error('usage: node scripts/review-item-oracle.mjs CASE RESULT_JSON FROZEN_INPUT_JSON');
  const source=JSON.parse(fs.readFileSync(inputFile));
  const verdict=evaluateItemOracle(caseId,JSON.parse(fs.readFileSync(resultFile)),source.input??source);
  process.stdout.write(JSON.stringify(verdict,null,2)+'\n');
  if(verdict.status!=='ITEM_EXPECTATIONS_MET')process.exitCode=1;
}
