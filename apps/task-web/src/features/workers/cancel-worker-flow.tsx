// 单 Worker 取消（permission-free flow）：与「取消任务」严格分开；CAS 用所属 Task revision；
// 501（unsupported_operation）如实提示且绝不回退取消整 Task；结果未知可显式原键重放。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {ConfirmDialog} from '@/components/ui/dialog';
import {ApiError, type Transport, type WorkerRecord} from '@/lib/transport/types';
import {taskKeys} from '../tasks/detail/query-keys';
import {ErrorNotice} from '../tasks/detail/shared/error-notice';
import {useLogicalAction} from '../tasks/detail/shared/logical-action';

export interface CancelWorkerFlowProps {
  taskId: string;
  revision: string | null;
  worker: WorkerRecord;
  transport: Transport;
  onChanged: () => void;
}

export function CancelWorkerFlow({taskId, revision, worker, transport, onChanged}: CancelWorkerFlowProps) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const action = useLogicalAction([taskId, 'worker.cancel', worker.id, revision ?? '']);

  const doCancel = (key: string) => {
    if (revision === null) return Promise.reject(new Error('缺少任务 revision'));
    return transport.cancelWorker(taskId, worker.id, {revision, idempotencyKey: key});
  };

  const isUnsupported = action.phase.kind === 'rejected' && action.phase.error instanceof ApiError && action.phase.error.status === 501;

  return (
    <div className="space-y-2" data-testid="cancel-worker-flow">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="destructive"
          size="sm"
          disabled={revision === null}
          onClick={() => setConfirming(true)}
          data-testid="cancel-worker-open"
        >
          取消该 Worker
        </Button>
        <span className="text-xs text-text-secondary">仅取消此 Worker（节点 {worker.nodeId}），不会取消整个任务。</span>
      </div>
      {revision === null ? (
        <p className="text-xs text-warning">未获得任务 revision，无法安全取消；请刷新任务。</p>
      ) : null}

      <ConfirmDialog
        open={confirming && action.phase.kind === 'idle'}
        title={`取消 Worker（节点 ${worker.nodeId}）？`}
        description={`只请求取消这一个 Worker（${worker.id}），不会取消整个任务。取消以所属 Task revision ${revision ?? '未知'} 提交；受理不代表已停止，以服务端 Worker 状态为准。`}
        destructive
        confirmText="确认取消该 Worker"
        onConfirm={() => {
          setConfirming(false);
          void action.submit(doCancel);
        }}
        onCancel={() => setConfirming(false)}
      />

      {action.phase.kind === 'submitting' ? <p className="text-sm text-text-secondary" role="status">正在提交取消请求…</p> : null}
      {action.phase.kind === 'accepted' ? (
        <div className="rounded-md border border-success/40 bg-success/5 p-3" role="status" data-testid="cancel-worker-accepted">
          <p className="text-sm font-medium text-success">取消请求已受理。</p>
          <p className="mt-1 text-sm text-text-secondary">受理不代表该 Worker 已停止；以轮询到的服务端状态为准。</p>
        </div>
      ) : null}
      {action.phase.kind === 'rejected' ? (
        <div className="space-y-2">
          <ErrorNotice
            error={action.phase.error}
            title="取消 Worker 失败"
            onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); }}
          />
          {isUnsupported ? (
            <p className="rounded border border-warning/40 bg-warning/5 p-2 text-sm text-text-primary" data-testid="cancel-worker-unsupported">
              当前服务不支持单 Worker 取消（501）。不会用「取消整个任务」代替执行。
            </p>
          ) : null}
        </div>
      ) : null}
      {action.phase.kind === 'unknown' ? (
        <ErrorNotice
          error={action.phase.error}
          title="取消结果未知"
          outcomeNote="取消请求可能已被受理。可显式原键重放一次（不重新执行），或先刷新核对 Worker 状态。"
          onReplay={() => void action.replay(doCancel)}
          onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); }}
        />
      ) : null}
    </div>
  );
}
