import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {TaskClient} from '../task-client/index.mjs';
import {launchService,waitPhase} from '../task-leader-report/live-consumer.fixture.mjs';
const here = file => fileURLToPath(new URL(file,import.meta.url));
test('same real HTTP configuration delivers two different no-upload tasks and DAGs, then normal reopen preserves results', {timeout:90000},async t=>{
  const root=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'generic-http-'))), state=path.join(root,'data'), handles=[];
  const start=async mode=>{const handle=launchService(process.execPath,[here('../task-service/main.mjs'),'--root',state,'--mode',mode,'--port','0','--config',here('./service.fixture.mjs')],{PATH:path.dirname(process.execPath)},root,[]);handles.push(handle);
    const ready=await handle.ready,c=JSON.parse(fs.readFileSync(ready.connectionFile));return {handle,client:new TaskClient({baseURL:c.url,token:c.token})};};
  t.after(async()=>{for(const h of handles)await h.stop();t.diagnostic('受控现场：'+root);});
  let {handle,client}=await start('create');const tasks=[];
  for(const intent of ['产品方案并行分析','研究与综合依赖报告']){
    const created=await client.createTask({intent},'create-'+tasks.length), deadline=Date.now()+35000;
    const pending=await waitPhase(()=>client.getTask(created.id),'awaiting-approval',deadline);
    const plan=await client.request('task.plan',{path:{taskId:created.id}});
    await client.approveTask(created.id,{expectedRevision:pending.revision,planRevision:plan.revision,planDigest:plan.digest},'approve-'+tasks.length);
    const done=await waitPhase(()=>client.getTask(created.id),'completed',deadline);
    const view=await client.getLeader(created.id);assert.equal(view.review.verdict,'accept');assert.equal(view.publication,null);
    const artifacts=await Promise.all(done.artifactIds.map(artifactId=>client.request('artifact.get',{path:{artifactId}})));
    const downloaded=await client.downloadArtifact(artifacts.find(x=>x.kind==='delivery').id);
    const report=JSON.parse(downloaded.content);
    assert.equal(report.scope,'independently-reviewed-files-not-external-effects');
    assert.equal(report.files.length,intent.includes('依赖')?1:2);
    for(const file of report.files) assert.equal(file.content,intent+':'+file.path.slice('results/'.length,-3));
    tasks.push({id:created.id,revision:done.revision,plan});
  }
  assert.notDeepEqual(tasks[0].plan.nodes.map(x=>x.id),tasks[1].plan.nodes.map(x=>x.id));
  const cancelTask=await client.createTask({intent:'待批准时取消'},'cancel-create');
  const awaiting=await waitPhase(()=>client.getTask(cancelTask.id),'awaiting-approval',Date.now()+15000);
  await client.request('task.cancel',{path:{taskId:cancelTask.id},idempotencyKey:'cancel-once',body:{expectedRevision:awaiting.revision}});
  await waitPhase(()=>client.getTask(cancelTask.id),'cancelled',Date.now()+15000);
  await handle.stop();({handle,client}=await start('open'));
  for(const task of tasks){const actual=await client.getTask(task.id);assert.equal(actual.status,'completed');assert.equal(actual.revision,task.revision);}
});
