// W2 测试夹具：构造冻结合同形状（packages/task-api/openapi.json）的对象与全方法假 Transport。
// 默认值复用 @/lib/transport/samples 减少漂移；所有 builder 均类型化并支持逐项覆盖。
// 仅用于组件测试，不作为真实服务验收。

import type {
  ArtifactRecord, LeaderRecord, PlanRecord, Question, QuestionsResponse, RunningQuestion, TaskAuditRecord, TaskRecord, Transport,
  WorkerRecord,
} from '@/lib/transport/types';
import {
  sampleArtifacts, sampleLeader, samplePlan, samplePreapprovalQuestion, sampleQuestions, sampleRunningQuestion,
  sampleTaskList, sampleWorkers,
} from '@/lib/transport/samples';

export const TASK_ID = 'task-test-0001';
export const WORKER_ID = 'worker-test-0001';
export const ARTIFACT_ID = 'artifact-test-0001';

export function makeTask(overrides: Partial<TaskRecord> = {}): TaskRecord {
  return {
    ...sampleTaskList[0]!,
    id: TASK_ID,
    ...overrides,
  };
}

/** 旧名保留：现构造合同 TaskRecord（旧 TaskDetail 聚合已删除，GET /v1/tasks/{taskId} 直接返回 Task）。 */
export const makeDetail = makeTask;

export function makePlan(overrides: Partial<PlanRecord> = {}): PlanRecord {
  return {...samplePlan, taskId: TASK_ID, ...overrides};
}

export function makeQuestion(overrides: Partial<Question> = {}): Question {
  return {...samplePreapprovalQuestion, taskId: TASK_ID, ...overrides};
}

export function makeRunningQuestion(overrides: Partial<RunningQuestion> = {}): RunningQuestion {
  return {...sampleRunningQuestion, taskId: TASK_ID, workerId: WORKER_ID, ...overrides};
}

export function makeQuestions(overrides: Partial<QuestionsResponse> = {}): QuestionsResponse {
  return {...sampleQuestions, taskId: TASK_ID, items: [], ...overrides};
}

export function makeWorker(overrides: Partial<WorkerRecord> = {}): WorkerRecord {
  return {...sampleWorkers[0]!, id: WORKER_ID, taskId: TASK_ID, ...overrides};
}

export function makeLeader(overrides: Partial<LeaderRecord> = {}): LeaderRecord {
  return {...sampleLeader, taskId: TASK_ID, ...overrides};
}

export function makeAudit(overrides: Partial<TaskAuditRecord> = {}): TaskAuditRecord {
  return {
    taskId: TASK_ID,
    acceptance: {status: 'pending', evidenceIds: [], digest: null},
    ...overrides,
  };
}

export function makeArtifact(overrides: Partial<ArtifactRecord> = {}): ArtifactRecord {
  return {...sampleArtifacts[0]!, id: ARTIFACT_ID, taskId: TASK_ID, ...overrides};
}

export type TransportCalls = Array<{method: string; args: unknown[]}>;

/**
 * 全方法假 Transport（覆盖合同全部访问面，含 createInput）：默认各读操作返回空投影；
 * overrides 注入行为；所有实现（含 overrides）都记录调用，供断言精确参数。
 */
export function makeFakeTransport(overrides: Partial<Transport> = {}): {transport: Transport; calls: TransportCalls} {
  const calls: TransportCalls = [];
  const impls: Transport = {
    createTask: async () => makeTask(),
    createInput: async () => makeArtifact(),
    listTasks: async () => ({items: [], nextCursor: null}),
    getTask: async () => makeTask(),
    getWorkers: async taskId => ({items: [], nextCursor: null, taskId}),
    getPlan: async () => makePlan(),
    getGraph: async taskId => ({taskId, planRevision: 1, nodes: [], edges: []}),
    approvePlan: async () => ({}),
    getQuestions: async taskId => ({...makeQuestions(), taskId}),
    answerTask: async () => ({}),
    cancelTask: async () => ({}),
    pauseTask: async () => ({}),
    resumeTask: async () => ({}),
    cancelWorker: async () => ({}),
    getLeader: async () => makeLeader(),
    getAudit: async () => makeAudit(),
    leaderReply: async () => ({}),
    repair: async () => ({}),
    getEvents: async taskId => ({items: [], nextCursor: null, taskId}),
    getArtifact: async artifactId => makeArtifact({id: artifactId}),
    getArtifactContent: async () => new Blob([]),
    ...overrides,
  };
  const transport = Object.fromEntries(
    Object.entries(impls).map(([method, impl]) => [
      method,
      (...args: unknown[]) => {
        calls.push({method, args});
        return (impl as (...inner: unknown[]) => unknown)(...args);
      },
    ]),
  ) as unknown as Transport;
  return {transport, calls};
}

export function callsOf(calls: TransportCalls, method: string): TransportCalls {
  return calls.filter(call => call.method === method);
}
