import { useMemo } from "react";
import { ReactFlow, Controls, Background, BackgroundVariant, Position, Handle, type Node, type Edge, type NodeProps, MarkerType } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { stateMeta, type RunDetail } from "@/lib/api";

const TONE: Record<string, string> = { ok: "#22c55e", err: "#ef4444", warn: "#eab308", info: "#3b82f6", mut: "#6b7280" };
const tc = (t: string) => TONE[t] ?? TONE.mut;
const COLW = 240, ROWH = 160, MAXC = 4, NW = 150;

function FlowNode({ data }: NodeProps) {
  const d = data as { label: string; color: string; dim?: boolean };
  return (
    <div style={{ width: NW, textAlign: "center", border: `1px solid ${d.color}`, background: d.color + (d.dim ? "0d" : "22"), fontSize: 12, borderRadius: 6, padding: "6px 0", opacity: d.dim ? 0.45 : 1 }}>
      <Handle id="tl" type="target" position={Position.Left} />
      <Handle id="tt" type="target" position={Position.Top} />
      <Handle id="sr" type="source" position={Position.Right} />
      <Handle id="sb" type="source" position={Position.Bottom} />
      {d.label}
    </div>
  );
}
const nodeTypes = { flow: FlowNode };

export function Dag({ run, onAtt }: { run: RunDetail; onAtt: (i: number) => void }) {
  const { nodes, edges } = useMemo(() => {
    const published = !!run.hasPublication;
    const accepted = ["ACCEPTED", "PUBLISHED", "CI_PENDING"].includes(run.state);
    const rejected = ["REJECTED", "BLOCKED", "ABORTED"].includes(run.state);
    type D = { id: string; label: string; color: string; dim?: boolean; att?: number };
    const seq: D[] = [{ id: "plan", label: "Leader/Plan", color: TONE.info }];
    const n = (run.attempts ?? []).length || 1;
    for (let i = 0; i < n; i++) {
      const a = (run.attempts ?? [])[i];
      seq.push({ id: "w" + i, label: `Worker${i + 1}`, color: a?.workerStatus === "completed" ? TONE.ok : a?.workerStatus ? TONE.err : TONE.info, att: i });
      seq.push({ id: "v" + i, label: "验证", color: run.verification === "pass" ? TONE.ok : run.verification ? TONE.err : TONE.mut });
      seq.push({ id: "r" + i, label: "审查", color: TONE.info });
    }
    const lastR = "r" + (n - 1);
    seq.push({ id: "pub", label: "发布", color: published ? TONE.ok : TONE.mut, dim: !published });
    seq.push({ id: "end", label: "结局", color: tc(stateMeta(run.state).tone) });

    const P = new Map<string, { x: number; y: number }>();
    const nodes: Node[] = seq.map((d, i) => { const row = Math.floor(i / MAXC), col = i % MAXC; const p = { x: col * COLW, y: row * ROWH }; P.set(d.id, p);
      return { id: d.id, type: "flow", position: p, data: { label: d.label, color: d.color, dim: d.dim, att: d.att } }; });

    const edges: Edge[] = [];
    const link = (s: string, t: string, color = "#444c56", o: { label?: string; animated?: boolean; opacity?: number } = {}) => {
      const a = P.get(s)!, b = P.get(t)!; const dy = b.y - a.y;
      let sh = "sr", th = "tl";
      if (Math.abs(dy) > 1) { if (dy > 0) { sh = "sb"; th = "tt"; } else { sh = "sr"; th = "tl"; } }
      edges.push({ id: `e${s}-${t}`, source: s, target: t, sourceHandle: sh, targetHandle: th, type: "smoothstep", animated: !!o.animated, markerEnd: { type: MarkerType.ArrowClosed, color, width: 16, height: 16 }, style: { stroke: color, opacity: o.opacity ?? 1, strokeWidth: 1.4 }, label: o.label, labelStyle: { fill: color, fontSize: 10 } });
    };
    for (let i = 0; i < seq.length - 2; i++) { if (seq[i].id[0] === "r") continue; link(seq[i].id, seq[i + 1].id, "#444c56", { animated: true }); }
    for (let i = 1; i < n; i++) link("r" + (i - 1), "w" + i, TONE.warn, { label: "返工" });
    if (accepted && published) { link(lastR, "pub", TONE.ok, { label: "接受", animated: true }); link("pub", "end", TONE.ok, { animated: true }); }
    else if (accepted) { link(lastR, "end", TONE.ok, { label: "接受·无需发布", animated: true }); link(lastR, "pub", TONE.mut, { opacity: 0.3 }); }
    if (rejected) { link(lastR, "end", TONE.err, { label: "拒绝/阻塞", animated: true }); link(lastR, "pub", TONE.mut, { opacity: 0.3 }); link("pub", "end", TONE.mut, { opacity: 0.3 }); }
    if (!accepted && !rejected) link(lastR, "end", TONE.info, { animated: true });
    return { nodes, edges };
  }, [run]);
  return (
    <div className="h-80 rounded-md border border-border overflow-hidden">
      <ReactFlow nodes={nodes} edges={edges} nodeTypes={nodeTypes} fitView proOptions={{ hideAttribution: true }} onNodeClick={(_, nd) => { const a = (nd.data as { att?: number })?.att; if (a != null) onAtt(a); }}>
        <Controls position="top-right" showInteractive={false} />
        <Background variant={BackgroundVariant.Dots} gap={16} />
      </ReactFlow>
    </div>
  );
}
