import { useEffect, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TaskList } from "@/components/TaskList";
import { RunDetail } from "@/components/RunDetail";
import { fetchRuns, fetchTasks, streamTasks, type TaskSummary } from "@/lib/api";

type Route = { view: "tasks" } | { view: "task"; key: string } | { view: "run"; id: string };
const parse = (): Route => { const h = location.hash.replace(/^#\/?/, ""); if (h.startsWith("task/")) return { view: "task", key: decodeURIComponent(h.slice(5)) }; if (h.startsWith("run/")) return { view: "run", id: decodeURIComponent(h.slice(4)) }; return { view: "tasks" }; };

export default function App() {
  const [route, setRoute] = useState<Route>(parse());
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [runs, setRuns] = useState<import("@/lib/api").RunDetail[]>([]);
  const [q, setQ] = useState("");
  const [light, setLight] = useState(false);
  useEffect(() => { const on = () => setRoute(parse()); window.addEventListener("hashchange", on); return () => window.removeEventListener("hashchange", on); }, []);
  useEffect(() => { fetchTasks().then(setTasks); fetchRuns().then(setRuns); return streamTasks(() => fetchTasks().then(setTasks)); }, []);
  useEffect(() => { document.documentElement.classList.toggle("light", light); }, [light]);
  const nav = (h: string) => { location.hash = h ? "#/" + h : "#/"; };
  const filtered = useMemo(() => tasks.filter((t) => !q || (t.title ?? "").toLowerCase().includes(q) || t.taskId.toLowerCase().includes(q)), [tasks, q]);
  return (
    <div className="min-h-screen">
      <nav className="sticky top-0 z-10 flex items-center gap-4 border-b border-border bg-card px-6 py-3">
        <span className="font-bold">Marshal 控制台</span>
        <Button variant="ghost" size="sm" onClick={() => nav("")}>任务</Button>
        <Input className="max-w-xs" placeholder="检索任务/Run…" value={q} onChange={(e) => setQ(e.target.value.toLowerCase())} />
        <div className="flex-1" />
        <Button variant="outline" size="sm" onClick={() => setLight(!light)}>{light ? "暗色" : "亮色"}</Button>
        <span className="text-xs text-muted-foreground border border-border rounded-full px-2 py-0.5">只读 · 控制在 CLI/Skill</span>
      </nav>
      <main className="p-6">
        {route.view === "tasks" && (<><div className="text-sm text-muted-foreground mb-3">任务列表（{filtered.length} 逻辑任务）· 按 Workspace 分组 · 倒排</div><TaskList tasks={filtered} onOpen={(t) => nav("task/" + encodeURIComponent(t))} /></>)}
        {route.view === "task" && (<TaskView key={route.key} title={route.key} runs={runs} />)}
        {route.view === "run" && (<><div className="text-sm text-muted-foreground mb-3"><a className="text-blue-500 cursor-pointer" onClick={() => nav("")}>任务</a> / {route.id}</div><RunDetail runId={route.id} /></>)}
      </main>
    </div>);
}

function TaskView({ title, runs }: { title: string; runs: import("@/lib/api").RunDetail[] }) {
  const rs = runs.filter((r) => r.title === title || r.taskId === title).sort((a, b) => (b.updatedAt ?? "").localeCompare(a.updatedAt ?? ""));
  return (<div className="space-y-4">
    <div className="text-sm text-muted-foreground"><a className="text-blue-500 cursor-pointer" onClick={() => { location.hash = "#/"; }}>任务</a> / {title} · Run（倒排）</div>
    <div className="space-y-2">{rs.map((r) => <RunRow key={r.runId} id={r.runId} />)}</div>
    {rs[0] && <RunDetail runId={rs[0].runId} />}
  </div>);
}
import { Badge } from "@/components/ui/badge";
function RunRow({ id }: { id: string }) { return (<div className="rounded-md border border-border bg-card px-3 py-2 cursor-pointer hover:border-blue-500 text-sm" onClick={() => { location.hash = "#/run/" + id; }}><Badge variant="info">run</Badge> <b>{id}</b></div>); }
