import { Button, Empty, Spin } from 'antd';
import type { TaskSummary } from '@/types/task';
import TaskListItem from './TaskListItem';

function groupLabel(value: string): string {
  const date = new Date(value);
  const now = new Date();
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const day = 86400000;
  if (date.getTime() >= start) return '今天';
  if (date.getTime() >= start - day) return '昨天';
  if (date.getTime() >= start - 7 * day) return '本周';
  return '更早';
}

export default function TaskList({ items, loading, nextCursor, onOpen, onRename, onDelete, onLoadMore }: {
  items: TaskSummary[];
  loading: boolean;
  nextCursor?: string;
  onOpen: (task: TaskSummary) => void;
  onRename: (task: TaskSummary, title: string) => Promise<void>;
  onDelete: (task: TaskSummary) => Promise<void>;
  onLoadMore?: () => void;
}) {
  const groups = items.reduce<Record<string, TaskSummary[]>>((all, item) => {
    (all[groupLabel(item.updatedAt)] ||= []).push(item);
    return all;
  }, {});
  if (loading && items.length === 0) return <div className="task-list__loading"><Spin /></div>;
  if (items.length === 0) return <Empty description="还没有任务。描述你的第一个目标即可开始。" />;
  return (
    <div className="task-list">
      {Object.entries(groups).map(([label, tasks]) => (
        <section key={label} className="task-list__group">
          <h3>{label}</h3>
          {tasks.map((task) => (
            <TaskListItem key={task.id} task={task} onOpen={() => onOpen(task)}
              onRename={(title) => onRename(task, title)} onDelete={() => onDelete(task)} />
          ))}
        </section>
      ))}
      {nextCursor && <Button block loading={loading} onClick={onLoadMore}>加载更多</Button>}
    </div>
  );
}
