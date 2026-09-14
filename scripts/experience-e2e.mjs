// 显式真实模型验收：所有业务写操作通过浏览器，API仅作独立只读核对。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {spawn,spawnSync,execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {pathToFileURL,fileURLToPath} from 'node:url';
import {verify} from '../packages/task-distribution/index.mjs';
import {cases,validateDelivery,hasInvitationDate,SemanticReviewRequired} from './experience-cases.mjs';

const opts={};for(let i=2;i<process.argv.length;i+=2){assert.ok(['--installed','--manifest','--case','--output'].includes(process.argv[i]));opts[process.argv[i]]=process.argv[i+1];}
const installed=opts['--installed'],caseId=opts['--case'],spec=cases[caseId];
assert.ok(installed&&path.isAbsolute(installed)&&spec&&opts['--manifest']);
const admission=verify({root:installed,manifestDigest:opts['--manifest']});
const root=path.resolve(fileURLToPath(new URL('..',import.meta.url)));
const output=opts['--output']??path.join(root,'.marshal','evidence',`experience-${caseId}-${Date.now()}`);
assert.ok(!fs.existsSync(output),'新证据目录，不覆盖失败');fs.mkdirSync(output,{recursive:true,mode:0o700});
const runtime=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'mx-e2e-')));fs.chmodSync(runtime,0o700);
const privateHome=path.join(runtime,'home');fs.mkdirSync(privateHome,{mode:0o700});
const settings=path.join(privateHome,'.marshal-client');
const sha=bytes=>'sha256:'+createHash('sha256').update(bytes).digest('hex');
const evidence={caseId,candidate:admission.sourceHead,manifestDigest:opts['--manifest'],runtime,
  scriptDigest:sha(fs.readFileSync(fileURLToPath(import.meta.url))),caseDigest:sha(Buffer.from(JSON.stringify(spec))),
  boundary:'真实模型、独立安装包、真实浏览器；非真人可用性',startedAt:new Date().toISOString(),steps:[],result:'RUNNING'};
