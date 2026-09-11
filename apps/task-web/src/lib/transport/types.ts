// 浏览器与 Node HTTP 客户端之间唯一的 Task API 访问面。
// 字段逐一对齐 packages/task-api/openapi.json：字段名/枚举/可空性都以该合同为准，
// 不添加服务端未投影字段，也不收窄服务端必返字段。

export type Revision = number;
export type TaskId = string;
export type WorkerId = string;
export type NodeId = string;
export type ArtifactId = string;
export type Sha256 = string;

// ---- Task（GET /v1/tasks、/v1/tasks/{taskId}）----

export type TaskStatus =
  | 'draft'
  | 'planning'
  | 'awaiting-answer'
  | 'awaiting-confirmation'
  | 'awaiting-approval'
  | 'queued'
  | 'running'
  | 'paused'
  | 'cancelling'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'intervention';

export type TaskPhase = 'intake' | 'planning' | 'execution' | 'verification' | 'delivery' | 'terminal';

export type TaskAllowedAction = 'approve' | 'answer' | 'cancel' | 'pause' | 'resume' | 'repair';

export interface PlanRef {
  revision: Revision;
  digest: Sha256;
}

export interface TaskRecord {
  id: TaskId;
  revision: Revision;
  status: TaskStatus;
  phase: TaskPhase;
  intent: string;
  createdAt: string;
  updatedAt: string;
  allowedActions: TaskAllowedAction[];
  plan: PlanRef | null;
  artifactIds: ArtifactId[];
  /** 失败时的 machine failure code；成功任务可能缺省。 */
  code?: string;
  deadlineAt?: string | null;
}

export interface TasksResponse {
  items: TaskRecord[];
  nextCursor: string | null;
}

// ---- Worker（GET /v1/tasks/{taskId}/workers、/v1/workers/{workerId}）----

export type WorkerRole = 'planner' | 'author' | 'reviewer' | 'integrator' | 'verifier';

export type WorkerStatus =
  | 'queued'
  | 'running'
  | 'awaiting-answer'
  | 'stopping'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'unknown';

export type WorkerPhase = 'planning' | 'development' | 'review' | 'verification' | 'integration' | 'terminal';

export interface Usage {
  tokens: number | null;
  cost: number | null;
  currency: string | null;
  source: 'reported' | 'estimated' | 'unavailable';
  coverage: number;
}

export interface Progress {
  summary: string;
  tool: string | null;
  source: 'agent' | 'execution' | 'supervisor';
}

export interface WorkerAudit {
  repairId: string | null;
  elapsedMs: number | null;
  elapsedSource: 'started-to-settlement';
  waitingMs: null;
  waitingSource: 'unavailable';
}

export interface WorkerRecord {
  id: WorkerId;
  taskId: TaskId;
  nodeId: NodeId;
  providerId: string;
  role: WorkerRole;
  status: WorkerStatus;
  phase: WorkerPhase;
  attempt: number;
  startedAt: string | null;
  finishedAt: string | null;
  lastObservedAt: string | null;
  progress: Progress | null;
  usage: Usage;
  audit?: WorkerAudit;
}

export interface WorkersResponse {
  items: WorkerRecord[];
  nextCursor: string | null;
  taskId: TaskId;
}

// ---- Plan（GET /v1/tasks/{taskId}/plan；POST /v1/tasks/{taskId}/plan/approve）----

export type PlanNodeRole = 'planner' | 'author' | 'reviewer' | 'integrator' | 'verifier';

export interface PlanNode {
  id: NodeId;
  role: PlanNodeRole;
  goal: string;
  scope: string[];
  providerId: string | null;
}

export interface PlanEdge {
  from: NodeId;
  to: NodeId;
}

// GET /v1/tasks/{taskId}/graph：执行投影，不以 Plan 或 Worker 状态推算。
export type GraphNodeStatus = 'pending' | 'ready' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled' | 'unknown';
export interface GraphNode {
  id: NodeId;
  role: PlanNodeRole;
  status: GraphNodeStatus;
  workerIds: WorkerId[];
}
export interface GraphRecord {
  taskId: TaskId;
  planRevision: Revision;
  nodes: GraphNode[];
  edges: PlanEdge[];
}

