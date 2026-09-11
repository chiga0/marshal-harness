// 成果元数据聚合：Task.artifactIds 逐 id 并行 GET /v1/artifacts/{artifactId}；
// 单项失败保留为 failed 占位（视图如实显示不可用行，不静默丢弃）；
// 全部失败视为整体加载失败（抛错交查询层，页面显示 ErrorNotice，不冒充空清单）。

import {useQuery, type UseQueryResult} from '@tanstack/react-query';
import type {ArtifactId, ArtifactRecord, LeaderRecord, Transport} from '@/lib/transport/types';
import {ApiError} from '@/lib/transport/types';
import {taskKeys} from '../tasks/detail/query-keys';
import {checkArtifact, matchesContract} from './traceability';

export type ArtifactEntry =
  | {status: 'ok'; artifact: ArtifactRecord; sources?: string[]}
  | {status: 'failed'; id: ArtifactId; error: unknown; sources?: string[]};

export interface UseTaskArtifactsOptions {
  taskId: string;
  artifactIds: readonly ArtifactId[];
  transport: Transport;
  refetchInterval: number | false;
  leader?: LeaderRecord | null;
}

/** 小批并发读取，避免 audit 引用集合引发瞬时请求风暴。 */
export async function mapArtifacts(ids: readonly string[], read: (id: string) => Promise<ArtifactEntry>): Promise<ArtifactEntry[]> {
  const entries: ArtifactEntry[] = new Array(ids.length);
  let next = 0;
  await Promise.all(Array.from({length: Math.min(4, ids.length)}, async () => {
    while (next < ids.length) { const index = next++; entries[index] = await read(ids[index]!); }
  }));
  return entries;
}

export function useTaskArtifacts({taskId, artifactIds, transport, refetchInterval, leader = null}: UseTaskArtifactsOptions): UseQueryResult<ArtifactEntry[]> {
  const idsKey = JSON.stringify([artifactIds, leader]);
  return useQuery<ArtifactEntry[]>({
    // idsKey 进查询键：产物集合变化即视为新数据；前缀仍在 ['task', taskId] 之下，组键失效可覆盖
    queryKey: [...taskKeys.artifacts(taskId), idsKey],
    queryFn: async ({signal}) => {
      const sources = new Map<string, string[]>();
      const add = (id: string, source: string) => sources.set(id, [...(sources.get(id) ?? []), source]);
      artifactIds.forEach(id => { if (!sources.has(id)) add(id, 'Task 成果'); });
      if (leader !== null) {
        if (!matchesContract(leader, 'LeaderView') || leader.taskId !== taskId) throw new ApiError(502, 'artifact_reference_mismatch', 'Leader 成果关联与当前 Task 不符', null);
        if (leader.publication?.receiptArtifactId) add(leader.publication.receiptArtifactId, '发布回执');
        if (leader.postverify?.evidenceArtifactId) add(leader.postverify.evidenceArtifactId, '发布后验证据');
      }
      const ids = [...sources.keys()];
      const entries = await mapArtifacts(ids, async (id): Promise<ArtifactEntry> => {
          try {
            if (signal.aborted) throw signal.reason;
            const artifact = await transport.getArtifact(id, {signal, expectedTaskId: taskId});
            // 即便宿主提供其他Transport实现，也不得把串ID/串Task成果放入本Task可下载清单。
            if (artifact.id !== id || artifact.taskId !== taskId) throw new ApiError(502, 'artifact_binding_mismatch', '成果归属不符，已拒绝展示和下载', null);
            checkArtifact(artifact, id, taskId);
            if (sources.get(id)!.some(source => source !== 'Task 成果') && artifact.kind !== 'evidence') throw new ApiError(502, 'artifact_binding_mismatch', '回执不是证据制品，已拒绝展示和下载', null);
            return {status: 'ok', artifact, sources: sources.get(id)!};
          } catch (error) {
            return {status: 'failed', id, error, sources: sources.get(id)!};
          }
        });
      if (ids.length > 0 && entries.every(entry => entry.status === 'failed')) {
        const first = entries[0] as Extract<ArtifactEntry, {status: 'failed'}>;
        throw first.error;
      }
      return entries;
    },
    refetchInterval,
  });
}
