// The graph is business state supplied in a proposed plan, not an instruction
// to spawn work. Admission/approval and budget reservation belong to Application.
export function checkedGraph(nodes, edges, {maxNodes = 64, maxEdges = 256} = {}) {
  if (!Array.isArray(nodes) || !nodes.length || nodes.length > maxNodes ||
      !Array.isArray(edges) || edges.length > maxEdges) throw new Error('invalid_plan_graph');
  const ids = nodes.map(node => node?.id);
  if (ids.some(id => typeof id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id)) ||
      new Set(ids).size !== ids.length) throw new Error('invalid_plan_graph');
  const incoming = new Map(ids.map(id => [id, new Set()]));
  const outgoing = new Map(ids.map(id => [id, new Set()]));
  for (const edge of edges) {
    if (!incoming.has(edge?.from) || !incoming.has(edge?.to) || edge.from === edge.to ||
        outgoing.get(edge.from).has(edge.to)) throw new Error('invalid_plan_graph');
    outgoing.get(edge.from).add(edge.to); incoming.get(edge.to).add(edge.from);
  }
  const remaining = new Map([...incoming].map(([id, dependencies]) => [id, dependencies.size]));
  const ready = ids.filter(id => remaining.get(id) === 0), order = [];
  for (let cursor = 0; cursor < ready.length; cursor++) {
    const id = ready[cursor]; order.push(id);
    for (const child of outgoing.get(id)) {
      remaining.set(child, remaining.get(child) - 1);
      if (remaining.get(child) === 0) ready.push(child);
    }
  }
  if (order.length !== ids.length) throw new Error('invalid_plan_graph');
  return {order, incoming, outgoing};
}

export function affectedNodes(nodes, edges, changed) {
  const graph = checkedGraph(nodes, edges), affected = new Set();
  for (const id of changed) {
    if (!graph.outgoing.has(id)) throw new Error('unknown_plan_node');
    affected.add(id);
  }
  for (const id of graph.order) if (affected.has(id)) {
    for (const child of graph.outgoing.get(id)) affected.add(child);
  }
  return graph.order.filter(id => affected.has(id));
}
