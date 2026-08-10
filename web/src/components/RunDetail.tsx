import { useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dag } from "@/components/Dag";
import { EVENT_LABEL, fetchRun, stateMeta, type RunDetail as RD } from "@/lib/api";

export function RunDetail({ runId }: { runId: string }) {
  const [d, setD] = useState<RD | null>(null);
  const attsRef = useRef<HTMLDivElement>(null);
  useEffect(() => { fetchRun(runId).then(setD); }, [runId]);
  if (!d) return <div className="text-sm text-muted-foreground">加载中…</div>;
  const m = stateMeta(d.state);
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <div className="lg:col-span-2 space-y-4">
        <div><div className="text-xs uppercase text-muted-foreground mb-1">流程 DAG（React Flow）</div>
          <Dag run={d} onAtt={(i) => attsRef.current?.children[i]?.scrollIntoView({ block: "center" })} /></div>
        <div><div className="text-xs uppercase text-muted-foreground mb-1">事件时间线</div>
          <div className="space-y-1">{(d.events ?? []).map((e) => (
            <div key={e.sequence} className="border-l-2 border-border bg-muted/40 rounded px-2 py-1 text-sm">
              {EVENT_LABEL[e.type] ?? e.type}<span className="block text-xs text-muted-foreground">{e.from} → {e.to} · {e.at}</span></div>))}</div></div>
      </div>
      <Card><CardHeader><CardTitle className="text-sm">Run 详情</CardTitle></CardHeader><CardContent className="space-y-2 text-sm">
        <div>状态 <Badge variant={m.tone}>{m.label}</Badge></div>
        <div className="text-muted-foreground">Workspace {d.workspace}</div>
        <div>验证 {d.verification ?? "未运行"}（{d.gatesPassed}/{d.gatesTotal}{d.gatesFailed?.length ? ` 失败:${d.gatesFailed.join(",")}` : ""}）</div>
        <div>审查/发布/结局 {d.hasReview ? "已" : "未"} / {d.hasPublication ? "已" : "未"} / {d.hasOutcome ? "已" : "无"}</div>
        <div>重试/返工 {d.operationalRetries} / {d.reworkRounds} · 尝试 {d.attemptsUsed}</div>
        <div>耗时 Worker {d.workerDurationSec}s · 全程 {d.totalDurationSec}s</div>
        <div>Token 入 {d.inputTokens} 出 {d.outputTokens}</div>
        <div>Artifact {d.artifacts?.length ? `${d.artifacts.length} 项` : "无"}</div>
        <div className="text-xs uppercase text-muted-foreground pt-2">Worker 尝试</div>
        <div ref={attsRef} className="space-y-1">{(d.attempts ?? []).map((a, i) => <div key={i} className="text-sm">· {a.id}{a.workerStatus ? `（${a.workerStatus}）` : ""}</div>)}</div>
      </CardContent></Card>
    </div>
  );
}
