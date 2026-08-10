import { useMemo } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { rel, stateMeta, type TaskSummary } from "@/lib/api";

export function TaskList({ tasks, onOpen }: { tasks: TaskSummary[]; onOpen: (title: string) => void }) {
  const groups = useMemo(() => {
    const logic = new Map<string, TaskSummary & { runCount: number }>();
    for (const t of tasks) {
      const key = (t.workspace ?? "(当前仓库)") + "|" + (t.title || t.taskId);
      const g = logic.get(key);
      if (g) { g.runCount += t.runCount; g.accepted += t.accepted; g.blocked += t.blocked; g.running += t.running; if (t.latestUpdate > g.latestUpdate) { g.latestUpdate = t.latestUpdate; g.latestState = t.latestState; } }
      else logic.set(key, { ...t });
    }
    const by = new Map<string, (TaskSummary & { runCount: number })[]>();
    for (const g of logic.values()) { const ws = g.workspace ?? "(当前仓库)"; (by.get(ws) ?? by.set(ws, []).get(ws)!).push(g); }
    for (const arr of by.values()) arr.sort((a, b) => b.latestUpdate.localeCompare(a.latestUpdate));
    return by;
  }, [tasks]);
  return (
    <div className="space-y-6">
      {[...groups.entries()].map(([ws, arr]) => (
        <div key={ws}>
          <div className="text-xs uppercase text-muted-foreground mb-2">Workspace · {ws}（{arr.length} 逻辑任务）</div>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {arr.map((g) => { const m = stateMeta(g.latestState); return (
              <Card key={g.taskId} className="cursor-pointer hover:border-blue-500" onClick={() => onOpen(g.title || g.taskId)}>
                <CardHeader className="p-4 pb-2"><CardTitle className="text-sm">{g.title || g.taskId}</CardTitle></CardHeader>
                <CardContent className="p-4 pt-0 space-y-1">
                  <Badge variant={m.tone}>{m.label}</Badge>
                  <div className="text-xs text-muted-foreground">{rel(g.latestUpdate)} · {g.runCount} Run · 成 {g.accepted} / 阻 {g.blocked} / 行 {g.running}</div>
                  <div className="text-[11px] font-mono text-muted-foreground">{g.taskId}{g.runCount > 1 ? ` 等 ${g.runCount} 次` : ""}</div>
                </CardContent>
              </Card>); })}
          </div>
        </div>))}
    </div>);
}
