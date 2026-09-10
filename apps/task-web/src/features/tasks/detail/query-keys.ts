// 详情域查询键：全部以 ['task', taskId] 为前缀，mutation 受理后按前缀失效。

export const taskKeys = {
  all: (taskId: string) => ['task', taskId] as const,
  detail: (taskId: string) => ['task', taskId, 'detail'] as const,
  workers: (taskId: string) => ['task', taskId, 'workers'] as const,
  leader: (taskId: string) => ['task', taskId, 'leader'] as const,
  publications: (taskId: string) => ['task', taskId, 'publications'] as const,
};
