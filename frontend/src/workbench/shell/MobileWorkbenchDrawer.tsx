import { useLocation, useNavigate } from 'react-router-dom';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import AgentAvatar from '@/components/Agents/AgentAvatar';
import { NavigationItemIcon, getVisibleNavigationItems, isNavigationItemActive } from '@/components/Navigation';
import { useAuthStore } from '@/stores/useAuthStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import RecentTaskList from '../tasks/RecentTaskList';
import { useRecentCapabilities } from '../capability/useRecentCapabilities';

export default function MobileWorkbenchDrawer({ close }: { close: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const capabilities = useRecentCapabilities(6);
  const tasks = useWorkspaceBootstrapStore((state) => state.recentTasks);
  const go = (path: string) => { close(); navigate(path); };
  return (
    <div className="mobile-workbench-drawer">
      <nav className="mobile-shell__nav" aria-label="主导航">
        {getVisibleNavigationItems({ isStaff }).map((item) => {
          const active = isNavigationItemActive(item.id, location.pathname);
          return (
            <button key={item.id} type="button" className={`mobile-shell__nav-item${active ? ' active' : ''}`}
              aria-current={active ? 'page' : undefined} onClick={() => go(item.path)}>
              <NavigationItemIcon item={item} surface="mobile" className="mobile-shell__nav-icon" /><span>{item.mobileLabel}</span>
            </button>
          );
        })}
      </nav>
      <section className="mobile-workbench-drawer__section"><h2>最近使用</h2>{capabilities.slice(0, 6).map((item) => (
        <button type="button" key={item.id} onClick={() => go(item.kind === 'chat' ? `/chat/${item.slug}` : `/app/${item.slug}`)}>
          <AgentAvatar application={item} size={28} tint={item.color} /><span>{item.name}</span>
        </button>
      ))}</section>
      <section className="mobile-workbench-drawer__section"><h2>最近任务</h2><RecentTaskList items={tasks} limit={6} onNavigate={close} />
        <button type="button" className="mobile-workbench-drawer__all" onClick={() => go('/tasks')}>查看全部任务 ›</button>
      </section>
      <div className="mobile-workbench-drawer__account"><AccountMenu variant="panel" onLogoutComplete={close} /></div>
    </div>
  );
}
