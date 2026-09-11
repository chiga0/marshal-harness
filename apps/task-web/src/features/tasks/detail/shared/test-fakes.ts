// 详情域测试夹具与假 Transport：对象形状逐一对齐冻结 Transport 合同（packages/task-api/openapi.json）。
// 仅用于组件测试，不作为真实服务验收。假 Transport 记录每次调用（方法名+参数），供断言绑定关系。

import type {
  ArtifactRecord,
  LeaderAuthorization,
  LeaderRecord,
  LeaderRequestDTO,
  PlanRecord,
  Question,
  QuestionsResponse,
  Revision,
  RunningQuestion,
  TaskRecord,
  TaskStatus,
  Transport,
  WorkerRecord,
} from '@/lib/transport/types';

export const TASK_ID = 'task-test-0001';
export const PREVIEW_DIGEST = 'sha256:7777777777777777777777777777777777777777777777777777777777777777';
export const PLAN_DIGEST = 'sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890';
export const REQUEST_DIGEST = 'sha256:0c57023789abb02551ac7837838ae3cb9dcd86ff0f3c58b4db372999eab51a7c';
export const QUESTION_DIGEST = 'sha256:6666666666666666666666666666666666666666666666666666666666666666';

// 到期字段必须始终是「未来」（绝对字面量会随墙钟过期，曾让当日 CI 全红）；模块加载即冻结，测试内一致。
const FUTURE = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString();

export function makeTask(overrides: Partial<TaskRecord> & {status?: TaskStatus; revision?: Revision} = {}): TaskRecord {
  return {
    id: TASK_ID,
    revision: 7,
    status: 'running',
    phase: 'execution',
    intent: '按窗口汇总两个地区的销售清单',
    createdAt: '2026-09-10T01:00:00.000Z',
    updatedAt: '2026-09-10T01:02:00.000Z',
    allowedActions: ['cancel', 'pause'],
    plan: null,
    artifactIds: [],
    deadlineAt: null,
    ...overrides,
  };
}

export function makePlan(overrides: Partial<PlanRecord> = {}): PlanRecord {
  return {
    taskId: TASK_ID,
    revision: 3,
    digest: PLAN_DIGEST,
    summary: '双地区作者 + 独立校验的三节点计划',
    nodes: [
      {id: 'east', role: 'author', goal: '报告东侧已付款流水', scope: ['东地区账本'], providerId: null},
      {id: 'west', role: 'author', goal: '报告西侧已付款流水', scope: ['西地区账本'], providerId: null},
      {id: 'verify', role: 'verifier', goal: '两地区独立校验', scope: ['双地区清单'], providerId: null},
    ],
    edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
    budget: {timeoutMs: 900000, maxAttempts: 3, maxWorkers: 3},
    deliverables: ['双地区对账报告'],
    acceptance: ['覆盖两地区全部流水'],
    assumptions: [],
    ...overrides,
  };
}

export function makePreapprovalQuestion(overrides: Partial<Question> = {}): Question {
  return {
    id: 'q-0001',
    taskId: TASK_ID,
    nodeId: null,
    revision: 1,
    subject: '输出语言',
    kind: 'clarification',
    prompt: '输出需要使用哪种语言？',
    options: [{value: 'zh', label: '中文'}, {value: 'en', label: '英文'}],
    deadlineAt: FUTURE,
    status: 'open',
    ...overrides,
  };
}

export function makeRunningQuestion(overrides: Partial<RunningQuestion> = {}): RunningQuestion {
  return {
    id: 'q-0002',
    taskId: TASK_ID,
    workerId: 'worker-0001',
    nodeId: 'east',
    revision: 1,
    kind: 'business',
    subject: QUESTION_DIGEST,
    questionDigest: QUESTION_DIGEST,
    prompt: '请填写本次已批准分析的起始日期',
    options: [],
    answer: null,
    deadlineAt: FUTURE,
    status: 'open',
    deliveryStatus: null,
    ...overrides,
  };
}

export function makeQuestions(overrides: Partial<QuestionsResponse> = {}): QuestionsResponse {
  return {
    taskRevision: 7,
    previewRevision: 1,
    previewDigest: PREVIEW_DIGEST,
    confirmBefore: FUTURE,
    preview: null,
    items: [],
    nextCursor: null,
    taskId: TASK_ID,
    ...overrides,
  };
}

