export interface TaskSummary { taskId: string; title?: string; workspace?: string; latestState: string; runCount: number; accepted: number; blocked: number; running: number; latestUpdate: string; }
export interface EventLite { sequence: number; type: string; from: string; to: string; at: string; }
export interface AttemptInfo { id: string; workerStatus?: string; }
export interface RunDetail { runId: string; taskId: string; title?: string; workspace?: string; state: string; reviewRound: number; attemptsUsed: number; operationalRetries: number; reworkRounds: number; workerDurationSec: number; totalDurationSec: number; inputTokens: number; outputTokens: number; verification?: string; gatesPassed: number; gatesTotal: number; gatesFailed?: string[]; artifacts?: string[]; hasReview: boolean; hasPublication: boolean; hasOutcome: boolean; updatedAt?: string; events: EventLite[]; attempts: AttemptInfo[]; }

export const STATE_META: Record<string, { label: string; tone: "ok"|"err"|"warn"|"info"|"mut" }> = {
  ACCEPTED: { label: "已接受·成功", tone: "ok" }, REJECTED: { label: "已拒绝", tone: "err" }, BLOCKED: { label: "阻塞·需人工", tone: "err" },
  RUNNING: { label: "执行中", tone: "info" }, VERIFYING: { label: "独立验证中", tone: "info" }, REVIEW_PENDING: { label: "待审查", tone: "warn" },
  REWORK_REQUESTED: { label: "返工中", tone: "warn" }, PUBLISHING: { label: "发布中", tone: "info" }, PUBLISHED: { label: "已发布", tone: "info" },
  CI_PENDING: { label: "等待CI", tone: "warn" }, READY: { label: "就绪", tone: "info" }, PLANNED: { label: "规划中", tone: "info" },
  ABORTED: { label: "已中止", tone: "mut" }, NO_CHANGE: { label: "无变更", tone: "mut" },
};
export const stateMeta = (s: string) => STATE_META[s] ?? { label: s, tone: "mut" as const };

export const EVENT_LABEL: Record<string, string> = {
  "planning.spec-accepted": "任务规格冻结", "planning.inputs-frozen": "输入冻结", "worker.started": "Worker 启动", "worker.completed": "Worker 完成",
  "worker.failed": "Worker 失败", "verification.completed": "独立验证完成", "review.accept": "审查接受", "review.rework": "要求返工",
  "publication.completed": "发布完成", "run.aborted": "人工中止",
};

export async function fetchTasks(): Promise<TaskSummary[]> { return (await (await fetch("/api/tasks")).json()).tasks ?? []; }
export async function fetchRuns(): Promise<RunDetail[]> { return (await (await fetch("/api/runs")).json()).runs ?? []; }
export async function fetchRun(id: string): Promise<RunDetail> { return (await (await fetch("/api/runs/" + id)).json()); }
export function streamTasks(cb: (t: TaskSummary[]) => void): () => void {
  const es = new EventSource("/api/stream");
  es.addEventListener("snapshot", (e) => cb((JSON.parse((e as MessageEvent).data).runs ?? [])));
  return () => es.close();
}
export const rel = (t: string) => { const d = (Date.now() - new Date(t).getTime()) / 1000; if (d < 60) return "刚刚"; if (d < 3600) return Math.floor(d / 60) + "分钟前"; if (d < 86400) return Math.floor(d / 3600) + "小时前"; return Math.floor(d / 86400) + "天前"; };
