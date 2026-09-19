import { useEffect, useState } from 'react';
import { LeftOutlined, RightOutlined, SearchOutlined } from '@ant-design/icons';
import { useLocation, useNavigate } from 'react-router-dom';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { NavigationItemIcon, getVisibleNavigationItems, isNavigationItemActive } from '@/components/Navigation';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkbenchUiStore } from '@/stores/useWorkbenchUiStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import CapabilityPicker from '../capability/CapabilityPicker';
import { useRecentCapabilities } from '../capability/useRecentCapabilities';
import RecentTaskList from '../tasks/RecentTaskList';
import './workbench-shell.css';

export default function DesktopSidebar() {
  const navigate = useNavigate();
  const location = useLocation();
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const collapsed = useWorkbenchUiStore((state) => state.sidebarCollapsed);
  const toggleSidebar = useWorkbenchUiStore((state) => state.toggleSidebar);
  const load = useWorkspaceBootstrapStore((state) => state.load);
  const recentCapabilities = useRecentCapabilities(6);
  const dirty = useWorkspaceBootstrapStore((state) => state.dirty);
  const recentTasks = useWorkspaceBootstrapStore((state) => state.recentTasks);
  const [pickerOpen, setPickerOpen] = useState(false);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => { if (dirty) void load(); }, [dirty, load]);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault(); setPickerOpen(true);
      }
    };
    window.addEventListener('keydown', listener);
    return () => window.removeEventListener('keydown', listener);
  }, []);

  const nav = getVisibleNavigationItems({ isStaff });
  return (
    <aside className={`workbench-sidebar${collapsed ? ' workbench-sidebar--collapsed' : ''}`}>
      <div className="workbench-sidebar__brand">
        <button type="button" onClick={() => navigate('/')} aria-label="Potal 首页"><span>P</span>{!collapsed && <strong>Potal</strong>}</button>
        <button type="button" className="workbench-sidebar__collapse" onClick={toggleSidebar} aria-label={collapsed ? '展开侧栏' : '收起侧栏'}>
          {collapsed ? <RightOutlined /> : <LeftOutlined />}
        </button>
      </div>
      <button type="button" className="workbench-sidebar__search" onClick={() => setPickerOpen(true)}>
        <SearchOutlined />{!collapsed && <><span>搜索能力和任务</span><kbd>Ctrl K</kbd></>}
      </button>
      <nav className="workbench-sidebar__nav" aria-label="主导航">
        {nav.map((item) => {
          const active = isNavigationItemActive(item.id, location.pathname);
          return (
            <button type="button" key={item.id} className={active ? 'active' : ''} title={item.desktopLabel}
              onClick={() => navigate(item.path)} aria-current={active ? 'page' : undefined}>
              <NavigationItemIcon item={item} /><span>{item.desktopLabel}</span>
            </button>
          );
        })}
      </nav>
      {!collapsed && (
        <div className="workbench-sidebar__scroll">
          <section><h2>最近使用</h2>{recentCapabilities.slice(0, 6).map((item) => (
            <button type="button" className="workbench-sidebar__capability" key={item.id}
              onClick={() => navigate(item.kind === 'chat' ? `/chat/${item.slug}` : `/app/${item.slug}`)}>
              <AgentAvatar application={item} size={24} tint={item.color} /><span>{item.name}</span>
            </button>
          ))}</section>
          <section><h2>最近任务</h2><RecentTaskList items={recentTasks} limit={6} /></section>
          <button type="button" className="workbench-sidebar__all-tasks" onClick={() => navigate('/tasks')}>查看全部任务</button>
        </div>
      )}
      <div className="workbench-sidebar__account"><AccountMenu /></div>
      <CapabilityPicker open={pickerOpen} onClose={() => setPickerOpen(false)} />
    </aside>
  );
}
