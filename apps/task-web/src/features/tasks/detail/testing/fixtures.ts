// W2 测试夹具：构造冻结 DTO 形状的对象与假 Transport。仅用于组件测试，不作为真实服务验收。

import type {
  Delivery, LeaderRecord, PendingQuestion, PlanRecord, PublicationRecord, TaskDetail, Transport, WorkerRecord,
} from '@/lib/transport/types';

export const TASK_ID = 'task-test-0001';

export function makeDetail(overrides: Partial<TaskDetail> = {}): TaskDetail {
  return {
    id: TASK_ID,
    intent: '按窗口汇总两个地区的销售清单',
    status: 'running',
    contextRefs: [],
    createdAt: '2026-09-10T01:00:00.000Z',
    deadlineAt: null,
    statusAt: '2026-09-10T01:02:00.000Z',
    attempts: 1,
    retryCount: 0,
    reworkCount: 0,
    allowedActions: ['cancel', 'pause'],
    elapsedMs: 120000,
    usage: null,
    failureCode: null,
    plan: null,
    latestDelivery: null,
    latestReview: null,
    acceptance: null,
    pendingQuestions: [],
    ...overrides,
  };
}

export function makePlan(overrides: Partial<PlanRecord> = {}): PlanRecord {
  return {
    revision: '3',
    taskId: TASK_ID,
    nodes: [
      {nodeId: 'east', role: 'author', description: '东侧汇总'},
      {nodeId: 'verify', role: 'verifier', description: '独立校验'},
    ],
    edges: [{from: 'east', to: 'verify'}],
    frozenAt: '2026-09-10T01:01:00.000Z',
    expiresAt: null,
    budgetMs: 900000,
    deadlineAt: null,
    planDigest: 'sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890',
    decisionDigest: 'sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed',
    acceptedAt: null,
    rejectedAt: null,
    ...overrides,
  };
}

export function makeQuestion(overrides: Partial<PendingQuestion> = {}): PendingQuestion {
  return {
    questionId: 'q-0001',
    text: '输出需要使用哪种语言？',
    options: [{value: 'zh', label: '中文'}, {value: 'en', label: '英文'}],
    deadlineAt: null,
    ...overrides,
  };
}

export function makeWorker(overrides: Partial<WorkerRecord> = {}): WorkerRecord {
  return {
    id: 'worker-0001',
    taskId: TASK_ID,
    nodeId: 'east',
    role: 'author',
    status: 'running',
    phase: 'working',
    providerId: 'pi',
    progress: {source: 'agent', summary: 'agent.running', tool: 'read:completed'},
    startedAt: '2026-09-10T01:00:20.000Z',
    finishedAt: null,
    lastObservedAt: '2026-09-10T01:03:00.000Z',
    attempts: 1,
    usage: null,
    audit: null,
    ...overrides,
  };
}

export function makeDelivery(files: Delivery['files'] = []): Delivery {
  return {
    digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    bytes: 1024,
    isLocalRecipient: true,
    files,
  };
}

export function makeLeader(extra: Record<string, unknown> = {}): LeaderRecord {
  return {
    status: 'active',
    attempts: 1,
    workers: ['worker-0001'],
    requestedVersion: null,
    deadlineAt: null,
    decisionDigest: null,
    ...extra,
  } as LeaderRecord;
}

export function makePublication(overrides: Partial<PublicationRecord> = {}): PublicationRecord {
  return {
    id: 'pub-0001',
    taskId: TASK_ID,
    status: 'created',
    authorizationDigest: 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
    snapshotDigest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    configurationDigest: 'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
    expected: null,
    createdAt: '2026-09-10T00:40:00.000Z',
    updatedAt: '2026-09-10T00:40:00.000Z',
    ...overrides,
  };
}

export type TransportCalls = Array<{method: string; args: unknown[]}>;

/** 全方法假 Transport：默认各 query 返回空；overrides 注入行为；所有实现（含 overrides）都记录调用。 */
export function makeFakeTransport(overrides: Partial<Transport> = {}): {transport: Transport; calls: TransportCalls} {
  const calls: TransportCalls = [];
  const impls: Transport = {
    listTasks: async () => ({tasks: [], nextCursor: null}),
    getTask: async () => makeDetail(),
    getWorkers: async () => ({workers: []}),
    getPlan: async () => makePlan(),
    freezePlan: async () => ({}),
    approveTask: async () => ({}),
    answerTask: async () => ({}),
    cancelTask: async () => ({}),
    pauseTask: async () => ({}),
    resumeTask: async () => ({}),
    cancelWorker: async () => ({}),
    getLeader: async () => makeLeader(),
    leaderReply: async () => ({}),
    getPublications: async () => ({publications: []}),
    getArtifactBearer: async () => new Blob([]),
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
