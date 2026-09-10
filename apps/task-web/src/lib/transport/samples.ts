// 合同样例：仅用于开发与组件测试，不作为真实服务验收（见 docs/ui-1/acceptance.md）。
import type {
  PlanRecord, TaskCursorTask, TaskDetail, WorkerRecord, LeaderRecord, PublicationRecord,
} from './types';

export const sampleTaskList: TaskCursorTask[] = [
  {
    id: 'task-11111111-1111-4111-8111-111111111111',
    intent: '按窗口汇总东、西两个地区的销售清单',
    status: 'running',
    contextRefs: [],
    createdAt: '2026-09-10T01:00:00.000Z',
    deadlineAt: '2026-09-11T00:00:00.000Z',
    statusAt: '2026-09-10T01:02:00.000Z',
    attempts: 3,
    retryCount: 0,
    reworkCount: 0,
    allowedActions: ['cancel', 'pause'],
    elapsedMs: 120000,
    usage: null,
    failureCode: null,
    revision: 3,
    updatedAt: '2026-09-10T01:02:00.000Z',
  },
  {
    id: 'task-22222222-2222-4222-8222-222222222222',
    intent: '生成两个区域的对账报告并独立复核',
    status: 'awaiting-confirmation',
    contextRefs: [],
    createdAt: '2026-09-10T00:30:00.000Z',
    deadlineAt: '2026-09-10T10:30:00.000Z',
    statusAt: '2026-09-10T00:34:00.000Z',
    attempts: 2,
    retryCount: 0,
    reworkCount: 0,
    allowedActions: ['approve', 'cancel'],
    elapsedMs: 240000,
    usage: null,
    failureCode: null,
    revision: 1,
    updatedAt: '2026-09-10T00:34:00.000Z',
  },
];

export const sampleTaskDetail: TaskDetail = {
  ...sampleTaskList[0]!,
  plan: null,
  latestDelivery: null,
  latestReview: null,
  acceptance: null,
  pendingQuestions: [],
};

export const sampleWorkers: WorkerRecord[] = [
  {
    id: 'worker-e1a2b3c4-0000-4000-9000-000000000001',
    taskId: sampleTaskList[0]!.id,
    nodeId: 'east',
    role: 'author',
    status: 'running',
    phase: 'working',
    providerId: 'pi',
    progress: {source: 'agent', summary: 'agent.running', tool: 'read:completed'},
    startedAt: '2026-09-10T01:00:20.000Z',
    finishedAt: null,
    lastObservedAt: '2026-09-10T01:03:00.000Z',
    attempts: 2,
    usage: null,
    audit: {repairId: null, elapsedMs: 160000, elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'},
  },
  {
    id: 'worker-e1a2b3c4-0000-4000-9000-000000000002',
    taskId: sampleTaskList[0]!.id,
    nodeId: 'west',
    role: 'author',
    status: 'running',
    phase: 'working',
    providerId: 'pi',
    progress: {source: 'agent', summary: 'agent.running', tool: 'edit:completed'},
    startedAt: '2026-09-10T01:00:21.000Z',
    finishedAt: null,
    lastObservedAt: '2026-09-10T01:02:40.000Z',
    attempts: 2,
    usage: null,
    audit: {repairId: null, elapsedMs: 159000, elapsedSource: 'started-to-settlement', waitingMs: null, waitingSource: 'unavailable'},
  },
];

export const samplePlan: PlanRecord = {
  revision: 3,
  taskId: sampleTaskList[0]!.id,
  nodes: [
    {nodeId: 'east', role: 'author', description: '报告东侧已付款流水'},
    {nodeId: 'west', role: 'author', description: '报告西侧已付款流水'},
    {nodeId: 'verify', role: 'verifier', description: '两地区独立校验'},
  ],
  edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}],
  frozenAt: '2026-09-10T01:01:00.000Z',
  expiresAt: null,
  budgetMs: 900000,
  deadlineAt: '2026-09-10T10:00:00.000Z',
  planDigest: 'sha256:b540509e80a56fc5686acb543b8c52798cec6a97f528829d268bd5ec0d269890',
  decisionDigest: 'sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed',
  acceptedAt: null,
  rejectedAt: null,
};

export const sampleLeader: LeaderRecord = {
  status: 'active',
  attempts: 2,
  workers: ['worker-east', 'worker-west'],
  requestedVersion: null,
  deadlineAt: null,
  decisionDigest: 'sha256:5a6a3f5cc352259644fd220e8a8886b8f698bc8ff20c77713df4dded0f941aed',
};

export const samplePublications: PublicationRecord[] = [
  {
    id: 'pub-11111111-aaaa-4aaa-aaaa-000000000001',
    taskId: sampleTaskList[1]!.id,
    status: 'created',
    authorizationDigest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    snapshotDigest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    configurationDigest: 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
    expected: null,
    createdAt: '2026-09-10T00:40:00.000Z',
    updatedAt: '2026-09-10T00:40:00.000Z',
  },
];