const persist=()=>fs.writeFileSync(path.join(output,'evidence.json'),JSON.stringify(evidence,null,2)+'\n',{mode:0o600});
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function bounded(promise,ms,label) {
  let timer;try{return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error(label)),ms);})]);}
  finally{clearTimeout(timer);}
}
let browser,page,child,closed,exit,address,token,taskId,spawnError;
let stdout='',stderr='';
const controllerDeadline=Date.now()+18*60*1000;
const api=async route=>{
  const response=await fetch(address+route,{headers:{Authorization:'Bearer '+token,Origin:address},signal:AbortSignal.timeout(12000)});
  assert.ok(response.ok,`只读API ${route} HTTP ${response.status}`);return response.json();
};
async function snapshot(name) {
  const visible=await page.evaluate(()=>({route:location.pathname+location.hash,taskStatus:document.querySelector('[data-testid="task-detail"] [data-testid="machine-state"]')?.textContent??null,tab:document.querySelector('nav[aria-label="详情子视图"] [aria-current="page"]')?.textContent??null}));
  (evidence.screenshots??=[]).push({name,...visible,capturedAt:new Date().toISOString()});
  await page.screenshot({path:path.join(output,name+'.png'),fullPage:true,animations:'disabled'});
}
async function checkNoOverflow(name) {
  const bounds=await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth}));
  assert.ok(bounds.scroll<=bounds.width+1,`${name}整页横向溢出 ${bounds.scroll}/${bounds.width}`);
}
async function connect() {
  await page.goto(address+'/ui/');
  await page.getByLabel('Bearer token',{exact:true}).fill(token);
  await page.getByRole('button',{name:'连接',exact:true}).click();
  await page.getByRole('link',{name:'新建任务',exact:true}).first().waitFor();
}
try {
  persist();
  const initialize=spawnSync(process.execPath,[path.join(installed,'packages/task-local/main.mjs'),'init','--settings-dir',settings],
    {env:{...process.env,HOME:privateHome},encoding:'utf8',timeout:30000});
  assert.equal(initialize.status,0,'全新安装初始化失败');
  const initialized=JSON.parse(initialize.stdout.trim());assert.equal(initialized.launcher.state,'installed');
  evidence.steps.push('全新HOME/init/固定Node启动器');
  const launcher=path.join(privateHome,'.local/bin/marshal');
  const agent=process.env.MARSHAL_TEST_AGENT??execFileSync('/bin/zsh',['-lc','command -v qwen'],{encoding:'utf8'}).trim();
  assert.ok(path.isAbsolute(agent));
  child=spawn(launcher,['serve','--settings-dir',settings,'--data-dir',path.join(runtime,'data'),'--port','0',
    '--ui',path.join(installed,'apps/task-web/dist'),'--agent-executable',agent],{env:{...process.env},stdio:['ignore','pipe','pipe']});
  closed=new Promise(resolve=>{
    child.once('close',(code,signal)=>{exit={code,signal};resolve(exit);});
    child.once('error',error=>{spawnError=error.code??'spawn_failed';exit={code:null,signal:null,error:spawnError};resolve(exit);});
  });
  child.stdout.on('data',bytes=>{stdout+=bytes;if(stdout.length>65536)child.kill('SIGTERM');});
  child.stderr.on('data',bytes=>{stderr+=bytes;if(stderr.length>65536)child.kill('SIGTERM');});
  const startup=Date.now()+45000;
  while(!stdout.includes('\n')) {assert.ok(!exit,'新安装服务启动退出:'+String(spawnError??exit?.code));assert.ok(Date.now()<startup,'启动超时');await sleep(100);}
  const ready=JSON.parse(stdout.split('\n')[0]);address=ready.address;
  token=JSON.parse(fs.readFileSync(ready.connectionFile,'utf8')).token;
  const saved=JSON.parse(fs.readFileSync(path.join(settings,'local.json'),'utf8'));
  assert.equal(saved.genericProfile,2,'必须测试新安装的默认配置');
  evidence.configuration={genericProfile:saved.genericProfile,config:path.basename(saved.config)};
  evidence.steps.push('新设置/空数据目录/安装包默认配置/独立端口');
  const modulePath=process.env.PLAYWRIGHT_MODULE;assert.ok(modulePath&&path.isAbsolute(modulePath));
  const playwright=await import(pathToFileURL(modulePath).href),engine=process.env.BROWSER_ENGINE??'chromium';
  assert.ok(['chromium','webkit'].includes(engine));
  browser=await playwright[engine].launch({headless:true,...(process.env.BROWSER_EXECUTABLE?{executablePath:process.env.BROWSER_EXECUTABLE}:{})});
  evidence.browser={engine,version:browser.version()};
  const context=await browser.newContext({viewport:{width:1440,height:1000},acceptDownloads:true});
  page=await context.newPage();page.setDefaultTimeout(20000);
  const browserErrors=[];page.on('pageerror',error=>browserErrors.push(String(error.message).slice(0,300)));
  await connect();await snapshot('01-workbench-empty');
  await page.getByRole('link',{name:'新建任务',exact:true}).first().click();
  await page.locator('#task-intent').fill(spec.intent);
  if(spec.files) await page.locator('#task-files').setInputFiles(spec.files.map(file=>({name:file.name,mimeType:'text/plain',buffer:Buffer.from(file.content)})));
  await snapshot('02-composer');
  const createdResponse=page.waitForResponse(response=>response.request().method()==='POST'&&new URL(response.url()).pathname==='/v1/tasks');
  await page.getByRole('button',{name:/创建任务|提交任务/,exact:false}).click();
  const response=await createdResponse;assert.equal(response.status(),201);const created=await response.json();taskId=created.id;evidence.taskId=taskId;
  await page.getByRole('link',{name:'查看任务详情',exact:true}).click();
  evidence.steps.push('页面填写需求/上传附件/创建任务');persist();
  let lastStatus='',lastLog=0,approvals=0,answers=0,seenRunning=false,liveCaptureAttempted=false;
  const stages=new Set(),observations=[];
  while(Date.now()<controllerDeadline) {
    const task=await api('/v1/tasks/'+taskId);stages.add(task.status);
    if(task.status!==lastStatus) {lastStatus=task.status;await snapshot('state-'+lastStatus);}
    if(Date.now()-lastLog>30000) {lastLog=Date.now();console.log(JSON.stringify({caseId,taskId,status:task.status,elapsedMs:Date.now()-Date.parse(evidence.startedAt)}));}
    if(['failed','intervention','expired','cancelled'].includes(task.status)) {evidence.task=task;throw new Error('真实任务未交付:'+task.status+':'+JSON.stringify(task.code??task.failure??task.outcome??null));}
    if(task.status==='completed') {evidence.task=task;break;}
    if(task.status==='awaiting-answer') {
      assert.ok(spec.answer,'该完整需求不应出现未约定的必要问答');
      assert.ok(answers<3,'问答未收敛');
      await page.getByLabel('答复内容',{exact:true}).fill(spec.answer);
      await page.getByTestId('leader-answer-open').click();
      await page.getByRole('dialog').getByRole('button',{name:'确认答复',exact:true}).click();
      answers++;await sleep(1800);
    } else if(task.status==='awaiting-approval') {
      assert.ok(approvals<2,'重复批准流程');
      // 计划可能按新版信息架构放在渐进详情中；通过真实summary展开。
      if(!(await page.getByTestId('plan-approve-open').isVisible())) {
        for(const summary of await page.locator('summary').all()) if(/计划/.test(await summary.innerText())) await summary.click();
      }
      await page.getByTestId('plan-approve-open').click();
      await page.getByRole('dialog').getByRole('button',{name:'批准执行',exact:true}).click();approvals++;
      evidence.plan=await api('/v1/tasks/'+taskId+'/plan');
    } else if(task.status==='running'&&!seenRunning) {
      seenRunning=true;await snapshot('03-running-overview');
    }
    const workers=await api('/v1/tasks/'+taskId+'/workers');
    for(const worker of workers.items) if(worker.observation) observations.push({workerId:worker.id,observation:worker.observation});
    const liveAuthor=workers.items.find(worker=>worker.role==='author'&&worker.status==='running'&&worker.observation);
    if(liveAuthor&&!liveCaptureAttempted) {
      liveCaptureAttempted=true;
      await page.locator('nav[aria-label="详情子视图"] a[href$="/team"]').click();
      await page.locator(`[data-worker-id="${liveAuthor.id}"]`).getByRole('link',{name:'明细',exact:true}).click();
      const drawer=page.getByTestId('worker-drawer');await drawer.waitFor();
      await drawer.getByTestId('worker-observation').waitFor();
      evidence.liveMemberCapture={workerId:liveAuthor.id,apiStatus:liveAuthor.status,apiActivity:liveAuthor.observation.activity,
        visibleStatus:await drawer.getByTestId('machine-state').first().textContent(),capturedAt:new Date().toISOString()};
      await snapshot('live-author');await page.keyboard.press('Escape');await drawer.waitFor({state:'hidden'});
      await page.locator('nav[aria-label="详情子视图"] a').first().click();
      await page.waitForFunction(()=>document.querySelector('nav[aria-label="详情子视图"] [aria-current="page"]')?.textContent==='概览');
    }
    await sleep(1000);
  }
  assert.equal(evidence.task?.status,'completed','任务整体超时');
  evidence.stages=[...stages];evidence.approvals=approvals;evidence.answers=answers;
  if(caseId==='M02')assert.ok(answers>0,'没有澄清冲突');
  await page.waitForFunction(()=>document.querySelector('[data-testid="task-detail"] [data-testid="machine-state"]')?.textContent==='completed',undefined,{timeout:15000});
  const audit=await api('/v1/tasks/'+taskId+'/audit'),leader=await api('/v1/tasks/'+taskId+'/leader');
  assert.equal(leader.review?.verdict,'accept');
  assert.equal(audit.acceptance?.status,'passed','独立验收必须通过');
  evidence.review=leader.review;evidence.acceptance=audit.acceptance;evidence.usage=audit.usage;
  evidence.workers=audit.workers;evidence.promptCoverage=audit.prompts.map(p=>({workerId:p.workerId,stage:p.observation.stage,coverage:p.observation.coverage,promptBytes:p.observation.promptBytes}));
  assert.ok(audit.workers.some(worker=>worker.observation),'默认配置必须提供真实活动观察');
  const authorIds=new Set(audit.workers.filter(worker=>worker.role==='author').map(worker=>worker.id));
  assert.ok(authorIds.size>0,'有实际作者执行');
  assert.ok(audit.prompts.some(prompt=>authorIds.has(prompt.workerId)&&prompt.observation.stage==='handed-off'&&prompt.observation.coverage==='policy-redacted'&&prompt.observation.snapshot),'必须能读取真实交接给作者的输入');
  evidence.observationSamples=observations.slice(-100);
  await page.locator('nav[aria-label="详情子视图"] a[href$="/team"]').click();
  for(const role of ['author','reviewer']) {
    const worker=audit.workers.find(worker=>worker.role===role);assert.ok(worker,'缺少独立成员:'+role);
    await page.locator(`[data-worker-id="${worker.id}"]`).getByRole('link',{name:'明细',exact:true}).click();
    const drawer=page.getByTestId('worker-drawer');await drawer.waitFor();
    await drawer.getByTestId('worker-observation').waitFor();
    const prompt=drawer.getByTestId('worker-prompt');await prompt.waitFor();
    const expand=prompt.getByTestId('worker-prompt-expand');if(await expand.count())await expand.click();
    const full=prompt.getByRole('button',{name:'展开完整留存输入（当前为节选）',exact:true});
    if(await full.count()) await full.click();
    const stored=audit.prompts.find(p=>p.workerId===worker.id)?.observation.snapshot;assert.ok(stored);
    const inputResponse=await fetch(address+'/v1/artifacts/'+stored.id+'/content',{headers:{Authorization:'Bearer '+token,Origin:address},signal:AbortSignal.timeout(12000)});
    assert.ok(inputResponse.ok);const inputBytes=Buffer.from(await inputResponse.arrayBuffer());
    assert.equal(sha(inputBytes),stored.digest);assert.equal(inputBytes.length,stored.bytes);
    await page.waitForFunction(expected=>document.querySelector('[data-testid="worker-prompt-text"]')?.textContent===expected,inputBytes.toString('utf8'),{timeout:15000});
    assert.equal(await prompt.locator('pre').textContent(),inputBytes.toString('utf8'),'页面必须呈现原留存输入，不是计划目标替身');
    const history=drawer.locator('summary').filter({hasText:'公开活动历史'});await history.click();
    assert.ok(await drawer.getByRole('list',{name:'最近活动时间线'}).isVisible());
    assert.ok((await drawer.innerText()).includes(worker.providerId));
    await snapshot('member-'+role);await page.keyboard.press('Escape');await drawer.waitFor({state:'hidden'});
  }
  evidence.steps.push('实际点击作者/独立Review成员，输入正文逐字比对与活动历史');
  if(caseId==='C01') {
    const authors=evidence.plan.nodes.filter(node=>node.role==='author');
    assert.ok(authors.length>=3,'需要互补分工和整合作者');
    const sourceIds=new Set(authors.map(node=>node.id));
    assert.ok(authors.some(node=>new Set(evidence.plan.edges.filter(edge=>edge.to===node.id&&sourceIds.has(edge.from)).map(edge=>edge.from)).size>=2),'整合作者必须依赖至少两位作者');
    const actual=audit.workers.filter(worker=>worker.role==='author'&&worker.startedAt&&worker.finishedAt);
    evidence.admittedExecutionOverlap=actual.some((a,i)=>actual.some((b,j)=>i<j&&Date.parse(a.startedAt)<Date.parse(b.finishedAt)&&Date.parse(b.startedAt)<Date.parse(a.finishedAt)));
    evidence.overlapBoundary='started-to-settlement仅说明已准入执行区间交叠，包含cleanup，不单独证明模型计算同时发生';
    assert.ok(evidence.admittedExecutionOverlap,'没有已准入执行区间交叠');
  }
  await page.locator('nav[aria-label="详情子视图"] a').first().click();
  await page.waitForFunction(()=>document.querySelector('nav[aria-label="详情子视图"] [aria-current="page"]')?.textContent==='概览');
  await page.getByTestId('task-journey').waitFor();await snapshot('04-completed-overview');
  for(const colorScheme of ['light','dark']) {
    await page.emulateMedia({colorScheme});
    for(const width of [1440,1024,375]) {await page.setViewportSize({width,height:1000});await checkNoOverflow('任务'+colorScheme+width);await snapshot('overview-'+colorScheme+'-'+width);}
  }
  await page.emulateMedia({colorScheme:'light'});await page.setViewportSize({width:1440,height:1000});
  await page.locator('nav[aria-label="详情子视图"] a[href$="/artifacts"]').click();
  const downloadPromise=page.waitForEvent('download');
  await page.getByTestId('final-delivery').getByTestId('download-button').click();
  const download=await downloadPromise;const savedPath=path.join(output,'deliverables.json');await download.saveAs(savedPath);
  const bytes=fs.readFileSync(savedPath),delivery=JSON.parse(bytes);
  let content;
  try {content=validateDelivery(caseId,delivery);} catch(error) {
    if (!(error instanceof SemanticReviewRequired)) throw error;
    evidence.structuralReview={status:'PENDING',reason:error.message};content=delivery.files[0].content;
  }
  const metadata=await Promise.all(evidence.task.artifactIds.map(id=>api('/v1/artifacts/'+encodeURIComponent(id))));
  const official=metadata.filter(artifact=>artifact.kind==='delivery');assert.equal(official.length,1);
  assert.equal(official[0].taskId,taskId);assert.equal(official[0].status,'ready');
  assert.equal(official[0].digest,sha(bytes));assert.equal(official[0].bytes,bytes.length);
  evidence.deliveryArtifact=official[0];
  for(const file of delivery.files) {assert.equal(sha(Buffer.from(file.content)),file.digest);assert.equal(Buffer.byteLength(file.content),file.bytes);}
  evidence.deliveryDigest=sha(bytes);evidence.steps.push('页面下载/独立业务断言/原始字节摘要');
  await snapshot('05-delivery');
  for(const width of [1440,1024,375]) {await page.setViewportSize({width,height:1000});await checkNoOverflow('成果'+width);await snapshot('delivery-'+width);}
  if(caseId==='S02') {
    const consumer=await browser.newContext({javaScriptEnabled:false,viewport:{width:1440,height:1000}});
    const external=[];await consumer.route('**/*',route=>{external.push(route.request().url());return route.abort();});
    const html=await consumer.newPage(),htmlErrors=[];html.on('pageerror',error=>htmlErrors.push(String(error.message).slice(0,300)));
    await html.setContent(content);
    const visibleText=await html.locator('body').innerText();
    assert.ok(hasInvitationDate(visibleText),'HTML可见正文缺少活动日期事实');
    for(const fact of ['蓝杉读书会','14:00','城市图书馆二层','介绍','日程','报名']) assert.ok(visibleText.includes(fact),'HTML可见正文缺失:'+fact);
    await html.locator('a[href="#signup"]').first().click();assert.ok(await html.locator('#signup').isVisible());
    for(const width of [1440,375]) {await html.setViewportSize({width,height:1000});const overflow=await html.evaluate(()=>document.documentElement.scrollWidth>innerWidth+1);assert.equal(overflow,false);await html.screenshot({path:path.join(output,'html-'+width+'.png'),fullPage:true});}
    assert.deepEqual(external,[],'页面不能依赖外网');assert.deepEqual(htmlErrors,[]);
    assert.equal(await html.locator('script').count(),0,'此冻结邀请页无需脚本');
    const activeAttributes=await html.locator('*').evaluateAll(elements=>elements.flatMap(element=>[...element.attributes]
      .filter(attr=>/^on/i.test(attr.name)||/^(?:href|src|action|formaction|xlink:href)$/i.test(attr.name)&&/^\s*javascript:/i.test(attr.value))
      .map(attr=>({tag:element.tagName,name:attr.name}))));
    assert.deepEqual(activeAttributes,[],'邀请页不得携带内联脚本入口');
    await consumer.close();evidence.steps.push('独立禁脚本/无网络HTML消费/可见正文/按钮/宽窄布局；不宣称JS运行验收');
  }
  assert.deepEqual(browserErrors,[],'浏览器未捕获异常');
  evidence.result=evidence.structuralReview?'PENDING':'PASS';evidence.semanticReview='PENDING：脚本只证明列出的客观断言，完整内容与视觉由独立审查另记';
} catch(error) {
  evidence.result='FAIL';evidence.failure={name:error.name,message:String(error.message).split('\n')[0].slice(0,1000)};process.exitCode=1;
  if(page&&token) {try{await snapshot('failure');}catch{}}
} finally {
  try{if(browser)await bounded(browser.close(),10000,'浏览器清理超时');}catch{evidence.browserCleanup='failed';evidence.result='FAIL';process.exitCode=1;}
  if(child&&!exit) {
    child.kill('SIGTERM');const timer=setTimeout(()=>child.kill('SIGKILL'),30000);
    try{await bounded(closed,35000,'服务清理超时');}catch{evidence.serviceCleanup='unknown';evidence.result='FAIL';process.exitCode=1;}finally{clearTimeout(timer);}
  }
  if(child) {evidence.serviceExit=exit;if(exit?.code!==0){evidence.result='FAIL';process.exitCode=1;}}
  evidence.finishedAt=new Date().toISOString();evidence.elapsedMs=Date.now()-Date.parse(evidence.startedAt);
  // 不落盘stdout、连接token、原生凭据；合成任务投影与报告保留供独立核验。
  persist();console.log(JSON.stringify({caseId,result:evidence.result,output,failure:evidence.failure,elapsedMs:evidence.elapsedMs}));
}
