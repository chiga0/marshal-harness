// 截图状态取自真实 DOM；文件名和 API 状态都不能冒充页面已收敛。
export async function captureScreenshot(page, {name, file, expectedStatus=null, timeout=15000}) {
  let synchronization='not-requested';
  if (expectedStatus!==null) {
    try {
      await page.waitForFunction(expected=>document.querySelector('[data-testid="task-detail"] [data-testid="machine-state"]')?.textContent===expected,
        expectedStatus,{timeout});
      synchronization='observed';
    } catch (error) {
      if(error.name!=='TimeoutError')throw error;
      synchronization='timeout';
    }
  }
  const visible=await page.evaluate(()=>({route:location.pathname+location.hash,
    taskStatus:document.querySelector('[data-testid="task-detail"] [data-testid="machine-state"]')?.textContent??null,
    tab:document.querySelector('nav[aria-label="详情子视图"] [aria-current="page"]')?.textContent??null}));
  await page.screenshot({path:file,fullPage:true,animations:'disabled'});
  return {name,...visible,expectedStatus,synchronization,
    statusMatches:expectedStatus===null?null:visible.taskStatus===expectedStatus,capturedAt:new Date().toISOString()};
}
