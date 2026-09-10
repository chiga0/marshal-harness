// 合同样例：仅用于开发与组件测试，不作为真实服务验收（见 docs/ui-1/acceptance.md）。
import type {
  PlanRecord, TaskRecord, WorkerRecord, LeaderRecord, ArtifactRecord, Question, RunningQuestion,
  QuestionsResponse,
} from './types';

const DIGEST = 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';

export const sampleTaskList: TaskRecord[] = [
  {
    id: 'task-11111111-1111-4111-8111-111111111111',
    revision: 1,
    status: 'running',
    phase: 'execution',
    intent: '按窗口汇总东、西两个地区的销售清单',
    createdAt: '2026-09-10T01:00:00.000Z',
    updatedAt: '2026-09-10T01:02:00.000Z',
    allowedActions: ['cancel', 'pause'],
    plan: null,
    artifactIds: [],
    deadlineAt: '2026-09-11T00:00:00.000Z',
  },
  {
    id: 'task-22222222-2222-4222-8222-222222222222',
    revision: 1,
    status: 'awaiting-confirmation',
    phase: 'planning',
    intent: '生成两个区域的对账报告并独立复核',
    createdAt: '2026-09-10T00:30:00.000Z',
    updatedAt: '2026-09-10T00:34:00.000Z',
    allowedActions: ['approve', 'cancel'],
    plan: null,
    artifactIds: [],
    deadlineAt: '2026-09-10T10:30:00.000Z',
  },
];

const usageUnavailable = {tokens: null, cost: null, currency: null, source: 'unavailable' as const, coverage: 0};

export const sampleWorkers: WorkerRecord[] = [
  {
    id: 'worker-e1a2b3c4-0000-4000-9000-000000000001',
    taskId: sampleTaskList[0]!.id,
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
    usage: usageUnavailable,
    audit: {repairId: null, elapsedMs: 160000, elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'},
  },
  {
    id: 'worker-e1a2b3c4-0000-4000-9000-000000000002',
    taskId: sampleTaskList[0]!.id,
    nodeId: 'west',
    providerId: 'pi',
    role: 'author',
    status: 'running',
    phase: 'development',
    attempt: 1,
    startedAt: '2026-09-10T01:00:21.000Z',
    finishedAt: null,
    lastObservedAt: '2026-09-10T01:02:40.000Z',
    progress: {summary: 'agent.running', tool: 'edit:completed', source: 'agent'},
    usage: usageUnavailable,
    audit: {repairId: null, elapsedMs: 159000, elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'},
  },
];

export const samplePlan: PlanRecord = {
  taskId: sampleTaskList[0]!.id,
  revision: 1,
  digest: 'sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890',
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
};

export const samplePreapprovalQuestion: Question = {
  id: 'question-aaaa0000-0000-4000-8000-000000000001',
  taskId: sampleTaskList[1]!.id,
  nodeId: null,
  revision: 1,
  subject: '输出语言',
  kind: 'clarification',
  prompt: '输出需要使用哪种语言？',
  options: [{value: 'zh', label: '中文'}, {value: 'en', label: 'English'}],
  deadlineAt: '2026-09-11T00:00:00.000Z',
  status: 'open',
};

export const sampleRunningQuestion: RunningQuestion = {
  id: 'question-bbbb0000-0000-4000-8000-000000000002',
  taskId: sampleTaskList[0]!.id,
  workerId: sampleWorkers[0]!.id,
  nodeId: 'east',
  revision: 1,
  kind: 'business',
  subject: DIGEST,
  questionDigest: DIGEST,
  prompt: '请填写本次已批准分析的起始日期',
  options: [],
  answer: null,
  deadlineAt: '2026-09-11T00:00:00.000Z',
  status: 'open',
  deliveryStatus: null,
};

export const sampleQuestions: QuestionsResponse = {
  taskRevision: 1,
  previewRevision: null,
  previewDigest: null,
  confirmBefore: '2026-09-11T00:00:00.000Z',
  preview: null,
  items: [samplePreapprovalQuestion],
  nextCursor: null,
  taskId: sampleTaskList[1]!.id,
};

export const sampleLeader: LeaderRecord = {
  taskId: sampleTaskList[0]!.id,
  taskRevision: 1,
  profile: 'task-managed-leader/v1',
  stage: 'work',
  policyDigest: DIGEST,
  activeWorkerId: null,
  pendingRequest: null,
  lastDecision: null,
  review: null,
  publication: null,
  postverify: null,
  summaryArtifactId: null,
};

export const sampleArtifacts: ArtifactRecord[] = [
  {
    id: 'artifact-cccc0000-0000-4000-8000-000000000001',
    taskId: sampleTaskList[0]!.id,
    name: 'east-ledger.csv',
    kind: 'candidate',
    status: 'ready',
    mediaType: 'text/csv',
    bytes: 1024,
    digest: DIGEST,
    createdAt: '2026-09-10T01:05:00.000Z',
  },
];
