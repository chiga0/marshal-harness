import { useMemo } from "react";
import { ReactFlow, MiniMap, Controls, Background, BackgroundVariant, type Node, type Edge } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { RunDetail } from "@/lib/api";

const TONE: Record<string, string> = { ok: "#22c55e", err: "#ef4444", warn: "#eab308", info: "#3b82f6", mut: "#6b7280" };

export function Dag({ run, onAtt }: { run: RunDetail; onAtt: (i: number) => void }) {
  const { nodes, edges } = useMemo(() => {
    const stages: { label: string; color: string; att?: number }[] = [{ label: "Leader/Plan", color: TONE.info }];
    (run.attempts ?? []).forEach((a, i) => stages.push({ label: `Worker${i + 1}`, color: a.workerStatus === "completed" ? TONE.ok : a.workerStatus ? TONE.err : TONE.info, att: i }));
    stages.push({ label: "验证", color: run.verification === "pass" ? TONE.ok : run.verification ? TONE.err : TONE.mut });
    stages.push({ label: "审查", color: run.hasReview ? TONE.ok : TONE.mut });
    stages.push({ label: "发布", color: run.hasPublication ? TONE.ok : TONE.mut });
    stages.push({ label: "结局", color: run.hasOutcome ? TONE.ok : TONE.mut });
    const nodes: Node[] = stages.map((s, i) => ({ id: String(i), position: { x: i * 170, y: 40 }, data: { label: s.label, att: s.att }, style: { border: `1px solid ${s.color}`, background: s.color + "22", color: "inherit", fontSize: 12, borderRadius: 6 } }));
    const edges: Edge[] = stages.slice(1).map((_, i) => ({ id: "e" + i, source: String(i), target: String(i + 1), animated: true, style: { stroke: "#444c56" } }));
    return { nodes, edges };
  }, [run]);
  return (
    <div className="h-56 rounded-md border border-border overflow-hidden">
      <ReactFlow nodes={nodes} edges={edges} fitView proOptions={{ hideAttribution: true }} onNodeClick={(_, n) => { const a = (n.data as { att?: number })?.att; if (a != null) onAtt(a); }}>
        <MiniMap pannable zoomable /> <Controls showInteractive={false} /> <Background variant={BackgroundVariant.Dots} gap={16} />
      </ReactFlow>
    </div>
  );
}
