// 仅保存本次合成任务的公开 HTTP 投影，不读取原生日志或错误正文。
// api 由 runner 注入，沿用其只读 GET 与 12 秒请求超时。
export async function captureFailureProjections({taskId,api}) {
  if (!taskId || typeof api !== 'function') return {status:'skipped',code:'task_or_connection_unavailable'};
  const names=['audit','leader'];
  const results=await Promise.allSettled(names.map(name=>Promise.resolve().then(()=>api('/v1/tasks/'+encodeURIComponent(taskId)+'/'+name))));
  const projections={};
  for (const [index,result] of results.entries()) projections[names[index]]=result.status==='fulfilled'
    ? {status:'captured',projection:result.value}
    : {status:'failed',code:'public_projection_read_failed'};
  const captured=results.filter(result=>result.status==='fulfilled').length;
  return {status:captured===names.length?'complete':captured?'partial':'failed',...projections};
}
