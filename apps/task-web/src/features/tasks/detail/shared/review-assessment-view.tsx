import type {Assessment} from './review-assessment';
const labels={pass:'文本评审通过',fail:'文本评审未通过',unknown:'依据未确认','not-applicable':'不适用'};
const kinds={'task-input':'原需求','approved-plan':'批准计划','confirmed-answer':'已确认回答','input-artifact':'原附件',candidate:'候选成果',context:'上下文'};
const counterLabels={initial:'初始状态',operation:'操作',failure:'故障点',result:'预期后态',recovery:'恢复依据'};
export function ReviewAssessmentView({assessment}:{assessment:Assessment}) {
 return <section aria-label="逐项文本评审" className="space-y-3" data-testid="review-assessment">
  <h3 className="text-sm font-semibold">逐项文本评审 · {assessment.checks.length} 项</h3>
  <p className="text-xs leading-5 text-text-secondary">检查方法：文本评审。通过表示本次文本判断，不表示软件、浏览器或外部效果已实际测试；配置检查另行核对。</p>
  <div className="divide-y divide-border">{assessment.criteria.map(criterion=>{
   const check=assessment.checks.find(c=>c.itemId===criterion.id)!;
   return <details key={criterion.id} open={['fail','unknown'].includes(check.assessment)} className="workspace-disclosure py-3" data-testid="review-assessment-item">
    <summary className="break-words"><span className="block text-xs text-text-secondary">{labels[check.assessment]}</span>{criterion.index+1}. {criterion.requirement}</summary>
    <div className="mt-3 space-y-3 text-sm leading-6">
     <p className="whitespace-pre-wrap break-words">{check.reason}</p>
     {check.evidence.length>0&&<div className="space-y-2" aria-label="评审引用">{check.evidence.map((ref,index)=>{const source=assessment.sources.find(s=>s.id===ref.sourceId)!;return <div key={index} className="border-l-2 border-border pl-3"><p className="break-words text-xs text-text-secondary">{kinds[source.kind]} · {source.label}</p><blockquote className="whitespace-pre-wrap break-words">{ref.quote===''?'原材料为空（0字节）':ref.quote.trim()===''?'引用内容仅含空白字符':ref.quote}</blockquote>{ref.quote.length>0&&ref.quote.trim()===''&&<details className="workspace-disclosure text-xs"><summary>查看空白字符原文表示</summary><code className="whitespace-pre-wrap break-all">{JSON.stringify(ref.quote)}</code></details>}</div>;})}</div>}
     {check.counterexample&&<details className="workspace-disclosure"><summary>查看公开反例记录（文本推演，非实测）</summary><dl className="mt-2 space-y-2">{Object.entries(check.counterexample).map(([key,value])=><div key={key}><dt className="font-medium">{counterLabels[key as keyof typeof counterLabels]}</dt><dd className="whitespace-pre-wrap break-words">{value}</dd></div>)}</dl></details>}
    </div>
   </details>;
  })}</div>
 </section>;
}
