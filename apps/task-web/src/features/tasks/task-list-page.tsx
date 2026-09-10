// 骨架占位：W1 工作包的列表实现将替换本文件。使用样例保持整个装配可跑可测。
import {sampleTaskList} from '../../lib/transport/samples';
import {Link} from 'react-router-dom';

export function TaskListPage() {
  return (
    <section aria-label="任务列表" className="flex flex-col gap-3 p-6">
      <div className="flex items-center justify-between">
        <h1 className="text-[22px] font-semibold leading-[30px]">任务</h1>
        <Link to="/tasks/new" className="text-sm text-accent underline-offset-4 hover:underline">新建</Link>
      </div>
      <ul className="space-y-2">
        {sampleTaskList.map(task => (
          <li key={task.id} className="rounded-md border border-border bg-surface p-4">
            <div className="flex items-center justify-between gap-3">
              <Link to={'/tasks/' + encodeURIComponent(task.id)} className="text-sm font-medium text-accent hover:underline">
                {task.intent}
              </Link>
              <span className="text-xs text-text-secondary">{task.updatedAt}</span>
            </div>
            <p className="mt-1 text-sm text-text-secondary">{task.status}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}
