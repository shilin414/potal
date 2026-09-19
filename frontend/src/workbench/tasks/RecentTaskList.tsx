import { useNavigate } from 'react-router-dom';
import type { TaskSummary } from '@/types/task';
import { useTaskStore } from '@/stores/useTaskStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import TaskListItem from './TaskListItem';

export default function RecentTaskList({ items, compact = true, limit = 6, onNavigate }: {
  items: TaskSummary[];
  compact?: boolean;
  limit?: number;
  onNavigate?: () => void;
}) {
  const navigate = useNavigate();
  const rename = useTaskStore((state) => state.rename);
  const remove = useTaskStore((state) => state.remove);
  const refreshBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const open = (task: TaskSummary) => {
    onNavigate?.();
    navigate(task.applicationSlug ? `/chat/${task.applicationSlug}?conversation=${task.id}` : `/?conversation=${task.id}`);
  };
  return (
    <div className="recent-task-list">
      {items.slice(0, limit).map((task) => (
        <TaskListItem key={task.id} task={task} compact={compact} onOpen={() => open(task)}
          onRename={async (title) => { await rename(task.id, title); await refreshBootstrap(true); }} onDelete={async () => { await remove(task.id); await refreshBootstrap(true); }} />
      ))}
    </div>
  );
}
