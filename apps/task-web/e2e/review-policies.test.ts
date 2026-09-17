import {test,expect} from 'vitest';
import {execFileSync} from 'node:child_process';
import {resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import {reviewPolicies} from '../src/features/tasks/detail/shared/review-policies.ts';

test('批准页固定政策与后端合同原文及不适用权限逐项一致',()=>{
  // 用原生 Node 读取后端文件 URL，避免 Vite 将其转换为浏览器 URL。
  const moduleURL=pathToFileURL(resolve(process.cwd(),'../../packages/task-application/review-assessment-contract.ts')).href;
  const raw=execFileSync(process.execPath,['--input-type=module','-e',
    'const {REVIEW_POLICIES}=await import(process.argv[1]);process.stdout.write(JSON.stringify(REVIEW_POLICIES));',moduleURL],{encoding:'utf8',timeout:10000,maxBuffer:16384});
  expect(reviewPolicies.map(p=>({id:p.id,text:p.text,allowNotApplicable:p.allowNotApplicable}))).toEqual(JSON.parse(raw));
});
