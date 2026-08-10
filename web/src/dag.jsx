import React from "react";
import { createRoot } from "react-dom/client";
import { ReactFlow, MiniMap, Controls, Background, BackgroundVariant } from "@xyflow/react";
import "@xyflow/react/dist/style.css";

// mount(el, data): data = { stages: [{id,label,color,att}], edges: [{from,to}] }
function mount(el, data) {
  const nodes = (data.stages || []).map((s, i) => ({
    id: String(i),
    position: { x: i * 170, y: 40 },
    data: { label: s.label },
    att: s.att,
    style: { border: "1px solid " + s.color, background: s.color + "22", color: "inherit", fontSize: 12, borderRadius: 6 },
  }));
  const edges = (data.edges || []).map((e, i) => ({
    id: "e" + i, source: String(e.from), target: String(e.to), animated: true,
    style: { stroke: "#444c56" },
  }));
  const onNodeClick = (_, n) => { if (n.att != null && window.__marshalAtt) window.__marshalAtt(n.att); };
  const root = createRoot(el);
  root.render(
    <div style={{ width: "100%", height: 220 }}>
      <ReactFlow nodes={nodes} edges={edges} onNodeClick={onNodeClick} fitView proOptions={{ hideAttribution: true }}>
        <MiniMap pannable zoomable />
        <Controls showInteractive={false} />
        <Background variant={BackgroundVariant.Dots} gap={16} />
      </ReactFlow>
    </div>
  );
  return root;
}
window.MarshalDAG = { mount };
