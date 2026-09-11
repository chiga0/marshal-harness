// 控制操作：按 allowedActions（contract 闭集 approve|answer|cancel|pause|resume|repair）提供 暂停/恢复/取消。
// body=ControlTask {expectedRevision=任务 revision, idempotencyKey}；暂停语义如实说明「只停止新调度」；
// 取消是终态操作且二次确认；受理（2xx）不代表状态已变化，终态视图只展示事实。

import {useState} from 'react';
import {useQueryClient} from '@tanstack/react-query';
import {Button} from '@/components/ui/button';
import {Card} from '@/components/ui/card';
import {ConfirmDialog} from '@/components/ui/dialog';
import type {OperationRecord, Revision, TaskRecord, Transport} from '@/lib/transport/types';
import {taskKeys} from '../query-keys';
import {casRevisionOf, isTerminalStatus} from '../shared/derive';
import {ErrorNotice} from '../shared/error-notice';
import {OperationReceipt} from '../shared/operation-receipt';
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
  task: TaskRecord;
  transport: Transport;
  onChanged: () => void;
}

export function TaskControls({task, transport, onChanged}: TaskControlsProps) {
  const [pendingAction, setPendingAction] = useState<ControlAction | null>(null);
  const [confirmedOperations, setConfirmedOperations] = useState<OperationRecord[]>([]);
  const revision = casRevisionOf(task);
  const terminal = isTerminalStatus(task.status);

  const available = (['pause', 'resume', 'cancel'] as ControlAction[]).filter(name => task.allowedActions.includes(name));

  return (
    <Card aria-label="任务控制" className="space-y-2" data-testid="task-controls">
      <h2 className="text-base font-semibold leading-6">控制</h2>
      {terminal ? (
        <p className="text-sm text-text-secondary" data-testid="controls-terminal">
          任务已到达终态（{task.status}）。此区域只展示事实回执，不再提供控制操作。
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
                disabled={pendingAction !== null}
                onClick={() => setPendingAction(name)}
                data-testid={`control-${name}`}
              >
                {CONTROL_COPY[name].button}
              </Button>
            ))}
          </div>
          <p className="text-xs text-text-secondary">
            暂停/恢复/取消都以当前 Task revision {revision} 提交；可用性以 allowedActions 为准。暂停只停止新的调度，不会立即停止运行中的 Worker。
          </p>
        </>
      ) : null}

      {pendingAction !== null ? (
        <ControlAttempt
          taskId={task.id}
          task={task}
          action={pendingAction}
          revision={revision}
          transport={transport}
          onClose={() => setPendingAction(null)}
          onChanged={onChanged}
          onConfirmed={operation => {
            setConfirmedOperations(previous => [...previous.filter(item => item.id !== operation.id), operation].slice(-20));
            setPendingAction(null);
          }}
        />
      ) : null}
      {confirmedOperations.length ? <section aria-label="已核对的任务控制回执" className="space-y-2">
        <p className="text-xs text-text-secondary">已核对操作结果；可按任务当前状态选择下一步。保留本页最近 20 条控制回执，操作成功不代表任务交付成功。</p>
        {confirmedOperations.map(operation => <OperationReceipt key={operation.id} result={operation} taskId={task.id} kind={operation.kind} transport={transport} />)}
      </section> : null}
    </Card>
  );
}

interface ControlAttemptProps {
  taskId: string;
  task: TaskRecord;
  action: ControlAction;
  revision: Revision;
  transport: Transport;
  onClose: () => void;
  onChanged: () => void;
  onConfirmed: (operation: OperationRecord) => void;
}

function ControlAttempt({taskId, task, action, revision, transport, onClose, onChanged, onConfirmed}: ControlAttemptProps) {
  const queryClient = useQueryClient();
  // 二次确认冻结所见 CAS；轮询不得替换用户正在确认的版本。
  const [confirmedRevision] = useState(revision);
  const logical = useLogicalAction([taskId, `task.${action}`, confirmedRevision], [taskId, `task.${action}`]);
  const run = (key: string): Promise<unknown> => {
    const body = {expectedRevision: confirmedRevision, idempotencyKey: key};
    if (action === 'pause') return transport.pauseTask(taskId, body);
    if (action === 'resume') return transport.resumeTask(taskId, body);
    return transport.cancelTask(taskId, body);
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
      <ControlPhase phase={logical.phase} depsStale={logical.depsStale || revision !== confirmedRevision} action={action}
        onReplay={() => void logical.replay(run)}
        onRefresh={() => { void queryClient.invalidateQueries({queryKey: taskKeys.all(taskId)}); onChanged(); }}
        onClose={onClose} />
      {logical.phase.kind === 'accepted' ? <OperationReceipt result={logical.phase.result} taskId={taskId} kind={`task.${action}`} transport={transport}
        onConfirmed={operation => {
          if (operation.taskRevision <= confirmedRevision || operation.taskRevision > task.revision) return;
          if (operation.status === 'succeeded' && !isTerminalStatus(task.status) &&
              (action === 'pause' ? task.status !== 'paused' : action === 'resume' ? task.status === 'paused' : true)) return;
          onConfirmed(operation);
        }} /> : null}
    </>
  );
}

function ControlPhase({phase, depsStale, action, onReplay, onRefresh, onClose}: {phase: ActionPhase; depsStale: boolean; action: ControlAction; onReplay: () => void; onRefresh: () => void; onClose: () => void}) {
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
        </div>
        <p className="mt-1 text-xs text-text-secondary">正在核对原操作回执与任务状态；确认结果前暂不开放其他控制操作。无需关闭提示，核对完成后自动恢复可用操作。</p>
      </div>
    );
  }
  if (phase.kind === 'rejected') {
    return (
      <div className="mt-2">
        {depsStale ? <StaleNote /> : null}
        <ErrorNotice error={phase.error} title="控制操作失败" onRefresh={onRefresh} />
        <Button size="sm" variant="outline" onClick={onClose}>已核对结果，关闭并重新选择操作</Button>
      </div>
    );
  }
  if (phase.kind === 'unknown') {
    return (
      <div className="mt-2">
        {depsStale ? <StaleNote /> : null}
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

/** 轮询推进 revision 时的未决动作保护提示（UI-02）：原键与状态保留，先核对再决定。 */
function StaleNote() {
  return (
    <p className="mb-1 text-xs leading-[18px] text-text-secondary" data-testid="control-deps-stale">
      检测到任务已推进到新版本（轮询 revision 已变化）。本次提交的键与状态保持不变；
      请先刷新核对任务状态，再决定原键重放或重新发起。
    </p>
  );
}
