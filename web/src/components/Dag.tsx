import { useMemo } from "react";
import { ReactFlow, Controls, Background, BackgroundVariant, Position, type Node, type Edge, MarkerType } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { stateMeta, type RunDetail } from "@/lib/api";

const TONE: Record<string, string> = { ok: "#22c55e", err: "#ef4444", warn: "#eab308", info: "#3b82f6", mut: "#6b7280" };
const tc = (t: string) => TONE[t] ?? TONE.mut;

export function Dag({ run, onAtt }: { run: RunDetail; onAtt: (i: number) => void }) {
  const { nodes, edges } = useMemo(() => {
    const nodes: Node[] = []; const edges: Edge[] = [];
    const ev = (run.events ?? []).map((e) => e.type);
    const published = !!run.hasPublication;
    const accepted = run.state === "ACCEPTED" || run.state === "PUBLISHED" || run.state === "CI_PENDING";
    const rejected = run.state === "REJECTED" || run.state === "BLOCKED" || run.state === "ABORTED";
    const Y = 120; let x = 0;
    const add = (id: string, label: string, color: string, px: number, py = Y, att?: number, dim = false) =>
      nodes.push({ id, position: { x: px, y: py }, data: { label, att }, sourcePosition: Position.Right, targetPosition: Position.Left,
        style: { border: `1px solid ${color}`, background: color + (dim ? "0d" : "22"), color: "inherit", fontSize: 12, borderRadius: 6, padding: "6px 10px", opacity: dim ? 0.45 : 1 } });
    const link = (s: string, t: string, color = "#444c56", opts: { label?: string; animated?: boolean; opacity?: number } = {}) =>
      edges.push({ id: `e${s}-${t}`, source: s, target: t, type: "smoothstep", animated: !!opts.animated, markerEnd: { type: MarkerType.ArrowClosed, color }, style: { stroke: color, opacity: opts.opacity ?? 1 }, label: opts.label as string, labelStyle: { fill: color, fontSize: 10 } });

    add("plan", "Leader/Plan", TONE.info, x); x += 150;
    const n = (run.attempts ?? []).length || 1;
    for (let i = 0; i < n; i++) {
      const a = (run.attempts ?? [])[i];
      add("w" + i, `Worker${i + 1}`, a?.workerStatus === "completed" ? TONE.ok : a?.workerStatus ? TONE.err : TONE.info, x, Y, i);
      link(i === 0 ? "plan" : "r" + (i - 1), "w" + i, i === 0 ? "#444c56" : TONE.warn, i === 0 ? {} : { label: "返工" });
      add("v" + i, "验证", run.verification === "pass" ? TONE.ok : run.verification ? TONE.err : TONE.mut, x + 150);
      link("w" + i, "v" + i);
      add("r" + i, "审查", TONE.info, x + 300);
      link("v" + i, "r" + i);
      x += 460;
    }
    const lastR = "r" + (n - 1);
    // 分支：accept → [发布] → 结局；reject/block → 结局（跳过发布）
    add("pub", "发布", published ? TONE.ok : TONE.mut, x, 30, undefined, !published);
    add("end", "结局", tc(stateMeta(run.state).tone), x + 160);
    if (accepted && published) { link(lastR, "pub", TONE.ok, { label: "接受", animated: true }); link("pub", "end", TONE.ok, { animated: true }); }
    else if (accepted) { link(lastR, "end", TONE.ok, { label: "接受·无需发布", animated: true }); link(lastR, "pub", TONE.mut, { opacity: 0.3 }); }
    if (rejected) { link(lastR, "end", TONE.err, { label: "拒绝/阻塞", animated: true }); link(lastR, "pub", TONE.mut, { opacity: 0.3 }); link("pub", "end", TONE.mut, { opacity: 0.3 }); }
    if (!accepted && !rejected) { link(lastR, "end", TONE.info, { animated: true }); }
    // rework 回路在循环里已用 r(i-1)->w(i) 表达
    void ev;
    return { nodes, edges };
  }, [run]);
  return (
    <div className="h-64 rounded-md border border-border overflow-hidden">
      <ReactFlow nodes={nodes} edges={edges} fitView proOptions={{ hideAttribution: true }} onNodeClick={(_, nd) => { const a = (nd.data as { att?: number })?.att; if (a != null) onAtt(a); }}>
        <Controls position="top-right" showInteractive={false} />
        <Background variant={BackgroundVariant.Dots} gap={16} />
      </ReactFlow>
    </div>
  );
}
