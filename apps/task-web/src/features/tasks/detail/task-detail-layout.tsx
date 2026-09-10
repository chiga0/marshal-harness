// 骨架占位：W2 工作包的任务详情与子视图（概览/团队/成果/活动）将替换本文件。
import {useParams, Link} from 'react-router-dom';

export function TaskDetailLayout() {
  const {taskId} = useParams<{taskId: string}>();
  return (
    <section aria-label="任务详情" className="p-6">
      <h1 className="text-[22px] font-semibold leading-[30px]">任务详情（骨架；由 W2 工作包完整实现）</h1>
      <p className="mt-2 text-sm text-text-secondary">{taskId}</p>
      <p className="mt-4"><Link className="text-accent hover:underline" to="/">返回列表</Link></p>
    </section>
  );
}