/** 当前 Graph 合同的闭集校验，拒绝坏投影而非补造节点或状态。 */
export function parseGraph(value: unknown, taskId: string): GraphRecord {
  const closed = (v: unknown, keys: string[]): v is Record<string, unknown> =>
    typeof v === 'object' && v !== null && !Array.isArray(v) &&
    Object.keys(v).length === keys.length && keys.every(key => Object.hasOwn(v, key));
  const id = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(v);
  const fail = (): never => { throw new ApiError(502, 'invalid_graph_response', '任务图响应不符合合同，未展示不可信投影', null); };
  if (!closed(value, ['taskId', 'planRevision', 'nodes', 'edges']) || !id(value.taskId) || value.taskId !== taskId ||
    !Number.isSafeInteger(value.planRevision) || (value.planRevision as number) < 1 ||
    !Array.isArray(value.nodes) || value.nodes.length > 64 || !Array.isArray(value.edges) || value.edges.length > 256) return fail();
  const ids = new Set<string>();
  for (const node of value.nodes) {
    if (!closed(node, ['id', 'role', 'status', 'workerIds']) || !id(node.id) || ids.has(node.id) ||
      typeof node.role !== 'string' || !['planner', 'author', 'reviewer', 'integrator', 'verifier'].includes(node.role) ||
      typeof node.status !== 'string' || !['pending', 'ready', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'unknown'].includes(node.status) ||
      !Array.isArray(node.workerIds) || node.workerIds.length > 100 || !node.workerIds.every(id) ||
      new Set(node.workerIds).size !== node.workerIds.length) return fail();
    ids.add(node.id);
  }
  const incoming = new Map([...ids].map(key => [key, 0]));
  const outgoing = new Map([...ids].map(key => [key, new Set<string>()]));
  for (const edge of value.edges) {
    if (!closed(edge, ['from', 'to']) || !id(edge.from) || !id(edge.to) || !ids.has(edge.from) || !ids.has(edge.to) ||
      edge.from === edge.to || outgoing.get(edge.from)!.has(edge.to)) return fail();
    outgoing.get(edge.from)!.add(edge.to);
    incoming.set(edge.to, incoming.get(edge.to)! + 1);
  }
  const ready = [...ids].filter(key => incoming.get(key) === 0);
  for (let i = 0; i < ready.length; i++) for (const next of outgoing.get(ready[i]!)!) {
    incoming.set(next, incoming.get(next)! - 1);
    if (incoming.get(next) === 0) ready.push(next);
  }
  if (ready.length !== ids.size) return fail();
  return value as unknown as GraphRecord;
}

export interface Limits {
  timeoutMs: number;
  maxAttempts: number;
  maxWorkers: number;
}

export interface PlanInteraction {
  profile: 'task-runtime-question/v1';
  policyDigest: Sha256;
  maxQuestions: number;
  maxWaitMs: number;
}

export interface PlanRepairPolicy {
  profile: 'task-local-repair/v1';
  policyDigest: Sha256;
}

export interface PlanRecord {
  interaction?: PlanInteraction;
  repair?: PlanRepairPolicy;
  taskId: TaskId;
  revision: Revision;
  digest: Sha256;
  summary: string;
  nodes: PlanNode[];
  edges: PlanEdge[];
  budget: Limits;
  deliverables: string[];
  acceptance: string[];
  assumptions: string[];
}

export interface PlanApproveBody {
  expectedRevision: Revision;
  planRevision: Revision;
  planDigest: Sha256;
  idempotencyKey: string;
}

// ---- 控制端点（POST cancel/pause/resume、POST /v1/workers/{workerId}/cancel；body = ControlTask）----

export interface ControlBody {
  expectedRevision: Revision;
  idempotencyKey: string;
}

/** 现有 Operation 回执；其成功不等于 Task 交付成功或 Worker ACK。 */
export interface OperationRecord {
  id: string;
  taskId: TaskId;
  workerId?: WorkerId;
  kind: 'task.approve' | 'task.cancel' | 'task.pause' | 'task.resume' | 'task.answer' | 'task.repair' | 'worker.cancel';
  status: 'accepted' | 'running' | 'succeeded' | 'failed' | 'unknown';
  taskRevision: Revision;
  createdAt: string;
  updatedAt: string;
  code?: string;
}

