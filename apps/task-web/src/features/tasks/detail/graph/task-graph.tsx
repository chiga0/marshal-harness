import {useId, useState} from 'react';
import {Link} from 'react-router-dom';
import {useQuery, useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ApiError, type GraphRecord, type GraphNodeStatus, type PlanNodeRole, type PlanRecord, type Transport, type WorkerRecord} from '@/lib/transport/types';
import {usePollMode} from '@/lib/queries/polling';
import {taskKeys} from '../query-keys';
import {StatusBadge} from '../shared/status-badge';
import {useNow} from '../shared/use-now';

const STATUS: Record<GraphNodeStatus, string> = {pending: '待调度', ready: '可执行', running: '执行中', waiting: '等待中', completed: '执行完成', failed: '执行失败', cancelled: '已取消', unknown: '状态未知'};
const ROLE: Record<PlanNodeRole, string> = {planner: '规划', author: '作者', reviewer: '评审', integrator: '整合', verifier: '校验'};
const tone = (status: GraphNodeStatus) => status === 'failed' ? 'danger' : status === 'waiting' ? 'warning' : status === 'running' ? 'default' : 'secondary';

/** 只按依赖拓扑布局；位置不编码进度或调度许可。输入已通过 Graph 合同校验。 */
export function graphPositions(graph: GraphRecord) {
  const ranks = new Map<string, number>();
  const remaining = new Set(graph.nodes.map(node => node.id));
  while (remaining.size) {
    let advanced = false;
    for (const id of remaining) {
      const parents = graph.edges.filter(edge => edge.to === id).map(edge => edge.from);
      if (parents.every(parent => ranks.has(parent))) {
        ranks.set(id, parents.length ? Math.max(...parents.map(parent => ranks.get(parent)!)) + 1 : 0);
        remaining.delete(id);
        advanced = true;
      }
    }
    if (!advanced) throw new Error('invalid_graph_topology');
  }
  const rows = new Map<number, number>();
  return graph.nodes.map(node => {
    const rank = ranks.get(node.id)!;
    const row = rows.get(rank) ?? 0;
    rows.set(rank, row + 1);
    return {node, x: 16 + rank * 288, y: 16 + row * 160};
  });
}

export interface TaskGraphProps {
  taskId: string;
  plan: PlanRecord | null;
  workers: WorkerRecord[] | null;
  transport: Transport;
}

export function TaskGraph({taskId, plan, workers, transport}: TaskGraphProps) {
  const queryClient = useQueryClient();
  const interval = usePollMode('detail');
  const now = useNow(5000);
  const query = useQuery<GraphRecord>({
    queryKey: taskKeys.graph(taskId),
    queryFn: async ({signal}) => {
      // Graph 无快照 revision：消费 signal 使作废/卸载请求被 Query 取消，旧 promise 不能覆盖新结果。
      // 同 key 的周期请求由 Query 去重；手动刷新显式取消旧请求，再接纳当前读取。
      const graph = await transport.getGraph(taskId, {signal});
      if (signal.aborted) throw new DOMException('已取消旧任务图读取', 'AbortError');
      const previous = queryClient.getQueryData<GraphRecord>(taskKeys.graph(taskId));
      if (previous && graph.planRevision < previous.planRevision) throw new ApiError(502, 'stale_graph_response', '任务图计划版本倒退，保留上次读数', null);
      return graph;
    },
    refetchInterval: interval ?? false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  });
  const graph = query.data ?? null;
  const stale = graph !== null && (query.isError || query.isPaused || now - query.dataUpdatedAt > 15000);
  const noPlan = query.error instanceof ApiError && query.error.status === 409 && query.error.code === 'plan_conflict';
  return <Card className="min-w-0 space-y-3" data-testid="task-graph">
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div><h2 className="text-base font-semibold">任务依赖图</h2><p className="text-xs text-text-secondary">箭头表示依赖方向，不代表正在调用；节点与 Worker 执行完成均不等于独立验收通过。</p></div>
      <Button size="sm" variant="outline" onClick={() => void query.refetch()} disabled={query.isFetching}>刷新任务图</Button>
    </div>
    <p className="text-xs text-text-secondary" role="status">
      {query.dataUpdatedAt ? `最近取得图投影：${new Date(query.dataUpdatedAt).toLocaleTimeString()}。` : ''}
      {stale ? '图读数陈旧，不能据此判断当前仍在执行。' : query.isFetching ? '正在读取任务图…' : ''}
      {query.isPaused ? '网络离线，读取已暂停。' : ''}
    </p>
    {query.isError ? <p role="alert" className="text-sm text-danger">{noPlan ? '尚无冻结计划，服务端暂未提供任务图。' : '任务图读取失败或响应不可用。'}{graph ? '保留上次图投影，非最新状态。' : '不生成替代图。'}{query.error instanceof ApiError ? `（${query.error.code}）` : ''}</p> : null}
    {graph ? <GraphView key={taskId} graph={graph} plan={plan} workers={workers} /> : !query.isError ? <p className="text-sm text-text-secondary">任务图尚未加载。</p> : null}
  </Card>;
}

