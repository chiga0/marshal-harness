// 详情域查询键：全部以 ['task', taskId] 为前缀，mutation 受理后按前缀整体失效。

export const taskKeys = {
  all: (taskId: string) => ['task', taskId] as const,
  detail: (taskId: string) => ['task', taskId, 'detail'] as const,
  workers: (taskId: string) => ['task', taskId, 'workers'] as const,
  leader: (taskId: string) => ['task', taskId, 'leader'] as const,
  audit: (taskId: string) => ['task', taskId, 'audit'] as const,
  plan: (taskId: string) => ['task', taskId, 'plan'] as const,
  graph: (taskId: string) => ['task', taskId, 'graph'] as const,
  questions: (taskId: string) => ['task', taskId, 'questions'] as const,
  artifacts: (taskId: string) => ['task', taskId, 'artifacts'] as const,
  events: (taskId: string) => ['task', taskId, 'events'] as const,
};