export function parseOperation(raw: unknown): OperationRecord {
  const op = raw as Partial<OperationRecord> | null;
  // 与 task-api/contract.mjs 的 Id、Revision、date-time 和 code 规则相同；
  // 不用 Date.parse 单独接受非合同日期，也不默许额外字段。
  const required = ['id', 'taskId', 'kind', 'status', 'taskRevision', 'createdAt', 'updatedAt'];
  const allowed = [...required, 'workerId', 'code'];
  const id = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/u.test(value) && [...value].length <= 128;
  const dateTime = (value: unknown): boolean => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Number.isFinite(Date.parse(value));
  const code = (value: unknown): boolean => typeof value === 'string' && [...value].length >= 1 && [...value].length <= 128 &&
    new TextEncoder().encode(value).byteLength <= 128 && /^[^\u0000]*[^\s\u0000][^\u0000]*$/u.test(value) &&
    [...value].every(character => { const point = character.codePointAt(0)!; return point < 0xd800 || point > 0xdfff; });
  if (!op || typeof op !== 'object' || Array.isArray(op) || !required.every(key => Object.hasOwn(op, key)) ||
    Object.keys(op).some(key => !allowed.includes(key)) || !id(op.id) || !id(op.taskId) ||
    !['task.approve', 'task.cancel', 'task.pause', 'task.resume', 'task.answer', 'task.repair', 'worker.cancel'].includes(op.kind ?? '') ||
    !['accepted', 'running', 'succeeded', 'failed', 'unknown'].includes(op.status ?? '') ||
    !Number.isSafeInteger(op.taskRevision) || (op.taskRevision ?? 0) < 1 ||
    !dateTime(op.createdAt) || !dateTime(op.updatedAt) ||
    // Operation.properties.workerId 是 Id；oneOf 又只允许 worker.cancel 携带它。
    // 非worker分支虽然写了null分支，null仍不满足外层Id，因此当前合同只允许省略。
    (op.kind === 'worker.cancel' ? !id(op.workerId) : Object.hasOwn(op, 'workerId')) ||
    (Object.hasOwn(op, 'code') && !code(op.code))) {
    throw new ApiError(502, 'invalid_operation_response', '操作回执不符合合同，不能确认结果', null);
  }
  return op as OperationRecord;
}

// ---- 问题（GET /v1/tasks/{taskId}/questions）----

export interface QuestionOption {
  value: string;
  label: string;
}

export type QuestionKind = 'clarification' | 'permission' | 'acceptance';
export type QuestionStatus = 'open' | 'answered' | 'expired' | 'cancelled';

/** 预批准问题（审批前的澄清/许可/验收口径，答复绑定 previewDigest）。 */
export interface Question {
  id: string;
  taskId: TaskId;
  slotId?: string;
  answer?: string | null;
  nodeId: NodeId | null;
  revision: Revision;
  subject: string;
  kind: QuestionKind;
  prompt: string;
  options: QuestionOption[];
  deadlineAt: string;
  status: QuestionStatus;
}

export type QuestionDeliveryStatus = 'pending' | 'dispatched' | 'acknowledged' | 'cancelled' | 'expired' | 'unknown';

/** 运行中 Worker 的有限业务问题（答复绑定 questionDigest；subject 恒等于 questionDigest）。 */
export interface RunningQuestion {
  id: string;
  taskId: TaskId;
  workerId: WorkerId;
  nodeId: NodeId;
  /** 合同固定 1。 */
  revision: 1;
  kind: 'business';
  subject: Sha256;
  questionDigest: Sha256;
  prompt: string;
  options: QuestionOption[];
  /** 合同 required 不含此键；未接纳答案时可能缺省。 */
  answer?: string | null;
  deadlineAt: string;
  status: QuestionStatus;
  deliveryStatus: QuestionDeliveryStatus | null;
}

export type QuestionItem = Question | RunningQuestion;

export interface QuestionsResponse {
  taskRevision: Revision;
  previewRevision: Revision | null;
  previewDigest: Sha256 | null;
  confirmBefore: string;
  preview: unknown | null;
  items: QuestionItem[];
  nextCursor: string | null;
  taskId: TaskId;
}

