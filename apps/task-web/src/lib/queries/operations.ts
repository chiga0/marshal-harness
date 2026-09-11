import type {QueryClient} from '@tanstack/react-query';
import type {OperationRecord} from '../transport/types';

export const SESSION_OPERATIONS_KEY = ['operation-receipts', 'connection'] as const;
export const SESSION_OPERATIONS_LIMIT = 20;
export const operationQueryKey = (op: Pick<OperationRecord, 'taskId' | 'id'>) => ['task', op.taskId, 'operation', op.id] as const;

/** 仅本连接已取得的回执，不是服务端历史；连接clear后不恢复，最多保留20项。 */
export function rememberOperation(client: QueryClient, operation: OperationRecord): void {
  client.setQueryDefaults(SESSION_OPERATIONS_KEY, {gcTime: Infinity});
  const previous = client.getQueryData<OperationRecord[]>(SESSION_OPERATIONS_KEY) ?? [];
  const retained = previous.filter(item => item.id !== operation.id);
  retained.push(operation);
  const evicted = retained.splice(0, Math.max(0, retained.length - SESSION_OPERATIONS_LIMIT));
  for (const item of evicted) client.removeQueries({queryKey: operationQueryKey(item), exact: true});
  client.setQueryData(SESSION_OPERATIONS_KEY, retained);
}
