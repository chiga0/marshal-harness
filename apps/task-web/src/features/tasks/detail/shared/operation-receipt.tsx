import {skipToken, useQuery, useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {ApiError, parseOperation, type OperationRecord, type Transport} from '@/lib/transport/types';
import {usePollMode} from '@/lib/queries/polling';
import {SESSION_OPERATIONS_KEY, SESSION_OPERATIONS_LIMIT, operationQueryKey} from '@/lib/queries/operations';
import {ErrorNotice} from './error-notice';

const LABEL: Record<OperationRecord['status'], string> = {accepted: '已受理', running: '处理中', succeeded: '操作成功', failed: '操作失败', unknown: '操作结果未知'};

/** 只读展示本连接实际收到的回执；与动作卡片生命周期无关。 */
export function TaskOperationReceipts({taskId, transport}: {taskId: string; transport: Transport}) {
  // 独立事件组件测试可无QueryProvider；真实应用始终在Provider内。
  let client;
  try { client = useQueryClient(); } catch { return null; }
  return client ? <SessionReceipts taskId={taskId} transport={transport} /> : null;
}

function SessionReceipts({taskId, transport}: {taskId: string; transport: Transport}) {
  const query = useQuery<OperationRecord[]>({queryKey: SESSION_OPERATIONS_KEY, queryFn: skipToken, initialData: [], enabled: false, gcTime: Infinity});
  const receipts = (query.data ?? []).filter(operation => operation.taskId === taskId);
  return <section aria-label="本次连接操作回执" className="mb-4 space-y-2">
    <h2 className="text-base font-semibold">本次连接取得的操作回执</h2>
    <p className="text-xs text-text-secondary">仅本连接最近 {SESSION_OPERATIONS_LIMIT} 条回执中属于此任务的记录，不是服务端全局历史；刷新或断开连接后清除。不自动重新执行操作。</p>
    {receipts.length ? receipts.map(receipt => <OperationStatus key={receipt.id} receipt={receipt} transport={transport} />) : <p className="text-sm text-text-secondary">本连接尚未取得此任务的操作回执；已有事实请结合任务与事件核对。</p>}
  </section>;
}

export function OperationReceipt({result, taskId, kind, workerId, transport}: {
  result: unknown; taskId: string; kind: OperationRecord['kind']; workerId?: string; transport: Transport;
}) {
  let receipt: OperationRecord;
  try {
    const envelope = result as {operation?: unknown} | null;
    receipt = parseOperation(envelope && typeof envelope === 'object' && 'operation' in envelope ? envelope.operation : result);
    if (receipt.taskId !== taskId || receipt.kind !== kind || (kind === 'worker.cancel' && receipt.workerId !== workerId)) throw new Error('binding');
  } catch {
    return <p role="alert" className="text-sm text-warning">未取得可核对的原 Operation 回执；不能确认操作结果。请核对任务、问题或 Worker 状态，不要新建替代操作。</p>;
  }
  return <OperationStatus key={receipt.id} receipt={receipt} transport={transport} />;
}

function OperationStatus({receipt, transport}: {receipt: OperationRecord; transport: Transport}) {
  const interval = usePollMode('detail');
  const query = useQuery({
    queryKey: operationQueryKey(receipt),
    queryFn: async ({signal}) => {
      const current = await transport.getOperation(receipt.id, {signal});
      if (current.id !== receipt.id || current.taskId !== receipt.taskId || current.kind !== receipt.kind || (current.workerId ?? null) !== (receipt.workerId ?? null)) {
        throw new ApiError(502, 'operation_binding_mismatch', '操作回执归属不符，保留原回执', null);
      }
      return current;
    },
    initialData: receipt,
    staleTime: 0,
    retry: false,
    refetchInterval: query => ['succeeded', 'failed'].includes(query.state.data?.status ?? '') ? false : interval ?? false,
    structuralSharing: (previous, current) => {
      const before = previous as OperationRecord | undefined, next = current as OperationRecord;
      return before && (next.taskRevision < before.taskRevision || Date.parse(next.updatedAt) < Date.parse(before.updatedAt)) ? before : next;
    },
  });
  const operation = query.data;
  return <section aria-label="操作回执" className="mt-2 space-y-1 rounded border border-border p-2 text-sm" data-testid="operation-receipt">
    <p>操作回执：<code className="break-all">{operation.id}</code></p>
    <p role="status">{LABEL[operation.status]}（<code>{operation.status}</code>）{query.isError ? '；读数可能陈旧' : ''}</p>
    <p className="text-xs text-text-secondary">{operation.kind} · Task revision {operation.taskRevision} · 更新于 {operation.updatedAt}</p>
    {operation.code ? <p className="break-all text-danger">结果码：{operation.code}</p> : null}
    <p className="text-xs text-text-secondary">这是该操作的回执，不代表整个任务交付成功、发布后验通过或 Worker 已消费答复。失败或未知时先核对事实，不自动重派。</p>
    {query.isError ? <ErrorNotice error={query.error} title="读取操作回执失败" onRefresh={() => void query.refetch()} /> : null}
    <Button size="sm" variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>刷新原操作回执</Button>
  </section>;
}