// POST /v1/tasks/{taskId}/questions/{questionId}/answers（两个互斥闭集分支）
export interface PreapprovalAnswerBody {
  branch: 'preapproval';
  expectedRevision: Revision;
  /** 合同固定 1。 */
  questionRevision: 1;
  previewDigest: Sha256;
  answer: string;
  idempotencyKey: string;
}

export interface RuntimeAnswerBody {
  branch: 'runtime';
  expectedRevision: Revision;
  /** 合同固定 1。 */
  questionRevision: 1;
  questionDigest: Sha256;
  answer: string;
  idempotencyKey: string;
}

export type AnswerBody = PreapprovalAnswerBody | RuntimeAnswerBody;

// ---- Leader（GET /v1/tasks/{taskId}/leader）----

export interface LeaderAuthorization {
  taskId: TaskId;
  planDigest: Sha256;
  artifactId: ArtifactId;
  artifactDigest: Sha256;
  bytes: number;
  acceptanceDigest: Sha256;
  reviewDigest: Sha256;
  targetId: string;
  targetPolicyDigest: Sha256;
  name: string;
  operation: string;
  expiresAt: string;
}

export type LeaderRequestStatus = 'pending' | 'replied' | 'closed';

export interface LeaderRequestDTO {
  id: string;
  kind: 'business' | 'publication';
  requestDigest: Sha256;
  subject: Sha256;
  nodeIds: NodeId[];
  prompt: string;
  options: QuestionOption[];
  authorization: LeaderAuthorization | null;
  deadlineAt: string;
  status: LeaderRequestStatus;
  replyDigest: Sha256 | null;
}

export interface LeaderReview {
  digest: Sha256;
  verdict: 'accept' | 'rework' | 'reject';
  selectionDigest: Sha256;
  policyDigest: Sha256;
  workerId: WorkerId;
  evidenceIds: string[];
}

export type LeaderActionStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'unknown' | 'cancelled';

export interface LeaderPublication {
  actionId: string;
  status: LeaderActionStatus;
  authorizationDigest: Sha256;
  receiptArtifactId: ArtifactId | null;
}

export interface LeaderPostverify {
  actionId: string;
  status: LeaderActionStatus;
  evidenceArtifactId: ArtifactId | null;
}

export type LeaderStage = 'intake' | 'work' | 'review' | 'verification' | 'delivery' | 'finalizing' | 'terminal';

export interface LeaderRecord {
  taskId: TaskId;
  taskRevision: Revision;
  profile: 'task-managed-leader/v1';
  stage: LeaderStage;
  policyDigest: Sha256;
  activeWorkerId: WorkerId | null;
  pendingRequest: LeaderRequestDTO | null;
  lastDecision: {digest: Sha256; callId: string; evidenceId: string} | null;
  review: LeaderReview | null;
  publication: LeaderPublication | null;
  postverify: LeaderPostverify | null;
  summaryArtifactId: ArtifactId | null;
}

// POST /v1/tasks/{taskId}/leader/requests/{requestId}/reply（两个互斥闭集分支）
export interface LeaderBusinessReplyBody {
  expectedRevision: Revision;
  requestDigest: Sha256;
  answer: string;
  idempotencyKey: string;
}

export interface LeaderPublicationReplyBody {
  expectedRevision: Revision;
  requestDigest: Sha256;
  decision: 'allow' | 'deny';
  idempotencyKey: string;
}

export type LeaderReplyBody = LeaderBusinessReplyBody | LeaderPublicationReplyBody;

// ---- 事件（GET /v1/tasks/{taskId}/events）----

export type EventSource = 'application' | 'agent' | 'execution' | 'verification';

export interface EventRecord {
  id: string;
  taskId: TaskId;
  sequence: number;
  type: string;
  at: string;
  workerId: WorkerId | null;
  summary: string;
  source: EventSource;
}

export interface Events {
  items: EventRecord[];
  nextCursor: string | null;
  taskId: TaskId;
}

// ---- 修复（POST /v1/tasks/{taskId}/repair；body = RepairTask）----

export interface RepairBody {
  expectedRevision: Revision;
  planDigest: Sha256;
  decisionDigest: Sha256;
  nodeIds: NodeId[];
  feedback: string;
  idempotencyKey: string;
}

