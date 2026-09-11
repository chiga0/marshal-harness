import {useState} from 'react';
import {useQuery} from '@tanstack/react-query';
import type {Transport} from '@/lib/transport/types';
import {taskKeys} from '../tasks/detail/query-keys';
import {checkArtifact, observedInputIds} from './traceability';
import {mapArtifacts, type ArtifactEntry} from './use-task-artifacts';

export function useObservedInputs({taskId, audit, transport, refetchInterval}: {
  taskId: string; audit: unknown; transport: Transport; refetchInterval: number | false;
}) {
  let refs: string[] | null = null, referenceError: unknown = null;
  try { refs = observedInputIds(audit, taskId); } catch (error) { referenceError = error; }
  const [loaded, setLoaded] = useState({taskId, count: 100});
  const count = loaded.taskId === taskId ? loaded.count : 100;
  const ids = refs?.slice(0, count) ?? [];
  const query = useQuery<ArtifactEntry[]>({
    queryKey: [...taskKeys.all(taskId), 'observed-inputs', ids],
    enabled: referenceError === null && refs !== null && ids.length > 0,
    queryFn: ({signal}) => mapArtifacts(ids, async id => {
      try {
        if (signal.aborted) throw signal.reason;
        // 只从已验证的当前 Task audit 精确关联取 ID；输入合同归属是 null，不放宽同 Task 成果门禁。
        const artifact = checkArtifact(await transport.getArtifact(id, {signal}), id, null, true);
        return {status: 'ok', artifact, sources: ['执行审计观测输入']};
      } catch (error) { return {status: 'failed', id, error, sources: ['执行审计观测输入']}; }
    }),
    refetchInterval,
    retry: false,
  });
  return {query, referenceError, total: refs?.length ?? null, loaded: ids.length,
    // 关联一旦不可验证，禁用查询之外也必须立即隐藏旧缓存数据。
    entries: referenceError !== null || refs === null ? null : ids.length === 0 ? [] : query.data ?? null,
    loadMore: () => setLoaded({taskId, count: count + 100})};
}
