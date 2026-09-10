// 浏览器与 Node HTTP 客户端之间唯一的 Task API 访问面。
// 字段与 packages/task-api/openapi.json 保持一致；本模块不进入浏览器静态资产产物以外的任何地方，
// 不 imoprt node:crypto/http-boundary（浏览器按 Web Crypto 或后端生产的摘要复验）。

export type Revision = string;
export type TaskId = string;
export type WorkerId = string;
export type NodeId = string;
export type Sha256 = string;

export type TaskStatus =
  | 'running'
  | 'awaiting-answer'
  | 'awaiting-confirmation'
  | 'awaiting-decision'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'unknown'
  | 'confirmed';

export type WorkerStatus =
  | 'queued'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'stopping'
  | 'unknown';

export type WorkerRole = 'author' | 'reviewer' | 'verifier' | 'leader' | 'unknown';

export type WorkerPhase = 'intake' | 'working' | 'verify' | 'review' | 'conclude' | 'terminal' | 'unknown';

export interface Usage {
  tokens: number | null;
  cost: number | null;
  currency: string | null;
  source: string;
  coverage: number;
}

export interface Progress {
  source: string;
  summary: string;
  tool: string | null;
}

export interface WorkerAudit {
  repairId: string | null;
  elapsedMs: number | null;
  elapsedSource: string;
  waitingMs: number | null;
  waitingSource: string;
}

export interface TaskRecord {
  id: TaskId;
  intent: string;
  status: TaskStatus;
  contextRefs: string[];
  createdAt: string;
  deadlineAt: string | null;
  statusAt: string;
  attempts: number;
  retryCount: number;
  reworkCount: number;
  allowedActions: string[];
  elapsedMs: number | null;
  usage: Usage | null;
  failureCode: string | null;
}

export interface TaskCursorTask extends TaskRecord {
  updatedAt: string;
}

export interface TasksResponse {
  tasks: TaskCursorTask[];
  nextCursor: string | null;
}

export interface WorkerRecord {
  id: WorkerId;
  taskId: TaskId;
  nodeId: NodeId;
  role: WorkerRole;
  status: WorkerStatus;
  phase: WorkerPhase;
  providerId: string;
  progress: Progress | null;
  startedAt: string;
  finishedAt: string | null;
  lastObservedAt: string;
  attempts: number;
  usage: Usage | null;
  audit: WorkerAudit | null;
}

export interface PlanNode {
  nodeId: NodeId;
  role: string;
  description: string;
}

export interface PlanEdge {
  from: NodeId;
  to: NodeId;
}

export interface PlanRecord {
  revision: Revision;
  taskId: TaskId;
  nodes: PlanNode[];
  edges: PlanEdge[];
  frozenAt: string;
  expiresAt: string | null;
  budgetMs: number | null;
  deadlineAt: string | null;
  planDigest: Sha256;
  decisionDigest: Sha256;
  acceptedAt: string | null;
  rejectedAt: string | null;
  digest?: Sha256;
}

export interface QuestionOption {
  value: string;
  label: string;
}

export interface PendingQuestion {
  questionId: string;
  text: string;
  options: QuestionOption[];
  deadlineAt: string | null;
}

export interface Review {
  passed: number;
  total: number;
  pending: number;
}

export interface Acceptance {
  status: 'passed' | 'failed' | 'pending' | 'unknown';
  digest: Sha256 | null;
  evidenceIds: string[];
}

export interface DeliveryFile {
  path: string;
  digest: Sha256;
  bytes: number;
}

export interface Delivery {
  digest: Sha256;
  bytes: number;
  isLocalRecipient: boolean;
  files?: DeliveryFile[];
}

export interface TaskDetail extends TaskRecord {
  plan?: PlanRecord | null;
  latestDelivery?: Delivery | null;
  latestReview?: Review | null;
  acceptance?: Acceptance | null;
  pendingQuestions?: PendingQuestion[];
}

export interface LeaderReply {
  requestId: string;
  accepted: boolean;
  answeredAt: string;
}

export interface LeaderRecord {
  status: string;
  attempts: number;
  workers: string[];
  requestedVersion: Revision | null;
  deadlineAt: string | null;
  decisionDigest: Sha256 | null;
}

export interface SelectionEntry {
  nodeId: NodeId;
  path: string;
  digest: Sha256;
  bytes: number;
}

export interface Selection {
  entries: SelectionEntry[];
  selectionDigest: Sha256;
}

export interface RepairReceipt {
  repairId: string;
  status: string;
  nodeIds: NodeId[];
  startedAt: string;
}

export interface PublicationExpected {
  snapshotDigest: Sha256;
  configurationDigest: Sha256;
}

export type PublicationStatus =
  | 'created'
  | 'publishing'
  | 'published'
  | 'postverify-passed'
  | 'postverify-failed'
  | 'unknown';

export interface PublicationRecord {
  id: string;
  taskId: TaskId;
  status: PublicationStatus;
  authorizationDigest: Sha256;
  snapshotDigest: Sha256;
  configurationDigest: Sha256;
  expected: PublicationExpected | null;
  createdAt: string;
  updatedAt: string;
}

export interface ModerationRecord {
  taskId: TaskId;
  pausedAt: string | null;
  pausedBy: string | null;
  resumedAt: string | null;
  cancelledAt: string | null;
}

export interface ApiErrorBody {
  code: string;
  message?: string;
  requestId?: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string | null;

  constructor(status: number, code: string, message: string, requestId: string | null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

export interface Transport {
  listTasks(options: {limit?: number; cursor?: string | null; filter?: {text?: string; status?: TaskStatus | 'any'}}): Promise<TasksResponse>;
  getTask(taskId: TaskId): Promise<TaskDetail>;
  getWorkers(taskId: TaskId): Promise<{workers: WorkerRecord[]}>;
  getPlan(taskId: TaskId): Promise<PlanRecord>;
  freezePlan(taskId: TaskId, body: {revision: Revision; decisionDigest: Sha256}): Promise<unknown>;
  approveTask(taskId: TaskId, body: {revision: Revision; decisionDigest?: Sha256}): Promise<unknown>;
  answerTask(taskId: TaskId, body: {questionId: string; revision: Revision; value: string}): Promise<unknown>;
  cancelTask(taskId: TaskId, body: {revision: Revision}): Promise<unknown>;
  pauseTask(taskId: TaskId, body: {revision: Revision}): Promise<unknown>;
  resumeTask(taskId: TaskId, body: {revision: Revision}): Promise<unknown>;
  cancelWorker(taskId: TaskId, workerId: WorkerId, body: {revision: Revision}): Promise<unknown>;
  getLeader(taskId: TaskId): Promise<LeaderRecord>;
  leaderReply(taskId: TaskId, body: {requestId: string; revision: Revision; outcome: 'accepted' | 'rejected' | 'unknown'}): Promise<unknown>;
  getPublications(taskId: TaskId): Promise<{publications: PublicationRecord[]}>;
  getArtifactBearer(digestRef: string): Promise<Blob>;
}