// ---- 成果（GET /v1/artifacts/{artifactId}、/v1/artifacts/{artifactId}/content）----

export type ArtifactKind = 'input' | 'candidate' | 'delivery' | 'evidence';
export type ArtifactStatus = 'ready' | 'partial' | 'unavailable';

export interface ArtifactRecord {
  id: ArtifactId;
  taskId: TaskId | null;
  name: string;
  kind: ArtifactKind;
  status: ArtifactStatus;
  mediaType: string;
  bytes: number;
  digest: Sha256;
  createdAt: string;
}

// ---- 创建任务（POST /v1/tasks；body = CreateTask）与输入（POST /v1/inputs；body = CreateInput）----

export interface Context {
  text?: string;
  inputRefs?: string[];
}

export interface Requirements {
  deliverables?: string[];
  acceptance?: string[];
}

export interface CreateTaskBody {
  intent: string;
  context?: Context;
  requirements?: Requirements;
  limits?: Limits;
}

export interface CreateInputBody {
  name: string;
  mediaType: string;
  /** 解码后 ≤256 KiB 的 base64 内容。 */
  contentBase64: string;
}

// ---- 验收（GET /v1/tasks/{taskId}/audit 的 acceptance 子投影；UI 只消费 acceptance，不消费的不建模）----

export type AcceptanceStatus = 'pending' | 'passed' | 'failed' | 'unknown';

export interface AcceptanceRecord {
  status: AcceptanceStatus;
  evidenceIds: string[];
  digest: Sha256 | null;
}

export interface TaskAuditRecord {
  taskId: TaskId;
  acceptance: AcceptanceRecord;
}

// ---- 错误合同 ----

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

// ---- Transport 访问面 ----

/** 读方法的可选调用参数：signal 贯通取消（卸载/查询作废）与默认 deadline。 */
export interface ReadOptions {
  signal?: AbortSignal;
}

export interface Transport {
  createTask(body: CreateTaskBody & {idempotencyKey: string}): Promise<TaskRecord>;
  createInput(body: CreateInputBody & {idempotencyKey: string}): Promise<ArtifactRecord>;
  listTasks(options: {limit?: number; cursor?: string | null; signal?: AbortSignal}): Promise<TasksResponse>;
  getTask(taskId: TaskId, options?: ReadOptions): Promise<TaskRecord>;
  getOperation(operationId: string, options?: ReadOptions): Promise<OperationRecord>;
  getWorkers(taskId: TaskId, options?: {cursor?: string | null; limit?: number; signal?: AbortSignal}): Promise<WorkersResponse>;
  getPlan(taskId: TaskId, options?: ReadOptions): Promise<PlanRecord>;
  getGraph(taskId: TaskId, options?: ReadOptions): Promise<GraphRecord>;
  approvePlan(taskId: TaskId, body: PlanApproveBody): Promise<unknown>;
  getQuestions(taskId: TaskId, options?: {cursor?: string | null; limit?: number; signal?: AbortSignal}): Promise<QuestionsResponse>;
  answerTask(taskId: TaskId, questionId: string, body: AnswerBody): Promise<unknown>;
  cancelTask(taskId: TaskId, body: ControlBody): Promise<unknown>;
  pauseTask(taskId: TaskId, body: ControlBody): Promise<unknown>;
  resumeTask(taskId: TaskId, body: ControlBody): Promise<unknown>;
  cancelWorker(workerId: WorkerId, body: ControlBody): Promise<unknown>;
  getLeader(taskId: TaskId, options?: ReadOptions): Promise<LeaderRecord>;
  getAudit(taskId: TaskId, options?: ReadOptions): Promise<TaskAuditRecord>;
  leaderReply(taskId: TaskId, requestId: string, body: LeaderReplyBody): Promise<unknown>;
  repair(taskId: TaskId, body: RepairBody): Promise<unknown>;
  getEvents(taskId: TaskId, options?: {cursor?: string | null; limit?: number; signal?: AbortSignal}): Promise<Events>;
  getArtifact(artifactId: ArtifactId, options?: ReadOptions): Promise<ArtifactRecord>;
  getArtifactContent(artifactId: ArtifactId, options?: ReadOptions): Promise<Blob>;
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}