export function makeWorker(overrides: Partial<WorkerRecord> = {}): WorkerRecord {
  return {
    id: 'worker-0001',
    taskId: TASK_ID,
    nodeId: 'east',
    providerId: 'pi',
    role: 'author',
    status: 'running',
    phase: 'development',
    attempt: 1,
    startedAt: '2026-09-10T01:00:20.000Z',
    finishedAt: null,
    lastObservedAt: '2026-09-10T01:03:00.000Z',
    progress: {summary: 'agent.running', tool: 'read:completed', source: 'agent'},
    usage: {tokens: null, cost: null, currency: null, source: 'unavailable', coverage: 0},
    audit: {repairId: null, elapsedMs: 160000, elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'},
    ...overrides,
  };
}

export function makeLeaderRequest(overrides: Partial<LeaderRequestDTO> = {}): LeaderRequestDTO {
  return {
    id: 'req-biz-1',
    kind: 'business',
    requestDigest: REQUEST_DIGEST,
    subject: REQUEST_DIGEST,
    nodeIds: [],
    prompt: '报告采用哪个地区的数据？',
    options: [{value: 'north', label: '北区'}, {value: 'south', label: '南区'}],
    authorization: null,
    deadlineAt: FUTURE,
    status: 'pending',
    replyDigest: null,
    ...overrides,
  };
}

export function makeAuthorization(overrides: Partial<LeaderAuthorization> = {}): LeaderAuthorization {
  return {
    taskId: TASK_ID,
    planDigest: 'sha256:2222222222222222222222222222222222222222222222222222222222222222',
    artifactId: 'artifact-1',
    artifactDigest: 'sha256:1111111111111111111111111111111111111111111111111111111111111111',
    bytes: 18,
    acceptanceDigest: 'sha256:3333333333333333333333333333333333333333333333333333333333333333',
    reviewDigest: 'sha256:4444444444444444444444444444444444444444444444444444444444444444',
    targetId: 'local-reports',
    targetPolicyDigest: 'sha256:5555555555555555555555555555555555555555555555555555555555555555',
    name: 'task-1-aaaa.json',
    operation: 'create-if-absent',
    expiresAt: FUTURE,
    ...overrides,
  };
}

export function makeLeader(overrides: Partial<LeaderRecord> = {}): LeaderRecord {
  return {
    taskId: TASK_ID,
    taskRevision: 7,
    profile: 'task-managed-leader/v1',
    stage: 'work',
    policyDigest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    activeWorkerId: null,
    pendingRequest: null,
    lastDecision: null,
    review: null,
    publication: null,
    postverify: null,
    summaryArtifactId: null,
    ...overrides,
  };
}

export function makeArtifact(overrides: Partial<ArtifactRecord> = {}): ArtifactRecord {
  return {
    id: 'artifact-1',
    taskId: TASK_ID,
    name: 'east-ledger.csv',
    kind: 'input',
    status: 'ready',
    mediaType: 'text/csv',
    bytes: 1024,
    digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    createdAt: '2026-09-10T01:05:00.000Z',
    ...overrides,
  };
}

export type TransportCalls = Array<{method: string; args: unknown[]}>;

/** 全方法假 Transport：默认各 query 返回空；overrides 注入行为；所有实现（含 overrides）都记录调用。 */
export function makeFakeTransport(overrides: Partial<Transport> = {}): {transport: Transport; calls: TransportCalls} {
  const calls: TransportCalls = [];
  const impls: Transport = {
    createTask: async () => makeTask(),
    createInput: async () => makeArtifact(),
    listTasks: async () => ({items: [], nextCursor: null}),
    getTask: async () => makeTask(),
    getWorkers: async () => ({items: [], nextCursor: null, taskId: TASK_ID}),
    getPlan: async () => makePlan(),
    getGraph: async taskId => ({taskId, planRevision: 1, nodes: [], edges: []}),
    approvePlan: async () => ({}),
    getQuestions: async () => makeQuestions(),
    answerTask: async () => ({}),
    cancelTask: async () => ({}),
    pauseTask: async () => ({}),
    resumeTask: async () => ({}),
    cancelWorker: async () => ({}),
    getLeader: async () => makeLeader(),
    getAudit: async () => ({taskId: TASK_ID, acceptance: {status: 'pending', evidenceIds: [], digest: null}}),
    leaderReply: async () => ({}),
    repair: async () => ({}),
    getEvents: async () => ({items: [], nextCursor: null, taskId: TASK_ID}),
    getArtifact: async () => makeArtifact(),
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
