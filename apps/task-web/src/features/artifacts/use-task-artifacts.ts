// 成果元数据聚合：Task.artifactIds 逐 id 并行 GET /v1/artifacts/{artifactId}；
// 单项失败保留为 failed 占位（视图如实显示不可用行，不静默丢弃）；
// 全部失败视为整体加载失败（抛错交查询层，页面显示 ErrorNotice，不冒充空清单）。

import {useQuery, type UseQueryResult} from '@tanstack/react-query';
import type {ArtifactId, ArtifactRecord, Transport} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {taskKeys} from '../tasks/detail/query-keys';

export type ArtifactEntry =
  | {status: 'ok'; artifact: ArtifactRecord}
  | {status: 'failed'; id: ArtifactId; error: unknown};

export interface UseTaskArtifactsOptions {
  taskId: string;
  artifactIds: readonly ArtifactId[];
  transport: Transport;
  refetchInterval: number | false;
}

export function useTaskArtifacts({taskId, artifactIds, transport, refetchInterval}: UseTaskArtifactsOptions): UseQueryResult<ArtifactEntry[]> {
  const idsKey = artifactIds.join(',');
  return useQuery<ArtifactEntry[]>({
    // idsKey 进查询键：产物集合变化即视为新数据；前缀仍在 ['task', taskId] 之下，组键失效可覆盖
    queryKey: [...taskKeys.artifacts(taskId), idsKey],
    queryFn: async ({signal}) => {
      const entries = await Promise.all(
        artifactIds.map(async (id): Promise<ArtifactEntry> => {
          try {
            const artifact = await transport.getArtifact(id, {signal, expectedTaskId: taskId});
            // 即便宿主提供其他Transport实现，也不得把串ID/串Task成果放入本Task可下载清单。
            if (artifact.id !== id || artifact.taskId !== taskId) throw new ApiError(502, 'artifact_binding_mismatch', '成果归属不符，已拒绝展示和下载', null);
            return {status: 'ok', artifact};
          } catch (error) {
            return {status: 'failed', id, error};
          }
        }),
      );
      if (artifactIds.length > 0 && entries.every(entry => entry.status === 'failed')) {
        const first = entries[0] as Extract<ArtifactEntry, {status: 'failed'}>;
        throw first.error;
      }
      return entries;
    },
    refetchInterval,
  });
}
