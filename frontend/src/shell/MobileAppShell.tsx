import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Button, Drawer } from 'antd';
import { MenuOutlined, PlusOutlined } from '@ant-design/icons';
import ConversationHistory from '@/components/ConversationHistory/ConversationHistory';
import AccountMenu from '@/components/AccountMenu/AccountMenu';
import MobileAgentSwitcher from '@/components/Mobile/MobileAgentSwitcher';
import { ThemePicker } from '@/components/Theme';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useApplicationEntityStore } from '@/stores/useApplicationEntityStore';
import { useWorkspaceBootstrapStore } from '@/stores/useWorkspaceBootstrapStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import { useAuthStore } from '@/stores/useAuthStore';
import type { ShellChrome } from './useShellChrome';
import './shell.css';

// 技能 / 案例库 / 工作流三个入口已从主页导航下掉（桌面端 Header 同步改动）；
// 页面、路由与数据都保留，仍可直接访问 /skills、/templates、/workflows。
// 恢复时把对应项加回数组即可。
const NAV_ITEMS = [
  { key: '/', label: '首页', icon: '🏠' },
  { key: '/agents', label: '智能体', icon: '🤖' },
  { key: '/apps', label: '应用', icon: '🧩' },
  { key: '/schedules', label: '定时任务', icon: '⏰' },
  { key: '/enterprise', label: '企业控制台', icon: '🏢' },
];

/**
 * MobileAppShell — bottom-anchored mobile layout (§20-§23).
 *
 * Shares WorkspaceHost/Renderer with the desktop shell (§24); only the
 * interaction chrome differs: the sidebar becomes a drawer and the agent
 * switcher moves into the top bar.
 */
const MobileAppShell: React.FC<{ chrome: ShellChrome }> = ({ chrome }) => {
  const mobileNavOpen = useWorkspaceStore((state) => state.mobileNavOpen);
  const setMobileNavOpen = useWorkspaceStore((state) => state.setMobileNavOpen);
  const navigate = useNavigate();
  const location = useLocation();
  const path = location.pathname;
  const isStaff = useAuthStore((state) => Boolean(state.user?.is_staff));
  const visibleNavItems = NAV_ITEMS.filter((item) => item.key !== '/enterprise' || isStaff);

  const go = (path: string) => {
    setMobileNavOpen(false);
    navigate(path);
  };

  // Same semantics as the desktop sidebar's 新建 (see Sidebar.tsx): a new
  // conversation of the active/main chat agent, never a silent no-op. The
  // lookup stays inside the entity cache + the bootstrap payload, so tapping
  // 新建 costs no catalog request (执行报告 §9.4).
  const handleNewConversation = () => {
    const entities = useApplicationEntityStore.getState();
    const activeApplicationId = useWorkspaceStore.getState().activeApplicationId;
    const slug = path.startsWith('/chat/')
      ? decodeURIComponent(path.slice('/chat/'.length))
      : null;
    const application = (slug ? entities.getBySlug(slug) : undefined)
      || entities.get(activeApplicationId)
      || useWorkspaceBootstrapStore.getState().defaultApplication;
    setMobileNavOpen(false);
    if (!application) {
      navigate('/');
      return;
    }
    useWorkspaceStore.getState().startNewConversation(application.id);
    useRunChatStore.getState().setActiveConversation(null);
    navigate(`/chat/${application.slug}`);
  };

  return (
    <div className="mobile-shell">
      {!chrome.hideHeader && (
        <header className="mobile-shell__bar">
          <button
            type="button"
            className="mobile-shell__icon-btn"
            aria-label="打开导航"
            onClick={() => setMobileNavOpen(true)}
          >
            <MenuOutlined />
          </button>
          {/* 移动端智能体切换走 Bottom Sheet，与首页选择器同一组件 (§7.2)；
              桌面端继续用 ApplicationSwitcher 的 Dropdown，互不影响。 */}
          <MobileAgentSwitcher />
          {/* 右侧主操作：回到首页。这里原本是账号头像（纯展示、无下拉，
              账号菜单本来就在移动端不可达），换成一个更常用的动作。 */}
          <Button
            type="primary"
            size="small"
            icon={<PlusOutlined />}
            className="mobile-shell__task-btn"
            onClick={() => go('/')}
          >
            新任务
          </Button>
        </header>
      )}

      <main className="mobile-shell__main">
        <Outlet />
      </main>

      <Drawer
        placement="left"
        open={mobileNavOpen}
        onClose={() => setMobileNavOpen(false)}
        width="82vw"
        title="Creation Studio"
        styles={{ body: { padding: 0, display: 'flex', flexDirection: 'column' } }}
      >
        <nav className="mobile-shell__nav">
          {visibleNavItems.map((item) => (
            <button
              key={item.key}
              type="button"
              className="mobile-shell__nav-item"
              onClick={() => go(item.key)}
            >
              <span aria-hidden>{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>
        <div className="mobile-shell__history">
          <ConversationHistory
            onConversationSelect={(id) => go(id ? `/?conversation=${id}` : '/')}
            onNewConversation={handleNewConversation}
          />
        </div>
        <div className="mobile-shell__theme">
          <ThemePicker />
        </div>
        <AccountMenu
          variant="panel"
          onLogoutComplete={() => setMobileNavOpen(false)}
        />
      </Drawer>
    </div>
  );
};

export default MobileAppShell;
