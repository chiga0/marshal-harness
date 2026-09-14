import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {TaskClient} from '../task-client/index.mjs';
import {launchService,waitPhase} from '../task-leader-report/live-consumer.fixture.mjs';
const here = file => fileURLToPath(new URL(file,import.meta.url));
// Node 22 may emit these exact public SQLite notices before rejecting configuration.
// Keep all unknown warnings and other output visible to the closed stderr assertion.
const withoutSQLiteWarnings = text => text
  .replace(/^\(node:\d+\) ExperimentalWarning: SQLite is an experimental feature and might change at any time\r?\n\(Use `node --trace-warnings \.\.\.` to show where the warning was created\)\r?\n/gm, '')
  .replace(/^\(node:\d+\) \[MARSHAL_SQLITE_DEFENSIVE_UNAVAILABLE\] Warning: SQLite defensive mode unavailable; trusted-single-user fixed-SQL Store only\r?\n/gm, '');
test('SQLite notice normalization preserves unknown warnings and private output',()=>{
  const error='{"code":"service_start_unavailable"}\n';
  const experimental='(node:123) ExperimentalWarning: SQLite is an experimental feature and might change at any time\n(Use `node --trace-warnings ...` to show where the warning was created)\n';
  const defensive='(node:123) [MARSHAL_SQLITE_DEFENSIVE_UNAVAILABLE] Warning: SQLite defensive mode unavailable; trusted-single-user fixed-SQL Store only\n';
  for(const notice of [experimental,defensive,experimental+defensive]){
    assert.equal(withoutSQLiteWarnings(notice+error),error);assert.equal(withoutSQLiteWarnings(error+notice),error);
    assert.equal(withoutSQLiteWarnings(notice.replaceAll('\n','\r\n')+error),error);
    assert.equal(withoutSQLiteWarnings('PRIVATE\n'+notice+error),'PRIVATE\n'+error);
    assert.equal(withoutSQLiteWarnings(notice+error+'PRIVATE\n'),error+'PRIVATE\n');
  }
  for(const notice of [defensive.replace('fixed-SQL','unknown'),defensive.replace('MARSHAL_SQLITE_DEFENSIVE_UNAVAILABLE','OTHER'),experimental.replace('SQLite','Other'), 'PRIVATE '+defensive])
    assert.equal(withoutSQLiteWarnings(notice+error),notice+error);
});
for (const configuration of ['service.fixture.mjs','short-service.fixture.mjs','review-service.fixture.mjs']) test('same real HTTP configuration delivers two different no-upload tasks and DAGs, then normal reopen preserves results: '+configuration, {timeout:90000},async t=>{
  const root=fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(),'generic-http-'))), state=path.join(root,'data'), handles=[];
  const start=async mode=>{const handle=launchService(process.execPath,[here('../task-service/main.mjs'),'--root',state,'--mode',mode,'--port','0','--config',here('./'+configuration)],{PATH:path.dirname(process.execPath)},root,[]);handles.push(handle);
    const ready=await handle.ready,c=JSON.parse(fs.readFileSync(ready.connectionFile));return {handle,client:new TaskClient({baseURL:c.url,token:c.token})};};
  t.after(async()=>{for(const h of handles)await h.stop();t.diagnostic('受控现场：'+root);});
  let {handle,client}=await start('create');const tasks=[];
  for(const intent of ['产品方案并行分析','研究与综合依赖报告']){
    const created=await client.createTask({intent},'create-'+tasks.length), deadline=Date.now()+35000;
    const pending=await waitPhase(()=>client.getTask(created.id),'awaiting-approval',deadline);
    const plan=await client.request('task.plan',{path:{taskId:created.id}});
    await client.approveTask(created.id,{expectedRevision:pending.revision,planRevision:plan.revision,planDigest:plan.digest},'approve-'+tasks.length);
    const done=await waitPhase(()=>client.getTask(created.id),'completed',deadline);
    if(configuration==='review-service.fixture.mjs') {
      const audit=await client.request('task.audit',{path:{taskId:created.id}});
      const authors=audit.workers.filter(w=>w.role==='author');
      const managed=audit.workers.filter(w=>['planner','reviewer'].includes(w.role));
      assert.ok(authors.length>0);assert.ok(managed.some(w=>w.role==='planner'));assert.ok(managed.some(w=>w.role==='reviewer'));
      for(const worker of authors) assert.equal(worker.providerId,'controlled');
      for(const worker of managed) assert.equal(worker.providerId,'controlled-managed');
    }
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
  await handle.stop();
  if(configuration!=='service.fixture.mjs') {
    const wrong=spawnSync(process.execPath,[here('../task-service/main.mjs'),'--root',state,'--mode','open','--port','0','--config',here('./service.fixture.mjs')],{env:{PATH:path.dirname(process.execPath)},cwd:root,encoding:'utf8',timeout:10000,maxBuffer:8192});
    assert.equal(wrong.error,undefined);assert.equal(wrong.status,1);assert.equal(wrong.signal,null);assert.equal(wrong.stdout,'');
    assert.equal(withoutSQLiteWarnings(wrong.stderr),'{"code":"service_start_unavailable"}\n');
  }
  ({handle,client}=await start('open'));
  for(const task of tasks){const actual=await client.getTask(task.id);assert.equal(actual.status,'completed');assert.equal(actual.revision,task.revision);}
});