export function GraphView({graph, plan, workers}: {graph: GraphRecord; plan: PlanRecord | null; workers: WorkerRecord[] | null}) {
  const [mode, setMode] = useState<'graph' | 'list'>('graph');
  const [selected, setSelected] = useState<string | null>(null);
  const marker = useId().replace(/:/g, '');
  const positions = graphPositions(graph);
  const width = Math.max(288, ...positions.map(item => item.x + 256));
  const height = Math.max(160, ...positions.map(item => item.y + 144));
  const matchedPlan = plan?.taskId === graph.taskId && plan.revision === graph.planRevision ? plan : null;
  const detail = graph.nodes.find(node => node.id === selected) ?? null;
  const goalFor = (id: string, role: PlanNodeRole) => matchedPlan?.nodes.find(node => node.id === id && node.role === role)?.goal ?? null;
  return <div className="min-w-0 space-y-3">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p className="text-xs text-text-secondary">计划版本 {graph.planRevision} · {graph.nodes.length} 个节点 · {graph.edges.length} 条依赖</p>
      <div role="group" aria-label="任务图显示方式" className="flex gap-2">
        <Button size="sm" variant={mode === 'graph' ? 'default' : 'outline'} aria-pressed={mode === 'graph'} onClick={() => setMode('graph')}>依赖图</Button>
        <Button size="sm" variant={mode === 'list' ? 'default' : 'outline'} aria-pressed={mode === 'list'} onClick={() => setMode('list')}>节点列表</Button>
      </div>
    </div>
    {!matchedPlan ? <p className="text-xs text-text-secondary">计划正文不可用或版本不匹配，节点目标暂不可用；状态仍来自 Graph。</p> : null}
    {graph.nodes.length === 0 ? <p className="text-sm text-text-secondary">服务端图投影没有节点。</p> : mode === 'graph' ? <div className="max-h-[520px] max-w-full overflow-auto rounded-md border border-border bg-surface-muted/30" aria-label="任务依赖图画布" tabIndex={0}>
      <div className="relative" style={{width, height}}>
        <svg width={width} height={height} className="pointer-events-none absolute inset-0 text-text-secondary" aria-hidden="true">
          <defs><marker id={marker} markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0 0L8 4L0 8Z" fill="currentColor" /></marker></defs>
          {graph.edges.map(edge => {
            const from = positions.find(item => item.node.id === edge.from)!;
            const to = positions.find(item => item.node.id === edge.to)!;
            return <path key={`${edge.from}:${edge.to}`} d={`M${from.x + 240},${from.y + 64} C${from.x + 264},${from.y + 64} ${to.x - 24},${to.y + 64} ${to.x},${to.y + 64}`} fill="none" stroke="currentColor" strokeWidth="1.5" markerEnd={`url(#${marker})`} />;
          })}
        </svg>
        {positions.map(({node, x, y}) => <button key={node.id} type="button" aria-pressed={selected === node.id} onClick={() => setSelected(node.id)} aria-label={`查看节点 ${node.id}，${ROLE[node.role]}，${STATUS[node.status]}`} style={{left: x, top: y, width: 240, height: 128}} className="absolute flex flex-col gap-1 overflow-hidden rounded-md border border-border bg-surface p-3 text-left text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent aria-pressed:border-accent">
          <span className="w-full truncate font-medium">{node.id} · {ROLE[node.role]}</span>
          <StatusBadge machine={node.status} label={STATUS[node.status]} tone={tone(node.status)} />
          <span className="line-clamp-2 text-xs text-text-secondary">{goalFor(node.id, node.role) ?? '目标暂不可用'}</span>
          <span className="text-xs text-text-secondary">{node.workerIds.length} 个 Worker 引用</span>
        </button>)}
      </div>
    </div> : <ul className="space-y-2" aria-label="任务节点列表">
      {graph.nodes.map(node => <li key={node.id} className="rounded border border-border p-3 text-sm">
        <button type="button" className="min-h-11 break-all text-left font-medium text-accent underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent" onClick={() => setSelected(node.id)}>查看节点 {node.id}</button> · {ROLE[node.role]} <StatusBadge machine={node.status} label={STATUS[node.status]} tone={tone(node.status)} />
        <p className="whitespace-pre-wrap break-words">{goalFor(node.id, node.role) ?? '目标暂不可用'}</p>
        <p className="break-all text-xs text-text-secondary">依赖：{graph.edges.filter(edge => edge.to === node.id).map(edge => edge.from).join('、') || '无前置依赖'}；Worker 引用：{node.workerIds.join('、') || '尚无'}</p>
      </li>)}
    </ul>}
    {detail ? <section aria-label="节点详情" className="space-y-2 rounded-md border border-border p-3 text-sm">
      <h3 className="break-all font-semibold">节点 {detail.id} · {ROLE[detail.role]}</h3>
      <StatusBadge machine={detail.status} label={STATUS[detail.status]} tone={tone(detail.status)} />
      <p className="whitespace-pre-wrap break-words">{goalFor(detail.id, detail.role) ?? '匹配版本的目标暂不可用'}</p>
      <p className="break-all">前置依赖：{graph.edges.filter(edge => edge.to === detail.id).map(edge => edge.from).join('、') || '无'}；后继节点：{graph.edges.filter(edge => edge.from === detail.id).map(edge => edge.to).join('、') || '无'}</p>
      <h4 className="font-medium">关联执行成员（仅 Graph.workerIds）</h4>
      {detail.workerIds.length === 0 ? <p>尚无关联 Worker，不推断节点已执行。</p> : <ul className="space-y-2">{detail.workerIds.map(id => {
        const worker = workers?.find(item => item.id === id && item.taskId === graph.taskId && item.nodeId === detail.id);
        return <li key={id} className="break-all"><Link className="inline-block min-h-11 text-accent underline" to={`/tasks/${encodeURIComponent(graph.taskId)}/team/${encodeURIComponent(id)}`}>Worker {id}</Link><span className="ml-2 text-text-secondary">{worker ? `${worker.status} · 尝试 ${worker.attempt}` : '详情未加载或不匹配，可在团队页核对并加载更多'}（不是验收结果）</span></li>;
      })}</ul>}
    </section> : graph.nodes.length > 0 ? <p className="text-xs text-text-secondary">选择节点查看完整目标、依赖和执行成员；也可切换至节点列表。</p> : null}
  </div>;
}
