// 成果元数据聚合：Task.artifactIds 逐 id 并行 GET /v1/artifacts/{artifactId}；
// 单项失败保留为 failed 占位（视图如实显示不可用行，不静默丢弃）；
// 全部失败视为整体加载失败（抛错交查询层，页面显示 ErrorNotice，不冒充空清单）。

import {useQuery, type UseQueryResult} from '@tanstack/react-query';
import type {ArtifactId, ArtifactRecord, Transport} from '@/lib/transport/types';
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
            return {status: 'ok', artifact: await transport.getArtifact(id, {signal})};
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
