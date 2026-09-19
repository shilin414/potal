import { useEffect, useMemo, useState } from 'react';
import { Empty, Input, Segmented, Spin } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { fetchApplicationPage, type ApplicationSummary } from '@/services/runApi';
import { fetchTasks } from '@/services/taskApi';
import type { TaskSummary } from '@/types/task';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';

export type CapabilityFilter = 'all' | 'agents' | 'apps';

interface Props {
  onSelect: (capability: ApplicationSummary) => void;
  onTaskSelect: (task: TaskSummary) => void;
}

export default function CapabilityPickerCore({ onSelect, onTaskSelect }: Props) {
  const recentCapabilities = useWorkspaceBootstrapStore((state) => state.recentCapabilities);
  const [filter, setFilter] = useState<CapabilityFilter>('all');
  const [query, setQuery] = useState('');
  const [items, setItems] = useState<ApplicationSummary[]>(recentCapabilities);
  const [tasks, setTasks] = useState<TaskSummary[]>([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let active = true;
    const timer = window.setTimeout(() => {
      const hasQuery = Boolean(query.trim());
      if (!hasQuery && filter === 'all') {
        setItems(recentCapabilities);
        setTasks([]);
        setLoading(false);
        return;
      }
      setLoading(true);
      const taskPromise = query.trim() ? fetchTasks({ q: query, limit: 8 }) : Promise.resolve({ items: [], nextCursor: '' });
      void Promise.all([fetchApplicationPage({
        kind: filter === 'agents' ? 'chat' : filter === 'apps' ? 'fixed' : 'all',
        scope: 'accessible',
        mode: 'consume',
        q: query,
        limit: 50,
      }), taskPromise]).then(([page, taskPage]) => {
        if (active) { setItems(page.items); setTasks(taskPage.items); }
      }).catch(() => {
        if (active) { setItems([]); setTasks([]); }
      }).finally(() => {
        if (active) setLoading(false);
      });
    }, 180);
    return () => { active = false; window.clearTimeout(timer); };
  }, [filter, query, recentCapabilities]);

  const shown = useMemo(() => {
    if (filter === 'agents') return items.filter((item) => item.kind === 'chat');
    if (filter === 'apps') return items.filter((item) => item.kind !== 'chat');
    return items;
  }, [filter, items]);

  return (
    <div className="capability-picker">
      <Input
        allowClear
        autoFocus
        prefix={<SearchOutlined />}
        placeholder="搜索智能体或应用"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      <Segmented
        block
        value={filter}
        onChange={(value) => setFilter(value as CapabilityFilter)}
        options={[
          { value: 'all', label: '全部' },
          { value: 'agents', label: '智能体' },
          { value: 'apps', label: '应用' },
        ]}
      />
      {tasks.length > 0 && <div className="capability-picker__tasks"><strong>任务</strong>{tasks.map((task) => <button type="button" key={task.id} onClick={() => onTaskSelect(task)}><span>{task.title || '未命名任务'}</span><small>{task.applicationName || '默认智能体'}</small></button>)}</div>}
      <div className="capability-picker__list" role="listbox" aria-label="能力列表">
        {loading ? <Spin /> : shown.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="没有匹配的能力" />
        ) : shown.map((item) => (
          <button
            type="button"
            className="capability-picker__row"
            key={item.id}
            onClick={() => onSelect(item)}
          >
            <AgentAvatar application={item} size={38} tint={item.color} />
            <span className="capability-picker__content">
              <strong>{item.name}</strong>
              <small>{item.description || (item.kind === 'chat' ? '智能体' : '应用')}</small>
            </span>
            <span className="capability-picker__badge">{item.kind === 'chat' ? '智能体' : '应用'}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
