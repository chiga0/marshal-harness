// 控制操作：按 allowedActions 提供 暂停/恢复/取消。暂停语义如实说明「只停止新调度」；
// 取消是终态操作且二次确认；受理（2xx）不代表状态已变化，终态视图只展示事实。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {TaskDetail, Transport} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {casRevisionOf, isTerminalStatus} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {useLogicalAction, type ActionPhase} from '../shared/logical-action';

type ControlAction = 'pause' | 'resume' | 'cancel';

const CONTROL_COPY: Record<ControlAction, {button: string; confirmTitle: string; confirmBody: string; destructive?: boolean}> = {
  pause: {
    button: '暂停任务',
    confirmTitle: '暂停该任务？',
    confirmBody: '暂停只停止新的调度：已经在运行的 Worker 不会被中途停止，会继续运行到当前阶段结束。它不是进程暂停。恢复后才会继续调度新的工作。',
  },
  resume: {
    button: '恢复任务',
    confirmTitle: '恢复该任务？',
    confirmBody: '恢复后任务重新接受调度。受理不代表已恢复完成，以服务端任务状态为准。',
  },
  cancel: {
    button: '取消任务',
    confirmTitle: '取消整个任务？',
    destructive: true,
    confirmBody: '取消是终态操作，不能撤销。取消请求被受理不代表任务已经停止：正在运行的 Worker 可能仍在收尾，最终以服务端任务状态为准。',
  },
};

export interface TaskControlsProps {
  detail: TaskDetail;
  transport: Transport;
  onChanged: () => void;
}

export function TaskControls({detail, transport, onChanged}: TaskControlsProps) {
  const [pendingAction, setPendingAction] = useState<ControlAction | null>(null);
  const revision = casRevisionOf(detail);
  const terminal = isTerminalStatus(detail.status);

  const available = (['pause', 'resume', 'cancel'] as ControlAction[]).filter(name => detail.allowedActions.includes(name));

  return (
    <Card aria-label="任务控制" className="space-y-2" data-testid="task-controls">
      <h2 className="text-base font-semibold leading-6">控制</h2>
      {terminal ? (
        <p className="text-sm text-text-secondary" data-testid="controls-terminal">
          任务已到达终态（{detail.status}）。此区域只展示事实回执，不再提供控制操作。
        </p>
      ) : null}
      {!terminal && available.length === 0 ? (
        <p className="text-sm text-text-secondary" data-testid="controls-unavailable">
          当前状态不提供控制操作（allowedActions 为空或不含 pause/resume/cancel）。
        </p>
      ) : null}
      {!terminal && available.length > 0 ? (
        <>
          <div className="flex flex-wrap gap-2">
            {available.map(name => (
              <Button
                key={name}
                size="sm"
                variant={CONTROL_COPY[name].destructive ? 'destructive' : 'outline'}
                disabled={revision === null}
                onClick={() => setPendingAction(name)}
                data-testid={`control-${name}`}
              >
                {CONTROL_COPY[name].button}
              </Button>
            ))}
          </div>
          <p className="text-xs text-text-secondary">
            暂停/恢复/取消都以当前 Task revision 提交；不可用时会如实说明。暂停只停止新的调度，不会立即停止运行中的 Worker。
          </p>
          {revision === null ? (
            <p className="text-xs text-warning">未获得任务 revision，控制操作已禁用；请刷新任务。</p>
          ) : null}
        </>
      ) : null}

      {pendingAction !== null ? (
        <ControlAttempt
          taskId={detail.id}
          action={pendingAction}
          revision={revision}
          transport={transport}
          onClose={() => setPendingAction(null)}
          onChanged={onChanged}
        />
      ) : null}
    </Card>
  );
}

interface ControlAttemptProps {
  taskId: string;
  action: ControlAction;
  revision: string | null;
  transport: Transport;
  onClose: () => void;
  onChanged: () => void;
}

function ControlAttempt({taskId, action, revision, transport, onClose, onChanged}: ControlAttemptProps) {
  const queryClient = useQueryClient();
  const logical = useLogicalAction([taskId, `task.${action}`, revision ?? '']);
  const run = (key: string): Promise<unknown> => {
    if (revision === null) return Promise.reject(new Error('缺少任务 revision'));
    if (action === 'pause') return transport.pauseTask(taskId, {revision, idempotencyKey: key});
    if (action === 'resume') return transport.resumeTask(taskId, {revision, idempotencyKey: key});
    return transport.cancelTask(taskId, {revision, idempotencyKey: key});
  };
  const copy = CONTROL_COPY[action];
  return (
    <>
      <ConfirmDialog
        open={logical.phase.kind === 'idle'}
        title={copy.confirmTitle}
        description={copy.confirmBody}
        destructive={copy.destructive ?? false}
        confirmText={copy.button}
        onConfirm={() => void logical.submit(run)}
        onCancel={onClose}
      />
      <ControlPhase phase={logical.phase} action={action}
        onReplay={() => void logical.replay(run)}
        onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); }}
        onClose={onClose} />
    </>
  );
}

function ControlPhase({phase, action, onReplay, onRefresh, onClose}: {phase: ActionPhase; action: ControlAction; onReplay: () => void; onRefresh: () => void; onClose: () => void}) {
  if (phase.kind === 'submitting') return <p className="mt-2 text-sm text-text-secondary" role="status">正在提交{action === 'cancel' ? '取消' : action === 'pause' ? '暂停' : '恢复'}请求…</p>;
  if (phase.kind === 'accepted') {
    return (
      <div className="mt-2 rounded-md border border-success/40 bg-success/5 p-3" role="status" data-testid={`control-${action}-accepted`}>
        <p className="text-sm font-medium text-success">请求已受理。</p>
        <p className="mt-1 text-sm text-text-secondary">
          {action === 'pause'
            ? '受理不代表已暂停：它只停止新的调度，运行中的 Worker 会继续到当前阶段结束，以服务端任务状态为准。'
            : action === 'cancel'
              ? '受理不代表任务已停止：终态以服务端任务状态为准。'
              : '受理不代表已恢复，以服务端任务状态为准。'}
        </p>
        <div className="mt-2 flex gap-2">
          <Button size="sm" variant="outline" onClick={onRefresh}>刷新任务状态</Button>
          <Button size="sm" variant="ghost" onClick={onClose}>关闭</Button>
        </div>
      </div>
    );
  }
  if (phase.kind === 'rejected') {
    return (
      <div className="mt-2">
        <ErrorNotice error={phase.error} title="控制操作失败" onRefresh={onRefresh} />
      </div>
    );
  }
  if (phase.kind === 'unknown') {
    return (
      <div className="mt-2">
        <ErrorNotice
          error={phase.error}
          title="控制操作结果未知"
          outcomeNote="请求可能已被服务端受理。可显式原键重放一次（不重新执行），或先刷新核对任务与回执。"
          onReplay={onReplay}
          onRefresh={onRefresh}
        />
      </div>
    );
  }
  return null;
}
