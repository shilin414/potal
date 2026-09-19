import { useEffect, useMemo, useState } from 'react';
import { Input, Select } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useTaskStore } from '@/stores/useTaskStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import type { TaskSummary } from '@/types/task';
import TaskList from './TaskList';
import './tasks.css';

export default function TaskCenterPage() {
  const navigate = useNavigate();
  const { items, loading, error, nextCursor, load, loadMore, rename, remove } = useTaskStore();
  const capabilities = useWorkspaceBootstrapStore((state) => state.recentCapabilities);
  const loadBootstrap = useWorkspaceBootstrapStore((state) => state.load);
  const [q, setQ] = useState('');
  const [applicationId, setApplicationId] = useState<number | undefined>();

  useEffect(() => { void loadBootstrap(); }, [loadBootstrap]);
  useEffect(() => {
    const timer = window.setTimeout(() => { void load({ q, applicationId, limit: 20 }); }, 220);
    return () => window.clearTimeout(timer);
  }, [applicationId, load, q]);

  const options = useMemo(() => capabilities.map((item) => ({ value: item.id, label: item.name })), [capabilities]);
  const open = (task: TaskSummary) => navigate(task.applicationSlug
    ? `/chat/${task.applicationSlug}?conversation=${task.id}` : `/?conversation=${task.id}`);

  return (
    <div className="task-center">
      <header className="task-center__header">
        <div><h1>任务</h1><p>继续、搜索和管理你的工作。</p></div>
        <div className="task-center__filters">
          <Input allowClear prefix={<SearchOutlined />} placeholder="搜索任务" value={q} onChange={(event) => setQ(event.target.value)} />
          <Select allowClear placeholder="全部能力" options={options} value={applicationId} onChange={setApplicationId} />
        </div>
      </header>
      {error && <div className="task-center__error">{error}</div>}
      <TaskList items={items} loading={loading} nextCursor={nextCursor} onOpen={open}
        onRename={(task, title) => rename(task.id, title)} onDelete={(task) => remove(task.id)} onLoadMore={loadMore} />
    </div>
  );
}
