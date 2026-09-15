import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {captureScreenshot} from './experience-screenshot.mjs';

test('真实浏览器截图等待页面状态并保留不一致证据',async t=>{
  assert.ok(process.env.PLAYWRIGHT_MODULE,'设置已安装 Playwright 入口');
  const {chromium}=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
  const browser=await chromium.launch({headless:true,...(process.env.BROWSER_EXECUTABLE?{executablePath:process.env.BROWSER_EXECUTABLE}:{})});
  t.after(()=>browser.close());
  const context=await browser.newContext();await context.route('**/*',route=>route.abort());
  const page=await context.newPage(),out=fs.mkdtempSync(path.join(os.tmpdir(),'marshal-screenshot-test-'));
  t.after(()=>fs.rmSync(out,{recursive:true,force:true}));
  const markup=status=>`<main data-testid="task-detail"><span data-testid="machine-state">${status}</span></main>`;
  await t.test('API先失败而页面仍运行时等待真实终态',async()=>{
    await page.setContent(markup('running'));
    await page.evaluate(()=>setTimeout(()=>{document.querySelector('[data-testid="machine-state"]').textContent='failed';},250));
    const result=await captureScreenshot(page,{name:'failed',file:path.join(out,'failed.png'),expectedStatus:'failed',timeout:3000});
    assert.equal(result.synchronization,'observed');assert.equal(result.taskStatus,'failed');assert.equal(result.statusMatches,true);
    assert.ok(fs.statSync(path.join(out,'failed.png')).size>0);
  });
  await t.test('页面未收敛不以文件名伪称失败页',async()=>{
    await page.setContent(markup('running'));
    const result=await captureScreenshot(page,{name:'failure',file:path.join(out,'stale.png'),expectedStatus:'failed',timeout:100});
    assert.equal(result.synchronization,'timeout');assert.equal(result.taskStatus,'running');assert.equal(result.expectedStatus,'failed');assert.equal(result.statusMatches,false);
  });
  await t.test('非终态已跳过仍记录真实画面及不匹配',async()=>{
    await page.setContent(markup('awaiting-approval'));
    const result=await captureScreenshot(page,{name:'running',file:path.join(out,'advanced.png'),expectedStatus:'running',timeout:100});
    assert.equal(result.taskStatus,'awaiting-approval');assert.equal(result.statusMatches,false);
  });
  await t.test('工作台不制造Task状态',async()=>{
    await page.setContent('<main>工作台</main>');
    const result=await captureScreenshot(page,{name:'workbench',file:path.join(out,'workbench.png')});
    assert.equal(result.taskStatus,null);assert.equal(result.statusMatches,null);assert.equal(result.synchronization,'not-requested');
  });
});
