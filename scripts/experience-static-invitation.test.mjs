import test from 'node:test';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {staticInvitationControls} from './experience-static-invitation.mjs';

// 显式真实浏览器组件回归。需 PLAYWRIGHT_MODULE；不加载或执行候选脚本。
test('静态邀请页交互边界使用真实浏览器的控件关联与禁用语义', async t => {
  assert.ok(process.env.PLAYWRIGHT_MODULE, '设置 PLAYWRIGHT_MODULE 为已安装 Playwright 入口');
  const {chromium}=await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href);
  const browser=await chromium.launch({headless:true,...(process.env.BROWSER_EXECUTABLE?{executablePath:process.env.BROWSER_EXECUTABLE}:{})});
  t.after(()=>browser.close());
  const context=await browser.newContext({javaScriptEnabled:false});
  await context.route('**/*',route=>route.abort());
  const page=await context.newPage();
  const cases=[
    ['无action仍是可提交表单，免责声明不豁免','<form><input id="name"><button>提交</button></form><small>仅演示，无实际提交</small>',2],
    ['纯锚点与原生展开保持可用','<a href="#signup">报名说明</a><section id="signup"><details><summary>说明</summary>线下说明</details></section>',0],
    ['显式禁用展示表单','<form><fieldset disabled><input><button>提交（演示）</button></fieldset></form>',0],
    ['disabled fieldset的首个legend内仍可操作','<form><fieldset disabled><legend><input></legend><button>提交</button></fieldset></form>',1],
    ['form属性关联的外置提交按钮','<form id="f"></form><button form="f">提交</button>',1],
    ['readonly不能禁用checkbox或submit','<form><input type="checkbox" readonly><input type="submit" readonly></form>',2],
    ['readonly文本input仍可隐式提交，textarea正文不提交','<form><input readonly value="说明"><textarea readonly>说明</textarea></form>',1],
    ['隐藏与inert内容不冒充可用操作','<form><input type="hidden"><div hidden><button>提交</button></div><div inert><input><button>提交</button></div></form>',0],
  ];
  for(const [name,markup,count] of cases) await t.test(name,async()=>{
    await page.setContent(markup);assert.equal((await staticInvitationControls(page)).length,count);
  });
  await t.test('无脚本readonly输入按Enter实际触发默认GET',async()=>{
    await context.route('https://invitation.invalid/**',route=>route.fulfill({status:200,contentType:'text/html',
      body:'<form><input readonly value="说明"></form>'}));
    await page.goto('https://invitation.invalid/readonly');
    assert.equal((await staticInvitationControls(page)).length,1);
    const request=page.waitForRequest(request=>request.url()==='https://invitation.invalid/readonly?');
    await page.locator('input').press('Enter');assert.equal((await request).method(),'GET');
  });
});
