import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Drawer } from 'antd';
import { MenuOutlined } from '@ant-design/icons';
import ConversationHistory from '@/components/ConversationHistory/ConversationHistory';
import ApplicationSwitcher from '@/components/Workspace/ApplicationSwitcher';
import { useWorkspaceStore } from '@/stores/useWorkspaceStore';
import { useAuthStore } from '@/stores/useAuthStore';
import { userAvatarFallback, userAvatarUrl } from '@/lib/chatIdentity';
import { useApplicationCatalogStore, resolveDefaultApplication } from '@/stores/useApplicationCatalogStore';
import { useRunChatStore } from '@/stores/useRunChatStore';
import type { ShellChrome } from './useShellChrome';
import './shell.css';

const NAV_ITEMS = [
  { key: '/', label: '首页', icon: '🏠' },
  { key: '/agents', label: '智能体', icon: '🤖' },
  { key: '/apps', label: '应用', icon: '🧩' },
  { key: '/workflows', label: '工作流', icon: '🔀' },
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
  const user = useAuthStore((state) => state.user);
  const navigate = useNavigate();
  const location = useLocation();
  const path = location.pathname;

  const go = (path: string) => {
    setMobileNavOpen(false);
    navigate(path);
  };

  // Same semantics as the desktop sidebar's 新建 (see Sidebar.tsx): a new
  // conversation of the active/main chat agent, never a silent no-op.
  const handleNewConversation = () => {
    const applications = useApplicationCatalogStore.getState().applications;
    const activeApplicationId = useWorkspaceStore.getState().activeApplicationId;
    const slug = path.startsWith('/chat/')
      ? decodeURIComponent(path.slice('/chat/'.length))
      : null;
    const application = (slug
      && applications.find((app) => app.slug === slug))
      || applications.find((app) => app.id === activeApplicationId)
      || resolveDefaultApplication(applications);
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
          <ApplicationSwitcher compact />
          <div className="mobile-shell__avatar">
            {userAvatarUrl(user)
              ? <img src={userAvatarUrl(user)} alt="" />
              : userAvatarFallback(user)}
          </div>
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
        styles={{ body: { padding: 0 } }}
      >
        <nav className="mobile-shell__nav">
          {NAV_ITEMS.map((item) => (
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
      </Drawer>
    </div>
  );
};

export default MobileAppShell;
